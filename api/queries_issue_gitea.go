package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cli/cli/v2/internal/ghrepo"
)

// Gitea has no GraphQL API, so issue lookups are answered here from REST and
// mapped onto the same Issue type the GraphQL path produces.
//
// The mapping is not lossless. Fields with no Gitea counterpart are left zero:
// Gitea reports no state reason, no author association, no reaction counts on
// the issue itself, no sub-issues or blocking relationships, no issue type, and
// no project cards. A caller that displays one of them shows absence rather
// than an error, which is the honest result: the host does not have the data.
//
// Two things do need translating rather than merely being absent.
//
// Issue state arrives lowercase ("open"/"closed") and every consumer compares
// it against the uppercase GraphQL enum, so it is upper-cased here. Leaving it
// as Gitea sends it would make a closed issue read as open to the view and list
// commands without any error being raised.
//
// An issue and a pull request share one number space in Gitea, and
// GET /repos/{o}/{r}/issues/{index} answers for both. GitHub's lookup goes
// through issueOrPullRequest and reports which of the two came back, so the
// pull_request member of the response is what decides Typename here. Callers
// such as `gm issue close` rely on that to refuse to act on a pull request.

// giteaIssue is the subset of Gitea's Issue schema this package maps.
type giteaIssue struct {
	ID        int64  `json:"id"`
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	HTMLURL   string `json:"html_url"`
	State     string `json:"state"`
	IsLocked  bool   `json:"is_locked"`
	PinOrder  int64  `json:"pin_order"`
	Comments  int64  `json:"comments"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	ClosedAt  string `json:"closed_at"`
	DueDate   string `json:"due_date"`

	User      giteaUser   `json:"user"`
	Assignees []giteaUser `json:"assignees"`
	Labels    []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Color       string `json:"color"`
	} `json:"labels"`
	Milestone *struct {
		ID          int64  `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		DueOn       string `json:"due_on"`
	} `json:"milestone"`
	Projects []struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	} `json:"projects"`

	// Repository identifies the repository the issue belongs to. Gitea returns
	// it on the issue itself, which is what lets a caller resolve the host and
	// owner without a second request.
	Repository struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Owner    string `json:"owner"`
		FullName string `json:"full_name"`
	} `json:"repository"`

	// PullRequest is non-nil only when the index refers to a pull request.
	PullRequest *struct {
		Merged   bool   `json:"merged"`
		MergedAt string `json:"merged_at"`
		Draft    bool   `json:"draft"`
		HTMLURL  string `json:"html_url"`
	} `json:"pull_request"`
}

// giteaComment is the subset of Gitea's Comment schema this package maps.
type giteaComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
	User      giteaUser `json:"user"`
}

// giteaReaction is Gitea's Reaction, which reports one row per user rather than
// a pre-aggregated count.
type giteaReaction struct {
	Content string `json:"content"`
}

// mapGiteaIssue translates a Gitea issue onto the shape the GraphQL path
// produces. hostname is carried in so repo-scoped callers keep working: Gitea
// responses have no host field, unlike GitHub's node ids.
func mapGiteaIssue(raw giteaIssue, hostname string) *Issue {
	issue := &Issue{
		ID:        strconv.FormatInt(raw.ID, 10),
		Number:    int(raw.Number),
		Title:     raw.Title,
		Body:      raw.Body,
		URL:       raw.HTMLURL,
		State:     strings.ToUpper(raw.State),
		Locked:    raw.IsLocked,
		IsPinned:  raw.PinOrder > 0,
		CreatedAt: parseGiteaTime(raw.CreatedAt),
		UpdatedAt: parseGiteaTime(raw.UpdatedAt),
		Author: Author{
			ID:    strconv.FormatInt(raw.User.ID, 10),
			Login: raw.User.Login,
			Name:  raw.User.FullName,
		},
	}

	if raw.ClosedAt != "" {
		closed := parseGiteaTime(raw.ClosedAt)
		issue.Closed = true
		issue.ClosedAt = &closed
	}

	if raw.PullRequest != nil {
		issue.Typename = TypePullRequest
	} else {
		issue.Typename = TypeIssue
	}

	issue.Assignees.Nodes = make([]GitHubUser, 0, len(raw.Assignees))
	for _, a := range raw.Assignees {
		issue.Assignees.Nodes = append(issue.Assignees.Nodes, GitHubUser{
			ID:         strconv.FormatInt(a.ID, 10),
			Login:      a.Login,
			Name:       a.FullName,
			DatabaseID: a.ID,
		})
	}
	issue.Assignees.TotalCount = len(issue.Assignees.Nodes)

	issue.Labels.Nodes = make([]IssueLabel, 0, len(raw.Labels))
	for _, l := range raw.Labels {
		issue.Labels.Nodes = append(issue.Labels.Nodes, IssueLabel{
			ID:          strconv.FormatInt(l.ID, 10),
			Name:        l.Name,
			Description: l.Description,
			Color:       l.Color,
		})
	}
	issue.Labels.TotalCount = len(issue.Labels.Nodes)

	if raw.Milestone != nil {
		milestone := Milestone{
			Number:      int(raw.Milestone.ID),
			Title:       raw.Milestone.Title,
			Description: raw.Milestone.Description,
		}
		if raw.Milestone.DueOn != "" {
			dueOn := parseGiteaTime(raw.Milestone.DueOn)
			milestone.DueOn = &dueOn
		}
		issue.Milestone = &milestone
	}

	for _, p := range raw.Projects {
		item := &ProjectV2Item{
			ID: strconv.FormatInt(p.ID, 10),
			Project: ProjectV2ItemProject{
				ID:    strconv.FormatInt(p.ID, 10),
				Title: p.Title,
			},
		}
		issue.ProjectItems.Nodes = append(issue.ProjectItems.Nodes, item)
	}
	issue.ProjectItems.TotalCount = len(issue.ProjectItems.Nodes)

	// The comment count is reported even though the bodies have not been
	// fetched, so a caller that only shows a count does not need a second
	// request. PageInfo.HasNextPage is left false when everything fits in one
	// page, which is what stops the preload loops from running.
	issue.Comments.TotalCount = int(raw.Comments)

	if raw.Repository.FullName != "" {
		issue.Repository = &PRRepository{
			ID:            strconv.FormatInt(raw.Repository.ID, 10),
			Name:          raw.Repository.Name,
			NameWithOwner: raw.Repository.FullName,
			DatabaseID:    raw.Repository.ID,
		}
	}

	return issue
}

// FetchIssueGitea returns one issue or pull request by index.
func FetchIssueGitea(client *Client, repo ghrepo.Interface, number int) (*Issue, error) {
	path := fmt.Sprintf("repos/%s/%s/issues/%d", repo.RepoOwner(), repo.RepoName(), number)

	var raw giteaIssue
	if err := client.REST(repo.RepoHost(), http.MethodGet, path, nil, &raw); err != nil {
		return nil, err
	}

	return mapGiteaIssue(raw, repo.RepoHost()), nil
}

// IssueCommentsGitea fetches the comments on an issue, paging through all of
// them. Gitea has no cursor, so the caller gets the full list and the
// connection is marked complete.
//
// Marking which comments the current user wrote costs one extra request for
// GET /user, since Gitea reports no viewer flag on a comment. It is worth it:
// `gm issue comment --edit-last` and `--delete-last` select the comment from
// exactly that set, and without the flag they would find nothing to act on.
//
// A failure to identify the viewer is not a failure to read the comments. An
// unauthenticated run of `gm issue view` on a public repository gets the
// conversation with no comment attributed to a viewer, which is the truth of the
// situation; turning it into an error would break anonymous browsing that Gitea
// otherwise allows.
func IssueCommentsGitea(client *Client, repo ghrepo.Interface, number int) (*Comments, error) {
	currentLogin, loginErr := CurrentLoginName(client, repo.RepoHost())
	if loginErr != nil && !isGiteaUnauthenticated(loginErr) {
		return nil, loginErr
	}
	if loginErr != nil {
		currentLogin = ""
	}

	comments := &Comments{}

	path := fmt.Sprintf("repos/%s/%s/issues/%d/comments", repo.RepoOwner(), repo.RepoName(), number)
	for page := 1; ; page++ {
		pagePath := fmt.Sprintf("%s?limit=%d&page=%d", path, giteaPageSize, page)

		var raw []giteaComment
		if err := client.REST(repo.RepoHost(), http.MethodGet, pagePath, nil, &raw); err != nil {
			return nil, err
		}

		for _, c := range raw {
			comments.Nodes = append(comments.Nodes, Comment{
				ID:   strconv.FormatInt(c.ID, 10),
				Body: c.Body,
				URL:  c.HTMLURL,
				Author: CommentAuthor{
					Login: c.User.Login,
				},
				CreatedAt:           parseGiteaTime(c.CreatedAt),
				IncludesCreatedEdit: c.UpdatedAt != "" && c.UpdatedAt != c.CreatedAt,
				ViewerDidAuthor:     c.User.Login == currentLogin,
			})
		}

		if len(raw) < giteaPageSize {
			break
		}
	}

	comments.TotalCount = len(comments.Nodes)
	return comments, nil
}

// IssueReactionGroupsGitea aggregates Gitea's per-user reaction rows into the
// counts the GraphQL shape carries. Gitea names reactions with GitHub's emoji
// shorthand, so each is translated to the enum value consumers compare against;
// an unrecognized one is skipped rather than reported under a wrong name.
func IssueReactionGroupsGitea(client *Client, repo ghrepo.Interface, number int) (ReactionGroups, error) {
	path := fmt.Sprintf("repos/%s/%s/issues/%d/reactions", repo.RepoOwner(), repo.RepoName(), number)

	counts := map[string]int{}
	for page := 1; ; page++ {
		pagePath := fmt.Sprintf("%s?limit=%d&page=%d", path, giteaPageSize, page)

		var raw []giteaReaction
		if err := client.REST(repo.RepoHost(), http.MethodGet, pagePath, nil, &raw); err != nil {
			return nil, err
		}

		for _, r := range raw {
			if content, ok := giteaReactionContent[r.Content]; ok {
				counts[content]++
			}
		}

		if len(raw) < giteaPageSize {
			break
		}
	}

	// Emitted in the order reactionEmoji declares, so the output is stable
	// rather than following map iteration order.
	var groups ReactionGroups
	for _, content := range []string{
		"THUMBS_UP", "THUMBS_DOWN", "LAUGH", "HOORAY", "CONFUSED", "HEART", "ROCKET", "EYES",
	} {
		if count := counts[content]; count > 0 {
			groups = append(groups, ReactionGroup{
				Content: content,
				Users:   ReactionGroupUsers{TotalCount: count},
			})
		}
	}

	return groups, nil
}

// giteaReactionContent maps Gitea's reaction names onto GitHub's enum values.
var giteaReactionContent = map[string]string{
	"+1":       "THUMBS_UP",
	"-1":       "THUMBS_DOWN",
	"laugh":    "LAUGH",
	"hooray":   "HOORAY",
	"confused": "CONFUSED",
	"heart":    "HEART",
	"rocket":   "ROCKET",
	"eyes":     "EYES",
}

// parseGiteaTime reads a Gitea timestamp. An empty or unparseable value yields
// the zero time rather than failing the lookup, since a missing timestamp is
// not a reason to hide an issue.
func parseGiteaTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

// isGiteaUnauthenticated reports whether an error is Gitea's refusal of an
// anonymous request. Gitea serves public repositories without a token, so a read
// that only needs to know who the viewer is has to tolerate not knowing.
func isGiteaUnauthenticated(err error) bool {
	var httpErr HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
}

// giteaPageSize is the per-page maximum gitea.com enforces through
// MAX_RESPONSE_ITEMS. A larger request is capped server-side, so asking for more
// would silently yield fewer results than a paging loop expects.
const giteaPageSize = 50
