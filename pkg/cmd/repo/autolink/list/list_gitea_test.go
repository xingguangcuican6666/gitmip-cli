package list

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

func TestListRunGitea(t *testing.T) {
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	}))

	ios, _, stdout, stderr := iostreams.Test()
	browserStub := &browser.Stub{}

	opts := &listOptions{
		IO:      ios,
		Browser: browserStub,
		BaseRepo: func() (ghrepo.Interface, error) {
			return ghrepo.NewWithHost("gitea", "tea", "git.example.com"), nil
		},
		// A stub that would fail the test if it were consulted: the guard has to
		// answer before any client call, web mode included.
		AutolinkClient: stubAutolinkLister{err: testAutolinkClientListError{}},
		WebMode:        true,
	}

	err := listRun(opts)

	require.ErrorIs(t, err, shared.ErrGiteaAutolinkUnsupported)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
	browserStub.Verify(t, "")
}
