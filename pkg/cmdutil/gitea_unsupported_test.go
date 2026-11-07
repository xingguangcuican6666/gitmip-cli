package cmdutil

import (
	"testing"

	"github.com/cli/cli/v2/internal/config"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testFactoryForHost builds a factory whose default host is host, with the host
// type forced so no hosts.yml on disk is read.
func testFactoryForHost(t *testing.T, host string, ht hosttype.Type) *Factory {
	t.Helper()
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{host: ht}))
	return &Factory{Config: func() (gh.Config, error) { return pinnedAuthConfig(host), nil }}
}

// pinnedAuthConfig builds a mock config whose auth answers DefaultHost with host.
func pinnedAuthConfig(host string) gh.Config {
	cfg := config.NewMockConfigFromString("hosts:\n    " + host + ":\n        git_protocol: https\n        user: someone\n")
	auth := cfg.Authentication()
	if setter, ok := auth.(interface{ SetDefaultHost(string, string) }); ok {
		setter.SetDefaultHost(host, "test")
	}
	cfg.AuthenticationFunc = func() gh.AuthConfig { return auth }
	return cfg
}

func newFamilyCmd(t *testing.T, ran *[]string) *cobra.Command {
	t.Helper()

	root := &cobra.Command{Use: "thing"}
	list := &cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error {
		*ran = append(*ran, "list")
		return nil
	}}
	nested := &cobra.Command{Use: "nested"}
	leaf := &cobra.Command{Use: "leaf", RunE: func(*cobra.Command, []string) error {
		*ran = append(*ran, "leaf")
		return nil
	}}
	nested.AddCommand(leaf)
	root.AddCommand(list, nested)
	root.PersistentFlags().StringP("repo", "R", "", "Select another repository")

	return root
}

func TestRefuseFamilyOnGiteaRefusesEveryLeaf(t *testing.T) {
	for _, name := range []string{"list", "nested leaf"} {
		t.Run(name, func(t *testing.T) {
			var ran []string
			f := testFactoryForHost(t, "git.example.com", hosttype.Gitea)
			// A fresh tree per case: cobra keeps the parsed args from a previous
			// Execute on the same command, so reusing it would exercise stale state.
			root := newFamilyCmd(t, &ran)
			root.SilenceErrors = true
			root.SilenceUsage = true

			RefuseFamilyOnGitea(root, f, "Things")

			root.SetArgs(splitArgsForTest(name))
			err := root.Execute()
			require.Error(t, err)

			var unsupported UnsupportedOnGiteaError
			require.ErrorAs(t, err, &unsupported)
			assert.Equal(t, "Things", unsupported.Product)
			assert.Equal(t, "git.example.com", unsupported.Host)

			assert.Empty(t, ran, "the wrapped command should not have run")
		})
	}
}

func TestRefuseFamilyOnGiteaPassesThroughOnGitHub(t *testing.T) {
	var ran []string
	f := testFactoryForHost(t, "github.com", hosttype.GitHub)
	root := newFamilyCmd(t, &ran)
	root.SilenceErrors = true
	root.SilenceUsage = true

	RefuseFamilyOnGitea(root, f, "Things")

	root.SetArgs(splitArgsForTest("list"))
	require.NoError(t, root.Execute())
	assert.Equal(t, []string{"list"}, ran)
}

func TestRefuseFamilyOnGiteaHonoursRepoOverride(t *testing.T) {
	var ran []string
	// The default host is GitHub, so only the -R override can make this refuse.
	// Both host types are installed so the override resolves as Gitea.
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"github.com":      hosttype.GitHub,
		"git.example.com": hosttype.Gitea,
	}))
	cfg := config.NewMockConfigFromString("hosts:\n    github.com:\n        git_protocol: https\n        user: someone\n")
	f := &Factory{Config: func() (gh.Config, error) { return cfg, nil }}
	root := newFamilyCmd(t, &ran)

	RefuseFamilyOnGitea(root, f, "Things")

	root.SetArgs(splitArgsForTest("list -R git.example.com/gitea/tea"))
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Things is a GitHub feature")
	assert.Empty(t, ran)
}

func TestRefuseFamilyOnGiteaHonoursRepoOverrideEnv(t *testing.T) {
	var ran []string
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"github.com":      hosttype.GitHub,
		"git.example.com": hosttype.Gitea,
	}))
	f := &Factory{Config: func() (gh.Config, error) { return pinnedAuthConfig("github.com"), nil }}
	root := newFamilyCmd(t, &ran)
	root.SilenceErrors = true
	root.SilenceUsage = true

	RefuseFamilyOnGitea(root, f, "Things")

	t.Setenv("GH_REPO", "git.example.com/gitea/tea")
	root.SetArgs(splitArgsForTest("list"))
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Things is a GitHub feature")
}

func TestRefuseFamilyOnGiteaIgnoresUnparseableOverride(t *testing.T) {
	var ran []string
	t.Cleanup(hosttype.SetHostTypeOverride(map[string]hosttype.Type{
		"github.com":      hosttype.GitHub,
		"git.example.com": hosttype.Gitea,
	}))
	f := &Factory{Config: func() (gh.Config, error) { return pinnedAuthConfig("github.com"), nil }}
	root := newFamilyCmd(t, &ran)
	root.SilenceErrors = true
	root.SilenceUsage = true

	RefuseFamilyOnGitea(root, f, "Things")

	// The command itself reports a malformed override with a message that names
	// what the user typed, so the wrapper must not guess a host here.
	root.SetArgs(splitArgsForTest("list -R not/a/repo/with/too/many/parts"))
	require.NoError(t, root.Execute())
	assert.Equal(t, []string{"list"}, ran)
}

func TestRefuseFamilyOnGiteaLeavesCommandsWithoutRunE(t *testing.T) {
	var ran []string
	f := testFactoryForHost(t, "git.example.com", hosttype.Gitea)

	root := &cobra.Command{Use: "thing"}
	root.AddCommand(&cobra.Command{Use: "help-only"})
	root.PersistentFlags().StringP("repo", "R", "", "Select another repository")

	RefuseFamilyOnGitea(root, f, "Things")

	// A leaf with nothing to run keeps help working rather than gaining an error.
	assert.Nil(t, root.Commands()[0].RunE)
	assert.Empty(t, ran)
}

func TestUnsupportedOnGiteaErrorMessage(t *testing.T) {
	assert.Equal(t,
		"Things is a GitHub feature and has no counterpart on Gitea, which git.example.com serves",
		UnsupportedOnGiteaError{Product: "Things", Host: "git.example.com"}.Error())
	assert.Equal(t,
		"Things is a GitHub feature and has no counterpart on Gitea",
		UnsupportedOnGiteaError{Product: "Things"}.Error())
}

func splitArgsForTest(s string) []string {
	var out []string
	current := ""
	for _, r := range s {
		if r == ' ' {
			if current != "" {
				out = append(out, current)
				current = ""
			}
			continue
		}
		current += string(r)
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}
