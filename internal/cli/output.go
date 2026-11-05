package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sidewalk-developer-group/deployer-cli/internal/config"
	"github.com/sidewalk-developer-group/deployer-cli/internal/credentials"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

// printJSON writes one indented document. Log streams use newline-delimited
// objects instead (see internal/follow) so they can be piped line by line.
func printJSON(printer *ui.Printer, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding output: %w", err)
	}

	printer.Raw(string(encoded) + "\n")

	return nil
}

func removeToken(dir, context string) error {
	return credentials.New(dir).Delete(context)
}

func newVersionCommand(f *Factory) *cobra.Command {
	var checkRemote bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Long: "Prints the CLI version and commit without authentication. With --check, also\n" +
			"checks for a newer CLI release on a best-effort basis; an unreachable instance\n" +
			"does not make this command fail.",
		Example: "  deployer version\n" +
			"  deployer version --check",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			printer := f.Printer()

			if f.JSON {
				return printJSON(printer, map[string]string{"version": f.Version, "commit": f.Commit})
			}

			printer.Printf("deployer %s (%s)\n", f.Version, orDash(f.Commit))

			if !checkRemote {
				return nil
			}

			// Best effort and unauthenticated: an instance that doesn't answer
			// must not make `version` fail.
			file, err := config.Load()
			if err != nil {
				return nil
			}

			_, contextConfig, err := file.Resolve(f.ContextName)
			if err != nil {
				return nil
			}

			apiURL := f.resolvedAPIURL(contextConfig)
			if apiURL == "" {
				return nil
			}

			client, err := newAnonymousClient(apiURL, f.Version)
			if err != nil {
				return nil
			}

			release, err := client.Release(cmd.Context())
			if err != nil {
				printer.Dim("could not reach %s for release info", apiURL)

				return nil
			}

			if release.Latest != "" && release.Latest != f.Version {
				printer.Info("%s is available (you have %s)", release.Latest, f.Version)

				if release.Install.Homebrew != "" {
					printer.Dim("upgrade: %s", release.Install.Homebrew)
				} else if release.DownloadURL != "" {
					printer.Dim("download: %s", release.DownloadURL)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&checkRemote, "check", false, "ask the instance whether a newer CLI exists")

	return cmd
}
