package list

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/safeurl"
	prShared "github.com/cli/cli/v2/pkg/cmd/pr/shared"
)

// Gitea has no GraphQL API, so issue listing is answered from REST.
//
// The list endpoint returns issues and pull requests together, because the two
// share one index space. `type=issues` narrows it to issues, which is what makes
// the count meaningful; without it a repository with mostly pull requests would
// report its total for both.
//
// Every filter gm exposes maps onto a Gitea query parameter except two. Labels
// and milestones are OR-matched server-side, where GitHub ANDs them, so asking
// for two labels returns issues carrying either. That difference is reported
// rather than silently accepted. Issue type has no counterpart at all.
//
// There is no query syntax either, so `--search` is passed through as Gitea's
// `q` parameter, which matches title and body text only. Qualifiers such as
// `no:assignee` or `sort:created-asc` would be treated as search text and
// silently match nothing, so they are rejected.

// giteaIssuePageSize is the per-page maximum gitea.com enforces.
const giteaIssuePageSize = 50

// giteaSearchQualifier matches the `key:value` forms GitHub's issue search
// accepts. Gitea has no equivalent grammar.
var giteaSearchQualifier = regexp.MustCompile(`(?:^|\s)[a-z-]+:`)

func listIssuesGitea(client *api.Client, repo ghrepo.Interface, filters prShared.FilterOptions, limit int) (*api.IssuesAndTotalCount, error) {
	if filters.IssueType != "" {
		return nil, fmt.Errorf("Gitea has no issue types, so `--type` is not supported")
	}
	if filters.Search != "" && giteaSearchQualifier.MatchString(filters.Search) {
		return nil, fmt.Errorf("Gitea does not support search qualifiers in `--search`; it matches title and body text only")
	}
	if len(filters.Labels) > 1 {
		return nil, fmt.Errorf("Gitea matches labels with OR, so filtering by more than one is not equivalent to GitHub; pass a single `--label`")
	}

	basePath, err := safeurl.JoinPath("repos", repo.RepoOwner(), repo.RepoName(), "issues")
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("type", "issues")
	query.Set("state", giteaIssueState(filters.State))
	if filters.Search != "" {
		query.Set("q", filters.Search)
	}
	if len(filters.Labels) == 1 {
		query.Set("labels", filters.Labels[0])
	}
	if filters.Milestone != "" {
		query.Set("milestones", filters.Milestone)
	}
	if filters.Author != "" {
		query.Set("created_by", filters.Author)
	}
	if filters.Assignee != "" {
		query.Set("assigned_by", filters.Assignee)
	}
	if filters.Mention != "" {
		query.Set("mentioned_by", filters.Mention)
	}

	var issues []api.Issue
	totalCount := 0

	for page := 1; len(issues) < limit; page++ {
		query.Set("limit", strconv.Itoa(giteaIssuePageSize))
		query.Set("page", strconv.Itoa(page))

		pagePath, err := withIssueQuery(basePath, query)
		if err != nil {
			return nil, err
		}

		raw, serverTotal, err := fetchGiteaIssuePage(client, repo.RepoHost(), pagePath)
		if err != nil {
			return nil, err
		}
		if page == 1 {
			totalCount = serverTotal
		}

		if len(raw) == 0 {
			break
		}

		for _, g := range raw {
			issue := mapGiteaListIssue(g)
			issues = append(issues, *issue)
			if len(issues) >= limit {
				break
			}
		}

		if len(raw) < giteaIssuePageSize {
			break
		}
	}

	return &api.IssuesAndTotalCount{Issues: issues, TotalCount: totalCount}, nil
}

// fetchGiteaIssuePage returns one page of issues and the server's total count.
// The count arrives only in a response header, so the response is read directly
// rather than decoded through api.Client.REST.
func fetchGiteaIssuePage(client *api.Client, hostname string, pagePath safeurl.SafeURL) ([]giteaListIssue, int, error) {
	resp, err := client.Request(hostname, http.MethodGet, pagePath.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	var raw []giteaListIssue
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, 0, err
	}

	total, _ := strconv.Atoi(resp.Header.Get("X-Total-Count"))
	return raw, total, nil
}

// giteaListIssue is the subset of Gitea's Issue schema listing needs.
type giteaListIssue struct {
	ID        int64  `json:"id"`
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	HTMLURL   string `json:"html_url"`
	State     string `json:"state"`
	IsLocked  bool   `json:"is_locked"`
	Comments  int64  `json:"comments"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	ClosedAt  string `json:"closed_at"`

	User      giteaListUser   `json:"user"`
	Assignees []giteaListUser `json:"assignees"`
	Labels    []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Color       string `json:"color"`
	} `json:"labels"`
	Milestone *struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	} `json:"milestone"`

	// PullRequest is non-nil when the row is a pull request rather than an
	// issue. `type=issues` should exclude those, and the check is kept because a
	// pull request in an issue list would be reported under the wrong command.
	PullRequest *struct {
		HTMLURL string `json:"html_url"`
	} `json:"pull_request"`
}

type giteaListUser struct {
	ID       int64  `json:"id"`
	Login    string `json:"login"`
	FullName string `json:"full_name"`
}

func mapGiteaListIssue(raw giteaListIssue) *api.Issue {
	issue := &api.Issue{
		ID:        strconv.FormatInt(raw.ID, 10),
		Number:    int(raw.Number),
		Title:     raw.Title,
		Body:      raw.Body,
		URL:       raw.HTMLURL,
		State:     strings.ToUpper(raw.State),
		Locked:    raw.IsLocked,
		CreatedAt: parseGiteaListTime(raw.CreatedAt),
		UpdatedAt: parseGiteaListTime(raw.UpdatedAt),
		Author: api.Author{
			ID:    strconv.FormatInt(raw.User.ID, 10),
			Login: raw.User.Login,
			Name:  raw.User.FullName,
		},
	}

	if raw.PullRequest != nil {
		issue.Typename = api.TypePullRequest
	} else {
		issue.Typename = api.TypeIssue
	}

	if raw.ClosedAt != "" {
		closed := parseGiteaListTime(raw.ClosedAt)
		issue.Closed = true
		issue.ClosedAt = &closed
	}

	for _, a := range raw.Assignees {
		issue.Assignees.Nodes = append(issue.Assignees.Nodes, api.GitHubUser{
			ID:         strconv.FormatInt(a.ID, 10),
			Login:      a.Login,
			Name:       a.FullName,
			DatabaseID: a.ID,
		})
	}
	issue.Assignees.TotalCount = len(issue.Assignees.Nodes)

	for _, l := range raw.Labels {
		issue.Labels.Nodes = append(issue.Labels.Nodes, api.IssueLabel{
			ID:          strconv.FormatInt(l.ID, 10),
			Name:        l.Name,
			Description: l.Description,
			Color:       l.Color,
		})
	}
	issue.Labels.TotalCount = len(issue.Labels.Nodes)

	if raw.Milestone != nil {
		issue.Milestone = &api.Milestone{
			Number: int(raw.Milestone.ID),
			Title:  raw.Milestone.Title,
		}
	}

	issue.Comments.TotalCount = int(raw.Comments)

	return issue
}

// giteaIssueState maps gm's state filter onto Gitea's, which uses the same words
// in lowercase and accepts "all" for both.
func giteaIssueState(state string) string {
	switch state {
	case "open", "":
		return "open"
	case "closed":
		return "closed"
	case "all":
		return "all"
	default:
		return state
	}
}

func withIssueQuery(base safeurl.SafeURL, query url.Values) (safeurl.SafeURL, error) {
	mutable, ok := base.(*safeurl.MutableSafeURL)
	if !ok {
		return nil, fmt.Errorf("internal error: expected a mutable URL")
	}
	for key, values := range query {
		for _, value := range values {
			mutable.SetQuery(key, value)
		}
	}
	return mutable, nil
}

func parseGiteaListTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
