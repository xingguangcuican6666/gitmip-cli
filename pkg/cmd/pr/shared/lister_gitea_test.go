package shared

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var giteaListerRepo = ghrepo.NewWithHost("gitea", "tea", "git.example.com")

func withGiteaListerHost(t *testing.T) {
	t.Helper()
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	}))
}

func giteaPRRow(number int, state string, merged bool, headRef string) map[string]any {
	return map[string]any{
		"number":   number,
		"title":    "pr",
		"state":    state,
		"merged":   merged,
		"html_url": "https://git.example.com/gitea/tea/pulls/" + itoa(number),
		"url":      "https://git.example.com/api/v1/repos/gitea/tea/pulls/" + itoa(number),
		"head":     map[string]any{"ref": headRef, "sha": "abc"},
		"base":     map[string]any{"ref": "main", "sha": "def"},
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestListerGitea(t *testing.T) {
	withGiteaListerHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/gitea/tea/pulls", url.Values{
			"state": []string{"open"}, "page": []string{"1"},
		}),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.JSONResponse([]map[string]any{
				giteaPRRow(1, "open", false, "feature"),
				giteaPRRow(2, "open", false, "other"),
			})(req)
			require.NoError(t, err)
			resp.Header.Set("X-Total-Count", "2")
			return resp, nil
		},
	)

	l := NewLister(&http.Client{Transport: reg})
	res, err := l.List(ListOptions{
		BaseRepo:     giteaListerRepo,
		State:        "open",
		LimitResults: 10,
	})
	require.NoError(t, err)

	assert.Equal(t, 2, res.TotalCount)
	require.Len(t, res.PullRequests, 2)
	assert.Equal(t, 1, res.PullRequests[0].Number)
}

func TestListerGiteaMergedFilter(t *testing.T) {
	withGiteaListerHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// Gitea has no merged state, so the filter is applied after the fetch and the
	// reported count cannot stay the server's unfiltered one.
	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/gitea/tea/pulls", url.Values{
			"state": []string{"all"}, "page": []string{"1"},
		}),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.JSONResponse([]map[string]any{
				giteaPRRow(1, "closed", true, "feature"),
				giteaPRRow(2, "closed", false, "other"),
			})(req)
			require.NoError(t, err)
			resp.Header.Set("X-Total-Count", "2")
			return resp, nil
		},
	)

	l := NewLister(&http.Client{Transport: reg})
	res, err := l.List(ListOptions{
		BaseRepo:     giteaListerRepo,
		State:        "merged",
		LimitResults: 10,
	})
	require.NoError(t, err)

	require.Len(t, res.PullRequests, 1)
	assert.Equal(t, 1, res.PullRequests[0].Number)
	assert.Equal(t, "MERGED", res.PullRequests[0].State)
	assert.Equal(t, 1, res.TotalCount)
}

func TestListerGiteaHeadBranchFilter(t *testing.T) {
	withGiteaListerHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/gitea/tea/pulls", url.Values{
			"state": []string{"open"}, "page": []string{"1"},
		}),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.JSONResponse([]map[string]any{
				giteaPRRow(1, "open", false, "feature"),
				giteaPRRow(2, "open", false, "other"),
			})(req)
			require.NoError(t, err)
			resp.Header.Set("X-Total-Count", "2")
			return resp, nil
		},
	)

	l := NewLister(&http.Client{Transport: reg})
	res, err := l.List(ListOptions{
		BaseRepo:     giteaListerRepo,
		State:        "open",
		HeadBranch:   "other",
		LimitResults: 10,
	})
	require.NoError(t, err)

	require.Len(t, res.PullRequests, 1)
	assert.Equal(t, 2, res.PullRequests[0].Number)
}

func TestListerGiteaRejectsUnknownState(t *testing.T) {
	withGiteaListerHost(t)
	l := NewLister(&http.Client{Transport: &httpmock.Registry{}})
	_, err := l.List(ListOptions{
		BaseRepo:     giteaListerRepo,
		State:        "sideways",
		LimitResults: 10,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid state")
}

func TestListerGiteaPagesUntilLimit(t *testing.T) {
	withGiteaListerHost(t)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	fullPage := make([]map[string]any, api.GiteaPageSize)
	for i := range fullPage {
		fullPage[i] = giteaPRRow(i+1, "open", false, "feature")
	}

	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/gitea/tea/pulls", url.Values{"state": []string{"open"}, "page": []string{"1"}}),
		httpmock.JSONResponse(fullPage),
	)
	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/gitea/tea/pulls", url.Values{"state": []string{"open"}, "page": []string{"2"}}),
		httpmock.JSONResponse([]map[string]any{giteaPRRow(999, "open", false, "feature")}),
	)

	l := NewLister(&http.Client{Transport: reg})
	res, err := l.List(ListOptions{
		BaseRepo:     giteaListerRepo,
		State:        "open",
		LimitResults: api.GiteaPageSize + 1,
	})
	require.NoError(t, err)
	assert.Len(t, res.PullRequests, api.GiteaPageSize+1)
}
