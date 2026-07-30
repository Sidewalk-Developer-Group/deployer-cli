// Package cli wires the cobra command tree. Commands stay thin: resolve input,
// call one API method, render. Anything cleverer belongs on the server, where it
// is audited and tested (docs/CLI_DESIGN.md).
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/config"
	"github.com/sidewalk-developer-group/deployer-cli/internal/credentials"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

// Factory holds global flags and builds the collaborators each command needs.
type Factory struct {
	Version string
	Commit  string

	ContextName string
	APIURL      string
	Org         string
	JSON        bool
	Color       bool
	Quiet       bool
	Debug       bool
	Timeout     time.Duration

	printer *ui.Printer
}

func (f *Factory) Printer() *ui.Printer {
	if f.printer == nil {
		f.printer = ui.New(os.Stdout, os.Stderr, f.Color)
		f.printer.Quiet = f.Quiet
	}

	return f.printer
}

// session is a resolved context plus a client authenticated for it.
type session struct {
	Name    string
	Context *config.Context
	Config  *config.File
	Client  *api.Client
	Store   *credentials.Store
	// usedFileToken records that the token came from the plaintext fallback,
	// so the command can warn once.
	usedFileToken bool
}

// resolvedAPIURL applies the documented precedence: flag, env, context file.
func (f *Factory) resolvedAPIURL(ctx *config.Context) string {
	if f.APIURL != "" {
		return f.APIURL
	}

	if fromEnv := os.Getenv("DEPLOYER_API_URL"); fromEnv != "" {
		return fromEnv
	}

	if ctx != nil {
		return ctx.APIURL
	}

	return ""
}

// session builds an authenticated client. A missing token is exit 3, not a
// generic failure: CI needs to tell "not logged in" apart from "deploy failed".
func (f *Factory) session() (*session, error) {
	file, err := config.Load()
	if err != nil {
		return nil, exit.With(exit.Usage, err)
	}

	name, contextConfig, err := file.Resolve(f.ContextName)
	if err != nil && !errors.Is(err, config.ErrNoContext) {
		return nil, exit.With(exit.Usage, err)
	}

	apiURL := f.resolvedAPIURL(contextConfig)
	if apiURL == "" {
		return nil, exit.Errorf(exit.Auth, "not logged in: run `deployer login --api https://deployer.example.com`")
	}

	dir, err := config.Dir()
	if err != nil {
		return nil, exit.With(exit.Usage, err)
	}
	store := credentials.New(dir)

	token := os.Getenv("DEPLOYER_TOKEN")
	usedFile := false

	if token == "" && name != "" {
		stored, fromFile, err := store.Get(name)
		if err != nil && !errors.Is(err, credentials.ErrNotFound) {
			return nil, exit.With(exit.Auth, err)
		}
		token, usedFile = stored, fromFile
	}

	if token == "" {
		return nil, exit.Errorf(exit.Auth, "no credentials for %s: run `deployer login` or set DEPLOYER_TOKEN", displayName(name, apiURL))
	}

	opts := []api.Option{api.WithTimeout(f.Timeout)}
	if f.Debug {
		opts = append(opts, api.WithDebug(os.Stderr))
	}

	client, err := api.New(apiURL, token, f.Version, opts...)
	if err != nil {
		return nil, exit.With(exit.Usage, err)
	}

	if contextConfig == nil {
		contextConfig = &config.Context{APIURL: apiURL}
	}

	return &session{
		Name:          name,
		Context:       contextConfig,
		Config:        file,
		Client:        client,
		Store:         store,
		usedFileToken: usedFile,
	}, nil
}

func displayName(name, apiURL string) string {
	if name != "" {
		return name
	}

	return apiURL
}

// organization resolves the org to act in and returns its public id. Slugs are
// what operators type; the API binds UUIDs, so the mapping is cached in the
// context after the first lookup (docs/CLI_DESIGN.md §3.2).
func (f *Factory) organization(ctx context.Context, s *session) (string, error) {
	wanted := f.Org
	if wanted == "" {
		wanted = os.Getenv("DEPLOYER_ORG")
	}
	if wanted == "" {
		wanted = s.Context.Organization
	}

	if s.Context.OrganizationID != "" && (wanted == "" || wanted == s.Context.Organization || wanted == s.Context.OrganizationID) {
		return s.Context.OrganizationID, nil
	}

	me, err := s.Client.Me(ctx)
	if err != nil {
		return "", err
	}

	if len(me.Organizations) == 0 {
		return "", exit.Errorf(exit.Usage, "%s belongs to no organization on this instance", me.Email)
	}

	if wanted == "" {
		if len(me.Organizations) > 1 {
			return "", exit.Errorf(
				exit.Usage,
				"several organizations available (%s): pass --org <slug> or run `deployer context use` after `deployer login`",
				strings.Join(orgSlugs(me), ", "),
			)
		}

		wanted = me.Organizations[0].Slug
	}

	for _, org := range me.Organizations {
		if org.Slug == wanted || org.ID == wanted {
			f.cacheOrganization(s, org.Slug, org.ID)

			return org.ID, nil
		}
	}

	return "", exit.Errorf(exit.Usage, "unknown organization %q (have: %s)", wanted, strings.Join(orgSlugs(me), ", "))
}

func orgSlugs(me *api.User) []string {
	slugs := make([]string, 0, len(me.Organizations))
	for _, org := range me.Organizations {
		slugs = append(slugs, org.Slug)
	}

	return slugs
}

// cacheOrganization persists the slug -> id mapping. Best effort: a read-only
// config dir must not break a deploy.
func (f *Factory) cacheOrganization(s *session, slug, id string) {
	if s.Name == "" || s.Config == nil {
		return
	}

	if s.Context.Organization == slug && s.Context.OrganizationID == id {
		return
	}

	s.Context.Organization = slug
	s.Context.OrganizationID = id
	s.Config.Set(s.Name, s.Context)

	if err := s.Config.Save(); err != nil && f.Debug {
		fmt.Fprintf(os.Stderr, "warning: could not cache organization: %v\n", err)
	}
}

// warnFileToken tells the operator their token is on disk in plaintext, once
// per command. Silence would be the wrong default for a deploy credential.
func (s *session) warnFileToken(printer *ui.Printer) {
	if s.usedFileToken {
		printer.Warn("token read from the plaintext fallback file — no OS keyring available on this machine")
	}
}

// NewRootCommand builds the command tree.
func NewRootCommand(version, commit string) *cobra.Command {
	factory := &Factory{Version: version, Commit: commit, Color: true}

	root := &cobra.Command{
		Use:   "deployer",
		Short: "Deploy to your own servers from the terminal",
		Long: "deployer is the command-line client for a Deployer instance.\n\n" +
			"It triggers deployments, streams their logs and reports their outcome as an\n" +
			"exit code, so the same command works at a prompt and in CI.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Without this, a typo like `deployer deply` prints help and exits 0.
		Args: cobra.NoArgs,
	}

	flags := root.PersistentFlags()
	flags.StringVar(&factory.ContextName, "context", "", "named instance to use (default: the current context)")
	flags.StringVar(&factory.APIURL, "api", "", "instance URL, overriding the context (e.g. https://deployer.example.com)")
	flags.StringVar(&factory.Org, "org", "", "organization slug to act in")
	flags.BoolVar(&factory.JSON, "json", false, "machine-readable output")
	flags.BoolVar(&factory.Quiet, "quiet", false, "suppress commentary; keep payload and errors")
	flags.BoolVar(&factory.Debug, "debug", false, "log requests to stderr (never the token)")
	flags.DurationVar(&factory.Timeout, "timeout", 30*time.Second, "per-request timeout")

	var noColor bool
	flags.BoolVar(&noColor, "no-color", false, "disable colour output")
	cobra.OnInitialize(func() { factory.Color = !noColor })

	root.AddCommand(
		newLoginCommand(factory),
		newLogoutCommand(factory),
		newWhoamiCommand(factory),
		newContextCommand(factory),
		newDeployCommand(factory),
		newStatusCommand(factory),
		newLogsCommand(factory),
		newProjectsCommand(factory),
		newEnvsCommand(factory),
		newVersionCommand(factory),
	)
	root.AddCommand(newActionCommands(factory)...)

	return root
}

// newAnonymousClient is for the unauthenticated endpoints (cli/version): no
// token is read, so it works before `deployer login`.
func newAnonymousClient(apiURL, version string) (*api.Client, error) {
	return api.New(apiURL, "", version)
}
