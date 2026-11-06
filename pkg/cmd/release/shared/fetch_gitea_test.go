package shared

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var giteaRepo = ghrepo.NewWithHost("gitea", "tea", "git.example.com")

func TestFetchReleaseGiteaPublished(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// Only the published lookup is registered. A draft listing would fail the
	// test, since the tag was found on the first request.
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases/tags/v1.2.3"),
		httpmock.StringResponse(`{"id":7,"tag_name":"v1.2.3","draft":false}`),
	)

	release, err := FetchRelease(context.Background(), &http.Client{Transport: reg}, giteaRepo, "v1.2.3")
	require.NoError(t, err)
	assert.Equal(t, "v1.2.3", release.TagName)
	assert.False(t, release.IsDraft)
}

// Gitea has no query for a draft by pending tag, so a miss on the tags endpoint
// falls back to listing drafts and matching tag_name locally.
func TestFetchReleaseGiteaDraftByPendingTag(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases/tags/v2.0.0"),
		httpmock.JSONErrorResponse(404, api.HTTPError{StatusCode: 404, Message: "Not Found"}),
	)

	var gotQuery string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		func(req *http.Request) (*http.Response, error) {
			gotQuery = req.URL.RawQuery
			return httpmock.StringResponse(`[
				{"id":8,"tag_name":"v1.9.0","draft":true},
				{"id":9,"tag_name":"v2.0.0","draft":true}
			]`)(req)
		},
	)

	release, err := FetchRelease(context.Background(), &http.Client{Transport: reg}, giteaRepo, "v2.0.0")
	require.NoError(t, err)
	assert.Equal(t, int64(9), release.DatabaseID)
	assert.True(t, release.IsDraft)

	// draft=true is Gitea's inclusion filter, which narrows to drafts only.
	assert.Contains(t, gotQuery, "draft=true")
}

func TestFetchReleaseGiteaIgnoresPublishedRowsInDraftList(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases/tags/v3.0.0"),
		httpmock.JSONErrorResponse(404, api.HTTPError{StatusCode: 404, Message: "Not Found"}),
	)
	// A row carrying the tag but not flagged draft must not be returned, so a
	// published release is never mistaken for a draft of the same name.
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		httpmock.StringResponse(`[{"id":3,"tag_name":"v3.0.0","draft":false}]`),
	)

	_, err := FetchRelease(context.Background(), &http.Client{Transport: reg}, giteaRepo, "v3.0.0")
	require.ErrorIs(t, err, ErrReleaseNotFound)
}

func TestFetchReleaseGiteaNotFoundWhenBothLookupsMiss(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases/tags/v9.9.9"),
		httpmock.JSONErrorResponse(404, api.HTTPError{StatusCode: 404, Message: "Not Found"}),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		httpmock.StringResponse(`[]`),
	)

	_, err := FetchRelease(context.Background(), &http.Client{Transport: reg}, giteaRepo, "v9.9.9")
	require.ErrorIs(t, err, ErrReleaseNotFound)
}

// A server error on the published lookup must not be masked by the draft lookup
// reporting not found, which is the behaviour the GitHub path has.
func TestFetchReleaseGiteaReportsServerError(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases/tags/v1.0.0"),
		httpmock.JSONErrorResponse(500, api.HTTPError{StatusCode: 500, Message: "boom"}),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		httpmock.StringResponse(`[]`),
	)

	_, err := FetchRelease(context.Background(), &http.Client{Transport: reg}, giteaRepo, "v1.0.0")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrReleaseNotFound)
	assert.Contains(t, err.Error(), "500")
}

func TestFetchReleaseGiteaPagesDrafts(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases/tags/v2.0.0"),
		httpmock.JSONErrorResponse(404, api.HTTPError{StatusCode: 404, Message: "Not Found"}),
	)

	// A full page of unrelated drafts, then the short page holding the match.
	// httpmock stubs are one-shot, so each page needs its own registration.
	fullPage := "["
	for i := range giteaReleasePageSize {
		if i > 0 {
			fullPage += ","
		}
		fullPage += `{"id":` + strconv.Itoa(1000+i) + `,"tag_name":"other","draft":true}`
	}
	fullPage += "]"

	pages := 0
	countPage := func(responder httpmock.Responder) httpmock.Responder {
		return func(req *http.Request) (*http.Response, error) {
			pages++
			return responder(req)
		}
	}

	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		countPage(httpmock.StringResponse(fullPage)),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/releases"),
		countPage(httpmock.StringResponse(`[{"id":99,"tag_name":"v2.0.0","draft":true}]`)),
	)

	release, err := FetchRelease(context.Background(), &http.Client{Transport: reg}, giteaRepo, "v2.0.0")
	require.NoError(t, err)
	assert.Equal(t, int64(99), release.DatabaseID)
	assert.Equal(t, 2, pages)
}
