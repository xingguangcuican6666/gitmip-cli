package delete

import (
	"testing"

	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/pkg/cmd/repo/autolink/shared"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteRunGitea(t *testing.T) {
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	}))

	ios, _, stdout, stderr := iostreams.Test()

	opts := &deleteOptions{
		IO:        ios,
		Browser:   &browser.Stub{},
		Prompter:  &prompter.PrompterMock{},
		ID:        "123",
		Confirmed: true,
		BaseRepo: func() (ghrepo.Interface, error) {
			return ghrepo.NewWithHost("gitea", "tea", "git.example.com"), nil
		},
		// Both stubs report errors, so a run that got past the guard would return
		// one of those instead of the guard's error.
		AutolinkViewClient:   &stubAutolinkViewer{err: errTestAutolinkClientView},
		AutolinkDeleteClient: &stubAutolinkDeleter{err: errTestAutolinkClientDelete},
	}

	err := deleteRun(opts)

	require.ErrorIs(t, err, shared.ErrGiteaAutolinkUnsupported)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
}
