package delete

import (
	"net/http"
	"testing"

	"github.com/cli/cli/v2/api"
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

// Gitea answers DELETE with 200 and an empty body. api.Client.REST decodes
// every 2xx body as JSON, which fails on nothing, so the Gitea path has to
// drain the response instead. This pins that behaviour.
func TestDeleteVariableGiteaToleratesEmptyBody(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotMethod, gotPath string
	reg.Register(
		httpmock.REST("DELETE", "api/v1/repos/gitea/tea/actions/variables/DEPLOY_ENV"),
		func(req *http.Request) (*http.Response, error) {
			gotMethod = req.Method
			gotPath = req.URL.Path
			return httpmock.StatusStringResponse(200, "")(req)
		},
	)

	client := api.NewClientFromHTTP(&http.Client{Transport: reg})
	err := deleteVariable(client, "git.example.com", "repos/gitea/tea/actions/variables/DEPLOY_ENV")
	require.NoError(t, err)
	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/api/v1/repos/gitea/tea/actions/variables/DEPLOY_ENV", gotPath)
}

func TestDeleteVariableGiteaReportsNotFound(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("DELETE", "api/v1/repos/gitea/tea/actions/variables/NOPE"),
		httpmock.StatusStringResponse(404, `{"message":"Not Found"}`),
	)

	client := api.NewClientFromHTTP(&http.Client{Transport: reg})
	err := deleteVariable(client, "git.example.com", "repos/gitea/tea/actions/variables/NOPE")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// A GitHub host keeps the REST path, so a 200 with an empty body would still be
// decoded. This asserts the dispatch, not the decode.
func TestDeleteVariableGitHubUsesREST(t *testing.T) {
	restore := hosttype.SetHostTypeOverride(nil)
	t.Cleanup(restore)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("DELETE", "repos/owner/repo/actions/variables/VAR"),
		httpmock.StatusStringResponse(204, `{}`),
	)

	client := api.NewClientFromHTTP(&http.Client{Transport: reg})
	err := deleteVariable(client, "github.com", "repos/owner/repo/actions/variables/VAR")
	require.NoError(t, err)
}
