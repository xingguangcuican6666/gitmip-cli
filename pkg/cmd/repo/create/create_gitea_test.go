package create

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withGiteaCreateHost(t *testing.T) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)
}

var giteaCreateRepo = ghrepo.NewWithHost("gitea", "tea", "git.example.com")

func TestRepoCreateGitea(t *testing.T) {
	withGiteaCreateHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var createBody map[string]any
	reg.Register(
		httpmock.REST("POST", "api/v1/orgs/gitea/repos"),
		httpmock.RESTPayload(201, `{
			"id": 7, "name": "new", "full_name": "gitea/new",
			"html_url": "https://git.example.com/gitea/new",
			"private": false, "default_branch": "main"
		}`, func(p map[string]any) { createBody = p }),
	)

	// Issues and wiki are not fields of the create request, so they arrive in a
	// follow-up PATCH only when the caller asked for something other than the
	// default.
	var editBody map[string]any
	reg.Register(
		httpmock.REST("PATCH", "api/v1/repos/gitea/new"),
		httpmock.RESTPayload(200, `{
			"id": 7, "name": "new", "full_name": "gitea/new",
			"html_url": "https://git.example.com/gitea/new",
			"private": false, "default_branch": "main"
		}`, func(p map[string]any) { editBody = p }),
	)

	repo, err := repoCreateGitea(&http.Client{Transport: reg}, "git.example.com", repoCreateInput{
		Name:              "new",
		OwnerLogin:        "gitea",
		Description:       "a description",
		HasIssuesEnabled:  false,
		HasWikiEnabled:    true,
		HomepageURL:       "https://example.com",
		InitReadme:        true,
		GitIgnoreTemplate: "Go",
		LicenseTemplate:   "MIT",
	})
	require.NoError(t, err)

	assert.Equal(t, "new", createBody["name"])
	assert.Equal(t, "a description", createBody["description"])
	assert.Equal(t, true, createBody["auto_init"])
	assert.Equal(t, "Default", createBody["readme"])
	assert.Equal(t, "Go", createBody["gitignores"])
	assert.Equal(t, "MIT", createBody["license"])
	assert.False(t, hasKeyTest(createBody, "private"), "private is omitted for a public repository")

	assert.Equal(t, false, editBody["has_issues"])
	assert.Equal(t, true, editBody["has_wiki"])
	assert.Equal(t, "https://example.com", editBody["website"])

	assert.Equal(t, "7", repo.ID)
	assert.Equal(t, "gitea", repo.Owner.Login)
	assert.Equal(t, "new", repo.Name)
	assert.Equal(t, "https://git.example.com/gitea/new", repo.URL)
	assert.Equal(t, "main", repo.DefaultBranchRef.Name)
}

func TestRepoCreateGiteaSkipsEditWhenDefaultsWanted(t *testing.T) {
	withGiteaCreateHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("POST", "api/v1/user/repos"),
		httpmock.StringResponse(`{"id":1,"name":"new","full_name":"someone/new",
			"html_url":"https://git.example.com/someone/new","default_branch":"main"}`),
	)
	// No PATCH is registered, so one would fail the stub verification.

	_, err := repoCreateGitea(&http.Client{Transport: reg}, "git.example.com", repoCreateInput{
		Name:             "new",
		HasIssuesEnabled: true,
		HasWikiEnabled:   true,
	})
	require.NoError(t, err)
}

func TestRepoCreateGiteaRefusesInternalVisibility(t *testing.T) {
	withGiteaCreateHost(t)
	_, err := repoCreateGitea(&http.Client{Transport: &httpmock.Registry{}}, "git.example.com", repoCreateInput{
		Name:       "new",
		Visibility: "INTERNAL",
	})
	assert.ErrorIs(t, err, ErrGiteaInternalVisibilityUnsupported)
}

func TestRepoCreateGiteaRefusesTeam(t *testing.T) {
	withGiteaCreateHost(t)
	_, err := repoCreateGitea(&http.Client{Transport: &httpmock.Registry{}}, "git.example.com", repoCreateInput{
		Name:     "new",
		TeamSlug: "a-team",
	})
	assert.ErrorIs(t, err, ErrGiteaTeamUnsupported)
}

func TestRepoCreateGiteaPrivate(t *testing.T) {
	withGiteaCreateHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var createBody map[string]any
	reg.Register(
		httpmock.REST("POST", "api/v1/user/repos"),
		httpmock.RESTPayload(201, `{"id":1,"name":"new","full_name":"someone/new",
			"html_url":"https://git.example.com/someone/new","private":true,"default_branch":"main"}`,
			func(p map[string]any) { createBody = p }),
	)

	repo, err := repoCreateGitea(&http.Client{Transport: reg}, "git.example.com", repoCreateInput{
		Name:             "new",
		Visibility:       "PRIVATE",
		HasIssuesEnabled: true,
		HasWikiEnabled:   true,
	})
	require.NoError(t, err)

	assert.Equal(t, true, createBody["private"])
	assert.True(t, repo.IsPrivate)
}

func TestRepoCreateFromTemplateGitea(t *testing.T) {
	withGiteaCreateHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var body map[string]any
	reg.Register(
		httpmock.REST("POST", "api/v1/repos/gitea/template/generate"),
		httpmock.RESTPayload(201, `{"id":9,"name":"new","full_name":"someone/new",
			"html_url":"https://git.example.com/someone/new","default_branch":"main"}`,
			func(p map[string]any) { body = p }),
	)

	repo, err := repoCreateFromTemplateGitea(&http.Client{Transport: reg}, "git.example.com",
		ghrepo.NewWithHost("gitea", "template", "git.example.com"),
		repoCreateInput{
			Name:       "new",
			OwnerLogin: "someone",
		})
	require.NoError(t, err)

	assert.Equal(t, "someone", body["owner"])
	assert.Equal(t, "new", body["name"])
	// git_content has to be explicit: without it Gitea generates an empty
	// repository, which is not what creating from a template means.
	assert.Equal(t, true, body["git_content"])
	assert.Equal(t, "https://git.example.com/someone/new", repo.URL)
}

func TestListTemplateRepositoriesGitea(t *testing.T) {
	withGiteaCreateHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/users/gitea/repos", url.Values{"limit": []string{"50"}}),
		httpmock.StringResponse(`[
			{"id":1,"name":"plain","full_name":"gitea/plain","template":false,"default_branch":"main"},
			{"id":2,"name":"tmpl","full_name":"gitea/tmpl","template":true,"default_branch":"trunk"}
		]`),
	)

	repos, err := listTemplateRepositoriesGitea(&http.Client{Transport: reg}, "git.example.com", "gitea")
	require.NoError(t, err)

	require.Len(t, repos, 1)
	assert.Equal(t, "tmpl", repos[0].Name)
	assert.Equal(t, "trunk", repos[0].DefaultBranchRef.Name)
}

func TestListTemplateRepositoriesGiteaToleratesMissingOwner(t *testing.T) {
	withGiteaCreateHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/users/nobody/repos", url.Values{"limit": []string{"50"}}),
		httpmock.StatusStringResponse(404, `{"message":"not found"}`),
	)

	repos, err := listTemplateRepositoriesGitea(&http.Client{Transport: reg}, "git.example.com", "nobody")
	require.NoError(t, err)
	assert.Empty(t, repos)
}

func hasKeyTest(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}
