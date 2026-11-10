package revert

import (
	"testing"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
)

func withGiteaRevertHost(t *testing.T) {
	t.Helper()
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	}))
}

// TestPRRevertGitea checks that a URL selector is refused before the lookup: the
// stubbed finder expects selector "123", so a lookup would fail with a selector
// mismatch instead of the refusal.
func TestPRRevertGitea(t *testing.T) {
	withGiteaRevertHost(t)

	http := &httpmock.Registry{}
	defer http.Verify(t)

	shared.StubFinderForRunCommandStyleTests(t, "123", &api.PullRequest{
		ID:     "SOME-ID",
		Number: 123,
		State:  "MERGED",
		Title:  "The title of the PR",
	}, ghrepo.NewWithHost("gitea", "tea", "git.example.com"))

	output, err := runCommand(http, true, "https://git.example.com/gitea/tea/pull/123")

	assert.EqualError(t, err, "Gitea has no revert endpoint, so `gm pr revert` cannot run against it; revert with git and open a pull request from the branch")
	assert.Equal(t, "", output.String())
	assert.Equal(t, "", output.Stderr())
}
