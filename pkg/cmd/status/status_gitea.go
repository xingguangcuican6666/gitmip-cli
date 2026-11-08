package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
)

// Gitea has no GraphQL API, so the four sections `gm status` prints come from
// three different endpoints of its own.
//
// GitHub answers the whole page in one aliased query with two searches inside it.
// Gitea's issue search takes one viewer-relative boolean per request, so the
// assignment list and the review-request list are two calls, and the current
// user's login has to be known before either can be made.
//
// The other differences:
//
//   - Mentions come from /notifications, since Gitea has no mention search. That
//     endpoint requires authentication, and its threads carry a subject whose
//     latest comment has to be fetched separately for the preview.
//   - Repository activity comes from /users/{login}/activities/feeds, whose rows
//     describe what happened with an op_type word rather than GitHub's event type
//     plus payload.
//   - The cross-repository search has no owner filter that gm can use for the
//     --org and --exclude flags, so both are applied locally.

// giteaStatusIssue is the part of Gitea's Issue that the status sections read.
type giteaStatusIssue struct {
	Number     int64  `json:"number"`
	Title      string `json:"title"`
	HTMLURL    string `json:"html_url"`
	State      string `json:"state"`
	UpdatedAt  string `json:"updated_at"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest *struct {
		Merged bool `json:"merged"`
	} `json:"pull_request"`
}

// giteaStatusNotification is the part of Gitea's NotificationThread that the
// mentions section reads.
type giteaStatusNotification struct {
	ID        int64  `json:"id"`
	Unread    bool   `json:"unread"`
	UpdatedAt string `json:"updated_at"`
	Subject   struct {
		Title            string `json:"title"`
		URL              string `json:"url"`
		HTMLURL          string `json:"html_url"`
		LatestCommentURL string `json:"latest_comment_url"`
		Type             string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

// giteaFeedRow is one activity feed entry.
//
// The content field is Gitea's own encoding of what the row is about: a number,
// a pipe, then the title of the issue or pull request it names.
type giteaFeedRow struct {
	OpType    string `json:"op_type"`
	Content   string `json:"content"`
	CreatedAt string `json:"created"`
	RefName   string `json:"ref_name"`
	Repo      struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repo"`
}

// ErrGiteaStatusUnauthenticated explains why an anonymous run cannot answer: every
// section here is about the caller.
var ErrGiteaStatusUnauthenticated = errors.New("gm status shows work assigned to you, so it needs an authenticated session")

// loadSearchResultsGitea fills the assigned and review-requested lists.
func (s *StatusGetter) loadSearchResultsGitea() error {
	login, err := s.CurrentUsername()
	if err != nil {
		if giteaUnauthenticated(err) {
			return ErrGiteaStatusUnauthenticated
		}
		return err
	}

	assigned, err := s.giteaIssueSearch("assigned", login)
	if err != nil {
		return err
	}
	reviewRequested, err := s.giteaIssueSearch("review_requested", login)
	if err != nil {
		return err
	}

	s.AssignedIssues = []StatusItem{}
	s.AssignedPRs = []StatusItem{}
	s.ReviewRequests = []StatusItem{}

	for _, row := range assigned {
		item := StatusItem{
			Repository: row.Repository.FullName,
			Identifier: fmt.Sprintf("%s#%d", row.Repository.FullName, row.Number),
			preview:    row.Title,
		}
		// The pull_request object is present only for a pull request, so it is
		// what splits the two lists.
		if row.PullRequest != nil {
			s.AssignedPRs = append(s.AssignedPRs, item)
		} else {
			s.AssignedIssues = append(s.AssignedIssues, item)
		}
	}

	for _, row := range reviewRequested {
		s.ReviewRequests = append(s.ReviewRequests, StatusItem{
			Repository: row.Repository.FullName,
			Identifier: fmt.Sprintf("%s#%d", row.Repository.FullName, row.Number),
			preview:    row.Title,
		})
	}

	return nil
}

// giteaIssueSearch runs one viewer-relative issue search.
//
// flag is Gitea's query parameter name: assigned, created, mentioned or
// review_requested. All of them mean "the authenticated user", which is why the
// login is looked up first.
func (s *StatusGetter) giteaIssueSearch(flag, login string) ([]giteaStatusIssue, error) {
	client := api.NewClientFromHTTP(s.Client)

	u, err := safeurl.JoinPath("repos", "issues", "search")
	if err != nil {
		return nil, err
	}
	u.SetQuery("state", "open")
	u.SetQuery(flag, "true")
	u.SetQuery("sort", "recentupdate")
	api.SetPageSizeMutable(u, s.hostname(), 25)

	var rows []giteaStatusIssue
	if err := client.REST(s.hostname(), http.MethodGet, u.String(), nil, &rows); err != nil {
		return nil, err
	}

	// The search is cross-repository, so --org and --exclude are applied here
	// rather than by the server.
	filtered := rows[:0]
	for _, row := range rows {
		owner, _, found := strings.Cut(row.Repository.FullName, "/")
		if s.Org != "" && (!found || owner != s.Org) {
			continue
		}
		if s.ShouldExclude(row.Repository.FullName) {
			continue
		}
		filtered = append(filtered, row)
	}

	return filtered, nil
}

// loadNotificationsGitea fills the mentions list.
//
// Gitea has no mention search, so the notification list is the source. Every
// thread is read; the subject type is filtered locally because Gitea's
// subject-type parameter is a repeated query key and safeurl.SetQuery replaces
// rather than appends, so two values cannot be sent that way.
func (s *StatusGetter) loadNotificationsGitea() error {
	client := api.NewClientFromHTTP(s.Client)

	// /notifications has no anonymous answer at all, so an unauthenticated run is
	// reported as such rather than as a failed search.
	if _, err := s.CurrentUsername(); err != nil {
		if giteaUnauthenticated(err) {
			return ErrGiteaStatusUnauthenticated
		}
	}

	u, err := safeurl.JoinPath("notifications")
	if err != nil {
		return err
	}
	u.SetQuery("all", "false")
	api.SetPageSizeMutable(u, s.hostname(), 50)

	var threads []giteaStatusNotification
	if err := client.REST(s.hostname(), http.MethodGet, u.String(), nil, &threads); err != nil {
		return err
	}

	s.Mentions = []StatusItem{}

	for _, thread := range threads {
		switch thread.Subject.Type {
		case "issue", "pull", "PullRequest":
		default:
			continue
		}

		if s.Org != "" && thread.Repository.Owner.Login != s.Org {
			continue
		}
		if s.ShouldExclude(thread.Repository.FullName) {
			continue
		}

		preview := thread.Subject.Title
		if thread.Subject.LatestCommentURL != "" {
			if body, err := s.giteaCommentBody(thread.Subject.LatestCommentURL); err == nil && body != "" {
				preview = body
			}
		}

		s.Mentions = append(s.Mentions, StatusItem{
			Repository: thread.Repository.FullName,
			Identifier: fmt.Sprintf("%s#%d", thread.Repository.FullName, giteaSubjectNumber(thread.Subject.HTMLURL)),
			preview:    preview,
		})
	}

	return nil
}

// giteaCommentBody fetches a comment's text by its API URL.
//
// The URL Gitea hands back is absolute, so it is requested as given rather than
// resolved against the REST root. A comment that has gone away is not an error:
// the section falls back to the subject title.
func (s *StatusGetter) giteaCommentBody(url string) (string, error) {
	client := api.NewClientFromHTTP(s.Client)

	resp, err := client.Request(s.hostname(), http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var comment struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&comment); err != nil {
		return "", err
	}
	return comment.Body, nil
}

// giteaSubjectNumber reads the issue or pull request number out of a web URL,
// whose last path segment is the number.
func giteaSubjectNumber(htmlURL string) int {
	last := htmlURL[strings.LastIndex(htmlURL, "/")+1:]
	number, err := strconv.Atoi(last)
	if err != nil {
		return 0
	}
	return number
}

// loadEventsGitea fills the repository activity section from the activity feed.
//
// Gitea's feed names what happened with an op_type word. The rows gm shows are
// the ones that create something or comment on something; the rest are dropped.
func (s *StatusGetter) loadEventsGitea() error {
	login, err := s.CurrentUsername()
	if err != nil {
		if giteaUnauthenticated(err) {
			return ErrGiteaStatusUnauthenticated
		}
		return err
	}

	client := api.NewClientFromHTTP(s.Client)

	u, err := safeurl.JoinPath("users", login, "activities", "feeds")
	if err != nil {
		return err
	}
	api.SetPageSizeMutable(u, s.hostname(), 50)

	var rows []giteaFeedRow
	if err := client.REST(s.hostname(), http.MethodGet, u.String(), nil, &rows); err != nil {
		var httpErr api.HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
			return fmt.Errorf("could not get events: %w", err)
		}
		// A host without activity feeds has no events to show.
		return nil
	}

	s.RepoActivity = []StatusItem{}

	for _, row := range rows {
		if s.Org != "" && row.Repo.Owner.Login != s.Org {
			continue
		}
		if s.ShouldExclude(row.Repo.FullName) {
			continue
		}

		reason, preview := giteaFeedReason(row)
		if reason == "" {
			continue
		}

		s.RepoActivity = append(s.RepoActivity, StatusItem{
			Repository: row.Repo.FullName,
			Identifier: fmt.Sprintf("%s#%d", row.Repo.FullName, giteaContentNumber(row.Content)),
			preview:    preview,
			Reason:     reason,
		})
	}

	return nil
}

// giteaContentNumber reads the leading number from a feed row's content, which
// Gitea formats as "<number>|<title>".
func giteaContentNumber(content string) int {
	number, _, _ := strings.Cut(content, "|")
	value, err := strconv.Atoi(number)
	if err != nil {
		return 0
	}
	return value
}

// giteaContentText returns the part of a feed row's content after the separator,
// which is the title Gitea stored with it.
func giteaContentText(content string) string {
	if _, after, ok := strings.Cut(content, "|"); ok {
		return after
	}
	return content
}

// giteaFeedReason translates a feed operation into the reason gm prints.
func giteaFeedReason(row giteaFeedRow) (reason, preview string) {
	title := giteaContentText(row.Content)
	switch row.OpType {
	case "create_issue":
		return "new issue", title
	case "create_pull_request":
		return "new PR", title
	case "comment_issue", "comment_pull_request", "comment_pull", "pull_request_comment":
		return "comment on " + title, title
	default:
		return "", ""
	}
}

// giteaStatusTime parses a feed timestamp, which is not used for ordering here
// but is kept so a future sort has something to read.
func giteaStatusTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

// giteaUnauthenticated reports whether an error is a 401.
func giteaUnauthenticated(err error) bool {
	var httpErr api.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
}
