package label

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/cli/cli/v2/api"
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

// giteaLabelsBody is a full page of two labels. A short page tells the lister
// the results are exhausted, so tests that want a second page need a full one.
func giteaLabelsBody(n int, names ...string) string {
	labels := make([]map[string]any, 0, len(names))
	for i, name := range names {
		labels = append(labels, map[string]any{
			"id":          100 + i,
			"name":        name,
			"color":       "fbca04",
			"description": "desc " + name,
			"url":         "https://git.example.com/api/v1/repos/gitea/tea/labels/100",
			"exclusive":   false,
			"is_archived": false,
		})
	}
	for i := len(names); i < n; i++ {
		labels = append(labels, map[string]any{
			"id":          200 + i,
			"name":        "filler",
			"color":       "000000",
			"description": "",
		})
	}
	b, _ := json.Marshal(labels)
	return string(b)
}

func stubGiteaLabels(reg *httpmock.Registry, body string, totalCount string) {
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/labels"),
		httpmock.WithHeader(httpmock.StringResponse(body), "X-Total-Count", totalCount),
	)
}

func TestListLabelsGitea(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaLabels(reg, giteaLabelsBody(2, "bug", "enhancement"), "2")

	client := &http.Client{Transport: reg}
	labels, totalCount, err := listLabelsGitea(client, giteaRepo, listQueryOptions{Limit: 30})
	require.NoError(t, err)

	assert.Equal(t, 2, totalCount)
	require.Len(t, labels, 2)
	assert.Equal(t, "bug", labels[0].Name)
	assert.Equal(t, "fbca04", labels[0].Color)
	assert.Equal(t, "desc bug", labels[0].Description)
	assert.Equal(t, "100", labels[0].ID)
}

func TestListLabelsGiteaSearchIsLocal(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// The search endpoint has no query parameter for label text, so the filter
	// runs locally and the reported total counts matches, not the server's 3.
	stubGiteaLabels(reg, giteaLabelsBody(3, "kind/bug", "docs", "kind/feature"), "3")

	client := &http.Client{Transport: reg}
	labels, totalCount, err := listLabelsGitea(client, giteaRepo, listQueryOptions{
		Limit: 30,
		Query: "kind",
	})
	require.NoError(t, err)

	assert.Equal(t, 2, totalCount)
	require.Len(t, labels, 2)
	assert.Equal(t, "kind/bug", labels[0].Name)
	assert.Equal(t, "kind/feature", labels[1].Name)
}

func TestListLabelsGiteaSearchMatchesDescription(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaLabels(reg, giteaLabelsBody(2, "bug", "enhancement"), "2")

	client := &http.Client{Transport: reg}
	labels, _, err := listLabelsGitea(client, giteaRepo, listQueryOptions{Limit: 30, Query: "enhance"})
	require.NoError(t, err)
	require.Len(t, labels, 1)
	assert.Equal(t, "enhancement", labels[0].Name)
}

func TestListLabelsGiteaStopsAtLimit(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// A full page of 50 with a limit of 2 must return 2 without a second
	// request, so only one stub is registered and Verify catches a second call.
	stubGiteaLabels(reg, giteaLabelsBody(giteaPageSize, "a", "b", "c"), "50")

	client := &http.Client{Transport: reg}
	labels, _, err := listLabelsGitea(client, giteaRepo, listQueryOptions{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, labels, 2)
}

func TestListLabelsGiteaPagesUntilExhausted(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// Two full pages followed by a short one, with limit -1 to list everything.
	stubGiteaLabels(reg, giteaLabelsBody(giteaPageSize, "a", "b"), "101")
	stubGiteaLabels(reg, giteaLabelsBody(giteaPageSize, "c"), "101")
	stubGiteaLabels(reg, giteaLabelsBody(1, "d"), "101")

	client := &http.Client{Transport: reg}
	labels, totalCount, err := listLabelsGitea(client, giteaRepo, listQueryOptions{Limit: -1})
	require.NoError(t, err)

	assert.Equal(t, 101, totalCount)
	assert.Len(t, labels, giteaPageSize*2+1)
}

func TestListLabelsGiteaDescOrderUsesReverseAlphabetical(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotQuery string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/labels"),
		httpmock.WithHeader(func(req *http.Request) (*http.Response, error) {
			gotQuery = req.URL.RawQuery
			return httpmock.StringResponse(giteaLabelsBody(1, "a"))(req)
		}, "X-Total-Count", "1"),
	)

	client := &http.Client{Transport: reg}
	_, _, err := listLabelsGitea(client, giteaRepo, listQueryOptions{Limit: 30, Order: "desc"})
	require.NoError(t, err)
	assert.Contains(t, gotQuery, "sort=reversealphabetically")
}

func TestListLabelsGiteaRejectsSortByCreated(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	client := &http.Client{Transport: reg}
	_, _, err := listLabelsGitea(client, giteaRepo, listQueryOptions{Limit: 30, Sort: "created", sortSpecified: true})
	require.EqualError(t, err, "Gitea does not sort labels by creation date; use `--sort name`")
}

func TestListLabelsGiteaAllowsDefaultSort(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaLabels(reg, giteaLabelsBody(1, "a"), "1")

	client := &http.Client{Transport: reg}
	labels, _, err := listLabelsGitea(client, giteaRepo, listQueryOptions{Limit: 30, Sort: "created"})
	require.NoError(t, err)
	assert.Len(t, labels, 1)
}

func TestListLabelsGiteaRejectsUnsupportedField(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	client := &http.Client{Transport: reg}
	_, _, err := listLabelsGitea(client, giteaRepo, listQueryOptions{Limit: 30, fields: []string{"name", "createdAt"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `Gitea does not provide the "createdAt" field`)
}

func TestGiteaLabelID(t *testing.T) {
	withGiteaHost(t)

	tests := []struct {
		name    string
		body    string
		lookup  string
		wantID  int64
		wantErr string
	}{
		{
			name:   "unique name resolves",
			body:   giteaLabelsBody(2, "bug", "enhancement"),
			lookup: "enhancement",
			wantID: 101,
		},
		{
			name:    "unknown name",
			body:    giteaLabelsBody(1, "bug"),
			lookup:  "enhancement",
			wantErr: "label not found",
		},
		{
			name:    "duplicate names are ambiguous",
			body:    `[{"id":1,"name":"bug"},{"id":2,"name":"bug"}]`,
			lookup:  "bug",
			wantErr: `label name "bug" is ambiguous in gitea/tea; 2 labels share it`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			stubGiteaLabels(reg, tt.body, "1")

			client := &http.Client{Transport: reg}
			id, err := giteaLabelID(api.NewClientFromHTTP(client), giteaRepo, tt.lookup)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

func TestDeleteLabelGiteaResolvesIDThenDeletes(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaLabels(reg, giteaLabelsBody(1, "bug"), "1")

	var deletedPath string
	reg.Register(
		httpmock.REST("DELETE", "api/v1/repos/gitea/tea/labels/100"),
		func(req *http.Request) (*http.Response, error) {
			deletedPath = req.URL.Path
			return &http.Response{StatusCode: 204, Request: req, Body: http.NoBody}, nil
		},
	)

	client := &http.Client{Transport: reg}
	err := deleteLabelGitea(client, giteaRepo, "bug")
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/repos/gitea/tea/labels/100", deletedPath)
}

func TestDeleteLabelGiteaUnknownLabel(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaLabels(reg, giteaLabelsBody(1, "bug"), "1")

	client := &http.Client{Transport: reg}
	err := deleteLabelGitea(client, giteaRepo, "nope")
	require.EqualError(t, err, `label "nope" not found in gitea/tea`)
}

func TestCreateLabelGiteaRejectsDuplicate(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// No POST stub is registered: a duplicate must be reported before any
	// create request goes out, and Verify proves no other request was made.
	stubGiteaLabels(reg, giteaLabelsBody(1, "bug"), "1")

	client := &http.Client{Transport: reg}
	err := createLabelGitea(client, giteaRepo, &createOptions{Name: "bug", Color: "ff0000"})
	require.ErrorIs(t, err, errLabelAlreadyExists)
}

func TestCreateLabelGiteaForceUpdatesExisting(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// Two list requests: one for the duplicate check in create, one for the id
	// resolution inside the update.
	stubGiteaLabels(reg, giteaLabelsBody(1, "bug"), "1")
	stubGiteaLabels(reg, giteaLabelsBody(1, "bug"), "1")

	var body string
	reg.Register(
		httpmock.REST("PATCH", "api/v1/repos/gitea/tea/labels/100"),
		func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			body = string(b)
			return httpmock.StatusStringResponse(200, `{}`)(req)
		},
	)

	client := &http.Client{Transport: reg}
	err := createLabelGitea(client, giteaRepo, &createOptions{
		Name:  "bug",
		Color: "ff0000",
		Force: true,
	})
	require.NoError(t, err)

	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &props))
	assert.Equal(t, "ff0000", props["color"])
}

func TestCreateLabelGiteaPostsWhenNew(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaLabels(reg, giteaLabelsBody(1, "bug"), "1")

	var body string
	reg.Register(
		httpmock.REST("POST", "api/v1/repos/gitea/tea/labels"),
		func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			body = string(b)
			return httpmock.StatusStringResponse(201, `{}`)(req)
		},
	)

	client := &http.Client{Transport: reg}
	err := createLabelGitea(client, giteaRepo, &createOptions{
		Name:        "enhancement",
		Color:       "#00aabb",
		Description: "new feature",
	})
	require.NoError(t, err)

	var posted map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &posted))
	assert.Equal(t, "enhancement", posted["name"])
	assert.Equal(t, "new feature", posted["description"])
	// The leading "#" is dropped so the stored value matches what Gitea returns.
	assert.Equal(t, "00aabb", posted["color"])
}

func TestUpdateLabelGiteaMapsNewNameToName(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	stubGiteaLabels(reg, giteaLabelsBody(1, "bug"), "1")

	var body string
	reg.Register(
		httpmock.REST("PATCH", "api/v1/repos/gitea/tea/labels/100"),
		func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			body = string(b)
			return httpmock.StatusStringResponse(200, `{}`)(req)
		},
	)

	client := &http.Client{Transport: reg}
	err := updateLabelGitea(api.NewClientFromHTTP(client), giteaRepo, &editOptions{
		Name:        "bug",
		NewName:     "big-bug",
		Color:       "ff0000",
		Description: "bigger",
	})
	require.NoError(t, err)

	var props map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &props))
	// Gitea's EditLabelOption renames with "name", not GitHub's "new_name".
	assert.Equal(t, "big-bug", props["name"])
	assert.NotContains(t, props, "new_name")
	assert.Equal(t, "ff0000", props["color"])
	assert.Equal(t, "bigger", props["description"])
}
