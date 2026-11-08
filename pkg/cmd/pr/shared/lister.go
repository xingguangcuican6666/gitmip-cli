package shared

import (
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"

	api "github.com/cli/cli/v2/api"
)

type PRLister interface {
	List(opt ListOptions) (*api.PullRequestAndTotalCount, error)
}

type ListOptions struct {
	BaseRepo ghrepo.Interface

	LimitResults int

	State      string
	BaseBranch string
	HeadBranch string

	Fields []string
}

type lister struct {
	httpClient *http.Client
}

func NewLister(httpClient *http.Client) PRLister {
	return &lister{
		httpClient: httpClient,
	}
}

func (l *lister) List(opts ListOptions) (*api.PullRequestAndTotalCount, error) {
	if hosttype.IsGitea(opts.BaseRepo.RepoHost()) {
		return listGitea(l.httpClient, opts)
	}

	type response struct {
		Repository struct {
			PullRequests struct {
				Nodes    []api.PullRequest
				PageInfo struct {
					HasNextPage bool
					EndCursor   string
				}
				TotalCount int
			}
		}
	}
	limit := opts.LimitResults
	fragment := fmt.Sprintf("fragment pr on PullRequest{%s}", api.PullRequestGraphQL(opts.Fields))
	query := fragment + `
		query PullRequestList(
			$owner: String!,
			$repo: String!,
			$limit: Int!,
			$endCursor: String,
			$baseBranch: String,
			$headBranch: String,
			$state: [PullRequestState!] = OPEN
		) {
			repository(owner: $owner, name: $repo) {
				pullRequests(
					states: $state,
					baseRefName: $baseBranch,
					headRefName: $headBranch,
					first: $limit,
					after: $endCursor,
					orderBy: {field: CREATED_AT, direction: DESC}
				) {
					totalCount
					nodes {
						...pr
					}
					pageInfo {
						hasNextPage
						endCursor
					}
				}
			}
		}`

	pageLimit := min(limit, 100)
	variables := map[string]any{
		"owner": opts.BaseRepo.RepoOwner(),
		"repo":  opts.BaseRepo.RepoName(),
	}

	switch opts.State {
	case "open":
		variables["state"] = []string{"OPEN"}
	case "closed":
		variables["state"] = []string{"CLOSED", "MERGED"}
	case "merged":
		variables["state"] = []string{"MERGED"}
	case "all":
		variables["state"] = []string{"OPEN", "CLOSED", "MERGED"}
	default:
		return nil, fmt.Errorf("invalid state: %s", opts.State)
	}

	if opts.BaseBranch != "" {
		variables["baseBranch"] = opts.BaseBranch
	}
	if opts.HeadBranch != "" {
		variables["headBranch"] = opts.HeadBranch
	}

	res := api.PullRequestAndTotalCount{}
	var check = make(map[int]struct{})
	client := api.NewClientFromHTTP(l.httpClient)

loop:
	for {
		variables["limit"] = pageLimit
		var data response
		err := client.GraphQL(opts.BaseRepo.RepoHost(), query, variables, &data)
		if err != nil {
			return nil, err
		}
		prData := data.Repository.PullRequests
		res.TotalCount = prData.TotalCount

		for _, pr := range prData.Nodes {
			if _, exists := check[pr.Number]; exists && pr.Number > 0 {
				continue
			}
			check[pr.Number] = struct{}{}

			res.PullRequests = append(res.PullRequests, pr)
			if len(res.PullRequests) == limit {
				break loop
			}
		}

		if prData.PageInfo.HasNextPage {
			variables["endCursor"] = prData.PageInfo.EndCursor
			pageLimit = min(pageLimit, limit-len(res.PullRequests))
		} else {
			break
		}
	}

	return &res, nil
}

type mockLister struct {
	called       bool
	expectFields []string

	result *api.PullRequestAndTotalCount
	err    error
}

func NewMockLister(result *api.PullRequestAndTotalCount, err error) *mockLister {
	return &mockLister{
		result: result,
		err:    err,
	}
}

func (m *mockLister) List(opt ListOptions) (*api.PullRequestAndTotalCount, error) {
	m.called = true
	if m.err != nil {
		return nil, m.err
	}

	if m.expectFields != nil {

		if !isEqualSet(m.expectFields, opt.Fields) {
			return nil, fmt.Errorf("unexpected fields: %v", opt.Fields)
		}
	}

	return m.result, m.err
}

func (m *mockLister) ExpectFields(fields []string) {
	m.expectFields = fields
}

// listGitea lists pull requests on a Gitea host.
//
// Gitea filters by state and by base branch and takes one state per request, so
// the states gm offers are mapped onto the ones Gitea has. "merged" is not one of
// them: Gitea reports a merged pull request as closed with merged set, which the
// list mapping derives, so a merged state filter is applied after the fetch.
//
// A head branch filter has no server-side counterpart, so it is applied here too.
// That means a filtered list can need more than one page before it is full.
func listGitea(httpClient *http.Client, opts ListOptions) (*api.PullRequestAndTotalCount, error) {
	state := giteaListState(opts.State)
	if state == "" {
		return nil, fmt.Errorf("invalid state: %s", opts.State)
	}

	client := api.NewClientFromHTTP(httpClient)
	wantedMerged := opts.State == "merged"

	res := &api.PullRequestAndTotalCount{}
	for page := 1; len(res.PullRequests) < opts.LimitResults; page++ {
		pagePRs, total, err := api.ListPullRequestsGitea(client, opts.BaseRepo, api.ListPullRequestsGiteaInput{
			State:      state,
			BaseBranch: opts.BaseBranch,
			Page:       page,
		})
		if err != nil {
			return nil, err
		}
		if page == 1 {
			res.TotalCount = total
		}
		if len(pagePRs) == 0 {
			break
		}

		for _, pr := range pagePRs {
			if wantedMerged && pr.State != "MERGED" {
				continue
			}
			if opts.HeadBranch != "" && pr.HeadRefName != opts.HeadBranch {
				continue
			}
			res.PullRequests = append(res.PullRequests, pr)
			if len(res.PullRequests) == opts.LimitResults {
				break
			}
		}

		if len(pagePRs) < api.GiteaPageSize {
			break
		}
	}

	// A locally filtered list cannot claim the server's unfiltered count.
	if wantedMerged || opts.HeadBranch != "" {
		res.TotalCount = len(res.PullRequests)
	}

	return res, nil
}

// giteaListState maps gm's state onto Gitea's, or "" when it is not one gm offers.
func giteaListState(state string) string {
	switch state {
	case "open":
		return "open"
	case "closed":
		// Gitea reports a merged pull request as closed, so asking for closed
		// returns merged ones too. That matches what the caller asked for: gm
		// treats merged as a kind of closed unless it is asked for alone.
		return "closed"
	case "merged", "all":
		return "all"
	default:
		return ""
	}
}
