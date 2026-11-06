package list

import (
	"net/http"
	"testing"

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

// Gitea returns a bare array whose value field is called "data", so neither the
// response envelope nor the field names of the shared decoder apply.
func TestGetVariablesGiteaBareArrayAndDataField(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotQuery string
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/actions/variables"),
		func(req *http.Request) (*http.Response, error) {
			gotQuery = req.URL.RawQuery
			return httpmock.StringResponse(`[
				{"name":"DEPLOY_ENV","data":"prod","description":"","owner_id":0,"repo_id":550},
				{"name":"REGION","data":"eu-west-1"}
			]`)(req)
		},
	)

	client := &http.Client{Transport: reg}
	variables, err := getRepoVariablesGitea(client, "git.example.com", "gitea", "tea")
	require.NoError(t, err)

	assert.Contains(t, gotQuery, "limit=50")
	require.Len(t, variables, 2)
	assert.Equal(t, "DEPLOY_ENV", variables[0].Name)
	// "data" is the value, not a field gh would otherwise read.
	assert.Equal(t, "prod", variables[0].Value)
	assert.Equal(t, "eu-west-1", variables[1].Value)
	// Gitea reports no timestamps and no visibility.
	assert.True(t, variables[0].UpdatedAt.IsZero())
	assert.Empty(t, variables[0].Visibility)
}

func TestGetVariablesGiteaOrg(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotPath string
	reg.Register(
		httpmock.REST("GET", "api/v1/orgs/gitea/actions/variables"),
		func(req *http.Request) (*http.Response, error) {
			gotPath = req.URL.Path
			return httpmock.StringResponse(`[]`)(req)
		},
	)

	client := &http.Client{Transport: reg}
	variables, err := getOrgVariablesGitea(client, "git.example.com", "gitea")
	require.NoError(t, err)
	assert.Empty(t, variables)
	assert.Equal(t, "/api/v1/orgs/gitea/actions/variables", gotPath)
}

func TestGetVariablesGiteaPages(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	full := "["
	for i := range giteaVariablePageSize {
		if i > 0 {
			full += ","
		}
		full += `{"name":"v","data":"x"}`
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
		httpmock.REST("GET", "api/v1/repos/gitea/tea/actions/variables"),
		count(httpmock.StringResponse(full)),
	)
	reg.Register(
		httpmock.REST("GET", "api/v1/repos/gitea/tea/actions/variables"),
		count(httpmock.StringResponse(`[{"name":"last","data":"y"}]`)),
	)

	client := &http.Client{Transport: reg}
	variables, err := getRepoVariablesGitea(client, "git.example.com", "gitea", "tea")
	require.NoError(t, err)
	assert.Len(t, variables, giteaVariablePageSize+1)
	assert.Equal(t, 2, pages)
}
