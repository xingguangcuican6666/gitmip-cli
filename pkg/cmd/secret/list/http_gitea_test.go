package list

import (
	"net/http"
	"testing"
	"time"

	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withGiteaHost(t *testing.T) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)
}

// Gitea returns a bare array and names the timestamp created_at, so the shared
// decoder, which expects {"secrets": [...]} with updated_at, cannot be reused.
func TestGetSecretsGiteaBareArray(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotQuery string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/actions/secrets"),
		func(req *http.Request) (*http.Response, error) {
			gotQuery = req.URL.RawQuery
			return httpmock.StringResponse(`[
				{"name":"TOKEN","description":"","created_at":"2026-01-02T03:04:05Z"},
				{"name":"OTHER","description":"d","created_at":"2026-02-03T04:05:06Z"}
			]`)(req)
		},
	)

	client := &http.Client{Transport: reg}
	secrets, err := getRepoSecretsGitea(client, "git.example.com", "gitea", "tea")
	require.NoError(t, err)

	assert.Contains(t, gotQuery, "limit=50")
	require.Len(t, secrets, 2)
	assert.Equal(t, "TOKEN", secrets[0].Name)
	// created_at lands in the UpdatedAt field, which is what the table shows.
	assert.Equal(t, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), secrets[0].UpdatedAt)
	// Gitea has no visibility model, so the field stays empty and the table
	// drops the column rather than printing a blank one.
	assert.Empty(t, secrets[0].Visibility)
	assert.Empty(t, secrets[0].SelectedReposURL)
}

func TestGetSecretsGiteaOrg(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotPath string
	reg.Register(
		httpmock.REST("GET", "api/v1/orgs/gitea/actions/secrets"),
		func(req *http.Request) (*http.Response, error) {
			gotPath = req.URL.Path
			return httpmock.StringResponse(`[]`)(req)
		},
	)

	client := &http.Client{Transport: reg}
	secrets, err := getOrgSecretsGitea(client, "git.example.com", "gitea")
	require.NoError(t, err)
	assert.Empty(t, secrets)
	assert.Equal(t, "/api/v1/orgs/gitea/actions/secrets", gotPath)
}

func TestGetSecretsGiteaPages(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	// A full page then a short one. Each stub is one-shot, so two are needed.
	full := "["
	for i := range giteaSecretPageSize {
		if i > 0 {
			full += ","
		}
		full += `{"name":"s","created_at":"2026-01-02T03:04:05Z"}`
	}
	full += "]"

	// httpmock stubs are one-shot, so each page needs its own registration.
	pages := 0
	count := func(responder httpmock.Responder) httpmock.Responder {
		return func(req *http.Request) (*http.Response, error) {
			pages++
			return responder(req)
		}
	}
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/actions/secrets"),
		count(httpmock.StringResponse(full)),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/actions/secrets"),
		count(httpmock.StringResponse(`[{"name":"last","created_at":"2026-01-02T03:04:05Z"}]`)),
	)

	client := &http.Client{Transport: reg}
	secrets, err := getRepoSecretsGitea(client, "git.example.com", "gitea", "tea")
	require.NoError(t, err)
	assert.Len(t, secrets, giteaSecretPageSize+1)
	assert.Equal(t, 2, pages)
}
