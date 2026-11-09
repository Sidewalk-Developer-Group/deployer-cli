package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/config"
	"github.com/sidewalk-developer-group/deployer-cli/internal/credentials"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
)

func newLoginCommand(f *Factory) *cobra.Command {
	var (
		apiURL     string
		contextArg string
		tokenStdin bool
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store an API token for a Deployer instance",
		Long: "Stores a personal access token for an instance in the OS keychain.\n\n" +
			"Create the token in the web app under Settings → Security → Personal access tokens with at least the\n" +
			"projects:read, deployments:read and deployments:create abilities (add\n" +
			"deployments:approve to approve or reject from the terminal).\n\n" +
			"The token is read from a prompt or from stdin — never from a flag, which\n" +
			"would leave it in shell history and in the process list. If the OS keychain\n" +
			"is unavailable, a plaintext file with mode 0600 is used with a warning.",
		Example: "  deployer login --api https://deployer.example.com",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			printer := f.Printer()

			file, err := config.Load()
			if err != nil {
				return exit.With(exit.Usage, err)
			}

			// An existing context's URL is the default, so re-authenticating
			// after a token rotation is just `deployer login`.
			existingName, existingContext, _ := file.Resolve(firstNonEmpty(contextArg, f.ContextName))

			url := firstNonEmpty(apiURL, f.APIURL, os.Getenv("DEPLOYER_API_URL"))
			if url == "" && existingContext != nil {
				url = existingContext.APIURL
			}

			if url == "" {
				url, err = promptLine("Deployer instance URL (e.g. https://deployer.example.com): ")
				if err != nil {
					return err
				}
			}

			if url == "" {
				return exit.Errorf(exit.Usage, "an instance URL is required: pass --api")
			}

			name := firstNonEmpty(contextArg, f.ContextName, existingName, config.DefaultContextName(url))

			token, err := readToken(tokenStdin)
			if err != nil {
				return err
			}

			client, err := api.New(url, token, f.Version)
			if err != nil {
				return exit.With(exit.Usage, err)
			}

			// Verify before storing: a typo'd token that silently "logs in" and
			// fails on the next deploy is worse than a failed login.
			me, err := client.Me(ctx)
			if err != nil {
				return err
			}

			contextConfig := &config.Context{APIURL: client.BaseURL()}
			if existingContext != nil && existingName == name {
				contextConfig = existingContext
				contextConfig.APIURL = client.BaseURL()
			}

			// One membership is unambiguous, so record it and skip the lookup on
			// every later command. Several: leave it unset and let --org decide.
			if len(me.Organizations) == 1 {
				contextConfig.Organization = me.Organizations[0].Slug
				contextConfig.OrganizationID = me.Organizations[0].ID
			}

			file.Set(name, contextConfig)
			file.CurrentContext = name

			if err := file.Save(); err != nil {
				return exit.With(exit.API, err)
			}

			dir, err := config.Dir()
			if err != nil {
				return exit.With(exit.Usage, err)
			}

			usedFile, err := credentials.New(dir).Set(name, token)
			if err != nil {
				return exit.With(exit.API, err)
			}

			printer.Success("logged in to %s as %s (context %q)", client.BaseURL(), me.Email, name)

			switch {
			case len(me.Organizations) == 1:
				printer.Dim("organization: %s", me.Organizations[0].Slug)
			case len(me.Organizations) > 1:
				printer.Dim("organizations: %s — pass --org <slug> or set it with `deployer context use %s --org <slug>`", strings.Join(orgSlugs(me), ", "), name)
			}

			if usedFile {
				printer.Warn("no OS keyring available: token stored in %s/credentials with 0600 permissions", dir)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&apiURL, "api", "", "instance URL to log in to")
	cmd.Flags().StringVar(&contextArg, "context", "", "name to store this instance under (default: its hostname)")
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the token from stdin instead of prompting")

	return cmd
}

func newLogoutCommand(f *Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the stored token for a context",
		Long: "Deletes the locally stored token. The token itself stays valid until it is\n" +
			"revoked in the web app under Settings → Security → Personal access tokens.",
		Example: "  deployer logout\n  deployer logout --context staging",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			printer := f.Printer()

			file, err := config.Load()
			if err != nil {
				return exit.With(exit.Usage, err)
			}

			name, _, err := file.Resolve(f.ContextName)
			if err != nil {
				if errors.Is(err, config.ErrNoContext) {
					printer.Dim("nothing to do: no context configured")

					return nil
				}

				return exit.With(exit.Usage, err)
			}

			dir, err := config.Dir()
			if err != nil {
				return exit.With(exit.Usage, err)
			}

			if err := credentials.New(dir).Delete(name); err != nil {
				return exit.With(exit.API, err)
			}

			printer.Success("removed the stored token for %q", name)
			printer.Dim("revoke it in the web app under Settings → Security → Personal access tokens if it should stop working")

			return nil
		},
	}
}

func newWhoamiCommand(f *Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who the stored token authenticates as",
		Long: "Verifies the stored token against the instance and prints the user it belongs\n" +
			"to, plus every organization they are a member of. The organization marked\n" +
			"(default) is the one commands use when --org is not passed.",
		Example: "  deployer whoami\n  deployer whoami --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			printer := f.Printer()

			s, err := f.session()
			if err != nil {
				return err
			}
			s.warnFileToken(printer)

			me, err := s.Client.Me(cmd.Context())
			if err != nil {
				return err
			}

			if f.JSON {
				return printJSON(printer, me)
			}

			printer.Printf("%s <%s>\n", me.Name, me.Email)
			printer.Dim("instance: %s (context %q)", s.Client.BaseURL(), s.Name)

			if len(me.Organizations) == 0 {
				printer.Dim("organizations: none")

				return nil
			}

			rows := make([][]string, 0, len(me.Organizations))
			for _, org := range me.Organizations {
				marker := ""
				if org.ID == s.Context.OrganizationID {
					marker = " (default)"
				}
				rows = append(rows, []string{org.Slug + marker, org.Name, strings.Join(org.Roles, ",")})
			}

			printer.Printf("\n")
			printer.Table([]string{"ORGANIZATION", "NAME", "ROLES"}, rows)

			return nil
		},
	}
}

// readToken takes the token from stdin when piped or asked for, else prompts
// with echo off. Never a flag: argv is visible in `ps` and shell history.
// Prompts go to stderr so `deployer login < token` stays scriptable.
func readToken(forceStdin bool) (string, error) {
	piped := !term.IsTerminal(int(os.Stdin.Fd()))

	if forceStdin || piped {
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", exit.With(exit.Usage, fmt.Errorf("reading token from stdin: %w", err))
			}

			return "", exit.Errorf(exit.Usage, "no token on stdin")
		}

		token := strings.TrimSpace(scanner.Text())
		if token == "" {
			return "", exit.Errorf(exit.Usage, "no token on stdin")
		}

		return token, nil
	}

	fmt.Fprint(os.Stderr, "Personal access token (input hidden): ")

	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)

	if err != nil {
		return "", exit.With(exit.Usage, fmt.Errorf("reading token: %w", err))
	}

	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", exit.Errorf(exit.Usage, "no token entered")
	}

	return token, nil
}

func promptLine(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", nil
	}

	fmt.Fprint(os.Stderr, prompt)

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return "", exit.Errorf(exit.Usage, "no input")
	}

	return strings.TrimSpace(scanner.Text()), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}
