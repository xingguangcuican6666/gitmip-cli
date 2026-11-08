package version

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/cli/cli/v2/internal/debug"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

func NewCmdVersion(f *cmdutil.Factory, version, buildDate string) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "version",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprint(f.IOStreams.Out, cmd.Root().Annotations["versionInfo"])
			return nil
		},
	}

	cmdutil.DisableAuthCheck(cmd)

	return cmd
}

func Format(version, buildDate string) string {
	return format(version, buildDate, debug.Enabled)
}

func format(version, buildDate string, debugBuild bool) string {
	version = strings.TrimPrefix(version, "v")

	var dateStr string
	if buildDate != "" {
		dateStr = fmt.Sprintf(" (%s)", buildDate)
	}

	var debugStr string
	if debugBuild {
		debugStr = " [debug build]"
	}

	return fmt.Sprintf("gm version %s%s%s\n%s\n", version, dateStr, debugStr, changelogURL(version))
}

func changelogURL(version string) string {
	// This fork ships from its own repository, so the release link points there
	// rather than at the upstream project it was forked from.
	path := "https://github.com/xingguangcuican6666/gitmip-cli"
	r := regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[\w.]+)?$`)
	if !r.MatchString(version) {
		return fmt.Sprintf("%s/releases/latest", path)
	}

	url := fmt.Sprintf("%s/releases/tag/v%s", path, strings.TrimPrefix(version, "v"))
	return url
}
