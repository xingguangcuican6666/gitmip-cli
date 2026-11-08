package status

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API and no search endpoint that takes GitHub's query
// language, so the three lists `gm pr status` prints are assembled from Gitea's
// own endpoints.
//
// GitHub answers the whole page in one aliased query. Here each list is its own
// request, because Gitea's issue search takes booleans about the authenticated
// viewer rather than a query string.
//
// The mapping:
//
//   - created by the viewer: GET /repos/issues/search with created=true.
//   - review requested from the viewer: the same endpoint with
//     review_requested=true. Gitea takes that flag on the cross-repository search
//     only, not on the per-repository issue list.
//   - the pull request for the current branch: GET /pulls, because Gitea filters
//     that list by base branch and not by head branch, so the head is matched
//     locally.
//
// Each search row is an issue, not a pull request, so a row whose pull_request
// field is empty is skipped. Numbers are the same either way: Gitea keeps issues
// and pull requests in one index space.

// giteaStatusSearchRow is the part of Gitea's Issue that pr status reads.
//
// The pull_request field is the discriminator: Gitea sends PullRequestMeta only
// when the issue is a pull request, so a row with no pull_request object is an
// issue and is skipped.
type giteaStatusSearchRow struct {
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	HTMLURL   string `json:"html_url"`
	UpdatedAt string `json:"updated_at"`

	PullRequest *struct {
		Draft    bool   `json:"draft"`
		HTMLURL  string `json:"html_url"`
		Merged   bool   `json:"merged"`
		MergedAt string `json:"merged_at"`
	} `json:"pull_request"`
}

// searchIssuesForStatus fetches the pull requests in repo that login created or
// that ask login for a review.
//
// Gitea's viewer booleans (created, review_requested) only mean something to an
// authenticated caller, and the cross-repository search silently ignores them
// when nobody is authenticated: an anonymous request with created=true returns
// every pull request in the owner's repositories rather than none. So the
// filters here are explicit rather than viewer-relative.
//
// Created uses the per-repository issue list, whose created_by takes a login and
// is exact. A review request has no per-repository counterpart, so that one stays
// on the cross-repository search, which does take review_requested and does
// require authentication.
func searchIssuesForStatus(client *api.Client, repo ghrepo.Interface, login string, reviewRequested bool, limit int) ([]api.PullRequest, error) {
	query := url.Values{}
	query.Set("type", "pulls")
	query.Set("state", "all")
	query.Set("limit", strconv.Itoa(limit))

	path := fmt.Sprintf("repos/%s/%s/issues", repo.RepoOwner(), repo.RepoName())
	if reviewRequested {
		path = "repos/issues/search"
		query.Set("owner", repo.RepoOwner())
		query.Set("review_requested", "true")
	} else {
		query.Set("created_by", login)
	}

	var rows []giteaStatusSearchRow
	if err := client.REST(repo.RepoHost(), http.MethodGet, path+"?"+query.Encode(), nil, &rows); err != nil {
		return nil, err
	}

	pullRequests := make([]api.PullRequest, 0, len(rows))
	for _, row := range rows {
		if row.PullRequest == nil {
			continue
		}

		// The row is an issue, so its state says closed for a merged pull
		// request. The pull_request object does report merged, which is what makes
		// the row MERGED here without a second request.
		state := strings.ToUpper(row.State)
		if row.PullRequest.Merged {
			state = "MERGED"
		}

		pullRequests = append(pullRequests, api.PullRequest{
			Number:    int(row.Number),
			Title:     row.Title,
			URL:       row.HTMLURL,
			State:     state,
			Closed:    state != "OPEN",
			IsDraft:   row.PullRequest.Draft,
			UpdatedAt: parseStatusTime(row.UpdatedAt),
		})
	}

	return pullRequests, nil
}

// pullRequestStatusGitea builds the same payload the GraphQL path returns.
func pullRequestStatusGitea(httpClient *http.Client, repo ghrepo.Interface, options requestOptions) (*pullRequestsPayload, error) {
	client := api.NewClientFromHTTP(httpClient)

	const listLimit = 10

	// Both lists are about the viewer, so an anonymous caller has none of either.
	// Looking up the login also decides whether the review-request query is worth
	// sending at all: Gitea answers it with an error for an unauthenticated
	// caller.
	login, loginErr := api.CurrentLoginName(client, repo.RepoHost())
	if loginErr != nil {
		var httpErr api.HTTPError
		if !errors.As(loginErr, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
			return nil, loginErr
		}
		login = ""
	}

	var created, reviewRequested []api.PullRequest
	if login != "" {
		var err error
		created, err = searchIssuesForStatus(client, repo, login, false, listLimit)
		if err != nil {
			return nil, err
		}

		reviewRequested, err = searchIssuesForStatus(client, repo, login, true, listLimit)
		if err != nil {
			return nil, err
		}
	}

	defaultBranch, err := api.RepoDefaultBranch(client, repo)
	if err != nil {
		return nil, err
	}

	currentPR, err := currentPullRequestGitea(client, repo, options)
	if err != nil {
		return nil, err
	}

	return &pullRequestsPayload{
		ViewerCreated: api.PullRequestAndTotalCount{
			PullRequests: created,
			TotalCount:   len(created),
		},
		ReviewRequested: api.PullRequestAndTotalCount{
			PullRequests: reviewRequested,
			TotalCount:   len(reviewRequested),
		},
		CurrentPR:     currentPR,
		DefaultBranch: defaultBranch,
	}, nil
}

// currentPullRequestGitea finds the pull request for the branch gm resolved.
//
// Gitea's list filters by base branch, not by head, so the head has to be matched
// locally. The first row whose head ref matches is the one reported, which is
// what taking the newest pull request for a branch does on GitHub.
func currentPullRequestGitea(client *api.Client, repo ghrepo.Interface, options requestOptions) (*api.PullRequest, error) {
	if options.CurrentPR > 0 {
		return statusPullRequest(client, repo, options.CurrentPR, options.Fields)
	}

	if options.HeadRef == "" {
		return nil, nil
	}

	wanted := options.HeadRef
	if _, after, ok := strings.Cut(wanted, ":"); ok {
		wanted = after
	}

	prs, _, err := api.ListPullRequestsGitea(client, repo, api.ListPullRequestsGiteaInput{
		State: "all",
		Page:  1,
	})
	if err != nil {
		return nil, err
	}

	for i := range prs {
		if prs[i].HeadRefName != wanted {
			continue
		}
		return statusPullRequest(client, repo, prs[i].Number, options.Fields)
	}

	return nil, nil
}

// statusPullRequest loads one pull request and the parts of it the status view
// reads that Gitea serves separately.
func statusPullRequest(client *api.Client, repo ghrepo.Interface, number int, fields []string) (*api.PullRequest, error) {
	pr, err := api.FetchPullRequestGitea(client, repo, number)
	if err != nil {
		return nil, err
	}
	if err := preloadStatusFields(client, repo, pr, fields); err != nil {
		return nil, err
	}
	return pr, nil
}

// preloadStatusFields fills the check rollup and the latest reviews, which Gitea
// serves from their own endpoints.
//
// The caller's --json field list is what decides this, so a plain `gm pr status`
// with no field selection loads neither and costs no extra requests.
func preloadStatusFields(client *api.Client, repo ghrepo.Interface, pr *api.PullRequest, fields []string) error {
	for _, f := range fields {
		switch f {
		case "statusCheckRollup", "statusCheckRollupWithCountByState":
			contexts, err := api.PullRequestStatusChecksGitea(client, repo, pr.HeadRefOid)
			if err != nil {
				return err
			}
			pr.StatusCheckRollup.Nodes = append(pr.StatusCheckRollup.Nodes, api.StatusCheckRollupNode{
				Commit: api.StatusCheckRollupCommit{
					StatusCheckRollup: api.CommitStatusCheckRollup{Contexts: contexts},
				},
			})
		case "latestReviews":
			reviews, err := api.PullRequestReviewsGitea(client, repo, pr.Number)
			if err != nil {
				return err
			}
			pr.LatestReviews = reviews
		}
	}
	return nil
}

// parseStatusTime reads an RFC 3339 timestamp, leaving the zero time for anything
// unparseable or absent.
func parseStatusTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
