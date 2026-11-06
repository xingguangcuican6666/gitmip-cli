package list

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var giteaReleaseRepo = ghrepo.NewWithHost("gitea", "tea", "git.example.com")

func giteaReleasesBody(entries ...[3]string) string {
	type release struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		TagName     string `json:"tag_name"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
		PublishedAt string `json:"published_at"`
		CreatedAt   string `json:"created_at"`
	}

	releases := make([]release, 0, len(entries))
	for i, e := range entries {
		releases = append(releases, release{
			ID:          int64(100 + i),
			Name:        e[0],
			TagName:     e[0],
			Draft:       e[1] == "draft",
			Prerelease:  e[1] == "prerelease",
			PublishedAt: e[2],
			CreatedAt:   e[2],
		})
	}
	b, _ := json.Marshal(releases)
	return string(b)
}

func TestFetchReleasesGitea(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotQuery string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		func(req *http.Request) (*http.Response, error) {
			gotQuery = req.URL.RawQuery
			return httpmock.StringResponse(giteaReleasesBody(
				[3]string{"v2.0.0", "", "2026-09-10T11:34:12Z"},
				[3]string{"v1.1.0-rc1", "prerelease", "2026-08-02T14:40:05Z"},
				[3]string{"v1.0.0", "", "2026-07-27T18:15:19Z"},
			))(req)
		},
	)

	client := &http.Client{Transport: reg}
	releases, err := fetchReleasesGitea(client, giteaReleaseRepo, 10, false, false, "desc")
	require.NoError(t, err)

	// No filter parameters are sent when drafts and prereleases are both kept:
	// Gitea's draft=true is an inclusion filter, so sending it would have
	// returned only drafts.
	assert.NotContains(t, gotQuery, "draft=")
	assert.NotContains(t, gotQuery, "pre-release=")

	require.Len(t, releases, 3)
	assert.Equal(t, "v2.0.0", releases[0].TagName)
	assert.True(t, releases[0].IsLatest, "newest non-draft non-prerelease is latest")
	assert.False(t, releases[1].IsLatest)
	assert.True(t, releases[1].IsPrerelease)
	assert.Equal(t, "v1.0.0", releases[2].TagName)
	assert.Equal(t, "2026-09-10T11:34:12Z", releases[0].PublishedAt.Format(time.RFC3339))
	assert.Equal(t, releases[0].PublishedAt, releases[0].CreatedAt)
}

func TestFetchReleasesGiteaExcludeFilters(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotQuery string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		func(req *http.Request) (*http.Response, error) {
			gotQuery = req.URL.RawQuery
			return httpmock.StringResponse(giteaReleasesBody(
				[3]string{"v2.0.0", "", "2026-09-10T11:34:12Z"},
			))(req)
		},
	)

	client := &http.Client{Transport: reg}
	releases, err := fetchReleasesGitea(client, giteaReleaseRepo, 10, true, true, "desc")
	require.NoError(t, err)
	require.Len(t, releases, 1)

	assert.Contains(t, gotQuery, "draft=false")
	assert.Contains(t, gotQuery, "pre-release=false")
}

func TestFetchReleasesGiteaAscOrderReversesAndKeepsLatest(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		httpmock.StringResponse(giteaReleasesBody(
			[3]string{"v3.0.0", "", "2026-09-10T11:34:12Z"},
			[3]string{"v2.0.0", "", "2026-08-02T14:40:05Z"},
			[3]string{"v1.0.0", "", "2026-07-27T18:15:19Z"},
		)),
	)

	client := &http.Client{Transport: reg}
	releases, err := fetchReleasesGitea(client, giteaReleaseRepo, 10, false, false, "asc")
	require.NoError(t, err)

	require.Len(t, releases, 3)
	// Gitea returns newest first; ascending output is the reverse.
	assert.Equal(t, "v1.0.0", releases[0].TagName)
	assert.Equal(t, "v2.0.0", releases[1].TagName)
	assert.Equal(t, "v3.0.0", releases[2].TagName)
	// The latest flag follows the release, not the position.
	assert.True(t, releases[2].IsLatest)
	assert.False(t, releases[0].IsLatest)
}

func TestFetchReleasesGiteaLatestSkipsDraftsAndPrereleases(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		httpmock.StringResponse(giteaReleasesBody(
			[3]string{"v3.0.0-rc1", "prerelease", "2026-09-10T11:34:12Z"},
			[3]string{"v2.0.0", "", "2026-08-02T14:40:05Z"},
			[3]string{"v1.0.0", "", "2026-07-27T18:15:19Z"},
		)),
	)

	client := &http.Client{Transport: reg}
	releases, err := fetchReleasesGitea(client, giteaReleaseRepo, 10, false, false, "desc")
	require.NoError(t, err)

	require.Len(t, releases, 3)
	assert.False(t, releases[0].IsLatest, "a prerelease is never latest")
	assert.True(t, releases[1].IsLatest)
	assert.False(t, releases[2].IsLatest)
}

func TestFetchReleasesGiteaStopsAtLimit(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// One full page, then the limit truncates. Only one stub is registered, so
	// a second request would fail the test.
	full := make([][3]string, giteaReleasePageSize)
	for i := range full {
		full[i] = [3]string{"v" + string(rune('a'+i%26)) + string(rune('0'+i/26)), "", "2026-09-10T11:34:12Z"}
	}
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		httpmock.StringResponse(giteaReleasesBody(full...)),
	)

	client := &http.Client{Transport: reg}
	releases, err := fetchReleasesGitea(client, giteaReleaseRepo, 2, false, false, "desc")
	require.NoError(t, err)
	assert.Len(t, releases, 2)
}

func TestFetchReleasesGiteaPagesUntilExhausted(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	full := make([][3]string, giteaReleasePageSize)
	for i := range full {
		full[i] = [3]string{"tag", "", "2026-09-10T11:34:12Z"}
	}

	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		httpmock.StringResponse(giteaReleasesBody(full...)),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		httpmock.StringResponse(giteaReleasesBody([3]string{"last", "", "2026-01-01T00:00:00Z"})),
	)

	client := &http.Client{Transport: reg}
	releases, err := fetchReleasesGitea(client, giteaReleaseRepo, -1, false, false, "desc")
	require.NoError(t, err)
	assert.Len(t, releases, giteaReleasePageSize+1)
}
