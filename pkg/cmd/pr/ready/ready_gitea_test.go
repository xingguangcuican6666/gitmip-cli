package ready

import (
	"testing"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/cmd/pr/shared"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
)

func withGiteaReadyHost(t *testing.T) {
	t.Helper()
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"git.example.com": hosttype.Gitea,
	}))
}

// TestPRReadyGitea checks that a URL selector is refused before the lookup: the
// stubbed finder expects selector "123", so a lookup would fail with a selector
// mismatch instead of the refusal.
func TestPRReadyGitea(t *testing.T) {
	withGiteaReadyHost(t)

	http := &httpmock.Registry{}
	defer http.Verify(t)

	shared.StubFinderForRunCommandStyleTests(t, "123", &api.PullRequest{
		ID:      "THE-ID",
		Number:  123,
		State:   "OPEN",
		IsDraft: true,
	}, ghrepo.NewWithHost("gitea", "tea", "git.example.com"))

	output, err := runCommand(http, true, "https://git.example.com/gitea/tea/pull/123")

	assert.EqualError(t, err, "Gitea does not expose a draft toggle in its API, so gitea/tea cannot change whether a pull request is a draft")
	assert.Equal(t, "", output.String())
	assert.Equal(t, "", output.Stderr())
}
