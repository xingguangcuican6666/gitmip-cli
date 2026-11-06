package status

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withGiteaStatusHost(t *testing.T) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)
}

// newGiteaStatusGetter builds a getter whose cached client is the plain one, so a
// stub registered once answers every lookup and the registry can verify the count
// exactly.
func newGiteaStatusGetter(reg *httpmock.Registry, opts *StatusOptions) *StatusGetter {
	client := &http.Client{Transport: reg}
	if opts.CachedClient == nil {
		opts.CachedClient = func(c *http.Client, _ time.Duration) *http.Client { return c }
	}
	return NewStatusGetter(client, "git.example.com", opts)
}

// stubGiteaUser registers the login lookup. The response is cached by the getter
// through CachedClient, which the tests point straight at the registry, so the
// stub has to be registered for every call that resolves the login.
func stubGiteaUser(reg *httpmock.Registry, times int) {
	for range times {
		reg.Register(
			httpmock.REST("GET", "api/v1/user"),
			httpmock.StringResponse(`{"id":1,"login":"jillvalentine"}`))
	}
}

func TestStatusLoadSearchResultsGitea(t *testing.T) {
	withGiteaStatusHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaUser(reg, 1)

	// assigned
	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/issues/search", url.Values{"assigned": []string{"true"}}),
		httpmock.StringResponse(`[
			{"number":11,"title":"an issue","html_url":"https://git.example.com/gitea/tea/issues/11",
			 "state":"open","repository":{"full_name":"gitea/tea"}},
			{"number":22,"title":"a pr","html_url":"https://git.example.com/gitea/tea/pulls/22",
			 "state":"open","repository":{"full_name":"gitea/tea"},
			 "pull_request":{"merged":false}}
		]`))
	// review_requested
	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/issues/search", url.Values{"review_requested": []string{"true"}}),
		httpmock.StringResponse(`[
			{"number":33,"title":"needs review","html_url":"https://git.example.com/gitea/tea/pulls/33",
			 "state":"open","repository":{"full_name":"gitea/tea"},
			 "pull_request":{"merged":false}}
		]`))

	sg := newGiteaStatusGetter(reg, &StatusOptions{})
	require.NoError(t, sg.loadSearchResultsGitea())

	require.Len(t, sg.AssignedIssues, 1)
	assert.Equal(t, "gitea/tea#11", sg.AssignedIssues[0].Identifier)
	assert.Equal(t, "an issue", sg.AssignedIssues[0].Preview())

	require.Len(t, sg.AssignedPRs, 1)
	assert.Equal(t, "gitea/tea#22", sg.AssignedPRs[0].Identifier)

	require.Len(t, sg.ReviewRequests, 1)
	assert.Equal(t, "gitea/tea#33", sg.ReviewRequests[0].Identifier)
}

func TestStatusLoadSearchResultsGiteaHonoursExcludeAndOrg(t *testing.T) {
	withGiteaStatusHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaUser(reg, 1)

	body := `[
		{"number":1,"title":"kept","repository":{"full_name":"gitea/tea"}},
		{"number":2,"title":"excluded","repository":{"full_name":"other/repo"}},
		{"number":3,"title":"wrong org","repository":{"full_name":"third/repo"}}
	]`
	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/issues/search", url.Values{"assigned": []string{"true"}}),
		httpmock.StringResponse(body))
	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/issues/search", url.Values{"review_requested": []string{"true"}}),
		httpmock.StringResponse(`[]`))

	sg := newGiteaStatusGetter(reg, &StatusOptions{
		Exclude: []string{"other/repo"},
		Org:     "gitea",
	})
	require.NoError(t, sg.loadSearchResultsGitea())

	require.Len(t, sg.AssignedIssues, 1)
	assert.Equal(t, "kept", sg.AssignedIssues[0].Preview())
}

func TestStatusLoadNotificationsGitea(t *testing.T) {
	withGiteaStatusHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaUser(reg, 1)

	reg.Register(
		httpmock.REST("GET", "api/v1/notifications"),
		httpmock.StringResponse(`[
			{"id":1,"unread":true,
			 "subject":{"title":"the issue","type":"issue",
			            "html_url":"https://git.example.com/gitea/tea/issues/7",
			            "latest_comment_url":"https://git.example.com/api/v1/repos/gitea/tea/issues/comments/99"},
			 "repository":{"full_name":"gitea/tea","owner":{"login":"gitea"}}},
			{"id":2,"unread":true,
			 "subject":{"title":"a commit","type":"commit","html_url":"https://git.example.com/gitea/tea/commit/abc"},
			 "repository":{"full_name":"gitea/tea","owner":{"login":"gitea"}}}
		]`))
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/issues/comments/99"),
		httpmock.StringResponse(`{"body":"hello @jillvalentine"}`))

	sg := newGiteaStatusGetter(reg, &StatusOptions{})
	require.NoError(t, sg.loadNotificationsGitea())

	require.Len(t, sg.Mentions, 1)
	assert.Equal(t, "gitea/tea#7", sg.Mentions[0].Identifier)
	assert.Equal(t, "hello @jillvalentine", sg.Mentions[0].Preview())
}

func TestStatusLoadEventsGitea(t *testing.T) {
	withGiteaStatusHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaUser(reg, 1)

	reg.Register(
		httpmock.REST("GET", "api/v1/users/jillvalentine/activities/feeds"),
		httpmock.StringResponse(`[
			{"op_type":"create_issue","content":"15|a new issue","repo":{"full_name":"gitea/tea","owner":{"login":"gitea"}}},
			{"op_type":"comment_pull_request","content":"16|a pr title","repo":{"full_name":"gitea/tea","owner":{"login":"gitea"}}},
			{"op_type":"star_repo","content":"17|starred","repo":{"full_name":"gitea/tea","owner":{"login":"gitea"}}}
		]`))

	sg := newGiteaStatusGetter(reg, &StatusOptions{})
	require.NoError(t, sg.loadEventsGitea())

	require.Len(t, sg.RepoActivity, 2)
	assert.Equal(t, "gitea/tea#15", sg.RepoActivity[0].Identifier)
	assert.Equal(t, "new issue", sg.RepoActivity[0].Reason)
	assert.Equal(t, "a new issue", sg.RepoActivity[0].Preview())
	assert.Equal(t, "comment on a pr title", sg.RepoActivity[1].Reason)
}

func TestStatusGiteaUnauthenticated(t *testing.T) {
	withGiteaStatusHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StatusStringResponse(401, `{"message":"token is required"}`))

	sg := newGiteaStatusGetter(reg, &StatusOptions{})
	err := sg.loadSearchResultsGitea()
	assert.ErrorIs(t, err, ErrGiteaStatusUnauthenticated)
}

func TestStatusLoadEventsGiteaToleratesMissingFeeds(t *testing.T) {
	withGiteaStatusHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaUser(reg, 1)
	reg.Register(
		httpmock.REST("GET", "api/v1/users/jillvalentine/activities/feeds"),
		httpmock.StatusStringResponse(404, `{"message":"not found"}`))

	sg := newGiteaStatusGetter(reg, &StatusOptions{})
	require.NoError(t, sg.loadEventsGitea())
	assert.Empty(t, sg.RepoActivity)
}

func TestGiteaContentHelpers(t *testing.T) {
	assert.Equal(t, 15, giteaContentNumber("15|a title"))
	assert.Equal(t, 0, giteaContentNumber("not a number"))
	assert.Equal(t, "a title", giteaContentText("15|a title"))
	assert.Equal(t, "no separator", giteaContentText("no separator"))

	assert.Equal(t, 7, giteaSubjectNumber("https://git.example.com/gitea/tea/issues/7"))
	assert.Equal(t, 0, giteaSubjectNumber("https://git.example.com/gitea/tea/pulls/abc"))

	reason, preview := giteaFeedReason(giteaFeedRow{OpType: "create_pull_request", Content: "9|the pr"})
	assert.Equal(t, "new PR", reason)
	assert.Equal(t, "the pr", preview)

	reason, _ = giteaFeedReason(giteaFeedRow{OpType: "rename_repo"})
	assert.Empty(t, reason)

	assert.True(t, giteaStatusTime("2021-01-01T00:00:00Z").Equal(time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)))
	assert.True(t, giteaStatusTime("not a time").IsZero())
}

func TestStatusRunGiteaUnauthenticated(t *testing.T) {
	withGiteaStatusHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StatusStringResponse(401, `{"message":"token is required"}`))

	ios, _, stdout, _ := iostreams.Test()
	ios.SetStdoutTTY(true)

	opts := &StatusOptions{
		HttpClient:   func() (*http.Client, error) { return &http.Client{Transport: reg}, nil },
		CachedClient: func(c *http.Client, _ time.Duration) *http.Client { return c },
		HostConfig:   testHostConfig("git.example.com"),
		IO:           ios,
	}

	err := statusRun(opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs an authenticated session")
	assert.Empty(t, stdout.String())
}
