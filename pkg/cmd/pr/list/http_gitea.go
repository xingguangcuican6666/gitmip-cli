package list

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/cli/cli/v2/api"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
)

// Gitea has no GraphQL API and no search endpoint for pull requests, so listing
// is answered here by walking GET /repos/{o}/{r}/pulls.
//
// Gitea filters server-side by state, base branch and author. Everything else gh
// exposes is filtered locally: head branch, draft, assignee, label and search
// text. Local filtering over pages means the limit counts matches rather than
// rows fetched, so a repository whose open pull requests mostly fail the filter
// costs several requests to fill a short list. That is what makes the filter
// correct rather than approximate; silently ignoring it would be the cheaper
// mistake.
//
// `--state merged` is a state GitHub has and Gitea does not. Gitea reports a
// merged pull request as closed with merged set, so the mapping derives MERGED
// and this filters on it locally.
//
// Search is matched against the title and body only. Gitea has no query syntax,
// so qualifiers such as `draft:true` or `review:approved` would be treated as
// text and match nothing; they are rejected rather than silently dropped.

// giteaPrSearchQualifier matches the `key:value` forms GitHub's search accepts.
// Gitea has no equivalent grammar and would treat such a token as text to find.
var giteaPrSearchQualifier = regexp.MustCompile(`(?:^|\s)[a-z-]+:`)

// listPullRequestsGitea returns the pull requests matching filters.
func listPullRequestsGitea(httpClient *http.Client, repo ghrepo.Interface, filters prShared.FilterOptions, limit int) (*api.PullRequestAndTotalCount, error) {
	if len(filters.Labels) > 1 {
		return nil, fmt.Errorf("Gitea filters pull request labels by id and matches them with OR, so filtering by more than one `--label` is not equivalent to GitHub; pass a single label")
	}
	if filters.Search != "" && giteaPrSearchQualifier.MatchString(filters.Search) {
		return nil, fmt.Errorf("Gitea does not support search qualifiers in `--search`; it matches title and body text only")
	}
	if filters.Assignee != "" && strings.EqualFold(filters.Assignee, "@me") {
		// @me is resolved by the caller on GitHub. Gitea's list has no assignee
		// parameter at all, so the filter is local and needs the login.
		return nil, fmt.Errorf("`--assignee @me` is not supported on Gitea; pass the login")
	}

	client := api.NewClientFromHTTP(httpClient)

	state := giteaPullRequestState(filters.State)

	var matches []api.PullRequest
	total := 0

	for page := 1; len(matches) < limit; page++ {
		raw, pageTotal, err := api.ListPullRequestsGitea(client, repo, api.ListPullRequestsGiteaInput{
			State:      state,
			BaseBranch: filters.BaseBranch,
			Author:     filters.Author,
			Page:       page,
		})
		if err != nil {
			return nil, err
		}
		if page == 1 {
			total = pageTotal
		}
		if len(raw) == 0 {
			break
		}

		for i := range raw {
			pr := raw[i]
			if !giteaPullRequestMatches(pr, filters) {
				continue
			}
			matches = append(matches, pr)
			if len(matches) >= limit {
				break
			}
		}

		if len(raw) < api.GiteaPageSize {
			break
		}
	}

	// The header reports a total, and a locally filtered list cannot claim the
	// server's unfiltered count: it would say "19 pull requests" next to four
	// rows. The number of matches is what the user sees, so that is what is
	// reported.
	if filtersLocallyApplied(filters) {
		total = len(matches)
	}

	return &api.PullRequestAndTotalCount{PullRequests: matches, TotalCount: total}, nil
}

// giteaPullRequestState maps gh's state filter onto Gitea's. Gitea has open,
// closed and all; merged is derived, so asking for merged means asking for all
// and filtering locally.
func giteaPullRequestState(state string) string {
	switch strings.ToLower(state) {
	case "open", "":
		return "open"
	case "closed":
		return "closed"
	case "merged", "all":
		return "all"
	default:
		return strings.ToLower(state)
	}
}

// giteaPullRequestMatches applies the filters Gitea cannot.
//
// The merged check reads filters.State rather than the state sent to Gitea,
// which is "all" for a merged request: Gitea has no merged state of its own, so
// the derivation on the mapped pull request is what decides it.
func giteaPullRequestMatches(pr api.PullRequest, filters prShared.FilterOptions) bool {
	if strings.EqualFold(filters.State, "merged") && pr.State != "MERGED" {
		return false
	}

	if filters.HeadBranch != "" && pr.HeadRefName != filters.HeadBranch {
		return false
	}

	if filters.Draft != nil && pr.IsDraft != *filters.Draft {
		return false
	}

	if filters.Assignee != "" && !giteaHasAssignee(pr, filters.Assignee) {
		return false
	}

	if len(filters.Labels) == 1 && !giteaHasLabel(pr, filters.Labels[0]) {
		return false
	}

	if filters.Search != "" && !giteaTextMatches(pr, filters.Search) {
		return false
	}

	return true
}

func giteaHasAssignee(pr api.PullRequest, login string) bool {
	for _, a := range pr.Assignees.Nodes {
		if strings.EqualFold(a.Login, login) {
			return true
		}
	}
	return false
}

func giteaHasLabel(pr api.PullRequest, name string) bool {
	for _, l := range pr.Labels.Nodes {
		if strings.EqualFold(l.Name, name) {
			return true
		}
	}
	return false
}

func giteaTextMatches(pr api.PullRequest, search string) bool {
	return strings.Contains(strings.ToLower(pr.Title), strings.ToLower(search)) ||
		strings.Contains(strings.ToLower(pr.Body), strings.ToLower(search))
}

// filtersLocallyApplied reports whether anything had to be filtered after the
// request, which is when the server's total no longer describes the result.
func filtersLocallyApplied(filters prShared.FilterOptions) bool {
	return filters.HeadBranch != "" ||
		filters.Draft != nil ||
		filters.Assignee != "" ||
		len(filters.Labels) > 0 ||
		filters.Search != "" ||
		strings.EqualFold(filters.State, "merged")
}

// listPullRequestsDispatch keeps the host check at the top of the list path, so
// the search-versus-list split below stays GitHub's.
func listPullRequestsDispatch(httpClient *http.Client, detector fd.Detector, repo ghrepo.Interface, filters prShared.FilterOptions, limit int) (*api.PullRequestAndTotalCount, error) {
	if hosttype.IsGitea(repo.RepoHost()) {
		return listPullRequestsGitea(httpClient, repo, filters, limit)
	}
	return listPullRequests(httpClient, detector, repo, filters, limit)
}
