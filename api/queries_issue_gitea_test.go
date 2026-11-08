package api

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var giteaIssueRepo = ghrepo.NewWithHost("gitea", "tea", "git.example.com")

func withGiteaIssueHost(t *testing.T) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)
}

// A trimmed body in the shape gitea.com returns for gitea/tea#160, including a
// closed issue with a milestone and a label.
const giteaIssueBody = `{
	"id": 131821,
	"number": 160,
	"title": "please keep setup-go syncing",
	"body": "text",
	"html_url": "https://gitea.com/gitea/tea/issues/160",
	"state": "closed",
	"is_locked": true,
	"pin_order": 2,
	"comments": 3,
	"created_at": "2024-05-01T10:00:00Z",
	"updated_at": "2024-05-02T10:00:00Z",
	"closed_at": "2024-05-03T10:00:00Z",
	"user": {"id": 43135, "login": "myrqs", "full_name": "My Rqs"},
	"assignees": [{"id": 9, "login": "darkman", "full_name": ""}],
	"labels": [{"id": 17304, "name": "proposal", "description": "d", "color": "0052cc"}],
	"milestone": {"id": 5729, "title": "1.4.0", "description": "", "due_on": "2024-06-01T00:00:00Z"},
	"projects": [{"id": 7, "title": "Board"}],
	"repository": {"id": 34989, "name": "tea", "owner": "gitea", "full_name": "gitea/tea"}
}`

// Gitea sends state in lowercase and every consumer compares against the
// uppercase GraphQL enum. A closed issue that mapped through as "closed" would
// read as open to the view and list commands with no error anywhere.
func TestMapGiteaIssueUpperCasesState(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/issues/160"),
		httpmock.StringResponse(giteaIssueBody),
	)

	issue, err := FetchIssueGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160)
	require.NoError(t, err)

	assert.Equal(t, "CLOSED", issue.State)
	assert.True(t, issue.Closed)
	require.NotNil(t, issue.ClosedAt)
	assert.True(t, issue.Locked)
	assert.True(t, issue.IsPinned, "pin_order > 0 means pinned")
	assert.Equal(t, 160, issue.Number)
	assert.Equal(t, 3, issue.Comments.TotalCount)
}

// Gitea's issues endpoint answers for a pull request index too, and callers such
// as `gm issue close` refuse to act on a pull request. Typename is what they
// branch on, so the pull_request member has to decide it.
func TestMapGiteaIssueTypename(t *testing.T) {
	withGiteaIssueHost(t)

	tests := []struct {
		name     string
		body     string
		wantType string
	}{
		{
			name:     "issue",
			body:     `{"id":1,"number":2,"state":"open"}`,
			wantType: TypeIssue,
		},
		{
			name:     "pull request",
			body:     `{"id":1,"number":2,"state":"open","pull_request":{"merged":false,"html_url":"https://gitea.com/gitea/tea/pulls/2"}}`,
			wantType: TypePullRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)
			reg.Register(
				httpmock.REST("GET", "api/v1/repos/gitea/tea/issues/2"),
				httpmock.StringResponse(tt.body),
			)

			issue, err := FetchIssueGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 2)
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, issue.Typename)
			if tt.wantType == TypePullRequest {
				assert.True(t, issue.IsPullRequest())
			} else {
				assert.False(t, issue.IsPullRequest())
			}
		})
	}
}

func TestMapGiteaIssueRelations(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/issues/160"),
		httpmock.StringResponse(giteaIssueBody),
	)

	issue, err := FetchIssueGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160)
	require.NoError(t, err)

	require.Len(t, issue.Assignees.Nodes, 1)
	assert.Equal(t, "darkman", issue.Assignees.Nodes[0].Login)
	assert.Equal(t, int64(9), issue.Assignees.Nodes[0].DatabaseID)
	assert.Equal(t, 1, issue.Assignees.TotalCount)

	require.Len(t, issue.Labels.Nodes, 1)
	assert.Equal(t, "proposal", issue.Labels.Nodes[0].Name)
	assert.Equal(t, "0052cc", issue.Labels.Nodes[0].Color)

	require.NotNil(t, issue.Milestone)
	assert.Equal(t, "1.4.0", issue.Milestone.Title)
	assert.Equal(t, 5729, issue.Milestone.Number)
	require.NotNil(t, issue.Milestone.DueOn)

	require.Len(t, issue.ProjectItems.Nodes, 1)
	assert.Equal(t, "Board", issue.ProjectItems.Nodes[0].Project.Title)

	assert.Equal(t, "myrqs", issue.Author.Login)
	assert.Equal(t, "My Rqs", issue.Author.Name)

	// The repository member is what lets a caller resolve the host without a
	// second request.
	require.NotNil(t, issue.Repository)
	assert.Equal(t, "gitea/tea", issue.Repository.NameWithOwner)
	assert.Equal(t, int64(34989), issue.Repository.DatabaseID)
}

// Fields Gitea does not report must stay zero rather than be invented, so a
// caller shows absence instead of a fabricated value.
func TestMapGiteaIssueAbsentFieldsStayZero(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/issues/160"),
		httpmock.StringResponse(giteaIssueBody),
	)

	issue, err := FetchIssueGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160)
	require.NoError(t, err)

	assert.Empty(t, issue.StateReason, "Gitea records no close reason")
	assert.Empty(t, issue.ActiveLockReason, "Gitea reports is_locked but no reason")
	assert.Nil(t, issue.IssueType)
	assert.Nil(t, issue.Parent)
	assert.Empty(t, issue.SubIssues.Nodes)
	assert.Empty(t, issue.BlockedBy.Nodes)
	assert.Empty(t, issue.Blocking.Nodes)
	assert.Empty(t, issue.ProjectCards.Nodes, "no v1 project cards")
	assert.Empty(t, issue.ReactionGroups, "not part of the issue response")
	assert.Empty(t, issue.Comments.Nodes, "bodies need their own request")
}

func TestIssueCommentsGitea(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// Marking which comments the viewer wrote costs a GET /user, since Gitea
	// reports no viewer flag on a comment.
	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StringResponse(`{"id":1,"login":"myrqs","full_name":""}`),
	)

	var gotQuery string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/issues/160/comments"),
		func(req *http.Request) (*http.Response, error) {
			gotQuery = req.URL.RawQuery
			return httpmock.StringResponse(`[
				{"id":11,"body":"mine","html_url":"https://gitea.com/gitea/tea/issues/160#issuecomment-11",
				 "created_at":"2024-05-01T10:00:00Z","updated_at":"2024-05-01T10:00:00Z",
				 "user":{"id":1,"login":"myrqs"}},
				{"id":12,"body":"edited","html_url":"https://gitea.com/gitea/tea/issues/160#issuecomment-12",
				 "created_at":"2024-05-02T10:00:00Z","updated_at":"2024-05-03T10:00:00Z",
				 "user":{"id":2,"login":"other"}}
			]`)(req)
		},
	)

	comments, err := IssueCommentsGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160)
	require.NoError(t, err)

	assert.Contains(t, gotQuery, "limit=50")
	require.Len(t, comments.Nodes, 2)
	assert.Equal(t, 2, comments.TotalCount)
	assert.Equal(t, "11", comments.Nodes[0].ID)
	assert.Equal(t, "mine", comments.Nodes[0].Body)
	assert.True(t, comments.Nodes[0].ViewerDidAuthor)
	assert.False(t, comments.Nodes[0].IncludesCreatedEdit, "created_at == updated_at")
	assert.False(t, comments.Nodes[1].ViewerDidAuthor)
	assert.True(t, comments.Nodes[1].IncludesCreatedEdit, "updated_at differs from created_at")
	assert.False(t, comments.PageInfo.HasNextPage, "the whole list is returned, so no preload follows")
}

// Gitea serves public repositories without a token. A failure to identify the
// viewer must not turn an anonymous read into an error, or `gm issue view` on a
// public repository would stop working for anyone not logged in.
func TestIssueCommentsGiteaToleratesAnonymous(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StatusStringResponse(401, `{"message":"token is required"}`),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/issues/160/comments"),
		httpmock.StringResponse(`[{"id":11,"body":"mine","user":{"id":1,"login":"myrqs"}}]`),
	)

	comments, err := IssueCommentsGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160)
	require.NoError(t, err)
	require.Len(t, comments.Nodes, 1)
	assert.False(t, comments.Nodes[0].ViewerDidAuthor)
}

// A non-401 failure identifying the viewer is a real error and must not be
// swallowed, or a server problem would read as "no comments are mine".
func TestIssueCommentsGiteaPropagatesOtherAuthFailures(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StatusStringResponse(403, `{"message":"forbidden"}`),
	)

	_, err := IssueCommentsGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

// Gitea reports one reaction row per user, so the counts the GraphQL shape
// carries have to be aggregated here, and the names translated to the enum
// values consumers compare against.
func TestIssueReactionGroupsGitea(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/issues/160/reactions"),
		httpmock.StringResponse(`[
			{"content":"+1"},{"content":"+1"},{"content":"heart"},
			{"content":"rocket"},{"content":"not-a-reaction"}
		]`),
	)

	groups, err := IssueReactionGroupsGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160)
	require.NoError(t, err)

	byContent := map[string]int{}
	for _, g := range groups {
		byContent[g.Content] = g.Count()
	}
	assert.Equal(t, 2, byContent["THUMBS_UP"])
	assert.Equal(t, 1, byContent["HEART"])
	assert.Equal(t, 1, byContent["ROCKET"])
	assert.Len(t, groups, 3, "an unrecognized reaction is skipped rather than misnamed")

	// The emoji a consumer prints comes from the translated enum value.
	for _, g := range groups {
		if g.Content == "THUMBS_UP" {
			assert.Equal(t, "\U0001f44d", g.Emoji())
		}
	}
}

func TestIssueReactionGroupsGiteaEmpty(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	// Gitea answers an empty reaction list with null rather than [].
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/issues/160/reactions"),
		httpmock.StringResponse(`null`),
	)

	groups, err := IssueReactionGroupsGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160)
	require.NoError(t, err)
	assert.Empty(t, groups)
}

func TestIssueStatusGitea(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// One request per bucket, each filtered by its own parameter and carrying its
	// total in X-Total-Count. Registry stubs are one-shot, so the responder is
	// registered once per bucket.
	seen := map[string]string{}
	counts := map[string]string{
		"assigned_by": "1",
		"created_by":  "7",
	}
	bucketResponder := func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		for _, key := range []string{"assigned_by", "mentioned_by", "created_by"} {
			if v := q.Get(key); v != "" {
				seen[key] = v
			}
		}
		assert.Equal(t, "issues", q.Get("type"), "pull requests share the index space and must be excluded")
		assert.Equal(t, "open", q.Get("state"))

		total := "0"
		for key, value := range counts {
			if q.Get(key) != "" {
				total = value
			}
		}

		body := `[]`
		if q.Get("created_by") != "" {
			body = `[{"id":1,"number":5,"title":"mine","state":"open","html_url":"https://gitea.com/gitea/tea/issues/5"}]`
		}

		// X-Total-Count is what makes the count meaningful for a bucket
		// holding more issues than one page, so the stub has to set it.
		resp, err := httpmock.StringResponse(body)(req)
		if err != nil {
			return nil, err
		}
		resp.Header.Set("X-Total-Count", total)
		return resp, nil
	}
	for range 3 {
		reg.Register(httpmock.MatchAny, bucketResponder)
	}

	payload, err := IssueStatusGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo,
		IssueStatusOptions{Username: "myrqs"})
	require.NoError(t, err)

	assert.Equal(t, "myrqs", seen["assigned_by"])
	assert.Equal(t, "myrqs", seen["mentioned_by"])
	assert.Equal(t, "myrqs", seen["created_by"])

	assert.Equal(t, 1, payload.Assigned.TotalCount)
	assert.Equal(t, 0, payload.Mentioned.TotalCount)
	assert.Equal(t, 7, payload.Authored.TotalCount)

	require.Len(t, payload.Authored.Issues, 1)
	assert.Equal(t, 5, payload.Authored.Issues[0].Number)
	assert.Equal(t, "OPEN", payload.Authored.Issues[0].State)
}

// The count comes from the header, not from the page length, so a bucket holding
// more issues than one page still reports how many there are.
func TestIssueStatusGiteaCountFromHeader(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	headerResponder := func(req *http.Request) (*http.Response, error) {
		resp, err := httpmock.StringResponse(`[{"id":1,"number":5,"title":"t","state":"open"}]`)(req)
		if err != nil {
			return nil, err
		}
		resp.Header.Set("X-Total-Count", "99")
		return resp, nil
	}
	for range 3 {
		reg.Register(httpmock.MatchAny, headerResponder)
	}

	payload, err := IssueStatusGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo,
		IssueStatusOptions{Username: "myrqs"})
	require.NoError(t, err)
	assert.Equal(t, 99, payload.Assigned.TotalCount)
	assert.Len(t, payload.Assigned.Issues, 1)
}

func TestSetIssueStateGitea(t *testing.T) {
	withGiteaIssueHost(t)

	for _, state := range []string{"closed", "open"} {
		t.Run(state, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			var gotMethod, gotPath, gotBody string
			reg.Register(
				httpmock.MatchAny,
				func(req *http.Request) (*http.Response, error) {
					gotMethod = req.Method
					gotPath = req.URL.Path
					b, _ := io.ReadAll(req.Body)
					gotBody = string(b)
					// A successful PATCH answers 201 with the updated issue, and
					// the write must not depend on decoding it.
					return httpmock.StatusStringResponse(201, "")(req)
				},
			)

			err := SetIssueStateGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160, state)
			require.NoError(t, err)

			assert.Equal(t, http.MethodPatch, gotMethod)
			assert.Equal(t, "/api/v1/repos/gitea/tea/issues/160", gotPath)

			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(gotBody), &body))
			assert.Equal(t, state, body["state"])
			// An unedited field must not be sent, or it would overwrite what is
			// there.
			assert.NotContains(t, body, "title")
			assert.NotContains(t, body, "body")
		})
	}
}

func TestLockIssueGitea(t *testing.T) {
	withGiteaIssueHost(t)

	tests := []struct {
		name         string
		reason       string
		wantMethod   string
		wantLockRes  bool
		wantReasonIn string
	}{
		{
			name:        "with reason",
			reason:      "off_topic",
			wantMethod:  http.MethodPut,
			wantLockRes: true,
		},
		{
			name:       "without reason omits the field",
			reason:     "",
			wantMethod: http.MethodPut,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			var gotMethod, gotPath, gotBody string
			reg.Register(
				httpmock.MatchAny,
				func(req *http.Request) (*http.Response, error) {
					gotMethod = req.Method
					gotPath = req.URL.Path
					b, _ := io.ReadAll(req.Body)
					gotBody = string(b)
					return httpmock.StatusStringResponse(204, "")(req)
				},
			)

			err := LockIssueGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160, tt.reason)
			require.NoError(t, err)

			assert.Equal(t, tt.wantMethod, gotMethod)
			assert.Equal(t, "/api/v1/repos/gitea/tea/issues/160/lock", gotPath)

			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(gotBody), &body))
			if tt.wantLockRes {
				// Gitea stores the reason as free text and does not validate it,
				// so GitHub's enum value goes through unchanged.
				assert.Equal(t, "off_topic", body["lock_reason"])
			} else {
				assert.NotContains(t, body, "lock_reason")
			}
		})
	}
}

func TestUnlockIssueGitea(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotMethod, gotPath string
	reg.Register(
		httpmock.MatchAny,
		func(req *http.Request) (*http.Response, error) {
			gotMethod = req.Method
			gotPath = req.URL.Path
			return httpmock.StatusStringResponse(204, "")(req)
		},
	)

	require.NoError(t, UnlockIssueGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160))
	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/api/v1/repos/gitea/tea/issues/160/lock", gotPath)
}

func TestPinAndUnpinIssueGitea(t *testing.T) {
	withGiteaIssueHost(t)

	tests := []struct {
		name       string
		call       func(*Client, ghrepo.Interface, int) error
		wantMethod string
	}{
		{"pin", PinIssueGitea, http.MethodPost},
		{"unpin", UnpinIssueGitea, http.MethodDelete},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			var gotMethod, gotPath string
			reg.Register(
				httpmock.MatchAny,
				func(req *http.Request) (*http.Response, error) {
					gotMethod = req.Method
					gotPath = req.URL.Path
					return httpmock.StatusStringResponse(204, "")(req)
				},
			)

			require.NoError(t, tt.call(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160))
			assert.Equal(t, tt.wantMethod, gotMethod)
			assert.Equal(t, "/api/v1/repos/gitea/tea/issues/160/pin", gotPath)
		})
	}
}

func TestDeleteIssueGitea(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotMethod, gotPath string
	reg.Register(
		httpmock.MatchAny,
		func(req *http.Request) (*http.Response, error) {
			gotMethod = req.Method
			gotPath = req.URL.Path
			return httpmock.StatusStringResponse(204, "")(req)
		},
	)

	require.NoError(t, DeleteIssueGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160))
	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/api/v1/repos/gitea/tea/issues/160", gotPath)
}

func TestCreateIssueGitea(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotMethod, gotPath, gotBody string
	reg.Register(
		httpmock.MatchAny,
		func(req *http.Request) (*http.Response, error) {
			gotMethod = req.Method
			gotPath = req.URL.Path
			b, _ := io.ReadAll(req.Body)
			gotBody = string(b)
			return httpmock.StringResponse(`{"id":99,"number":161,"title":"new","state":"open",
				"html_url":"https://gitea.com/gitea/tea/issues/161"}`)(req)
		},
	)

	issue, err := CreateIssueGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo,
		IssueCreateGiteaInput{
			Title:       "new",
			Body:        "body",
			Assignees:   []string{"myrqs"},
			LabelIDs:    []int64{17304},
			ProjectIDs:  []int64{7},
			MilestoneID: 5729,
		})
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/api/v1/repos/gitea/tea/issues", gotPath)
	assert.Equal(t, 161, issue.Number)
	assert.Equal(t, "https://gitea.com/gitea/tea/issues/161", issue.URL)
	assert.Equal(t, TypeIssue, issue.Typename)

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(gotBody), &body))
	assert.Equal(t, "new", body["title"])
	assert.Equal(t, "body", body["body"])
	// Assignees are logins on Gitea, ids on GitHub, so no user lookup happens.
	assert.Equal(t, []any{"myrqs"}, body["assignees"])
	assert.Equal(t, []any{float64(17304)}, body["labels"])
	assert.Equal(t, []any{float64(7)}, body["projects"])
	assert.Equal(t, float64(5729), body["milestone"])
}

// An omitted field means no change on Gitea, and a zero milestone would clear
// one, so an unset milestone must not appear in the body at all.
func TestCreateIssueGiteaOmitsEmptyOptionalFields(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotBody string
	reg.Register(
		httpmock.MatchAny,
		func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			gotBody = string(b)
			return httpmock.StringResponse(`{"id":1,"number":2,"title":"t","state":"open"}`)(req)
		},
	)

	_, err := CreateIssueGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo,
		IssueCreateGiteaInput{Title: "t"})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(gotBody), &body))
	assert.Equal(t, "t", body["title"])
	assert.NotContains(t, body, "milestone")
	assert.NotContains(t, body, "labels")
	assert.NotContains(t, body, "projects")
	assert.NotContains(t, body, "assignees")
	assert.NotContains(t, body, "body")
}

func TestCreateCommentGitea(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotMethod, gotPath, gotBody string
	reg.Register(
		httpmock.MatchAny,
		func(req *http.Request) (*http.Response, error) {
			gotMethod = req.Method
			gotPath = req.URL.Path
			b, _ := io.ReadAll(req.Body)
			gotBody = string(b)
			return httpmock.StatusStringResponse(201,
				`{"id":42,"body":"hi","html_url":"https://gitea.com/gitea/tea/issues/160#issuecomment-42"}`)(req)
		},
	)

	url, err := CreateCommentGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 160, "hi")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/api/v1/repos/gitea/tea/issues/160/comments", gotPath)
	assert.Equal(t, "https://gitea.com/gitea/tea/issues/160#issuecomment-42", url)

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(gotBody), &body))
	assert.Equal(t, "hi", body["body"])
}

func TestUpdateAndDeleteCommentGitea(t *testing.T) {
	withGiteaIssueHost(t)

	// Comments are addressed by numeric id within a repository, not by a global
	// node id, so the repository is part of the path.
	t.Run("update", func(t *testing.T) {
		reg := &httpmock.Registry{}
		defer reg.Verify(t)

		var gotMethod, gotPath, gotBody string
		reg.Register(
			httpmock.MatchAny,
			func(req *http.Request) (*http.Response, error) {
				gotMethod = req.Method
				gotPath = req.URL.Path
				b, _ := io.ReadAll(req.Body)
				gotBody = string(b)
				return httpmock.StatusStringResponse(200,
					`{"id":42,"body":"new","html_url":"https://gitea.com/gitea/tea/issues/160#issuecomment-42"}`)(req)
			},
		)

		url, err := UpdateCommentGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 42, "new")
		require.NoError(t, err)

		assert.Equal(t, http.MethodPatch, gotMethod)
		assert.Equal(t, "/api/v1/repos/gitea/tea/issues/comments/42", gotPath)
		assert.Equal(t, "https://gitea.com/gitea/tea/issues/160#issuecomment-42", url)

		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(gotBody), &body))
		assert.Equal(t, "new", body["body"])
	})

	t.Run("delete", func(t *testing.T) {
		reg := &httpmock.Registry{}
		defer reg.Verify(t)

		var gotMethod, gotPath string
		reg.Register(
			httpmock.MatchAny,
			func(req *http.Request) (*http.Response, error) {
				gotMethod = req.Method
				gotPath = req.URL.Path
				return httpmock.StatusStringResponse(204, "")(req)
			},
		)

		require.NoError(t, DeleteCommentGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, 42))
		assert.Equal(t, http.MethodDelete, gotMethod)
		assert.Equal(t, "/api/v1/repos/gitea/tea/issues/comments/42", gotPath)
	})
}

func TestRepoAssignableUsersGitea(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/assignees"),
		httpmock.StringResponse(`[
			{"id":1,"login":"myrqs","full_name":"My Rqs"},
			{"id":2,"login":"other","full_name":""}
		]`),
	)

	users, err := RepoAssignableUsersGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo)
	require.NoError(t, err)

	require.Len(t, users, 2)
	assert.Equal(t, "myrqs", users[0].Login())
	assert.Equal(t, "My Rqs", users[0].Name())
	assert.Equal(t, "1", users[0].ID())
	assert.Equal(t, "other", users[1].Login())
	assert.Equal(t, "other", users[1].DisplayName(), "a user with no full name displays as its login")
}

func TestRepoLabelsGitea(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/labels"),
		httpmock.StringResponse(`[{"id":17304,"name":"proposal","color":"0052cc"}]`),
	)

	labels, err := RepoLabelsGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo)
	require.NoError(t, err)

	require.Len(t, labels, 1)
	assert.Equal(t, "17304", labels[0].ID, "the id is a decimal string, matching what LabelsToIDs returns")
	assert.Equal(t, "proposal", labels[0].Name)
}

func TestRepoMilestonesGitea(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotQuery string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/milestones"),
		func(req *http.Request) (*http.Response, error) {
			gotQuery = req.URL.RawQuery
			return httpmock.StringResponse(`[{"id":5729,"title":"1.4.0","state":"open"}]`)(req)
		},
	)

	milestones, err := RepoMilestonesGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, "open")
	require.NoError(t, err)

	assert.Contains(t, gotQuery, "state=open")
	require.Len(t, milestones, 1)
	assert.Equal(t, "5729", milestones[0].ID)
	assert.Equal(t, "1.4.0", milestones[0].Title)
}

func TestRepoMilestonesGiteaRejectsUnknownState(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	_, err := RepoMilestonesGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo, "maybe")
	require.EqualError(t, err, "invalid state: maybe")
}

func TestRepoLabelsGiteaPages(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	full := "["
	for i := range giteaPageSize {
		if i > 0 {
			full += ","
		}
		full += `{"id":1,"name":"l"}`
	}
	full += "]"

	pages := 0
	count := func(responder httpmock.Responder) httpmock.Responder {
		return func(req *http.Request) (*http.Response, error) {
			pages++
			return responder(req)
		}
	}
	reg.Register(httpmock.REST("GET", "api/v1/repos/gitea/tea/labels"), count(httpmock.StringResponse(full)))
	reg.Register(httpmock.REST("GET", "api/v1/repos/gitea/tea/labels"),
		count(httpmock.StringResponse(`[{"id":2,"name":"last"}]`)))

	labels, err := RepoLabelsGitea(NewClientFromHTTP(&http.Client{Transport: reg}), giteaIssueRepo)
	require.NoError(t, err)
	assert.Len(t, labels, giteaPageSize+1)
	assert.Equal(t, 2, pages)
}

// Gitea has no linked-branch type, so `gm issue develop` has to say so rather
// than fail on an introspection query the host cannot answer.
func TestCheckLinkedBranchFeatureGitea(t *testing.T) {
	withGiteaIssueHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	err := CheckLinkedBranchFeature(NewClientFromHTTP(&http.Client{Transport: reg}), "git.example.com")
	require.ErrorIs(t, err, ErrGiteaLinkedBranchUnsupported)
}

// A GitHub host keeps the introspection path, so the detector is still asked.
func TestCheckLinkedBranchFeatureGitHub(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(nil)
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.GraphQL(`query LinkedBranchFeature\b`),
		httpmock.StringResponse(`{"data":{"LinkedBranch":{"fields":[{"name":"id"}]}}}`),
	)

	require.NoError(t, CheckLinkedBranchFeature(NewClientFromHTTP(&http.Client{Transport: reg}), "github.com"))
}
