package cli

import (
	"github.com/spf13/cobra"

	"github.com/sidewalk-developer-group/deployer-cli/internal/config"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
)

// A context is one Deployer instance plus a default organization. Self-hosted
// means an operator may hold several; nothing here is a secret (see the
// credentials package for tokens).
func newContextCommand(f *Factory) *cobra.Command {
	root := &cobra.Command{
		Use:   "context",
		Short: "Manage the configured instances",
		Long: "A context is one Deployer instance plus the organization to act in. Deployer is\n" +
			"self-hosted, so you may hold several — work, staging, a customer's instance —\n" +
			"and switch with `context use`. `deployer login` creates one for you.\n\n" +
			"Tokens live in the OS keychain, or a plaintext fallback file when unavailable.",
		Example: "  deployer context ls\n" +
			"  deployer context use production\n" +
			"  deployer context use production --org acme\n" +
			"  deployer context rm old-instance",
		Aliases: []string{"contexts"},
		Args:    cobra.NoArgs,
	}

	root.AddCommand(
		&cobra.Command{
			Use:     "ls",
			Aliases: []string{"list"},
			Short:   "List configured instances",
			Long:    "Lists each configured instance. The current one is marked with an asterisk.",
			Example: "  deployer context ls\n  deployer context ls --json",
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				printer := f.Printer()

				file, err := config.Load()
				if err != nil {
					return exit.With(exit.Usage, err)
				}

				if len(file.Contexts) == 0 {
					printer.Printf("no contexts yet — run `deployer login --api https://deployer.example.com`\n")

					return nil
				}

				if f.JSON {
					return printJSON(printer, file)
				}

				rows := make([][]string, 0, len(file.Contexts))
				for _, name := range file.Names() {
					marker := ""
					if name == file.CurrentContext {
						marker = "*"
					}
					rows = append(rows, []string{marker, name, file.Contexts[name].APIURL, orDash(file.Contexts[name].Organization)})
				}

				printer.Table([]string{"", "CONTEXT", "INSTANCE", "ORGANIZATION"}, rows)

				return nil
			},
		},
		newContextUseCommand(f),
		&cobra.Command{
			Use:   "rm <name>",
			Short: "Forget an instance (and its stored token)",
			Long: "Removes the context from the config file and deletes its stored token, so no\n" +
				"credential is left behind for an instance you can no longer see. The token\n" +
				"itself stays valid until revoked in the web app under Settings > Tokens.",
			Example: "  deployer context rm staging",
			Args:    wantArgs(1, 1, "deployer context rm <name>   — names come from `deployer context ls`"),
			RunE: func(cmd *cobra.Command, args []string) error {
				printer := f.Printer()

				file, err := config.Load()
				if err != nil {
					return exit.With(exit.Usage, err)
				}

				if err := file.Remove(args[0]); err != nil {
					return exit.With(exit.Usage, err)
				}

				if err := file.Save(); err != nil {
					return exit.With(exit.API, err)
				}

				dir, err := config.Dir()
				if err == nil {
					// Leaving a token behind for a context that no longer
					// exists would be a credential nobody can see or rotate.
					_ = removeToken(dir, args[0])
				}

				printer.Success("removed context %q", args[0])

				return nil
			},
		},
	)

	return root
}

func newContextUseCommand(f *Factory) *cobra.Command {
	var org string

	cmd := &cobra.Command{
		Use:   "use <name>",
		Short: "Make an instance the default, optionally setting its organization",
		Long: "Makes a context the default for later commands. With --org, also sets the\n" +
			"organization those commands act in — useful when your account belongs to more\n" +
			"than one and you would otherwise have to pass --org every time.",
		Example: "  deployer context use production\n  deployer context use staging --org acme",
		Args:    wantArgs(1, 1, "deployer context use <name> [--org <slug>]   — names come from `deployer context ls`"),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := f.Printer()

			file, err := config.Load()
			if err != nil {
				return exit.With(exit.Usage, err)
			}

			contextConfig, ok := file.Contexts[args[0]]
			if !ok {
				return exit.Errorf(exit.Usage, "unknown context %q", args[0])
			}

			file.CurrentContext = args[0]

			if org != "" {
				// Clear the cached id: it belongs to the previous slug, and a
				// stale mapping would silently deploy into the wrong org.
				contextConfig.Organization = org
				contextConfig.OrganizationID = ""
				file.Set(args[0], contextConfig)
			}

			if err := file.Save(); err != nil {
				return exit.With(exit.API, err)
			}

			printer.Success("using context %q (%s)", args[0], contextConfig.APIURL)

			if contextConfig.Organization != "" {
				printer.Dim("organization: %s", contextConfig.Organization)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&org, "org", "", "default organization slug for this context")

	return cmd
}
