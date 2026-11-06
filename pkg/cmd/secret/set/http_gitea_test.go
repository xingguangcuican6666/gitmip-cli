package set

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/cmd/secret/shared"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var giteaSecretRepo = ghrepo.NewWithHost("gitea", "tea", "git.example.com")

func withGiteaHost(t *testing.T) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	})
	t.Cleanup(restore)
}

// Gitea takes the value in plaintext over PUT, so the body carries "data"
// and there is no key_id or encrypted_value anywhere in the request.
func TestSetSecretGiteaSendsPlaintext(t *testing.T) {
	withGiteaHost(t)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	var gotMethod, gotPath, gotBody string
	reg.Register(
		httpmock.MatchAny,
		func(req *http.Request) (*http.Response, error) {
			gotMethod = req.Method
			gotPath = req.URL.Path
			b, _ := io.ReadAll(req.Body)
			gotBody = string(b)
			return httpmock.StatusStringResponse(201, "")(req)
		},
	)

	client := api.NewClientFromHTTP(&http.Client{Transport: reg})
	res := setSecretGitea("git.example.com", client, giteaSecretRepo, "", shared.Repository, shared.Actions, "TOKEN", []byte("hunter2"))
	require.NoError(t, res.err)
	assert.Equal(t, "TOKEN", res.key)

	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "/api/v1/repos/gitea/tea/actions/secrets/TOKEN", gotPath)

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(gotBody), &body))
	assert.Equal(t, "hunter2", body["data"])
	assert.NotContains(t, body, "encrypted_value")
	assert.NotContains(t, body, "key_id")
}

func TestSetSecretGiteaPathPerEntity(t *testing.T) {
	withGiteaHost(t)

	tests := []struct {
		name     string
		entity   shared.SecretEntity
		orgName  string
		wantPath string
	}{
		{
			name:     "repository",
			entity:   shared.Repository,
			wantPath: "/api/v1/repos/gitea/tea/actions/secrets/TOKEN",
		},
		{
			name:     "organization",
			entity:   shared.Organization,
			orgName:  "gitea",
			wantPath: "/api/v1/orgs/gitea/actions/secrets/TOKEN",
		},
		{
			name:     "user",
			entity:   shared.User,
			wantPath: "/api/v1/user/actions/secrets/TOKEN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			var gotPath string
			reg.Register(httpmock.MatchAny, func(req *http.Request) (*http.Response, error) {
				gotPath = req.URL.Path
				return httpmock.StatusStringResponse(201, "")(req)
			})

			client := api.NewClientFromHTTP(&http.Client{Transport: reg})
			res := setSecretGitea("git.example.com", client, giteaSecretRepo, tt.orgName, tt.entity, shared.Actions, "TOKEN", []byte("v"))
			require.NoError(t, res.err)
			assert.Equal(t, tt.wantPath, gotPath)
		})
	}
}

func TestSetSecretGiteaRejectsUnsupportedCombinations(t *testing.T) {
	withGiteaHost(t)

	tests := []struct {
		name    string
		entity  shared.SecretEntity
		app     shared.App
		wantErr string
	}{
		{
			name:    "codespaces app",
			entity:  shared.Repository,
			app:     shared.Codespaces,
			wantErr: "Gitea has no Codespaces secrets; only `--app actions` is available",
		},
		{
			name:    "dependabot app",
			entity:  shared.Repository,
			app:     shared.Dependabot,
			wantErr: "Gitea has no Dependabot secrets; only `--app actions` is available",
		},
		{
			name:    "environment entity",
			entity:  shared.Environment,
			app:     shared.Actions,
			wantErr: "Gitea has no deployment environments, so `--env` is not supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			client := api.NewClientFromHTTP(&http.Client{Transport: reg})
			res := setSecretGitea("git.example.com", client, giteaSecretRepo, "", tt.entity, tt.app, "TOKEN", []byte("v"))
			require.EqualError(t, res.err, tt.wantErr)
			// Verify above proves no request was registered as used, so a
			// rejection costs no round trip.
		})
	}
}

func TestSetRunGiteaRejectsNoStore(t *testing.T) {
	withGiteaHost(t)

	ios, _, _, _ := iostreams.Test()
	opts := &SetOptions{IO: ios, DoNotStore: true, UserSecrets: false}

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	client := api.NewClientFromHTTP(&http.Client{Transport: reg})
	err := setRunGitea(opts, client, "git.example.com", giteaSecretRepo, "",
		map[string][]byte{"TOKEN": []byte("v")}, shared.Actions, shared.Repository)
	require.ErrorIs(t, err, errGiteaNoStoreUnsupported)
}

func TestSetRunGiteaReportsEachSecret(t *testing.T) {
	withGiteaHost(t)

	ios, _, stdout, _ := iostreams.Test()
	ios.SetStdoutTTY(true)
	opts := &SetOptions{IO: ios}

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(httpmock.MatchAny, httpmock.StatusStringResponse(201, ""))
	reg.Register(httpmock.MatchAny, httpmock.StatusStringResponse(201, ""))

	client := api.NewClientFromHTTP(&http.Client{Transport: reg})
	err := setRunGitea(opts, client, "git.example.com", giteaSecretRepo, "",
		map[string][]byte{"A": []byte("1"), "B": []byte("2")}, shared.Actions, shared.Repository)
	require.NoError(t, err)

	out := stdout.String()
	assert.Contains(t, out, "Set Actions secret A for gitea/tea")
	assert.Contains(t, out, "Set Actions secret B for gitea/tea")
}
