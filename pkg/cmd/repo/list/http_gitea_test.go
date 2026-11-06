package list

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// giteaSearchBody is a two-repository search response shaped like Gitea's.
// xorm/xorm carries the Go language and one topic; xorm/website2 carries HTML.
const giteaSearchBody = `{
	"ok": true,
	"data": [
		{
			"id": 263,
			"owner": {"id": 6, "login": "xorm", "type": "Organization"},
			"name": "xorm",
			"full_name": "xorm/xorm",
			"description": "Simple and Powerful ORM for Go",
			"html_url": "https://git.example.com/xorm/xorm",
			"ssh_url": "git@git.example.com:xorm/xorm.git",
			"website": "https://xorm.io",
			"default_branch": "master",
			"created_at": "2019-10-14T09:19:05Z",
			"updated_at": "2026-10-31T04:07:28Z",
			"archived": false,
			"empty": false,
			"private": false,
			"internal": false,
			"fork": false,
			"mirror": false,
			"template": false,
			"has_issues": true,
			"has_wiki": true,
			"stars_count": 500,
			"forks_count": 100,
			"watchers_count": 20,
			"size": 30000,
			"language": "Go",
			"licenses": ["BSD-3-Clause"],
			"topics": ["orm", "go"]
		},
		{
			"id": 264,
			"owner": {"id": 6, "login": "xorm", "type": "Organization"},
			"name": "website2",
			"full_name": "xorm/website2",
			"description": "Official website",
			"html_url": "https://git.example.com/xorm/website2",
			"ssh_url": "git@git.example.com:xorm/website2.git",
			"website": "",
			"default_branch": "main",
			"created_at": "2022-04-12T13:21:44Z",
			"updated_at": "2026-08-19T23:03:46Z",
			"archived": false,
			"empty": false,
			"private": false,
			"internal": false,
			"fork": false,
			"mirror": false,
			"template": false,
			"has_issues": false,
			"has_wiki": false,
			"stars_count": 5,
			"forks_count": 3,
			"watchers_count": 2,
			"size": 800,
			"language": "HTML",
			"licenses": ["MIT"],
			"topics": []
		}
	]
}`

func withGiteaHost(t *testing.T) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)
}

// stubGiteaOwner registers the GET /users/{owner} lookup that resolves an owner
// handle to the numeric id Gitea's search requires.
func stubGiteaOwner(reg *httpmock.Registry, owner string, id int) {
	reg.Register(
		httpmock.REST("GET", "api/v1/users/"+owner),
		httpmock.StringResponse(`{"id":`+strconv.Itoa(id)+`,"login":"`+owner+`","type":"Organization"}`),
	)
}

func Test_listReposGitea(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaOwner(reg, "xorm", 6)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/search"),
		httpmock.WithHeader(httpmock.StringResponse(giteaSearchBody), "X-Total-Count", "2"),
	)

	client := http.Client{Transport: reg}
	res, err := listReposGitea(&client, "git.example.com", 10, "xorm", FilterOptions{})
	require.NoError(t, err)

	assert.Equal(t, "xorm", res.Owner)
	assert.Equal(t, 2, res.TotalCount)
	require.Len(t, res.Repositories, 2)

	first := res.Repositories[0]
	assert.Equal(t, "xorm/xorm", first.NameWithOwner)
	assert.Equal(t, "Simple and Powerful ORM for Go", first.Description)
	assert.Equal(t, "https://git.example.com/xorm/xorm", first.URL)
	assert.Equal(t, "https://xorm.io", first.HomepageURL)
	assert.Equal(t, "git@git.example.com:xorm/xorm.git", first.SSHURL)
	assert.Equal(t, 500, first.StargazerCount)
	assert.Equal(t, 100, first.ForkCount)
	assert.Equal(t, 20, first.Watchers.TotalCount)
	assert.Equal(t, 30000, first.DiskUsage)
	assert.True(t, first.HasIssuesEnabled)
	assert.True(t, first.HasWikiEnabled)
	assert.True(t, first.IsInOrganization)
	assert.Equal(t, "public", first.Visibility)
	require.NotNil(t, first.PrimaryLanguage)
	assert.Equal(t, "Go", first.PrimaryLanguage.Name)
	require.NotNil(t, first.LicenseInfo)
	assert.Equal(t, "BSD-3-Clause", first.LicenseInfo.Key)
	assert.Equal(t, "master", first.DefaultBranchRef.Name)
	assert.Equal(t, int64(263), first.DatabaseID)

	// Gitea has no pushed_at, so the listing falls back to updated_at to keep
	// the displayed time consistent with the sort order.
	require.NotNil(t, first.PushedAt)
	assert.Equal(t, first.UpdatedAt, *first.PushedAt)
}

func Test_listReposGiteaClientSideLanguageFilter(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaOwner(reg, "xorm", 6)
	// The server has no language filter, so both repos come back and the
	// language filter is applied locally.
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/search"),
		httpmock.StringResponse(giteaSearchBody),
	)

	client := http.Client{Transport: reg}
	res, err := listReposGitea(&client, "git.example.com", 10, "xorm", FilterOptions{Language: "go"})
	require.NoError(t, err)

	require.Len(t, res.Repositories, 1)
	assert.Equal(t, "xorm/xorm", res.Repositories[0].NameWithOwner)
}

func Test_listReposGiteaVisibilityFilter(t *testing.T) {
	withGiteaHost(t)

	tests := []struct {
		name       string
		visibility string
		wantNames  []string
	}{
		{
			name:       "public keeps non-private non-internal",
			visibility: "public",
			wantNames:  []string{"xorm/xorm", "xorm/website2"},
		},
		{
			name:       "internal drops everything",
			visibility: "internal",
			wantNames:  nil,
		},
		{
			name:       "private drops everything",
			visibility: "private",
			wantNames:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			stubGiteaOwner(reg, "xorm", 6)
			reg.Register(
				httpmock.REST("GET", "api/v1/repos/search"),
				httpmock.StringResponse(giteaSearchBody),
			)

			client := http.Client{Transport: reg}
			res, err := listReposGitea(&client, "git.example.com", 10, "xorm",
				FilterOptions{Visibility: tt.visibility})
			require.NoError(t, err)

			var got []string
			for _, r := range res.Repositories {
				got = append(got, r.NameWithOwner)
			}
			assert.Equal(t, tt.wantNames, got)
		})
	}
}

func Test_listReposGiteaRejectsMultipleTopics(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	client := http.Client{Transport: reg}
	_, err := listReposGitea(&client, "git.example.com", 10, "xorm", FilterOptions{
		Topic: []string{"orm", "go"},
	})
	require.EqualError(t, err, "Gitea supports filtering repositories by one topic at a time")
}

func Test_listReposGiteaOwnerLookupFailure(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/users/nosuchuser"),
		httpmock.StatusStringResponse(404, `{"message":"user does not exist"}`),
	)

	client := http.Client{Transport: reg}
	_, err := listReposGitea(&client, "git.example.com", 10, "nosuchuser", FilterOptions{})
	require.Error(t, err)
}

func Test_listReposGiteaPaginationStopsOnShortPage(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaOwner(reg, "xorm", 6)
	// A page smaller than giteaPageSize signals the end of results, so only
	// one search request is issued even though the limit is higher.
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/search"),
		httpmock.StringResponse(giteaSearchBody),
	)

	client := http.Client{Transport: reg}
	res, err := listReposGitea(&client, "git.example.com", 100, "xorm", FilterOptions{})
	require.NoError(t, err)
	assert.Len(t, res.Repositories, 2)
}
