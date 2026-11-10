package credits

import (
	"net/http"
	"testing"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withGiteaCreditsHost(t *testing.T) {
	t.Helper()
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	}))
}

func TestCreditsRunGitea(t *testing.T) {
	withGiteaCreditsHost(t)

	tests := []struct {
		name string
		opts *CreditsOptions
	}{
		{
			name: "repository argument names a Gitea host",
			opts: &CreditsOptions{Repository: "git.example.com/gitea/tea"},
		},
		{
			name: "base repository resolves to a Gitea host",
			opts: &CreditsOptions{BaseRepo: func() (ghrepo.Interface, error) {
				return ghrepo.NewWithHost("gitea", "tea", "git.example.com"), nil
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			ios, _, stdout, stderr := iostreams.Test()

			opts := tt.opts
			opts.IO = ios
			opts.HttpClient = func() (*http.Client, error) {
				return &http.Client{Transport: reg}, nil
			}

			err := creditsRun(opts)

			require.EqualError(t, err, "Gitea has no contributors endpoint, so credits cannot be fetched from gitea/tea")
			assert.Empty(t, reg.Requests)
			assert.Empty(t, stdout.String())
			assert.Empty(t, stderr.String())
		})
	}
}

func TestCreditsRunFetchesOnGitHub(t *testing.T) {
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	reg.Register(
		httpmock.REST("GET", "repos/OWNER/REPO/contributors"),
		httpmock.JSONResponse([]map[string]string{
			{"login": "monalisa", "type": "User"},
			{"login": "dependabot[bot]", "type": "Bot"},
		}),
	)

	ios, _, stdout, stderr := iostreams.Test()

	opts := &CreditsOptions{
		IO: ios,
		HttpClient: func() (*http.Client, error) {
			return &http.Client{Transport: reg}, nil
		},
		Repository: "github.com/OWNER/REPO",
	}

	require.NoError(t, creditsRun(opts))
	assert.Equal(t, "monalisa\n", stdout.String())
	assert.Empty(t, stderr.String())
}
