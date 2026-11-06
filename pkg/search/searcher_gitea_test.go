package search

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withGiteaSearchHost(t *testing.T) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)
}

func newGiteaSearcher(t *testing.T, reg *httpmock.Registry) Searcher {
	t.Helper()
	withGiteaSearchHost(t)
	return NewSearcher(&http.Client{Transport: reg}, "git.example.com", nil)
}

func TestGiteaSearchRepositories(t *testing.T) {
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/search", url.Values{
			"q":     []string{"gitea"},
			"page":  []string{"1"},
			"limit": []string{"2"},
		}),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.JSONResponse(map[string]any{
				"ok": true,
				"data": []map[string]any{{
					"id":                7,
					"name":              "tea",
					"full_name":         "gitea/tea",
					"html_url":          "https://git.example.com/gitea/tea",
					"description":       "a CLI",
					"private":           false,
					"fork":              false,
					"archived":          false,
					"default_branch":    "main",
					"language":          "Go",
					"size":              100,
					"stars_count":       42,
					"forks_count":       5,
					"watchers_count":    3,
					"open_issues_count": 1,
					"has_issues":        true,
					"created_at":        "2020-01-01T00:00:00Z",
					"updated_at":        "2021-01-01T00:00:00Z",
					"owner":             map[string]any{"id": 3, "login": "gitea", "type": "Organization"},
				}},
			})(req)
			if err != nil {
				return nil, err
			}
			resp.Header.Set("X-Total-Count", "17")
			return resp, nil
		},
	)

	s := newGiteaSearcher(t, reg)
	result, err := s.Repositories(Query{Kind: KindRepositories, Keywords: []string{"gitea"}, Limit: 2})
	require.NoError(t, err)

	assert.Equal(t, 17, result.Total)
	require.Len(t, result.Items, 1)
	assert.Equal(t, "gitea/tea", result.Items[0].FullName)
	assert.Equal(t, "7", result.Items[0].ID)
	assert.Equal(t, 42, result.Items[0].StargazersCount)
	assert.Equal(t, "gitea", result.Items[0].Owner.Login)
	assert.Equal(t, "Organization", result.Items[0].Owner.Type)
	assert.Equal(t, "public", result.Items[0].Visibility)
}

func TestGiteaSearchIssues(t *testing.T) {
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/issues/search", url.Values{
			"q":     []string{"tea"},
			"page":  []string{"1"},
			"limit": []string{"5"},
			"type":  []string{"issues"},
		}),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.JSONResponse([]map[string]any{{
				"id":         11,
				"number":     4,
				"title":      "a bug",
				"body":       "text",
				"state":      "open",
				"html_url":   "https://git.example.com/gitea/tea/issues/4",
				"is_locked":  false,
				"comments":   2,
				"created_at": "2021-01-01T00:00:00Z",
				"updated_at": "2021-01-02T00:00:00Z",
				"user":       map[string]any{"id": 1, "login": "someone"},
				"labels":     []map[string]any{{"id": 9, "name": "bug", "color": "ee0701"}},
				"assignees":  []map[string]any{{"id": 2, "login": "other"}},
				"repository": map[string]any{"full_name": "gitea/tea", "html_url": "https://git.example.com/gitea/tea"},
			}})(req)
			if err != nil {
				return nil, err
			}
			resp.Header.Set("X-Total-Count", "1")
			return resp, nil
		},
	)

	s := newGiteaSearcher(t, reg)
	result, err := s.Issues(Query{Kind: KindIssues, Keywords: []string{"tea"}, Limit: 5,
		Qualifiers: Qualifiers{Type: "issue"}})
	require.NoError(t, err)

	assert.Equal(t, 1, result.Total)
	require.Len(t, result.Items, 1)
	assert.Equal(t, 4, result.Items[0].Number)
	assert.Equal(t, "open", result.Items[0].State())
	assert.False(t, result.Items[0].IsPullRequest())
	assert.Equal(t, "gitea/tea", result.Items[0].RepositoryURL)
	assert.Equal(t, "bug", result.Items[0].Labels[0].Name)
	assert.Equal(t, "someone", result.Items[0].Author.Login)
}

func TestGiteaSearchIssuesKeywordOnlyQueryString(t *testing.T) {
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// The qualifier must not be rendered into q: Gitea matches q as text, so
	// "type:issue" there would be searched for literally.
	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/issues/search", url.Values{
			"q":     []string{"tea"},
			"page":  []string{"1"},
			"limit": []string{"5"},
			"type":  []string{"issues"},
		}),
		httpmock.StringResponse(`[]`),
	)

	s := newGiteaSearcher(t, reg)
	_, err := s.Issues(Query{Kind: KindIssues, Keywords: []string{"tea"}, Limit: 5,
		Qualifiers: Qualifiers{Type: "issue"}})
	require.NoError(t, err)
}

func TestGiteaSearchCodeAndCommitsUnsupported(t *testing.T) {
	s := newGiteaSearcher(t, &httpmock.Registry{})

	_, err := s.Code(Query{Kind: KindCode, Keywords: []string{"x"}, Limit: 1})
	assert.ErrorIs(t, err, ErrGiteaCodeSearchUnsupported)

	_, err = s.Commits(Query{Kind: KindCommits, Keywords: []string{"x"}, Limit: 1})
	assert.ErrorIs(t, err, ErrGiteaCommitSearchUnsupported)
}

func TestGiteaSearchIssuesRefusesUnsupportedQualifiers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		qualifiers Qualifiers
		wantErr    string
	}{
		{name: "mentions", qualifiers: Qualifiers{Mentions: "someone"}, wantErr: "only match `mentions:` against the authenticated user"},
		{name: "review-requested", qualifiers: Qualifiers{ReviewRequested: "someone"}, wantErr: "only match `review-requested:` against the authenticated user"},
		{name: "label", qualifiers: Qualifiers{Label: []string{"a", "b"}}, wantErr: "matches labels with OR"},
		{name: "repo", qualifiers: Qualifiers{Repo: []string{"gitea/tea"}}, wantErr: "cannot be scoped to a repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newGiteaSearcher(t, &httpmock.Registry{})
			_, err := s.Issues(Query{Kind: KindIssues, Keywords: []string{"x"}, Limit: 1, Qualifiers: tc.qualifiers})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestGiteaSearchRepositoriesRefusesUnsupportedQualifiers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		qualifiers Qualifiers
		wantErr    string
	}{
		{name: "stars", qualifiers: Qualifiers{Stars: ">100"}, wantErr: "Gitea cannot search with `stars:`"},
		{name: "language", qualifiers: Qualifiers{Language: "go"}, wantErr: "Gitea cannot search with `language:`"},
		{name: "followers", qualifiers: Qualifiers{Followers: ">10"}, wantErr: "Gitea cannot search with `followers:`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newGiteaSearcher(t, &httpmock.Registry{})
			_, err := s.Repositories(Query{Kind: KindRepositories, Keywords: []string{"x"}, Limit: 1, Qualifiers: tc.qualifiers})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestGiteaSearchViewerQualifiersPassThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.QueryMatcher("GET", "api/v1/repos/issues/search", url.Values{
			"q":        []string{"x"},
			"page":     []string{"1"},
			"limit":    []string{"1"},
			"created":  []string{"true"},
			"assigned": []string{"true"},
		}),
		httpmock.StringResponse(`[]`),
	)

	s := newGiteaSearcher(t, reg)
	_, err := s.Issues(Query{Kind: KindIssues, Keywords: []string{"x"}, Limit: 1,
		Qualifiers: Qualifiers{Author: "@me", Assignee: "@me"}})
	require.NoError(t, err)
}

func TestGiteaWebURL(t *testing.T) {
	s := newGiteaSearcher(t, &httpmock.Registry{})

	assert.Equal(t,
		"https://git.example.com/explore/repos?q=gitea",
		s.URL(Query{Kind: KindRepositories, Keywords: []string{"gitea"}}))
	assert.Equal(t,
		"https://git.example.com/issues?q=tea&state=open",
		s.URL(Query{Kind: KindIssues, Keywords: []string{"tea"}, Qualifiers: Qualifiers{State: "open"}}))
}
