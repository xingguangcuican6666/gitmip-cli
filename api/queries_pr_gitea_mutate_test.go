package api

import (
	"net/http"
	"testing"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withGiteaPullHost(t *testing.T) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)
}

var giteaPullRepo = ghrepo.NewWithHost("gitea", "tea", "git.example.com")

func TestPullRequestStatusChecksGitea(t *testing.T) {
	withGiteaPullHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/commits/abc123/status"),
		httpmock.StringResponse(`{
			"sha": "abc123",
			"state": "pending",
			"total_count": 3,
			"statuses": [
				{"context": "ci/lint", "status": "success", "target_url": "https://ci/1", "description": "ok", "created_at": "2024-05-01T10:00:00Z"},
				{"context": "ci/test", "status": "failure", "target_url": "https://ci/2", "description": "boom"},
				{"context": "ci/flaky", "status": "warning", "target_url": "https://ci/3"}
			]
		}`),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	contexts, err := PullRequestStatusChecksGitea(client, giteaPullRepo, "abc123")
	require.NoError(t, err)

	require.Len(t, contexts.Nodes, 3)
	assert.Equal(t, "StatusContext", contexts.Nodes[0].TypeName)
	assert.Equal(t, "ci/lint", contexts.Nodes[0].Context)
	assert.Equal(t, StatusStateSuccess, contexts.Nodes[0].State)
	assert.Equal(t, "https://ci/1", contexts.Nodes[0].TargetURL)
	assert.Equal(t, "ok", contexts.Nodes[0].Description)
	assert.Equal(t, StatusStateFailure, contexts.Nodes[1].State)
	assert.Equal(t, StatusStateWarning, contexts.Nodes[2].State)
	assert.Equal(t, 3, contexts.StatusContextCount)
}

func TestPullRequestStatusChecksGiteaEmpty(t *testing.T) {
	withGiteaPullHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/commits/abc123/status"),
		httpmock.StringResponse(`{"sha":"abc123","state":"pending","total_count":0,"statuses":[]}`),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	contexts, err := PullRequestStatusChecksGitea(client, giteaPullRepo, "abc123")
	require.NoError(t, err)
	assert.Empty(t, contexts.Nodes)
}

func TestSetPullRequestStateGitea(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
	}{
		{name: "closed", state: "closed"},
		{name: "open", state: "open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withGiteaPullHost(t)
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			var body map[string]any
			reg.Register(
				httpmock.REST("PATCH", "api/v1/repos/gitea/tea/pulls/123"),
				httpmock.RESTPayload(200, `{}`, func(p map[string]any) { body = p }),
			)

			client := NewClientFromHTTP(&http.Client{Transport: reg})
			err := SetPullRequestStateGitea(client, giteaPullRepo, 123, tc.state)
			require.NoError(t, err)
			assert.Equal(t, tc.state, body["state"])
		})
	}
}

func TestSetPullRequestStateGiteaRejectsUnknownState(t *testing.T) {
	withGiteaPullHost(t)
	client := NewClientFromHTTP(&http.Client{Transport: &httpmock.Registry{}})
	err := SetPullRequestStateGitea(client, giteaPullRepo, 1, "merged")
	assert.EqualError(t, err, `state must be open or closed, got "merged"`)
}

func TestAddReviewGitea(t *testing.T) {
	withGiteaPullHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var body map[string]any
	reg.Register(
		httpmock.REST("POST", "api/v1/repos/gitea/tea/pulls/7/reviews"),
		httpmock.RESTPayload(200, `{}`, func(p map[string]any) { body = p }),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	err := AddReviewGitea(client, giteaPullRepo, 7, GiteaReviewEventApproved, "looks good", "")
	require.NoError(t, err)

	assert.Equal(t, "APPROVED", body["event"])
	assert.Equal(t, "looks good", body["body"])
	_, hasCommit := body["commit_id"]
	assert.False(t, hasCommit, "an empty commit id must be omitted")
}

func TestAddReviewGiteaRejectsUnknownEvent(t *testing.T) {
	withGiteaPullHost(t)
	client := NewClientFromHTTP(&http.Client{Transport: &httpmock.Registry{}})
	err := AddReviewGitea(client, giteaPullRepo, 7, "PENDING", "", "")
	assert.EqualError(t, err, `unsupported review event "PENDING"`)
}

func TestGiteaReviewEventFor(t *testing.T) {
	assert.Equal(t, GiteaReviewEventApproved, GiteaReviewEventFor(ReviewApprove))
	assert.Equal(t, GiteaReviewEventRequestChanges, GiteaReviewEventFor(ReviewRequestChanges))
	assert.Equal(t, GiteaReviewEventComment, GiteaReviewEventFor(ReviewComment))
}

func TestCreatePullRequestGitea(t *testing.T) {
	withGiteaPullHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var body map[string]any
	reg.Register(
		httpmock.REST("POST", "api/v1/repos/gitea/tea/pulls"),
		httpmock.RESTPayload(200, `{
			"id": 9,
			"number": 42,
			"title": "a title",
			"state": "open",
			"html_url": "https://git.example.com/gitea/tea/pulls/42",
			"base": {"ref": "main", "sha": "base"},
			"head": {"ref": "feature", "sha": "head"},
			"user": {"id": 1, "login": "someone"}
		}`, func(p map[string]any) { body = p }),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	pr, err := CreatePullRequestGitea(client, giteaPullRepo, CreatePullRequestGiteaInput{
		Title:     "a title",
		Body:      "a body",
		Base:      "main",
		Head:      "feature",
		Assignees: []string{"someone"},
		Reviewers: []string{"reviewer"},
		Labels:    []int64{3},
		Milestone: 5,
	})
	require.NoError(t, err)

	assert.Equal(t, "a title", body["title"])
	assert.Equal(t, "a body", body["body"])
	assert.Equal(t, "main", body["base"])
	assert.Equal(t, "feature", body["head"])
	assert.Equal(t, []any{"someone"}, body["assignees"])
	assert.Equal(t, []any{"reviewer"}, body["reviewers"])
	assert.Equal(t, []any{float64(3)}, body["labels"])
	assert.Equal(t, float64(5), body["milestone"])

	assert.Equal(t, 42, pr.Number)
	assert.Equal(t, "https://git.example.com/gitea/tea/pulls/42", pr.URL)
	assert.Equal(t, "OPEN", pr.State)
}

func TestCreatePullRequestGiteaOmitsEmptyOptionalFields(t *testing.T) {
	withGiteaPullHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var body map[string]any
	reg.Register(
		httpmock.REST("POST", "api/v1/repos/gitea/tea/pulls"),
		httpmock.RESTPayload(200, `{"number":1,"state":"open"}`, func(p map[string]any) { body = p }),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	_, err := CreatePullRequestGitea(client, giteaPullRepo, CreatePullRequestGiteaInput{
		Title: "t",
		Base:  "main",
		Head:  "feature",
	})
	require.NoError(t, err)

	for _, key := range []string{"body", "assignees", "reviewers", "labels", "milestone", "allow_maintainer_edit"} {
		assert.False(t, hasKey(body, key), "%s should be omitted when empty", key)
	}
}

func hasKey(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

func TestUpdatePullRequestBranchGitea(t *testing.T) {
	withGiteaPullHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("POST", "api/v1/repos/gitea/tea/pulls/5/update"),
		httpmock.StatusStringResponse(204, ""),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	require.NoError(t, UpdatePullRequestBranchGitea(client, giteaPullRepo, 5))
}

func TestComparePullRequestBaseBranchGitea(t *testing.T) {
	withGiteaPullHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// The pull request is read twice: once for its refs and once for merge_base.
	for range 2 {
		reg.Register(
			httpmock.REST("GET", "api/v1/repos/gitea/tea/pulls/5"),
			httpmock.StringResponse(`{
				"id": 5, "number": 5, "state": "open",
				"base": {"ref": "main", "sha": "basesha", "repo_id": 1},
				"head": {"ref": "feature", "sha": "headsha", "repo_id": 2,
					"repo": {"id": 2, "name": "tea", "full_name": "someone/tea", "owner": {"id": 2, "login": "someone"}}},
				"merge_base": "basesha",
				"user": {"id": 1, "login": "someone"}
			}`),
		)
	}
	// ahead: base...someone:feature
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/compare/main...someone:feature"),
		httpmock.StringResponse(`{"total_commits": 2}`),
	)
	// behind: merge base...base
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/compare/basesha...main"),
		httpmock.StringResponse(`{"total_commits": 0}`),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	cmp, err := ComparePullRequestBaseBranchGitea(client, giteaPullRepo, 5)
	require.NoError(t, err)

	assert.Equal(t, 2, cmp.AheadBy)
	assert.Equal(t, 0, cmp.BehindBy)
	assert.Equal(t, "ahead", cmp.Status)
}

func TestComparePullRequestBaseBranchGiteaBehind(t *testing.T) {
	withGiteaPullHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	for range 2 {
		reg.Register(
			httpmock.REST("GET", "api/v1/repos/gitea/tea/pulls/5"),
			httpmock.StringResponse(`{
				"id": 5, "number": 5, "state": "open",
				"base": {"ref": "main", "sha": "basesha", "repo_id": 1},
				"head": {"ref": "feature", "sha": "headsha", "repo_id": 1},
				"merge_base": "oldsha",
				"user": {"id": 1, "login": "someone"}
			}`),
		)
	}
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/compare/main...feature"),
		httpmock.StringResponse(`{"total_commits": 0}`),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/compare/oldsha...main"),
		httpmock.StringResponse(`{"total_commits": 3}`),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	cmp, err := ComparePullRequestBaseBranchGitea(client, giteaPullRepo, 5)
	require.NoError(t, err)

	assert.Equal(t, 0, cmp.AheadBy)
	assert.Equal(t, 3, cmp.BehindBy)
	assert.Equal(t, "behind", cmp.Status)
}

func TestDisableAutoMergeGitea(t *testing.T) {
	withGiteaPullHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("DELETE", "api/v1/repos/gitea/tea/pulls/5/merge"),
		httpmock.StatusStringResponse(204, ""),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	require.NoError(t, DisableAutoMergeGitea(client, giteaPullRepo, 5))
}

func TestMergePullRequestGitea(t *testing.T) {
	withGiteaPullHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var body map[string]any
	reg.Register(
		httpmock.REST("POST", "api/v1/repos/gitea/tea/pulls/5/merge"),
		httpmock.RESTPayload(200, `{}`, func(p map[string]any) { body = p }),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	err := MergePullRequestGitea(client, giteaPullRepo, 5, MergePullRequestGiteaInput{
		Method:                 GiteaMergeMethodSquash,
		MergeTitle:             "subject",
		HeadCommitID:           "abc",
		DeleteBranchAfterMerge: true,
		MergeWhenChecksSucceed: true,
	}, true)
	require.NoError(t, err)

	assert.Equal(t, "squash", body["do"])
	assert.Equal(t, "subject", body["merge_title_field"])
	assert.Equal(t, "abc", body["head_commit_id"])
	assert.Equal(t, true, body["delete_branch_after_merge"])
	assert.Equal(t, true, body["merge_when_checks_succeed"])
	assert.Equal(t, true, body["force_merge"])
}

func TestBranchDeleteRemoteGitea(t *testing.T) {
	withGiteaPullHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("DELETE", "api/v1/repos/gitea/tea/git/branches/feature%2Fone"),
		httpmock.StatusStringResponse(204, ""),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	require.NoError(t, BranchDeleteRemoteGitea(client, giteaPullRepo, "feature/one"))
}
