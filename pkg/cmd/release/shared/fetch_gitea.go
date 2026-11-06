package shared

import (
	"context"
	"errors"
	"net/http"
	"strconv"

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

	release, publishedErr := fetchReleasePath(ctx, httpClient, repo.RepoHost(), publishedPath)
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

		var drafts []Release
		if err := client.REST(repo.RepoHost(), http.MethodGet, pagePath.String(), nil, &drafts); err != nil {
			return nil, err
		}

		for i := range drafts {
			if drafts[i].IsDraft && drafts[i].TagName == tagName {
				return &drafts[i], nil
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
