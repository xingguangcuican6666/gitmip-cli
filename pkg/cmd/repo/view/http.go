package view

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/go-gh/v2/pkg/asciisanitizer"
	"golang.org/x/text/transform"
)

var NotFoundError = errors.New("not found")

type RepoReadme struct {
	Filename string
	Content  string
	BaseURL  string
}

func RepositoryReadme(client *http.Client, repo ghrepo.Interface, branch string) (*RepoReadme, error) {
	if hosttype.IsGitea(repo.RepoHost()) {
		return giteaRepositoryReadme(client, repo, branch)
	}

	apiClient := api.NewClientFromHTTP(client)
	var response struct {
		Name    string
		Content string
		HTMLURL string `json:"html_url"`
	}

	readmePath, err := getReadmePath(repo, branch)
	if err != nil {
		return nil, err
	}

	err = apiClient.REST(repo.RepoHost(), "GET", readmePath.String(), nil, &response)
	if err != nil {
		var httpError api.HTTPError
		if errors.As(err, &httpError) && httpError.StatusCode == 404 {
			return nil, NotFoundError
		}
		return nil, err
	}

	decoded, err := base64.StdEncoding.DecodeString(response.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to decode readme: %w", err)
	}

	return newRepoReadme(response.Name, decoded, response.HTMLURL)
}

// giteaContentEntry is the subset of Gitea's ContentsResponse schema this
// package reads.
type giteaContentEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	HTMLURL  string `json:"html_url"`
}

// giteaRepositoryReadme finds the repository README through Gitea's contents
// API, which has no /readme endpoint.
//
// The root directory is listed with GET /contents and the first entry whose
// name matches a README convention is read. GitHub's own precedence is roughly
// README.md, README.rst, README, README.txt, then the same names in lower case;
// the match here is case-insensitive on the "readme" stem, which covers the
// conventions Gitea itself renders.
func giteaRepositoryReadme(client *http.Client, repo ghrepo.Interface, branch string) (*RepoReadme, error) {
	apiClient := api.NewClientFromHTTP(client)

	listPath, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "contents")
	if err != nil {
		return nil, err
	}
	if branch != "" {
		listPath.SetQuery("ref", branch)
	}

	var entries []giteaContentEntry
	if err := apiClient.REST(repo.RepoHost(), "GET", listPath.String(), nil, &entries); err != nil {
		var httpError api.HTTPError
		if errors.As(err, &httpError) && httpError.StatusCode == 404 {
			return nil, NotFoundError
		}
		return nil, err
	}

	entry, ok := pickReadme(entries)
	if !ok {
		return nil, NotFoundError
	}

	filePath, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "contents", entry.Path)
	if err != nil {
		return nil, err
	}
	if branch != "" {
		filePath.SetQuery("ref", branch)
	}

	var file giteaContentEntry
	if err := apiClient.REST(repo.RepoHost(), "GET", filePath.String(), nil, &file); err != nil {
		return nil, err
	}

	decoded, err := decodeGiteaContents(file.Content, file.Encoding)
	if err != nil {
		return nil, fmt.Errorf("failed to decode readme: %w", err)
	}

	return newRepoReadme(file.Name, decoded, file.HTMLURL)
}

// pickReadme returns the entry most likely to be the rendered README. Exact
// stem matches rank above prefixed names such as README_CN.md, so a repository
// with both shows the primary one.
func pickReadme(entries []giteaContentEntry) (giteaContentEntry, bool) {
	var fallback giteaContentEntry
	var haveFallback bool

	for _, e := range entries {
		if e.Type != "file" {
			continue
		}
		stem := strings.ToLower(strings.TrimSuffix(e.Name, extensionOf(e.Name)))
		if stem == "readme" {
			return e, true
		}
		if strings.HasPrefix(stem, "readme") && !haveFallback {
			fallback, haveFallback = e, true
		}
	}

	return fallback, haveFallback
}

func extensionOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

// decodeGiteaContents returns the file bytes from a contents response. Gitea
// base64-encodes file content the same way GitHub does.
func decodeGiteaContents(content, encoding string) ([]byte, error) {
	if content == "" {
		return nil, nil
	}
	if encoding != "" && !strings.EqualFold(encoding, "base64") {
		return nil, fmt.Errorf("unsupported content encoding %q", encoding)
	}
	// Gitea wraps base64 at 60 columns, which StdEncoding tolerates only once
	// the newlines are removed.
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(content, "\n", ""))
}

func newRepoReadme(name string, decoded []byte, baseURL string) (*RepoReadme, error) {
	sanitized, err := io.ReadAll(transform.NewReader(bytes.NewReader(decoded), &asciisanitizer.Sanitizer{}))
	if err != nil {
		return nil, err
	}

	return &RepoReadme{
		Filename: name,
		Content:  string(sanitized),
		BaseURL:  baseURL,
	}, nil
}

func getReadmePath(repo ghrepo.Interface, branch string) (safeurl.SafeURL, error) {
	path, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "readme")
	if err != nil {
		return nil, err
	}
	if branch != "" {
		path.SetQuery("ref", branch)
	}
	return path, nil
}
