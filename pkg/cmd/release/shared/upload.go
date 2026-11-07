package shared

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"golang.org/x/sync/errgroup"
)

type errNetwork struct{ error }

type AssetForUpload struct {
	Name  string
	Label string

	Size     int64
	MIMEType string
	Open     func() (io.ReadCloser, error)

	ExistingURL safeurl.SafeURL
}

func AssetsFromArgs(args []string) (assets []*AssetForUpload, err error) {
	labeledArgs, unlabeledArgs := cmdutil.Partition(args, func(arg string) bool {
		return strings.Contains(arg, "#")
	})

	args, err = cmdutil.GlobPaths(unlabeledArgs)
	if err != nil {
		return nil, err
	}

	args = append(args, labeledArgs...)

	for _, arg := range args {
		var label string
		fn := arg
		if idx := strings.IndexRune(arg, '#'); idx > 0 {
			fn = arg[0:idx]
			label = arg[idx+1:]
		}

		var fi os.FileInfo
		fi, err = os.Stat(fn)
		if err != nil {
			return
		}

		assets = append(assets, &AssetForUpload{
			Open: func() (io.ReadCloser, error) {
				return os.Open(fn)
			},
			Size:     fi.Size(),
			Name:     fi.Name(),
			Label:    label,
			MIMEType: typeForFilename(fi.Name()),
		})
	}
	return
}

func typeForFilename(fn string) string {
	ext := fileExt(fn)
	switch ext {
	case ".zip":
		return "application/zip"
	case ".js":
		return "application/javascript"
	case ".tar":
		return "application/x-tar"
	case ".tgz", ".tar.gz":
		return "application/x-gtar"
	case ".bz2":
		return "application/x-bzip2"
	case ".dmg":
		return "application/x-apple-diskimage"
	case ".rpm":
		return "application/x-rpm"
	case ".deb":
		return "application/x-debian-package"
	}

	t := mime.TypeByExtension(ext)
	if t == "" {
		return "application/octet-stream"
	}
	return t
}

func fileExt(fn string) string {
	fn = strings.ToLower(fn)
	if strings.HasSuffix(fn, ".tar.gz") {
		return ".tar.gz"
	}
	return path.Ext(fn)
}

func ConcurrentUpload(httpClient *http.Client, hostname string, uploadURL safeurl.SafeURL, numWorkers int, assets []*AssetForUpload) error {
	if numWorkers == 0 {
		return errors.New("the number of concurrent workers needs to be greater than 0")
	}

	ctx := context.Background()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(numWorkers)

	for _, a := range assets {
		asset := *a
		g.Go(func() error {
			return uploadWithDelete(gctx, httpClient, hostname, uploadURL, asset)
		})
	}

	return g.Wait()
}

func shouldRetry(err error) bool {
	if _, ok := errors.AsType[errNetwork](err); ok {
		return true
	}
	var httpError api.HTTPError
	return errors.As(err, &httpError) && httpError.StatusCode >= 500
}

// Allow injecting backoff interval in tests.
var retryInterval = time.Millisecond * 200

func uploadWithDelete(ctx context.Context, httpClient *http.Client, hostname string, uploadURL safeurl.SafeURL, a AssetForUpload) error {
	if a.ExistingURL != nil && a.ExistingURL.String() != "" {
		if err := deleteAsset(ctx, httpClient, hostname, a.ExistingURL); err != nil {
			return err
		}
	}

	// The host decides which upload shape to send: Gitea takes a multipart form
	// and names the file in a query parameter, GitHub takes a raw body and names
	// it in the same parameter.
	giteaHost := hosttype.IsGitea(hostname)

	bo := backoff.NewConstantBackOff(retryInterval)
	return backoff.Retry(func() error {
		_, err := uploadAsset(ctx, httpClient, uploadURL, a, giteaHost)
		if err == nil || shouldRetry(err) {
			return err
		}
		return backoff.Permanent(err)
	}, backoff.WithContext(backoff.WithMaxRetries(bo, 3), ctx))
}

// uploadAssetGitea posts one asset to Gitea's release assets endpoint.
//
// Three things differ from GitHub's upload. The file travels as a multipart form
// field named attachment rather than as the raw request body. The asset's name is
// a query parameter, not a form field, so it has to be on the URL. There is no
// label, so one the caller supplied is dropped rather than sent where nothing
// would read it.
//
// The upload URL Gitea reports on a release already points at the assets
// collection, so it is used as given.
func uploadAssetGitea(ctx context.Context, httpClient *http.Client, uploadURL safeurl.SafeURL, asset AssetForUpload) (*ReleaseAsset, error) {
	u, err := url.Parse(uploadURL.String())
	if err != nil {
		return nil, err
	}
	params := u.Query()
	params.Set("name", asset.Name)
	u.RawQuery = params.Encode()

	f, err := asset.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("attachment", asset.Name)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	// The body is buffered so ContentLength is known and the request can be
	// retried by the backoff wrapper around this call.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(body.Len())
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := api.NewClientFromHTTP(httpClient).DoRequest(req)
	if err != nil {
		if _, ok := errors.AsType[api.HTTPError](err); ok {
			return nil, err
		}
		return nil, errNetwork{err}
	}
	defer resp.Body.Close()

	var newAsset giteaUploadAsset
	if err := json.NewDecoder(resp.Body).Decode(&newAsset); err != nil {
		return nil, err
	}

	return &ReleaseAsset{
		ID:                 strconv.FormatInt(newAsset.ID, 10),
		Name:               newAsset.Name,
		Size:               newAsset.Size,
		DownloadCount:      newAsset.DownloadCount,
		BrowserDownloadURL: newAsset.BrowserDownloadURL,
		APIURL:             giteaAssetURLFromUpload(u.String(), newAsset.ID),
	}, nil
}

// giteaUploadAsset is Gitea's Attachment as the upload endpoint returns it.
type giteaUploadAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	DownloadCount      int    `json:"download_count"`
	BrowserDownloadURL string `json:"browser_download_url"`
	UUID               string `json:"uuid"`
}

// giteaAssetURLFromUpload builds the per-asset API URL from the assets collection
// URL the upload went to.
func giteaAssetURLFromUpload(uploadURL string, id int64) string {
	base := uploadURL
	if idx := strings.IndexRune(base, '?'); idx > 0 {
		base = base[:idx]
	}
	return base + "/" + strconv.FormatInt(id, 10)
}

func uploadAsset(ctx context.Context, httpClient *http.Client, uploadURL safeurl.SafeURL, asset AssetForUpload, giteaHost bool) (*ReleaseAsset, error) {
	if giteaHost {
		return uploadAssetGitea(ctx, httpClient, uploadURL, asset)
	}

	u, err := url.Parse(uploadURL.String())
	if err != nil {
		return nil, err
	}
	params := u.Query()
	params.Set("name", asset.Name)
	params.Set("label", asset.Label)
	u.RawQuery = params.Encode()

	// Since u is derived from uploadURL, an already-trusted safeurl.SafeURL, the resulting URL is safe to declare as such.
	safeURL := safeurl.NewImmutableSafeURL(u.String())

	f, err := asset.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", safeURL.String(), f)
	if err != nil {
		return nil, err
	}
	req.ContentLength = asset.Size
	req.Header.Set("Content-Type", asset.MIMEType)
	req.GetBody = asset.Open

	// DoRequest rather than Request because ContentLength and GetBody are set on the request
	// itself, and neither can be expressed as a header. The upload URL is supplied by the API
	// and points at the uploads host, so there is no endpoint resolution to delegate here.
	// TODO(api-client-rollout)
	// This line of code is part of a mechanical roll out of the api client.
	// As a follow up, consider whether the api client can be injected to this call site, rather than constructed
	resp, err := api.NewClientFromHTTP(httpClient).DoRequest(req)
	if err != nil {
		// Only transport failures are retryable as network errors. A response that arrived and
		// carried a failing status is reported as is, so shouldRetry can judge it by status.
		if _, ok := errors.AsType[api.HTTPError](err); ok {
			return nil, err
		}
		return nil, errNetwork{err}
	}
	defer resp.Body.Close()

	var newAsset ReleaseAsset
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&newAsset); err != nil {
		return nil, err
	}

	return &newAsset, nil
}

func deleteAsset(ctx context.Context, httpClient *http.Client, hostname string, assetURL safeurl.SafeURL) error {
	// The asset URL is supplied by the API, so it is absolute and requested as given.
	// TODO(api-client-rollout)
	// This line of code is part of a mechanical roll out of the api client.
	// As a follow up, consider whether the api client can be injected to this call site, rather than constructed
	resp, err := api.NewClientFromHTTP(httpClient).RequestWithContext(ctx, hostname, http.MethodDelete, assetURL.String(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}
