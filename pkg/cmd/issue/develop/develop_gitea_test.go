package develop

import (
	"net/http"
	"testing"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDevelopRunGitea(t *testing.T) {
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	}))

	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	ios, _, stdout, stderr := iostreams.Test()

	opts := &DevelopOptions{
		IO: ios,
		HttpClient: func() (*http.Client, error) {
			return &http.Client{Transport: reg}, nil
		},
		IssueNumber: 42,
		List:        true,
		BaseRepo: func() (ghrepo.Interface, error) {
			return ghrepo.NewWithHost("gitea", "tea", "git.example.com"), nil
		},
	}

	err := developRun(opts)

	require.ErrorIs(t, err, api.ErrGiteaLinkedBranchUnsupported)
	assert.Empty(t, reg.Requests)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
}
