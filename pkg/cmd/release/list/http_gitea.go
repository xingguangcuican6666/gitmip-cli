package list

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
)

// Gitea has no GraphQL API, so releases come from GET /repos/{owner}/{repo}/releases.
// Three things differ from the GraphQL path.
//
// Gitea returns releases newest first and has no order parameter, so
// `--order asc` is applied by reversing the collected result once the limit is
// reached. Reversing a page at a time would not work: the limit has to be taken
// from the newest end first.
//
// Gitea has no notion of a "latest" release, so it is computed: the newest
// release that is neither a draft nor a prerelease, counting only the releases
// in scope after filtering. That is the same rule GitHub documents, but it is
// derived from the page rather than read from the server.
//
// Gitea reports no immutability and no createdAt separate from publishedAt, so
// isImmutable stays false and createdAt is the publication time.

// giteaReleasePageSize is the per-page maximum gitea.com enforces.
const giteaReleasePageSize = 50

// errImmutableSafeURL signals that safeurl returned its immutable variant,
// which cannot take query parameters. JoinPath never returns it; the check
// exists so a future change to the URL construction fails loudly instead of
// silently dropping the pagination parameters.
var errImmutableSafeURL = errors.New("internal error: expected a mutable URL")

// giteaRelease is the subset of Gitea's Release schema listing needs.
type giteaRelease struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	TagName     string `json:"tag_name"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
	CreatedAt   string `json:"created_at"`
}

func fetchReleasesGitea(httpClient *http.Client, repo ghrepo.Interface, limit int, excludeDrafts bool, excludePreReleases bool, order string) ([]Release, error) {
	apiClient := api.NewClientFromHTTP(httpClient)

	basePath, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "releases")
	if err != nil {
		return nil, err
	}

	// Gitea's draft and pre-release filters are inclusion filters, not the
	// GitHub-style exclusion they read like: draft=true returns only drafts.
	// Excluding therefore means passing false, and including everything means
	// omitting the parameter entirely, which was confirmed against a live
	// Gitea where draft=true returned an empty list for a repository holding
	// published releases.
	query := url.Values{}
	if excludeDrafts {
		query.Set("draft", "false")
	}
	if excludePreReleases {
		query.Set("pre-release", "false")
	}

	var raw []giteaRelease
	for page := 1; limit < 0 || len(raw) < limit; page++ {
		query.Set("limit", strconv.Itoa(giteaReleasePageSize))
		query.Set("page", strconv.Itoa(page))

		pageURL, err := withReleaseQuery(basePath, query)
		if err != nil {
			return nil, err
		}

		var pageResults []giteaRelease
		if err := apiClient.REST(repo.RepoHost(), http.MethodGet, pageURL.String(), nil, &pageResults); err != nil {
			return nil, err
		}

		raw = append(raw, pageResults...)

		if len(pageResults) < giteaReleasePageSize {
			break
		}
	}

	if limit >= 0 && len(raw) > limit {
		raw = raw[:limit]
	}

	releases := make([]Release, 0, len(raw))
	for _, r := range raw {
		releases = append(releases, Release{
			Name:         r.Name,
			TagName:      r.TagName,
			IsDraft:      r.Draft,
			IsPrerelease: r.Prerelease,
			CreatedAt:    parseGiteaReleaseTime(r.CreatedAt),
			PublishedAt:  parseGiteaReleaseTime(r.PublishedAt),
		})
	}

	// Gitea returns newest first. Marking the latest release before reversing
	// keeps the flag on the newest eligible release regardless of output order.
	markGiteaLatestRelease(releases)

	if strings.EqualFold(order, "asc") {
		reverseReleases(releases)
	}

	return releases, nil
}

// markGiteaLatestRelease flags the newest release that is neither a draft nor a
// prerelease. Releases arrive newest first, so the first match is the latest.
func markGiteaLatestRelease(releases []Release) {
	for i := range releases {
		if !releases[i].IsDraft && !releases[i].IsPrerelease {
			releases[i].IsLatest = true
			return
		}
	}
}

func reverseReleases(releases []Release) {
	for i, j := 0, len(releases)-1; i < j; i, j = i+1, j-1 {
		releases[i], releases[j] = releases[j], releases[i]
	}
}

func withReleaseQuery(base safeurl.SafeURL, query url.Values) (safeurl.SafeURL, error) {
	mutable, ok := base.(*safeurl.MutableSafeURL)
	if !ok {
		return nil, errImmutableSafeURL
	}
	for key, values := range query {
		for _, value := range values {
			mutable.SetQuery(key, value)
		}
	}
	return mutable, nil
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
