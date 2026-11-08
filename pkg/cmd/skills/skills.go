package skills

import (
	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/gm/ghtelemetry"
	"github.com/cli/cli/v2/pkg/cmd/skills/install"
	skilllist "github.com/cli/cli/v2/pkg/cmd/skills/list"
	"github.com/cli/cli/v2/pkg/cmd/skills/preview"
	"github.com/cli/cli/v2/pkg/cmd/skills/publish"
	"github.com/cli/cli/v2/pkg/cmd/skills/search"
	"github.com/cli/cli/v2/pkg/cmd/skills/update"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

// NewCmdSkills returns the top-level "skill" command.
func NewCmdSkills(f *cmdutil.Factory, telemetry ghtelemetry.InvocationRecorder) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill <command>",
		Short: "Install and manage agent skills (preview)",
		Long: heredoc.Doc(`
			Install and manage agent skills from GitHub repositories.

			Working with agent skills in the GitHub CLI is in preview and
			subject to change without notice.
		`),
		Aliases: []string{"skills"},
		GroupID: "core",
		Example: heredoc.Doc(`
			# Search for skills
			$ gm skill search terraform

			# Install a skill
			$ gm skill install github/awesome-copilot documentation-writer

			# List installed skills
			$ gm skill list

			# Preview a skill before installing
			$ gm skill preview github/awesome-copilot documentation-writer

			# Update all installed skills
			$ gm skill update --all

			# Validate skills for publishing
			$ gm skill publish --dry-run
		`),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			telemetry.SetSampleRate(ghtelemetry.SAMPLE_ALL)
			return nil
		},
	}

	cmd.AddCommand(install.NewCmdInstall(f, telemetry, nil))
	cmd.AddCommand(skilllist.NewCmdList(f, telemetry, nil))
	cmd.AddCommand(preview.NewCmdPreview(f, telemetry, nil))
	cmd.AddCommand(publish.NewCmdPublish(f, nil))
	cmd.AddCommand(search.NewCmdSearch(f, telemetry, nil))
	cmd.AddCommand(update.NewCmdUpdate(f, nil))

	return cmd
}
