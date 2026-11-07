package cmdutil

import (
	"os"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/spf13/cobra"
)

// RefuseFamilyOnGitea makes every command in a family fail with an explanation
// when the host it would talk to is Gitea.
//
// A Gitea host has no GraphQL API, and some GitHub features have no Gitea
// counterpart at all. For a family that is entirely one or the other, wrapping
// each leaf command beats letting the request go out and reporting the 404 or the
// "host does not support the GraphQL API" message that comes back, neither of
// which says what the user can do instead.
//
// product is the name of the feature, used in the message.
func RefuseFamilyOnGitea(cmd *cobra.Command, f *Factory, product string) {
	for _, leaf := range leafCommands(cmd) {
		original := leaf.RunE
		if original == nil {
			// A leaf with no RunE has nothing to call, so there is nothing to
			// refuse: the help for it is still useful.
			continue
		}

		leaf.RunE = func(c *cobra.Command, args []string) error {
			if host, isGitea := giteaHostForCommand(c, f); isGitea {
				return UnsupportedOnGiteaError{Product: product, Host: host}
			}
			return original(c, args)
		}
	}
}

// leafCommands returns every command in the tree that has no children.
func leafCommands(cmd *cobra.Command) []*cobra.Command {
	if len(cmd.Commands()) == 0 {
		return []*cobra.Command{cmd}
	}

	var leaves []*cobra.Command
	for _, child := range cmd.Commands() {
		leaves = append(leaves, leafCommands(child)...)
	}
	return leaves
}

// giteaHostForCommand resolves the host a command would talk to and reports
// whether it is Gitea.
//
// A repository override wins, because -R and GH_REPO can name a host that is not
// the default one. Otherwise the configured default host decides. An unresolvable
// override is not an error here: the command itself reports that, with a message
// that names what the user actually typed.
func giteaHostForCommand(c *cobra.Command, f *Factory) (string, bool) {
	if host, ok := hostFromRepoOverride(c); ok {
		return host, hosttype.IsGitea(host)
	}

	if f.Config == nil {
		return "", false
	}
	cfg, err := f.Config()
	if err != nil {
		return "", false
	}
	host, _ := cfg.Authentication().DefaultHost()
	return host, hosttype.IsGitea(host)
}

// hostFromRepoOverride reads a repository override and returns its host when one
// was given.
func hostFromRepoOverride(c *cobra.Command) (string, bool) {
	override := ""
	if flag := c.Flags().Lookup("repo"); flag != nil && flag.Changed {
		override = flag.Value.String()
	}
	if override == "" {
		override = os.Getenv("GH_REPO")
	}
	if override == "" {
		return "", false
	}

	repo, err := ghrepo.FromFullName(override)
	if err != nil {
		return "", false
	}
	return repo.RepoHost(), true
}

// UnsupportedOnGiteaError reports that a command cannot run against a Gitea host,
// and why.
//
// The message names the host so a user with several hosts configured can tell
// which one the command resolved to.
type UnsupportedOnGiteaError struct {
	Product string
	Host    string
}

func (e UnsupportedOnGiteaError) Error() string {
	if e.Host == "" {
		return e.Product + " is a GitHub feature and has no counterpart on Gitea"
	}
	return e.Product + " is a GitHub feature and has no counterpart on Gitea, which " + e.Host + " serves"
}
