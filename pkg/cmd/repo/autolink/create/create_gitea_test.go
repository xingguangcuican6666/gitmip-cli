package create

import (
	"testing"

	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/cmd/repo/autolink/shared"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateRunGitea(t *testing.T) {
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	}))

	ios, _, stdout, stderr := iostreams.Test()

	opts := &createOptions{
		IO:          ios,
		Browser:     &browser.Stub{},
		KeyPrefix:   "TICKET-",
		URLTemplate: "https://example.com/TICKET?query=<num>",
		BaseRepo: func() (ghrepo.Interface, error) {
			return ghrepo.NewWithHost("gitea", "tea", "git.example.com"), nil
		},
		// The stub succeeds, so a run that got past the guard would print a
		// success line instead of returning an error.
		AutolinkClient: &stubAutolinkCreator{},
	}

	err := createRun(opts)

	require.ErrorIs(t, err, shared.ErrGiteaAutolinkUnsupported)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
}
