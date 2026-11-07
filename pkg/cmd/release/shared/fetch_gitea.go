package shared

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

// Gitea has no GraphQL API, which is what GitHub uses to find a draft release
// by its pending tag name. A draft has no tag yet, so the tags endpoint cannot
// answer for one, and Gitea offers no query by draft tag either.
//
// Drafts are therefore found by listing them. GET /releases?draft=true returns
// only drafts, and each carries a tag_name, so the pending tag is matched
// locally. This costs one extra request over GitHub's path and only sees the
// drafts the token has access to, which matches what Gitea itself shows.

// giteaReleasePageSize is the per-page maximum gitea.com enforces.
const giteaReleasePageSize = 50

// errImmutableReleaseURL signals that safeurl returned its immutable variant,
// which cannot take query parameters. JoinPath never returns it; the check
// exists so a future change to the URL construction fails loudly instead of
// silently dropping the pagination parameters.
var errImmutableReleaseURL = errors.New("internal error: expected a mutable URL")

func fetchReleaseGitea(ctx context.Context, httpClient *http.Client, repo ghrepo.Interface, tagName string) (*Release, error) {
	publishedPath, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "releases", "tags", tagName)
	if err != nil {
		return nil, err
	}

	release, publishedErr := fetchReleaseGiteaPath(ctx, httpClient, repo, publishedPath)
	if publishedErr == nil {
		return release, nil
	}

	// A failed published lookup is only worth reporting if the draft lookup
	// also fails, so the draft path runs regardless of why the first one did.
	draft, draftErr := fetchDraftReleaseGitea(ctx, httpClient, repo, tagName)
	if draftErr == nil {
		return draft, nil
	}

	if isNotFound(publishedErr) {
		return nil, ErrReleaseNotFound
	}
	return nil, publishedErr
}

// fetchDraftReleaseGitea finds the draft release whose pending tag is tagName.
func fetchDraftReleaseGitea(ctx context.Context, httpClient *http.Client, repo ghrepo.Interface, tagName string) (*Release, error) {
	basePath, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "releases")
	if err != nil {
		return nil, err
	}

	client := api.NewClientFromHTTP(httpClient)

	for page := 1; ; page++ {
		pagePath, err := withGiteaDraftQuery(basePath, page)
		if err != nil {
			return nil, err
		}

		// Decoded into the Gitea shape rather than into Release, so the assets
		// are mapped and each one gets an API URL.
		var drafts []giteaRelease
		if err := client.REST(repo.RepoHost(), http.MethodGet, pagePath.String(), nil, &drafts); err != nil {
			return nil, err
		}

		for _, raw := range drafts {
			if raw.IsDraft && raw.TagName == tagName {
				return mapGiteaRelease(raw), nil
			}
		}

		if len(drafts) < giteaReleasePageSize {
			break
		}
	}

	return nil, ErrReleaseNotFound
}

func withGiteaDraftQuery(base safeurl.SafeURL, page int) (safeurl.SafeURL, error) {
	mutable, ok := base.(*safeurl.MutableSafeURL)
	if !ok {
		return nil, errImmutableReleaseURL
	}
	// Gitea's draft parameter is an inclusion filter, so "true" narrows the
	// list to drafts rather than deciding whether to show them.
	mutable.SetQuery("draft", "true")
	mutable.SetQuery("limit", strconv.Itoa(giteaReleasePageSize))
	mutable.SetQuery("page", strconv.Itoa(page))
	return mutable, nil
}

// giteaAsset is Gitea's Attachment.
//
// It carries a numeric id and no url, where GitHub's asset carries both a node_id
// and an API url. gh's shared ReleaseAsset reads GitHub's spellings, so decoding a
// Gitea release into it directly would leave every asset unaddressable, and the
// upload and delete paths have nothing to send.
type giteaAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	DownloadCount      int    `json:"download_count"`
	CreatedAt          string `json:"created_at"`
	UUID               string `json:"uuid"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// giteaRelease is Gitea's Release, decoded so its assets can be mapped.
type giteaRelease struct {
	DatabaseID      int64        `json:"id"`
	TagName         string       `json:"tag_name"`
	Name            string       `json:"name"`
	Body            string       `json:"body"`
	IsDraft         bool         `json:"draft"`
	IsPrerelease    bool         `json:"prerelease"`
	CreatedAt       string       `json:"created_at"`
	PublishedAt     string       `json:"published_at"`
	TargetCommitish string       `json:"target_commitish"`
	APIURL          string       `json:"url"`
	UploadURL       string       `json:"upload_url"`
	TarballURL      string       `json:"tarball_url"`
	ZipballURL      string       `json:"zipball_url"`
	URL             string       `json:"html_url"`
	Assets          []giteaAsset `json:"assets"`
	Author          struct {
		Login string `json:"login"`
	} `json:"author"`
}

// fetchReleaseGiteaPath reads one release and maps its assets onto gh's shape.
func fetchReleaseGiteaPath(ctx context.Context, httpClient *http.Client, repo ghrepo.Interface, path safeurl.SafeURL) (*Release, error) {
	client := api.NewClientFromHTTP(httpClient)

	resp, err := client.RequestWithContext(ctx, repo.RepoHost(), http.MethodGet, path.String(), nil)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrReleaseNotFound
		}
		return nil, err
	}
	defer resp.Body.Close()

	var raw giteaRelease
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	return mapGiteaRelease(raw), nil
}

// mapGiteaRelease turns a Gitea release into gh's Release.
//
// Each asset's API URL is built from the release's own URL and the asset's id,
// which is the address Gitea's per-asset endpoints take.
func mapGiteaRelease(raw giteaRelease) *Release {
	release := &Release{
		DatabaseID:      raw.DatabaseID,
		ID:              strconv.FormatInt(raw.DatabaseID, 10),
		TagName:         raw.TagName,
		Name:            raw.Name,
		Body:            raw.Body,
		IsDraft:         raw.IsDraft,
		IsPrerelease:    raw.IsPrerelease,
		TargetCommitish: raw.TargetCommitish,
		APIURL:          raw.APIURL,
		UploadURL:       raw.UploadURL,
		TarballURL:      raw.TarballURL,
		ZipballURL:      raw.ZipballURL,
		URL:             raw.URL,
	}
	release.Author.Login = raw.Author.Login
	release.Author.ID = ""

	if raw.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, raw.CreatedAt); err == nil {
			release.CreatedAt = t
		}
	}
	if raw.PublishedAt != "" {
		if t, err := time.Parse(time.RFC3339, raw.PublishedAt); err == nil {
			release.PublishedAt = &t
		}
	}

	for _, a := range raw.Assets {
		release.Assets = append(release.Assets, ReleaseAsset{
			ID:                 strconv.FormatInt(a.ID, 10),
			Name:               a.Name,
			Size:               a.Size,
			DownloadCount:      a.DownloadCount,
			BrowserDownloadURL: a.BrowserDownloadURL,
			APIURL:             giteaAssetURL(raw.APIURL, a.ID),
			CreatedAt:          parseGiteaReleaseTime(a.CreatedAt),
		})
	}

	return release
}

// giteaAssetURL builds the per-asset API URL from the release's.
func giteaAssetURL(releaseURL string, id int64) string {
	if releaseURL == "" {
		return ""
	}
	return releaseURL + "/assets/" + strconv.FormatInt(id, 10)
}

func parseGiteaReleaseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

// DecodeGiteaReleaseFromURL performs a request and decodes Gitea's release
// response onto gh's shape.
//
// It exists so the write paths, which hold a URL they built themselves rather than
// a repository, can map the response the same way the read paths do.
func DecodeGiteaReleaseFromURL(httpClient *http.Client, host, method string, path safeurl.SafeURL, body io.Reader) (*Release, error) {
	client := api.NewClientFromHTTP(httpClient)

	resp, err := client.Request(host, method, path.String(), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return decodeGiteaReleaseBody(host, resp)
}

// DecodeGiteaRelease performs the request fn and decodes its response.
func DecodeGiteaRelease(httpClient *http.Client, repo ghrepo.Interface, fn func() (*http.Response, error)) (*Release, error) {
	resp, err := fn()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return decodeGiteaReleaseBody(repo.RepoHost(), resp)
}

// decodeGiteaReleaseBody reads a release response onto gh's shape.
//
// An empty body is not an error: Gitea answers some writes with 204, and the
// caller then has no release to print, which it reports rather than inventing.
func decodeGiteaReleaseBody(host string, resp *http.Response) (*Release, error) {
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	var raw giteaRelease
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}

	return mapGiteaRelease(raw), nil
}
