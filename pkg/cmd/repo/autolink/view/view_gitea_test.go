package view

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

func TestViewRunGitea(t *testing.T) {
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	}))

	ios, _, stdout, stderr := iostreams.Test()

	opts := &viewOptions{
		IO:      ios,
		Browser: &browser.Stub{},
		ID:      "1",
		BaseRepo: func() (ghrepo.Interface, error) {
			return ghrepo.NewWithHost("gitea", "tea", "git.example.com"), nil
		},
		// The stub returns a nil autolink, so a run that got past the guard would
		// panic rather than pass quietly.
		AutolinkClient: &stubAutolinkViewer{},
	}

	err := viewRun(opts)

	require.ErrorIs(t, err, shared.ErrGiteaAutolinkUnsupported)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
}
