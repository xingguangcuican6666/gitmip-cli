package revert

import (
	"testing"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRevertRunGitea(t *testing.T) {
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	}))

	ios, _, stdout, stderr := iostreams.Test()

	opts := &RevertOptions{
		IO:          ios,
		SelectorArg: "123",
		BaseRepo: func() (ghrepo.Interface, error) {
			return ghrepo.NewWithHost("gitea", "tea", "git.example.com"), nil
		},
		// The stub expects a different selector, so a lookup would fail with a
		// mismatch instead of reaching the refusal.
		Finder: shared.NewMockFinder("not-the-selector", &api.PullRequest{
			ID:     "SOME-ID",
			Number: 123,
			State:  "MERGED",
			Title:  "The title of the PR",
		}, ghrepo.NewWithHost("gitea", "tea", "git.example.com")),
	}

	err := revertRun(opts)

	require.EqualError(t, err, "Gitea has no revert endpoint, so `gm pr revert` cannot run against it; revert with git and open a pull request from the branch")
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
}
