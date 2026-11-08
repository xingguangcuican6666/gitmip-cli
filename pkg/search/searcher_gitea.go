package search

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
)

// Gitea has no GraphQL API and no search endpoint that takes GitHub's query
// language, so its searches are built from its own parameters instead.
//
// Three things make this a different shape rather than a different path.
//
// A query is keywords plus enumerated parameters, not a query string. Gitea's
// issue search takes q for text and separate booleans and strings for the things
// GitHub spells as qualifiers (label:, milestone:, assigned:, created:,
// review-requested:). Repositories are similar: q for text, and named parameters
// for topic, starred-by, private and archived. A qualifier with no Gitea
// parameter is refused rather than dropped, because dropping it silently returns
// a broader result than the caller asked for.
//
// Totals arrive in a header. Both search endpoints report their match count in
// X-Total-Count and page through Link, so the count is read from the response
// rather than from a field of the body.
//
// Code and commit search do not exist. Gitea's API has no code index and no
// commit index, so those two searches are refused instead of being answered from
// something that only looks like them.

// giteaSearchPageSize is the page size to request from Gitea's search endpoints.
//
// Gitea caps a page at MAX_RESPONSE_ITEMS, which is 50 on gitea.com. Requesting
// more is answered with fewer rows, and a paging loop that asked for 100 would
// read 50 rows as a short page and stop before the result was complete.
const giteaSearchPageSize = 50

// giteaSearchRow is a repository from Gitea's repository search.
type giteaSearchRow struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	Fork          bool   `json:"fork"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
	Language      string `json:"language"`
	Size          int    `json:"size"`
	Stars         int    `json:"stars_count"`
	Forks         int    `json:"forks_count"`
	Watchers      int    `json:"watchers_count"`
	OpenIssues    int    `json:"open_issues_count"`
	HasIssues     bool   `json:"has_issues"`
	HasWiki       bool   `json:"has_wiki"`
	HasProjects   bool   `json:"has_projects"`
	Website       string `json:"website"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	Owner         struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
}

type giteaRepoSearchPayload struct {
	OK   bool             `json:"ok"`
	Data []giteaSearchRow `json:"data"`
}

// giteaIssueSearchRow is an issue or pull request from Gitea's issue search.
type giteaIssueSearchRow struct {
	ID        int64  `json:"id"`
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"`
	HTMLURL   string `json:"html_url"`
	IsLocked  bool   `json:"is_locked"`
	Comments  int    `json:"comments"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	ClosedAt  string `json:"closed_at"`
	User      struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"user"`
	Assignees []struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"assignees"`
	Labels []struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"labels"`
	Milestone *struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	} `json:"milestone"`
	PullRequest *struct {
		Merged bool `json:"merged"`
		Draft  bool `json:"draft"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
}

// giteaRefusedQualifier names a GitHub search qualifier Gitea cannot express.
type giteaRefusedQualifier struct {
	name string
	why  string
}

// giteaUnsupportedQualifiersCheck refuses the qualifiers that would otherwise be
// silently dropped.
//
// Each entry is a qualifier that changes which rows match. Ignoring one would
// return a superset, which reads as success while answering a different question,
// so the search fails instead.
func giteaUnsupportedIssueQualifiers(q Qualifiers) error {
	refused := []giteaRefusedQualifier{
		{name: "author-date", why: "Gitea's issue search takes no date-range qualifiers"},
		{name: "closed", why: "Gitea's issue search takes no date-range qualifiers"},
		{name: "created", why: "Gitea's issue search takes no date-range qualifiers"},
		{name: "updated", why: "Gitea's issue search takes no date-range qualifiers"},
		{name: "comments", why: "Gitea's issue search takes no comment-count qualifier"},
		{name: "reactions", why: "Gitea's issue search takes no reaction qualifier"},
		{name: "interactions", why: "Gitea's issue search takes no interaction qualifier"},
		{name: "involves", why: "Gitea's issue search has no involves qualifier"},
		{name: "commenter", why: "Gitea's issue search has no commenter qualifier"},
		{name: "review", why: "Gitea's issue search has no review-verdict qualifier"},
		{name: "reviewed-by", why: "Gitea's issue search has no reviewed-by qualifier"},
		{name: "team", why: "Gitea's issue search has no team qualifier"},
		{name: "project", why: "Gitea's issue search has no project qualifier"},
		{name: "no", why: "Gitea's issue search cannot express negated qualifiers"},
		{name: "language", why: "Gitea's issue search has no language qualifier"},
	}

	set := map[string]string{}
	for _, r := range refused {
		set[r.name] = r.why
	}

	return checkRefused(set, q)
}

func giteaUnsupportedRepoQualifiers(q Qualifiers) error {
	set := map[string]string{
		"archived":           "",
		"followers":          "Gitea's repository search takes no follower-count qualifier",
		"forks":              "Gitea's repository search takes no fork-count qualifier",
		"size":               "Gitea's repository search takes no size qualifier",
		"stars":              "Gitea's repository search takes no star-count qualifier",
		"pushed":             "Gitea's repository search takes no push-date qualifier",
		"created":            "Gitea's repository search takes no date-range qualifiers",
		"language":           "Gitea's repository search has no language parameter",
		"license":            "Gitea's repository search has no license parameter",
		"topic":              "Gitea's repository search has a topic boolean but no topic-value parameter",
		"in":                 "Gitea's repository search has no in: qualifier",
		"user":               "Gitea's repository search addresses an owner by numeric uid",
		"org":                "Gitea's repository search addresses an owner by numeric uid",
		"is":                 "Gitea's repository search has no is: qualifier; private, archived and fork are their own parameters",
		"mirror":             "Gitea's repository search has no mirror qualifier",
		"template":           "",
		"good-first-issues":  "Gitea's repository search has no good-first-issue qualifier",
		"help-wanted-issues": "Gitea's repository search has no help-wanted qualifier",
	}

	return checkRefused(set, q)
}

// checkRefused reports the first qualifier in set that the caller set.
func checkRefused(set map[string]string, q Qualifiers) error {
	for name, why := range set {
		if !qualifierIsSet(q, name) {
			continue
		}
		if why == "" {
			continue
		}
		return fmt.Errorf("Gitea cannot search with `%s:`: %s", name, why)
	}
	return nil
}

// qualifierIsSet reports whether the named qualifier carries a value.
func qualifierIsSet(q Qualifiers, name string) bool {
	switch name {
	case "author-date":
		return q.AuthorDate != ""
	case "closed":
		return q.Closed != ""
	case "created":
		return q.Created != ""
	case "updated":
		return q.Updated != ""
	case "comments":
		return q.Comments != ""
	case "reactions":
		return q.Reactions != ""
	case "interactions":
		return q.Interactions != ""
	case "involves":
		return q.Involves != ""
	case "commenter":
		return q.Commenter != ""
	case "review":
		return q.Review != ""
	case "reviewed-by":
		return q.ReviewedBy != ""
	case "team":
		return q.Team != ""
	case "project":
		return q.Project != ""
	case "no":
		return len(q.No) > 0
	case "language":
		return q.Language != ""
	case "followers":
		return q.Followers != ""
	case "forks":
		return q.Forks != ""
	case "size":
		return q.Size != ""
	case "stars":
		return q.Stars != ""
	case "pushed":
		return q.Pushed != ""
	case "license":
		return len(q.License) > 0
	case "topic":
		return len(q.Topic) > 0 || q.Topics != ""
	case "in":
		return len(q.In) > 0
	case "user", "org":
		return len(q.User) > 0
	case "is":
		return len(q.Is) > 0
	case "mirror":
		return q.Status != ""
	case "good-first-issues":
		return q.GoodFirstIssues != ""
	case "help-wanted-issues":
		return q.HelpWantedIssues != ""
	default:
		return false
	}
}

// searchGiteaRepositories runs one page of Gitea's repository search.
func (s searcher) searchGiteaRepositories(query Query) (RepositoriesResult, error) {
	if err := giteaUnsupportedRepoQualifiers(query.Qualifiers); err != nil {
		return RepositoriesResult{}, err
	}

	u, err := safeurlJoin("repos", "search")
	if err != nil {
		return RepositoriesResult{}, err
	}
	// Only the keywords go in q. Gitea's search matches q as text, so a qualifier
	// rendered into it would be searched for literally rather than applied.
	u.SetQuery("q", query.KeywordsString())
	u.SetQuery("page", strconv.Itoa(query.Page))
	api.SetPageSizeMutable(u, s.host, query.Limit)

	if q := query.Qualifiers; q.Archived != nil {
		u.SetQuery("archived", strconv.FormatBool(*q.Archived))
	}
	if q := query.Qualifiers; len(q.Topic) > 0 || q.Topics != "" {
		u.SetQuery("topic", "true")
	}
	if q := query.Qualifiers; q.Fork != "" {
		u.SetQuery("fork", q.Fork)
	}

	resp, total, err := s.getGitea(u.String())
	if err != nil {
		return RepositoriesResult{}, err
	}
	defer resp.Body.Close()

	var payload giteaRepoSearchPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return RepositoriesResult{}, err
	}

	result := RepositoriesResult{Total: total}
	for _, row := range payload.Data {
		result.Items = append(result.Items, giteaRepositoryToSearchResult(row))
	}
	return result, nil
}

// searchGiteaIssues runs one page of Gitea's issue search.
func (s searcher) searchGiteaIssues(query Query) (IssuesResult, error) {
	if err := giteaUnsupportedIssueQualifiers(query.Qualifiers); err != nil {
		return IssuesResult{}, err
	}

	u, err := safeurlJoin("repos", "issues", "search")
	if err != nil {
		return IssuesResult{}, err
	}
	u.SetQuery("q", query.KeywordsString())
	u.SetQuery("page", strconv.Itoa(query.Page))
	api.SetPageSizeMutable(u, s.host, query.Limit)

	q := query.Qualifiers
	if q.State != "" {
		u.SetQuery("state", q.State)
	}
	if len(q.Label) > 0 {
		// Gitea takes label names comma separated and matches any of them, so a
		// multi-label search means OR here where GitHub means AND. Refusing is
		// the only way to stay honest about which rows come back.
		if len(q.Label) > 1 {
			return IssuesResult{}, fmt.Errorf("Gitea matches labels with OR, so searching for more than one `label:` is not equivalent to GitHub; search one label at a time")
		}
		u.SetQuery("labels", q.Label[0])
	}
	if q.Milestone != "" {
		u.SetQuery("milestones", q.Milestone)
	}
	if q.Type != "" {
		// gm spells the kind qualifier as type:issue / type:pr.
		switch q.Type {
		case "issue", "issues":
			u.SetQuery("type", "issues")
		case "pr", "pull-request":
			u.SetQuery("type", "pulls")
		}
	}
	if q.Assignee != "" {
		u.SetQuery("assigned", "true")
	}
	if q.Author != "" {
		u.SetQuery("created", "true")
	}
	if q.ReviewRequested != "" {
		u.SetQuery("review_requested", "true")
	}
	if q.Mentions != "" {
		u.SetQuery("mentioned", "true")
	}
	if len(q.Repo) > 0 {
		// Gitea's issue search is cross-repository and takes only an owner filter,
		// so there is no way to scope it to one repository. Answering with the
		// whole owner's issues would be a broader answer than the one asked for.
		return IssuesResult{}, fmt.Errorf("Gitea's issue search cannot be scoped to a repository, so `repo:` is not supported; use `gm issue list -R <owner>/<repo> --search` instead")
	}
	if q.Mentions != "" {
		if err := giteaViewerOnlyQualifier("mentions", q.Mentions); err != nil {
			return IssuesResult{}, err
		}
	}
	if q.Assignee != "" {
		if err := giteaViewerOnlyQualifier("assignee", q.Assignee); err != nil {
			return IssuesResult{}, err
		}
	}
	if q.Author != "" {
		if err := giteaViewerOnlyQualifier("author", q.Author); err != nil {
			return IssuesResult{}, err
		}
	}
	if q.ReviewRequested != "" {
		if err := giteaViewerOnlyQualifier("review-requested", q.ReviewRequested); err != nil {
			return IssuesResult{}, err
		}
	}

	resp, total, err := s.getGitea(u.String())
	if err != nil {
		return IssuesResult{}, err
	}
	defer resp.Body.Close()

	var rows []giteaIssueSearchRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return IssuesResult{}, err
	}

	result := IssuesResult{Total: total}
	for _, row := range rows {
		result.Items = append(result.Items, giteaIssueToSearchResult(row))
	}
	return result, nil
}

// giteaViewerOnlyQualifier refuses a qualifier naming someone other than the
// authenticated user.
//
// Gitea's issue search takes booleans about the viewer (assigned, created,
// mentioned, review_requested) rather than a name. Honouring the boolean for a
// qualifier that named somebody else would answer a different question, so the
// search fails unless the qualifier is about the viewer.
func giteaViewerOnlyQualifier(name, value string) error {
	switch value {
	case "", "@me":
		return nil
	}
	return fmt.Errorf("Gitea's issue search can only match `%s:` against the authenticated user, so searching for %q is not supported", name, value)
}

// getGitea performs a GET and returns the response with the total from the
// X-Total-Count header.
func (s searcher) getGitea(url string) (*http.Response, int, error) {
	resp, err := api.NewClientFromHTTP(s.client).Request(s.host, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}

	total, _ := strconv.Atoi(resp.Header.Get("X-Total-Count"))
	return resp, total, nil
}

// safeurlJoin builds a relative API path from its segments.
func safeurlJoin(parts ...string) (*safeurl.MutableSafeURL, error) {
	return safeurl.JoinPath(parts...)
}

// giteaRepositoryToSearchResult maps a repository onto the search result shape.
//
// The id is the numeric one rendered as a string, because gm's search results use
// a string id and Gitea has no node id to put there.
func giteaRepositoryToSearchResult(row giteaSearchRow) Repository {
	return Repository{
		CreatedAt:       parseGiteaSearchTime(row.CreatedAt),
		DefaultBranch:   row.DefaultBranch,
		Description:     row.Description,
		ForksCount:      row.Forks,
		FullName:        row.FullName,
		HasIssues:       row.HasIssues,
		HasProjects:     row.HasProjects,
		HasWiki:         row.HasWiki,
		Homepage:        row.Website,
		ID:              strconv.FormatInt(row.ID, 10),
		IsArchived:      row.Archived,
		IsFork:          row.Fork,
		IsPrivate:       row.Private,
		Language:        row.Language,
		Name:            row.Name,
		OpenIssuesCount: row.OpenIssues,
		Owner: User{
			ID:    strconv.FormatInt(row.Owner.ID, 10),
			Login: row.Owner.Login,
			Type:  row.Owner.Type,
		},
		Size:            row.Size,
		StargazersCount: row.Stars,
		URL:             row.HTMLURL,
		UpdatedAt:       parseGiteaSearchTime(row.UpdatedAt),
		WatchersCount:   row.Watchers,
		Visibility:      giteaVisibility(row.Private),
	}
}

// giteaIssueToSearchResult maps an issue or pull request onto the search result
// shape.
//
// isPullRequest comes from the pull_request object, which Gitea sends only for a
// pull request. A merged pull request is reported as closed by the issue state, so
// the merged flag is what makes it read as merged here.
func giteaIssueToSearchResult(row giteaIssueSearchRow) Issue {
	issue := Issue{
		Author: User{
			ID:    strconv.FormatInt(row.User.ID, 10),
			Login: row.User.Login,
		},
		Body:          row.Body,
		CommentsCount: row.Comments,
		CreatedAt:     parseGiteaSearchTime(row.CreatedAt),
		ID:            strconv.FormatInt(row.ID, 10),
		IsLocked:      row.IsLocked,
		Number:        int(row.Number),
		StateInternal: row.State,
		Title:         row.Title,
		URL:           row.HTMLURL,
		UpdatedAt:     parseGiteaSearchTime(row.UpdatedAt),
		// The repository name is carried in full_name form, which is what the
		// display and export paths split to print. GitHub gives the same thing as
		// an API URL whose last two segments are owner and repo.
		RepositoryURL: row.Repository.FullName,
	}

	if row.ClosedAt != "" {
		issue.ClosedAt = parseGiteaSearchTime(row.ClosedAt)
	}

	for _, a := range row.Assignees {
		issue.Assignees = append(issue.Assignees, User{ID: strconv.FormatInt(a.ID, 10), Login: a.Login})
	}
	for _, l := range row.Labels {
		issue.Labels = append(issue.Labels, Label{ID: strconv.FormatInt(l.ID, 10), Name: l.Name, Color: l.Color})
	}

	if row.PullRequest != nil {
		issue.PullRequest = PullRequest{}
		isDraft := row.PullRequest.Draft
		issue.IsDraft = &isDraft
	}

	return issue
}

func giteaVisibility(private bool) string {
	if private {
		return "private"
	}
	return "public"
}

// parseGiteaSearchTime reads an RFC 3339 timestamp, leaving the zero time for
// anything absent or unparseable.
func parseGiteaSearchTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

// searchRepositoriesGiteaAll pages through Gitea's repository search.
func (s searcher) searchRepositoriesGiteaAll(query Query) (RepositoriesResult, error) {
	result := RepositoriesResult{}

	remaining := query.Limit
	page := 1
	for remaining > 0 {
		pageQuery := query
		// Gitea caps a page at its own MAX_RESPONSE_ITEMS, which is 50 on
		// gitea.com, so asking for more yields fewer rows than the loop would
		// treat as a full page and stops early.
		pageQuery.Limit = min(remaining, giteaSearchPageSize)
		pageQuery.Page = page

		pageResult, err := s.searchGiteaRepositories(pageQuery)
		if err != nil {
			return result, err
		}

		result.Total = pageResult.Total
		result.Items = append(result.Items, pageResult.Items...)
		remaining -= len(pageResult.Items)

		if len(pageResult.Items) < pageQuery.Limit || pageResult.Total <= len(result.Items) {
			break
		}
		page++
	}

	if len(result.Items) > query.Limit {
		result.Items = result.Items[:query.Limit]
	}
	return result, nil
}

// searchIssuesGiteaAll pages through Gitea's issue search.
func (s searcher) searchIssuesGiteaAll(query Query) (IssuesResult, error) {
	result := IssuesResult{}

	remaining := query.Limit
	page := 1
	for remaining > 0 {
		pageQuery := query
		pageQuery.Limit = min(remaining, giteaSearchPageSize)
		pageQuery.Page = page

		pageResult, err := s.searchGiteaIssues(pageQuery)
		if err != nil {
			return result, err
		}

		result.Total = pageResult.Total
		result.Items = append(result.Items, pageResult.Items...)
		remaining -= len(pageResult.Items)

		if len(pageResult.Items) < pageQuery.Limit || pageResult.Total <= len(result.Items) {
			break
		}
		page++
	}

	if len(result.Items) > query.Limit {
		result.Items = result.Items[:query.Limit]
	}
	return result, nil
}

// giteaWebURL builds the web search URL for a kind.
//
// Gitea's web UI has no single /search endpoint that takes a type the way
// GitHub's does. Its searches live at their own paths: repositories under
// /explore/repos, issues and pull requests under /issues and /pulls, and users
// under /explore/users. The kind decides the path and the keywords go in q.
func (s searcher) giteaWebURL(query Query) string {
	base := "https://" + s.host

	var path string
	switch query.Kind {
	case KindRepositories:
		path = "/explore/repos"
	case KindIssues:
		// The issue kind covers both issues and pull requests; gm's PR search
		// sets the type qualifier, which the issues dashboard honours.
		path = "/issues"
	case "pull requests":
		path = "/pulls"
	default:
		path = "/explore"
	}

	qs := url.Values{}
	qs.Set("q", query.KeywordsString())
	if q := query.Qualifiers; q.State != "" {
		qs.Set("state", q.State)
	}

	return base + path + "?" + qs.Encode()
}
