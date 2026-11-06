package set

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/cmd/variable/shared"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var giteaVarRepo = ghrepo.NewWithHost("gitea", "tea", "git.example.com")

func withGiteaHost(t *testing.T) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)
}

// Gitea puts the variable name in the path on create, and the body carries only
// the value, since CreateVariableOption has no name field.
func TestSetVariableGiteaCreates(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotMethod, gotPath, gotBody string
	reg.Register(
		httpmock.REST("POST", "api/v1/repos/gitea/tea/actions/variables/DEPLOY_ENV"),
		func(req *http.Request) (*http.Response, error) {
			gotMethod = req.Method
			gotPath = req.URL.Path
			b, _ := io.ReadAll(req.Body)
			gotBody = string(b)
			return httpmock.StatusStringResponse(201, "")(req)
		},
	)

	client := api.NewClientFromHTTP(&http.Client{Transport: reg})
	res := setVariableGitea(client, "git.example.com", setOptions{
		Entity:     shared.Repository,
		Key:        "DEPLOY_ENV",
		Value:      "prod",
		Repository: giteaVarRepo,
	})
	require.NoError(t, res.Err)
	assert.Equal(t, createdOperation, res.Operation)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/api/v1/repos/gitea/tea/actions/variables/DEPLOY_ENV", gotPath)

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(gotBody), &body))
	assert.Equal(t, "prod", body["value"])
	// The name is in the path, so it must not also be in the body, and neither
	// visibility nor selected repositories exist on Gitea.
	assert.NotContains(t, body, "name")
	assert.NotContains(t, body, "visibility")
	assert.NotContains(t, body, "selected_repository_ids")
}

// POST answering 409 is Gitea's signal that the name is taken, which is what
// the update is for. The reported operation has to say "Updated" so the user
// knows the value replaced an existing one.
func TestSetVariableGiteaUpdatesOnConflict(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("POST", "api/v1/repos/gitea/tea/actions/variables/DEPLOY_ENV"),
		httpmock.StatusStringResponse(409, `{"message":"variable name already exists."}`),
	)

	var gotMethod string
	reg.Register(
		httpmock.REST("PUT", "api/v1/repos/gitea/tea/actions/variables/DEPLOY_ENV"),
		func(req *http.Request) (*http.Response, error) {
			gotMethod = req.Method
			return httpmock.StatusStringResponse(204, "")(req)
		},
	)

	client := api.NewClientFromHTTP(&http.Client{Transport: reg})
	res := setVariableGitea(client, "git.example.com", setOptions{
		Entity:     shared.Repository,
		Key:        "DEPLOY_ENV",
		Value:      "prod",
		Repository: giteaVarRepo,
	})
	require.NoError(t, res.Err)
	assert.Equal(t, updatedOperation, res.Operation)
	assert.Equal(t, http.MethodPut, gotMethod)
}

func TestSetVariableGiteaOrgPath(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotPath string
	reg.Register(
		httpmock.REST("POST", "api/v1/orgs/gitea/actions/variables/DEPLOY_ENV"),
		func(req *http.Request) (*http.Response, error) {
			gotPath = req.URL.Path
			return httpmock.StatusStringResponse(201, "")(req)
		},
	)

	client := api.NewClientFromHTTP(&http.Client{Transport: reg})
	res := setVariableGitea(client, "git.example.com", setOptions{
		Entity:       shared.Organization,
		Key:          "DEPLOY_ENV",
		Value:        "prod",
		Organization: "gitea",
	})
	require.NoError(t, res.Err)
	assert.Equal(t, "/api/v1/orgs/gitea/actions/variables/DEPLOY_ENV", gotPath)
}

func TestSetVariableGiteaRejectsEnvironment(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	client := api.NewClientFromHTTP(&http.Client{Transport: reg})
	res := setVariableGitea(client, "git.example.com", setOptions{
		Entity:      shared.Environment,
		Key:         "DEPLOY_ENV",
		Value:       "prod",
		Environment: "staging",
		Repository:  giteaVarRepo,
	})
	require.EqualError(t, res.Err,
		`failed to set variable "DEPLOY_ENV": Gitea has no deployment environments, so `+"`--env` is not supported")
}

// A server error other than 409 must not be retried as an update, or a failed
// create would silently turn into a write to a variable that may not exist.
func TestSetVariableGiteaDoesNotUpdateOnOtherErrors(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("POST", "api/v1/repos/gitea/tea/actions/variables/DEPLOY_ENV"),
		httpmock.StatusStringResponse(403, `{"message":"forbidden"}`),
	)

	client := api.NewClientFromHTTP(&http.Client{Transport: reg})
	res := setVariableGitea(client, "git.example.com", setOptions{
		Entity:     shared.Repository,
		Key:        "DEPLOY_ENV",
		Value:      "prod",
		Repository: giteaVarRepo,
	})
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "403")
	assert.Equal(t, createdOperation, res.Operation)
}
