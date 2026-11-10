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

var giteaRepo = ghrepo.NewWithHost("gitea", "tea", "git.example.com")

func withGiteaHost(t *testing.T) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)
}

// This is the shape gitea.com returns for gitea/tea, trimmed to the fields the
// mapping is expected to translate.
const giteaRepositoryBody = `{
	"id": 550,
	"owner": {"id": 3, "login": "gitea", "type": "Organization"},
	"name": "tea",
	"full_name": "gitea/tea",
	"description": "A command line tool",
	"website": "https://gitea.com/gitea/cli",
	"html_url": "https://gitea.com/gitea/tea",
	"ssh_url": "git@gitea.com:gitea/tea.git",
	"clone_url": "https://gitea.com/gitea/tea.git",
	"default_branch": "main",
	"created_at": "2019-05-19T11:37:14Z",
	"updated_at": "2026-10-29T21:06:27Z",
	"archived": true,
	"archived_at": "2026-10-29T21:07:11Z",
	"empty": false,
	"private": false,
	"internal": false,
	"fork": false,
	"mirror": false,
	"template": false,
	"size": 66862,
	"stars_count": 279,
	"forks_count": 152,
	"watchers_count": 76,
	"open_issues_count": 0,
	"open_pr_counter": 0,
	"has_issues": true,
	"has_wiki": true,
	"has_projects": false,
	"has_pull_requests": true,
	"allow_merge_commits": false,
	"allow_squash_merge": true,
	"allow_rebase": false,
	"default_delete_branch_after_merge": true,
	"topics": ["gitea", "cli"],
	"language": "Go",
	"licenses": ["MIT"],
	"permissions": {"admin": false, "push": false, "pull": true}
}`

func TestFetchRepositoryGitea(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotPath string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		func(req *http.Request) (*http.Response, error) {
			gotPath = req.URL.Path
			return httpmock.StringResponse(giteaRepositoryBody)(req)
		},
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})

	// The fields argument is ignored on Gitea: GET /repos/{o}/{r} returns the
	// whole object in one response, so there is no partial selection to make.
	repo, err := FetchRepository(client, giteaRepo, []string{"name", "owner"})
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/repos/gitea/tea", gotPath)

	assert.Equal(t, "550", repo.ID)
	assert.Equal(t, int64(550), repo.DatabaseID)
	assert.Equal(t, "tea", repo.Name)
	assert.Equal(t, "gitea/tea", repo.NameWithOwner)
	assert.Equal(t, "A command line tool", repo.Description)
	assert.Equal(t, "https://gitea.com/gitea/cli", repo.HomepageURL)
	assert.Equal(t, "https://gitea.com/gitea/tea", repo.URL)
	assert.Equal(t, "git@gitea.com:gitea/tea.git", repo.SSHURL)
	assert.Equal(t, "gitea", repo.Owner.Login)
	assert.Equal(t, "3", repo.Owner.ID)

	// The pseudo-field the GraphQL path sets has to survive the REST mapping,
	// or every caller that reads RepoHost gets an empty host.
	assert.Equal(t, "git.example.com", repo.RepoHost())
}

func TestFetchRepositoryGiteaFlagMapping(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		httpmock.StringResponse(giteaRepositoryBody),
	)

	repo, err := FetchRepository(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo, nil)
	require.NoError(t, err)

	assert.True(t, repo.IsArchived)
	assert.False(t, repo.IsEmpty)
	assert.False(t, repo.IsFork)
	assert.False(t, repo.IsPrivate)
	assert.False(t, repo.IsMirror)
	assert.False(t, repo.IsTemplate)
	assert.True(t, repo.IsInOrganization, "owner.type == Organization")
	assert.True(t, repo.HasIssuesEnabled)
	assert.True(t, repo.HasWikiEnabled)
	assert.False(t, repo.HasProjectsEnabled)

	// Gitea's three allow_* flags map onto the merge-method booleans callers
	// use to decide which merge options to offer.
	assert.False(t, repo.MergeCommitAllowed)
	assert.True(t, repo.SquashMergeAllowed)
	assert.False(t, repo.RebaseMergeAllowed)
	assert.True(t, repo.DeleteBranchOnMerge)

	assert.Equal(t, 279, repo.StargazerCount)
	assert.Equal(t, 152, repo.ForkCount)
	assert.Equal(t, 76, repo.Watchers.TotalCount)
	assert.Equal(t, 0, repo.Issues.TotalCount)
	assert.Equal(t, 0, repo.PullRequests.TotalCount)
	assert.Equal(t, 66862, repo.DiskUsage)
	assert.Equal(t, "main", repo.DefaultBranchRef.Name)
}

func TestFetchRepositoryGiteaDerivedFields(t *testing.T) {
	withGiteaHost(t)

	tests := []struct {
		name           string
		body           string
		wantVisibility string
		wantPermission string
		wantCanAdmin   bool
		wantLanguage   string
		wantLicense    string
		wantTopics     []string
	}{
		{
			name:           "public read-only",
			body:           giteaRepositoryBody,
			wantVisibility: "public",
			wantPermission: "READ",
			wantCanAdmin:   false,
			wantLanguage:   "Go",
			wantLicense:    "MIT",
			wantTopics:     []string{"gitea", "cli"},
		},
		{
			name:           "private",
			body:           `{"id":1,"full_name":"o/r","private":true,"permissions":{"admin":false,"push":true,"pull":true}}`,
			wantVisibility: "private",
			wantPermission: "WRITE",
			wantLanguage:   "",
			wantTopics:     nil,
		},
		{
			// Gitea reports internal as a flag of its own, and private wins
			// when both are somehow set.
			name:           "internal",
			body:           `{"id":1,"full_name":"o/r","internal":true,"permissions":{"admin":true,"push":true,"pull":true}}`,
			wantVisibility: "internal",
			wantPermission: "ADMIN",
			wantCanAdmin:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)
			reg.Register(
				httpmock.REST("GET", "api/v1/repos/gitea/tea"),
				httpmock.StringResponse(tt.body),
			)

			repo, err := FetchRepository(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo, nil)
			require.NoError(t, err)

			assert.Equal(t, tt.wantVisibility, repo.Visibility)
			assert.Equal(t, tt.wantPermission, repo.ViewerPermission)
			assert.Equal(t, tt.wantCanAdmin, repo.ViewerCanAdminister)

			if tt.wantLanguage == "" {
				assert.Nil(t, repo.PrimaryLanguage)
			} else {
				require.NotNil(t, repo.PrimaryLanguage)
				assert.Equal(t, tt.wantLanguage, repo.PrimaryLanguage.Name)
			}

			if tt.wantLicense == "" {
				assert.Nil(t, repo.LicenseInfo)
			} else {
				require.NotNil(t, repo.LicenseInfo)
				// Gitea reports an SPDX identifier only, so the key doubles as
				// the display name; there is no nickname to offer.
				assert.Equal(t, tt.wantLicense, repo.LicenseInfo.Key)
				assert.Equal(t, tt.wantLicense, repo.LicenseInfo.Name)
				assert.Empty(t, repo.LicenseInfo.Nickname)
			}

			var topics []string
			for _, node := range repo.RepositoryTopics.Nodes {
				topics = append(topics, node.Topic.Name)
			}
			assert.Equal(t, tt.wantTopics, topics)
		})
	}
}

// Fields with no Gitea counterpart must stay zero rather than be invented, so
// callers see absence instead of a fabricated value.
func TestFetchRepositoryGiteaAbsentFieldsStayZero(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		httpmock.StringResponse(giteaRepositoryBody),
	)

	repo, err := FetchRepository(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo, nil)
	require.NoError(t, err)

	assert.True(t, repo.PushedAt == nil, "Gitea reports no pushed_at")
	assert.Nil(t, repo.CodeOfConduct)
	assert.Nil(t, repo.ContactLinks)
	assert.Nil(t, repo.FundingLinks)
	assert.Nil(t, repo.IssueTemplates)
	assert.Nil(t, repo.PullRequestTemplates)
	assert.Nil(t, repo.LatestRelease)
	assert.Empty(t, repo.AssignableUsers.Nodes)
	assert.Empty(t, repo.MentionableUsers.Nodes)
	assert.Empty(t, repo.Milestones.Nodes)
	assert.Empty(t, repo.Labels.Nodes)
	assert.False(t, repo.AutoMergeAllowed)
	assert.False(t, repo.HasDiscussionsEnabled)
	assert.Empty(t, repo.OpenGraphImageURL)
	assert.Empty(t, repo.ViewerDefaultMergeMethod)
	assert.Empty(t, repo.ViewerSubscription)
	assert.False(t, repo.ViewerHasStarred)
}

func TestFetchRepositoryGiteaParent(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		httpmock.StringResponse(`{
			"id": 2, "full_name": "me/tea", "fork": true,
			"parent": {"id": 550, "name": "tea", "full_name": "gitea/tea",
				"owner": {"id": 3, "login": "gitea", "type": "Organization"}}
		}`),
	)

	repo, err := FetchRepository(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo, nil)
	require.NoError(t, err)

	require.NotNil(t, repo.Parent)
	assert.Equal(t, "tea", repo.Parent.Name)
	assert.Equal(t, "gitea", repo.Parent.Owner.Login)
	assert.Equal(t, "gitea/tea", repo.Parent.NameWithOwner)
	// The hostname has to reach the parent too, or a caller that resolves the
	// parent's host gets an empty string.
	assert.Equal(t, "git.example.com", repo.Parent.RepoHost())
}

func TestFetchRepositoryGiteaNotFound(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/nope"),
		httpmock.StatusStringResponse(404, `{"message":"owner/repo does not exist","url":"https://git.example.com/api/swagger"}`),
	)

	_, err := FetchRepository(NewClientFromHTTP(&http.Client{Transport: reg}),
		ghrepo.NewWithHost("gitea", "nope", "git.example.com"), nil)
	require.Error(t, err)

	// Gitea reports a missing repository as 404, so it arrives as an HTTPError
	// rather than the GraphQL NOT_FOUND wrapper the GitHub path synthesises.
	var httpErr HTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, 404, httpErr.StatusCode)
}

// The GitHub path must be untouched: a github.com host still issues GraphQL.
func TestFetchRepositoryGitHubStillUsesGraphQL(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(nil)
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.GraphQL(`query RepositoryInfo\b`),
		httpmock.StringResponse(`{"data":{"repository":{"id":"MDEx","name":"cli","owner":{"login":"cli"}}}}`),
	)

	repo, err := FetchRepository(NewClientFromHTTP(&http.Client{Transport: reg}),
		ghrepo.New("cli", "cli"), []string{"name", "owner"})
	require.NoError(t, err)
	assert.Equal(t, "cli", repo.Name)
	assert.Equal(t, "github.com", repo.RepoHost())
}

func TestLastCommitGitea(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// Two round trips: the repository for its default branch, then the commit
	// list scoped to that branch. GitHub answers both from one GraphQL query.
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		httpmock.StringResponse(`{"id":550,"full_name":"gitea/tea","default_branch":"main"}`),
	)

	var gotQuery string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/commits"),
		func(req *http.Request) (*http.Response, error) {
			gotQuery = req.URL.RawQuery
			return httpmock.StringResponse(`[{"sha":"397e6300ec93937b7745ebcc19b95ca9cb30568b"}]`)(req)
		},
	)

	commit, err := LastCommit(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo)
	require.NoError(t, err)
	assert.Equal(t, "397e6300ec93937b7745ebcc19b95ca9cb30568b", commit.OID)
	assert.Contains(t, gotQuery, "sha=main")
	assert.Contains(t, gotQuery, "limit=1")
}

func TestLastCommitGiteaEscapesBranchName(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		httpmock.StringResponse(`{"id":1,"default_branch":"feature/foo"}`),
	)

	var gotQuery string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/commits"),
		func(req *http.Request) (*http.Response, error) {
			gotQuery = req.URL.RawQuery
			return httpmock.StringResponse(`[{"sha":"abc"}]`)(req)
		},
	)

	commit, err := LastCommit(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo)
	require.NoError(t, err)
	assert.Equal(t, "abc", commit.OID)
	// A branch name containing a slash must be escaped, or it would be read as
	// a path segment by the server.
	assert.Contains(t, gotQuery, "sha=feature%2Ffoo")
}

func TestLastCommitGiteaNoDefaultBranch(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		httpmock.StringResponse(`{"id":1,"empty":true}`),
	)

	_, err := LastCommit(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo)
	require.EqualError(t, err, "repository gitea/tea has no default branch")
}

func TestLastCommitGiteaEmptyCommitList(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		httpmock.StringResponse(`{"id":1,"default_branch":"main"}`),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/commits"),
		httpmock.StringResponse(`[]`),
	)

	_, err := LastCommit(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo)
	require.EqualError(t, err, `repository gitea/tea has no commits on branch "main"`)
}

func TestRepoParentGitea(t *testing.T) {
	withGiteaHost(t)

	tests := []struct {
		name      string
		body      string
		wantOwner string
		wantRepo  string
		wantNil   bool
	}{
		{
			name:      "fork reports its parent",
			body:      `{"id":2,"parent":{"id":550,"name":"tea","owner":{"login":"gitea"}}}`,
			wantOwner: "gitea",
			wantRepo:  "tea",
		},
		{
			name:    "non-fork has no parent",
			body:    `{"id":550}`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)
			reg.Register(
				httpmock.REST("GET", "api/v1/repos/gitea/tea"),
				httpmock.StringResponse(tt.body),
			)

			parent, err := RepoParent(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo)
			require.NoError(t, err)

			if tt.wantNil {
				assert.Nil(t, parent)
				return
			}
			require.NotNil(t, parent)
			assert.Equal(t, tt.wantOwner, parent.RepoOwner())
			assert.Equal(t, tt.wantRepo, parent.RepoName())
			assert.Equal(t, "git.example.com", parent.RepoHost())
		})
	}
}

func TestRepoDefaultBranchGitea(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		httpmock.StringResponse(`{"id":550,"default_branch":"main"}`),
	)

	branch, err := RepoDefaultBranch(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo)
	require.NoError(t, err)
	assert.Equal(t, "main", branch)
}

// RepoDefaultBranch short-circuits when handed a Repository it already has, so
// the REST path must populate DefaultBranchRef for that to keep working.
func TestRepoDefaultBranchGiteaFromLoadedRepository(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		httpmock.StringResponse(`{"id":550,"default_branch":"trunk"}`),
	)

	repo, err := FetchRepository(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo, nil)
	require.NoError(t, err)

	// No further stub is registered, so a second request would fail the test.
	branch, err := RepoDefaultBranch(NewClientFromHTTP(&http.Client{Transport: reg}), repo)
	require.NoError(t, err)
	assert.Equal(t, "trunk", branch)
}

func TestIssueRepoInfoGitea(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		httpmock.StringResponse(giteaRepositoryBody),
	)

	repo, err := IssueRepoInfo(NewClientFromHTTP(&http.Client{Transport: reg}), giteaRepo)
	require.NoError(t, err)
	assert.Equal(t, "550", repo.ID)
	assert.Equal(t, "tea", repo.Name)
	assert.True(t, repo.HasIssuesEnabled)
	assert.Equal(t, "READ", repo.ViewerPermission)
}

func TestSetRepositoryArchivedGitea(t *testing.T) {
	for _, tc := range []struct {
		name     string
		archived bool
	}{
		{name: "archived", archived: true},
		{name: "unarchived", archived: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withGiteaHost(t)
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			var body map[string]any
			reg.Register(
				httpmock.REST("PATCH", "api/v1/repos/gitea/tea"),
				httpmock.RESTPayload(200, `{}`, func(p map[string]any) { body = p }),
			)

			client := NewClientFromHTTP(&http.Client{Transport: reg})
			require.NoError(t, SetRepositoryArchivedGitea(client, giteaRepo, tc.archived))

			// Only the archived flag is sent, so a concurrent edit to any other
			// field cannot be clobbered by this request.
			assert.Equal(t, map[string]any{"archived": tc.archived}, body)
		})
	}
}

// The network lookup is what a Gitea host hits when a command resolves its
// default repository with a prompt available. Each remote is read on its own,
// and the parent relationship Gitea reports is what lets the caller offer the
// upstream as the default.
func TestRepoNetworkGitea(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StringResponse(`{"id":1,"login":"myrqs"}`),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/me/tea"),
		httpmock.StringResponse(`{
			"id": 2, "name": "tea", "full_name": "me/tea", "fork": true,
			"parent": {"id": 550, "name": "tea", "full_name": "gitea/tea",
				"owner": {"id": 3, "login": "gitea", "type": "Organization"}},
			"permissions": {"admin": false, "push": true, "pull": true}
		}`),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/me/other"),
		httpmock.StringResponse(`{
			"id": 9, "name": "other", "full_name": "me/other",
			"permissions": {"admin": false, "push": false, "pull": true}
		}`),
	)

	client := NewClientFromHTTP(&http.Client{Transport: reg})
	result, err := RepoNetwork(client, []ghrepo.Interface{
		ghrepo.NewWithHost("me", "tea", "git.example.com"),
		ghrepo.NewWithHost("me", "other", "git.example.com"),
	})
	require.NoError(t, err)

	assert.Equal(t, "myrqs", result.ViewerLogin)
	require.Len(t, result.Repositories, 2)

	// Ordering matches the remotes given, so the caller's first remote stays first.
	assert.Equal(t, "me/tea", result.Repositories[0].NameWithOwner)
	assert.True(t, result.Repositories[0].ViewerCanPush())
	require.NotNil(t, result.Repositories[0].Parent)
	assert.Equal(t, "gitea/tea", result.Repositories[0].Parent.NameWithOwner)
	assert.Equal(t, "git.example.com", result.Repositories[0].Parent.RepoHost())

	assert.Equal(t, "me/other", result.Repositories[1].NameWithOwner)
	assert.False(t, result.Repositories[1].ViewerCanPush())
}

// A remote that no longer exists must not fail the whole lookup: GitHub
// tolerates NOT_FOUND per repository, and the nil placeholder keeps the
// ordering the caller relies on.
func TestRepoNetworkGiteaToleratesNotFound(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StringResponse(`{"id":1,"login":"myrqs"}`),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/me/gone"),
		httpmock.StatusStringResponse(404, `{"message":"not found"}`),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/me/tea"),
		httpmock.StringResponse(`{"id": 2, "name": "tea", "full_name": "me/tea",
			"permissions": {"admin": false, "push": true, "pull": true}}`),
	)

	result, err := RepoNetwork(NewClientFromHTTP(&http.Client{Transport: reg}), []ghrepo.Interface{
		ghrepo.NewWithHost("me", "gone", "git.example.com"),
		ghrepo.NewWithHost("me", "tea", "git.example.com"),
	})
	require.NoError(t, err)

	require.Len(t, result.Repositories, 2)
	assert.Nil(t, result.Repositories[0], "a missing repository stays as a placeholder")
	require.NotNil(t, result.Repositories[1])
	assert.Equal(t, "me/tea", result.Repositories[1].NameWithOwner)
}

// Gitea serves public repositories without a token, so an anonymous run can
// still resolve the network; it just does not learn who the viewer is.
func TestRepoNetworkGiteaToleratesAnonymous(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StatusStringResponse(401, `{"message":"token is required"}`),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea"),
		httpmock.StringResponse(`{"id": 550, "name": "tea", "full_name": "gitea/tea",
			"permissions": {"admin": false, "push": false, "pull": true}}`),
	)

	result, err := RepoNetwork(NewClientFromHTTP(&http.Client{Transport: reg}), []ghrepo.Interface{
		ghrepo.NewWithHost("gitea", "tea", "git.example.com"),
	})
	require.NoError(t, err)

	assert.Empty(t, result.ViewerLogin)
	require.Len(t, result.Repositories, 1)
	assert.Equal(t, "gitea/tea", result.Repositories[0].NameWithOwner)
}

// Any failure other than a 401 while identifying the viewer is a real error and
// must not be swallowed.
func TestRepoNetworkGiteaPropagatesOtherViewerFailures(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StatusStringResponse(403, `{"message":"forbidden"}`),
	)

	_, err := RepoNetwork(NewClientFromHTTP(&http.Client{Transport: reg}), []ghrepo.Interface{
		ghrepo.NewWithHost("me", "tea", "git.example.com"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

// A server failure on one repository is not a missing repository; it has to be
// reported rather than turned into a nil placeholder.
func TestRepoNetworkGiteaPropagatesRepositoryFailures(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StringResponse(`{"id":1,"login":"myrqs"}`),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/me/tea"),
		httpmock.StatusStringResponse(500, `{"message":"boom"}`),
	)

	result, err := RepoNetwork(NewClientFromHTTP(&http.Client{Transport: reg}), []ghrepo.Interface{
		ghrepo.NewWithHost("me", "tea", "git.example.com"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.Equal(t, "myrqs", result.ViewerLogin, "what was read before the failure is kept")
}

// The GitHub path must still issue GraphQL: the dispatch is by host type, not
// by which function happens to be reachable.
func TestRepoNetworkGitHubStillUsesGraphQL(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(nil)
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.GraphQL(`query RepositoryNetwork\b`),
		httpmock.StringResponse(`{"data":{"viewer":{"login":"cli"},"repo_000":{"name":"cli","owner":{"login":"cli"},"nameWithOwner":"cli/cli"}}}`),
	)

	result, err := RepoNetwork(NewClientFromHTTP(&http.Client{Transport: reg}),
		[]ghrepo.Interface{ghrepo.New("cli", "cli")})
	require.NoError(t, err)
	assert.Equal(t, "cli", result.ViewerLogin)
	require.Len(t, result.Repositories, 1)
	assert.Equal(t, "cli/cli", result.Repositories[0].NameWithOwner)
}

// A fork search has to return the viewer's own forks of the requested
// repository, not every accessible fork. The viewer's repository search is
// narrowed by uid and mode=fork, then matched against the parent.
func TestRepoFindForksGitea(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StringResponse(`{"id":7,"login":"myrqs"}`),
	)

	var gotQuery string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/search"),
		func(req *http.Request) (*http.Response, error) {
			gotQuery = req.URL.RawQuery
			return httpmock.StringResponse(`{"ok":true,"data":[
				{"id":2,"name":"tea","full_name":"myrqs/tea","fork":true,
				 "parent":{"id":550,"name":"tea","full_name":"gitea/tea"},
				 "permissions":{"admin":false,"push":true,"pull":true}},
				{"id":3,"name":"tea","full_name":"myrqs/tea-readonly","fork":true,
				 "parent":{"id":550,"name":"tea","full_name":"gitea/tea"},
				 "permissions":{"admin":false,"push":false,"pull":true}},
				{"id":4,"name":"tea","full_name":"myrqs/other-fork","fork":true,
				 "parent":{"id":9,"name":"other","full_name":"gitea/other"},
				 "permissions":{"admin":false,"push":true,"pull":true}}
			]}`)(req)
		},
	)

	forks, err := RepoFindForks(NewClientFromHTTP(&http.Client{Transport: reg}),
		ghrepo.NewWithHost("gitea", "tea", "git.example.com"), 3)
	require.NoError(t, err)

	assert.Contains(t, gotQuery, "uid=7")
	assert.Contains(t, gotQuery, "mode=fork")

	require.Len(t, forks, 1, "only a writable fork of the requested repository counts")
	assert.Equal(t, "myrqs/tea", forks[0].NameWithOwner)
	assert.Equal(t, "git.example.com", forks[0].RepoHost())
}

// The search lists all of the viewer's forks, so matches can start on a later
// page. The loop must keep going rather than treating a page with no match as
// the end of the results.
func TestRepoFindForksGiteaPages(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/user"),
		httpmock.StringResponse(`{"id":7,"login":"myrqs"}`),
	)

	full := `{"ok":true,"data":[`
	for i := range giteaPageSize {
		if i > 0 {
			full += ","
		}
		full += `{"id":9,"full_name":"myrqs/unrelated","fork":true,
			"parent":{"id":9,"full_name":"gitea/unrelated"},
			"permissions":{"admin":false,"push":true,"pull":true}}`
	}
	full += `]}`

	pages := 0
	count := func(responder httpmock.Responder) httpmock.Responder {
		return func(req *http.Request) (*http.Response, error) {
			pages++
			return responder(req)
		}
	}
	reg.Register(httpmock.REST("GET", "api/v1/repos/search"), count(httpmock.StringResponse(full)))
	reg.Register(httpmock.REST("GET", "api/v1/repos/search"), count(httpmock.StringResponse(`{"ok":true,"data":[
		{"id":2,"full_name":"myrqs/tea","fork":true,
		 "parent":{"id":550,"full_name":"gitea/tea"},
		 "permissions":{"admin":false,"push":true,"pull":true}}
	]}`)))

	forks, err := RepoFindForks(NewClientFromHTTP(&http.Client{Transport: reg}),
		ghrepo.NewWithHost("gitea", "tea", "git.example.com"), 3)
	require.NoError(t, err)

	assert.Equal(t, 2, pages)
	require.Len(t, forks, 1)
	assert.Equal(t, "myrqs/tea", forks[0].NameWithOwner)
}

// A limit of zero is the caller asking for nothing; no request is needed.
func TestRepoFindForksGiteaZeroLimit(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	forks, err := RepoFindForks(NewClientFromHTTP(&http.Client{Transport: reg}),
		ghrepo.NewWithHost("gitea", "tea", "git.example.com"), 0)
	require.NoError(t, err)
	assert.Empty(t, forks)
}
