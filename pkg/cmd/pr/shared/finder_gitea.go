package shared

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/set"
	"golang.org/x/sync/errgroup"
)

// Gitea has no GraphQL API, so the pull request finder is answered here from
// REST.
//
// Two of the things the GraphQL finder does in one query need more than one
// request here. Finding a pull request by number is a single GET. Finding one by
// branch has to page through GET /repos/{o}/{r}/pulls, because Gitea's list takes
// a base_branch filter and no head_branch filter, so the head is matched locally.
//
// The same preloads apply as on GitHub: reviews, comments and checks are separate
// endpoints, so they are fetched only when a caller asked for the field. Closing
// issue references and project items have no counterpart and are left empty.

// findByNumberGitea returns one pull request by index.
func findByNumberGitea(httpClient *http.Client, repo ghrepo.Interface, number int) (*api.PullRequest, error) {
	return api.FetchPullRequestGitea(api.NewClientFromHTTP(httpClient), repo, number)
}

// findForRefsGitea finds the pull request whose head branch matches prRefs.
//
// Gitea's list endpoint filters by base branch and by state but not by head
// branch, so pages are walked and the head matched locally. The walk stops as
// soon as a match is found.
//
// The list rows are the same object GET /pulls/{index} returns, so the match is
// returned directly instead of being fetched a second time. What those rows do
// not carry is the diff counts: additions, deletions and changedFiles are zero
// until something asks for them.
func findForRefsGitea(httpClient *http.Client, prRefs PRFindRefs, stateFilters []string) (*api.PullRequest, error) {
	client := api.NewClientFromHTTP(httpClient)
	baseRepo := prRefs.BaseRepo()

	// Gitea's list is one state at a time and knows only open and closed, with no
	// merged state of its own. "all" covers every state the caller might have
	// asked for, and the filter is applied locally so a caller asking for open
	// only does not get a closed pull request.
	wanted := map[string]bool{}
	for _, s := range stateFilters {
		wanted[strings.ToUpper(s)] = true
	}
	if len(wanted) == 0 {
		wanted["OPEN"] = true
	}
	allStates := len(wanted) > 1

	// The default branch matters for the same reason it does on GitHub: when the
	// head is the default branch, a merged or closed pull request is not what the
	// caller means by "the pull request for this branch".
	defaultBranch, err := api.RepoDefaultBranch(client, baseRepo)
	if err != nil {
		return nil, err
	}

	for page := 1; ; page++ {
		raw, _, err := api.ListPullRequestsGitea(client, baseRepo, api.ListPullRequestsGiteaInput{
			State:      giteaFinderState(allStates),
			BaseBranch: prRefs.BaseBranchName(),
			Page:       page,
		})
		if err != nil {
			return nil, err
		}

		if len(raw) == 0 {
			break
		}

		for i := range raw {
			pr := raw[i]

			if !wanted[pr.State] {
				continue
			}
			if !prRefs.Matches(pr.BaseRefName, pr.HeadLabel()) {
				continue
			}
			if pr.State != "OPEN" && defaultBranch == prRefs.QualifiedHeadRef() {
				continue
			}

			return &pr, nil
		}

		if len(raw) < api.GiteaPageSize {
			break
		}
	}

	return nil, &NotFoundError{fmt.Errorf("no pull requests found for branch %q", prRefs.QualifiedHeadRef())}
}

// giteaFinderState picks the state filter for the list request. Gitea takes one
// state per request, so a caller asking for more than one gets "all" and the
// filter is applied locally.
func giteaFinderState(allStates bool) string {
	if allStates {
		return "all"
	}
	return "open"
}

// preloadGiteaPullRequest fetches the parts of a pull request that Gitea serves
// from their own endpoints, for the fields a caller asked for.
func preloadGiteaPullRequest(httpClient *http.Client, repo ghrepo.Interface, pr *api.PullRequest, fields []string) error {
	client := api.NewClientFromHTTP(httpClient)
	wanted := set.NewStringSet()
	wanted.AddValues(fields)

	var g errgroup.Group

	if wanted.Contains("reviews") || wanted.Contains("latestReviews") {
		g.Go(func() error {
			reviews, err := api.PullRequestReviewsGitea(client, repo, pr.Number)
			if err != nil {
				return err
			}
			pr.Reviews = reviews
			pr.LatestReviews = reviews
			return nil
		})
	}

	if wanted.Contains("comments") {
		g.Go(func() error {
			comments, err := api.IssueCommentsGitea(client, repo, pr.Number)
			if err != nil {
				return err
			}
			pr.Comments = *comments
			return nil
		})
	}

	if wanted.Contains("files") {
		g.Go(func() error {
			files, err := api.PullRequestFilesGitea(client, repo, pr.Number)
			if err != nil {
				return err
			}
			pr.Files.Nodes = files
			return nil
		})
	}

	if wanted.Contains("commits") {
		g.Go(func() error {
			commits, total, err := api.PullRequestCommitsGitea(client, repo, pr.Number)
			if err != nil {
				return err
			}
			pr.Commits.Nodes = commits
			pr.Commits.TotalCount = total
			return nil
		})
	}

	return g.Wait()
}

// giteaHost reports whether repo is on a Gitea host.
func giteaHost(repo ghrepo.Interface) bool {
	return hosttype.IsGitea(repo.RepoHost())
}

// findByNumberDispatch keeps the host check in one place for the callers that
// already have a number.
func findByNumberDispatch(httpClient *http.Client, repo ghrepo.Interface, number int, fields []string) (*api.PullRequest, error) {
	if giteaHost(repo) {
		return findByNumberGitea(httpClient, repo, number)
	}
	return findByNumber(httpClient, repo, number, fields)
}

// findForRefsDispatch keeps the host check in one place for the callers that
// resolve a pull request from a branch.
func findForRefsDispatch(httpClient *http.Client, prRefs PRFindRefs, stateFilters, fields []string) (*api.PullRequest, error) {
	if giteaHost(prRefs.BaseRepo()) {
		return findForRefsGitea(httpClient, prRefs, stateFilters)
	}
	return findForRefs(httpClient, prRefs, stateFilters, fields)
}
