package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var slugRuns = regexp.MustCompile(`[^a-z0-9]+`)
var phpPattern = regexp.MustCompile(`^\d+\.\d+$`)

func derivedSlug(s string) string {
	return strings.Trim(slugRuns.ReplaceAllString(strings.ToLower(s), "-"), "-")
}
func required(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("a value is required")
	}
	return nil
}
func slugValid(s string) error {
	if !slugPattern.MatchString(s) {
		return fmt.Errorf("use lowercase letters and numbers separated by single hyphens")
	}
	return nil
}
func pathValid(s string) error {
	if s != "" && !strings.HasPrefix(s, "/") {
		return fmt.Errorf("must start with /")
	}
	return nil
}
func urlValid(s string) error {
	if s == "" {
		return nil
	}
	u, e := url.Parse(s)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("must be an http(s) URL")
	}
	return nil
}
func maxLength(n int) func(string) error {
	return func(s string) error {
		if utf8.RuneCountInString(s) > n {
			return fmt.Errorf("must be at most %d characters", n)
		}
		return nil
	}
}

type intakeField struct {
	key, label string
	value      *string
	def        func() string
	validate   func(string) error
	options    []choice
	secret     bool
}

func (f intakeField) read(p *prompter) error {
	def := *f.value
	if def == "" && f.def != nil {
		def = f.def()
	}
	var v string
	var err error
	if len(f.options) > 0 {
		v, err = p.choose(f.label, f.options, def)
	} else if f.secret {
		v, err = p.askSecret(f.label)
		if v == "" {
			v = *f.value
		}
	} else {
		v, err = p.ask(f.label, def, f.validate)
	}
	if err == nil {
		*f.value = v
	}
	return err
}
func intake(p *prompter, fields []intakeField) error {
	for _, f := range fields {
		if p != nil {
			if err := f.read(p); err != nil {
				return err
			}
		} else {
			if *f.value == "" && f.def != nil {
				*f.value = f.def()
			}
			if f.validate != nil {
				if err := f.validate(*f.value); err != nil {
					return exit.Errorf(exit.Usage, "--%s: %v", strings.ReplaceAll(f.key, "_", "-"), err)
				}
			}
			if len(f.options) > 0 {
				v, err := selectChoice(*f.value, f.options)
				if err != nil {
					return exit.With(exit.Usage, err)
				}
				*f.value = v
			}
		}
	}
	return nil
}

// submit retries only fields named by validation errors; unrelated errors stay API errors.
func submit(p *prompter, fields []intakeField, title string, post func() error, abortMessage ...string) (bool, error) {
	for {
		if p != nil {
			rows := [][]string{}
			for _, f := range fields {
				v := *f.value
				// Pick-lists store ids; the operator chose by name.
				for _, o := range f.options {
					if o.value == v {
						v = o.label
					}
				}
				if f.secret {
					v = "not set"
					if *f.value != "" {
						v = "set"
					}
				}
				// "(optional)" is a prompt hint; in the summary it is noise.
				rows = append(rows, []string{strings.TrimSuffix(f.label, " (optional)"), v})
			}
			summary := *p.printer
			summary.Out = p.out
			summary.Table(nil, rows)
			yes, err := p.confirm(title)
			if err != nil {
				return false, err
			}
			if !yes {
				message := "aborted, nothing created"
				if len(abortMessage) > 0 {
					message = abortMessage[0]
				}
				fmt.Fprintln(p.out, message)
				return false, nil
			}
		}
		err := post()
		if err == nil {
			return true, nil
		}
		var problem *api.Problem
		if p == nil || !errors.As(err, &problem) || problem.Status != 422 || len(problem.Errors) == 0 {
			return false, err
		}
		byKey := map[string]int{}
		for i, f := range fields {
			byKey[f.key] = i
		}
		keys := []string{}
		for k := range problem.Errors {
			if _, ok := byKey[k]; !ok {
				return false, err
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			f := fields[byKey[k]]
			for _, m := range problem.Errors[k] {
				p.failure(fmt.Errorf("%s: %s", f.label, m))
			}
			if err := f.read(p); err != nil {
				return false, err
			}
		}
	}
}
func choices(values ...string) []choice {
	out := []choice{}
	for _, v := range values {
		out = append(out, choice{v, v})
	}
	return out
}
func resolveChoice(value string, options []choice) (string, error) {
	v, e := exactChoice(value, options)
	if e != nil {
		return "", exit.With(exit.Usage, e)
	}
	return v, nil
}

func exactChoice(value string, options []choice) (string, error) {
	for _, option := range options {
		if value == option.value || value == option.label {
			return option.value, nil
		}
	}
	return "", fmt.Errorf("unknown value %q (valid values: %s)", value, choiceLabels(options))
}

type repositoryOptions struct{ api.CreateRepositoryOptions }

func repositoryFields(p *prompter, o *repositoryOptions) ([]intakeField, error) {
	fields := []intakeField{{key: "repo_url", label: "Repository URL (optional)", value: &o.RepoURL, validate: maxLength(512)}}
	if err := intake(p, fields); err != nil {
		return nil, err
	}
	if o.RepoURL == "" {
		return fields, nil
	}
	provider := o.Provider
	if provider == "" {
		host := o.RepoURL
		u, _ := url.Parse(host)
		if u != nil && u.Hostname() != "" {
			host = u.Hostname()
		} else if strings.Contains(host, "@") {
			host = strings.SplitN(strings.SplitN(host, "@", 2)[1], ":", 2)[0]
		}
		provider = map[string]string{"github.com": "github", "gitlab.com": "gitlab", "bitbucket.org": "bitbucket"}[host]
	}
	if o.Provider == "" {
		o.Provider = provider
	}
	if p == nil && o.Provider == "" {
		return nil, exit.Errorf(exit.Usage, "missing required flag(s): --provider (github, gitlab, bitbucket, gitea, generic)")
	}
	next := []intakeField{{key: "provider", label: "Provider", value: &o.Provider, options: choices("github", "gitlab", "bitbucket", "gitea", "generic")}, {key: "default_branch", label: "Default branch", value: &o.DefaultBranch, def: func() string { return "main" }}}
	// Recognized hosts need no provider question; explicit flags still validate.
	if provider != "" {
		if _, err := resolveChoice(o.Provider, next[0].options); err != nil {
			return nil, err
		}
		fields = append(fields, next[0])
		next = next[1:]
	}
	if strings.HasPrefix(o.RepoURL, "https://") {
		next = append(next, intakeField{key: "clone_token", label: "Clone token (optional)", value: &o.CloneToken, secret: true}, intakeField{key: "clone_username", label: "Clone username (optional)", value: &o.CloneUsername})
	} else if o.CloneToken != "" || o.CloneUsername != "" {
		return nil, exit.Errorf(exit.Usage, "--clone-token and --clone-username require an https:// repository URL")
	}
	if err := intake(p, next); err != nil {
		return nil, err
	}
	return append(fields, next...), nil
}
func repositoryHint(printer *ui.Printer, r *api.Repository) {
	if r != nil && r.CloneAuth == "deploy_key" && r.DeployKey != nil && r.DeployKey.PublicKey != "" {
		printer.Printf("%s\n", r.DeployKey.PublicKey)
		printer.Dim("add this as a read-only deploy key on %s", r.Provider)
	}
}

type projectCreateOptions struct {
	api.CreateProjectOptions
	repositoryOptions
	Group string
	JSON  bool
}

func createProject(ctx context.Context, c *api.Client, printer *ui.Printer, p *prompter, org string, o projectCreateOptions) error {
	if p == nil && o.Name == "" {
		return exit.Errorf(exit.Usage, "missing required flag(s): --name")
	}
	groups, err := c.ProjectGroups(ctx, org)
	if err != nil {
		return err
	}
	groupChoices := choices("none")
	for _, g := range groups {
		groupChoices = append(groupChoices, choice{g.ID, g.Name})
	}
	if o.Group != "" {
		o.ProjectGroupID, err = resolveChoice(o.Group, groupChoices)
		if err != nil {
			return err
		}
	}
	fields := []intakeField{{"name", "Name", &o.Name, nil, func(s string) error {
		if e := required(s); e != nil {
			return e
		}
		return maxLength(255)(s)
	}, nil, false}, {"slug", "Slug", &o.Slug, func() string { return derivedSlug(o.Name) }, slugValid, nil, false}, {"description", "Description (optional)", &o.Description, nil, maxLength(2000), nil, false}, {"project_type", "Type", &o.ProjectType, func() string { return "generic" }, nil, choices("laravel", "php", "node", "static", "generic"), false}}
	if len(groups) > 0 || o.Group != "" {
		fields = append(fields, intakeField{key: "project_group_id", label: "Group", value: &o.ProjectGroupID, def: func() string { return "none" }, options: groupChoices})
	}
	if err = intake(p, fields); err != nil {
		return err
	}
	more := []intakeField{}
	if o.ProjectType == "laravel" || o.ProjectType == "php" {
		more = append(more, intakeField{key: "php_version", label: "PHP version (optional)", value: &o.PHPVersion, validate: func(s string) error {
			if s != "" && !phpPattern.MatchString(s) {
				return fmt.Errorf("use major.minor, for example 8.3")
			}
			return nil
		}})
	}
	more = append(more, intakeField{key: "base_path", label: "Base path (optional)", value: &o.BasePath, validate: pathValid})
	if err = intake(p, more); err != nil {
		return err
	}
	fields = append(fields, more...)
	repoFields, err := repositoryFields(p, &o.repositoryOptions)
	if err != nil {
		return err
	}
	var project *api.Project
	ok, err := submit(p, append(fields, repoFields...), fmt.Sprintf("Create project %q?", o.Name), func() error {
		request := o.CreateProjectOptions
		if request.ProjectGroupID == "none" {
			request.ProjectGroupID = ""
		}
		var e error
		project, e = c.CreateProject(ctx, org, request)
		return e
	})
	if err != nil || !ok {
		return err
	}
	var repo *api.Repository
	if o.RepoURL != "" {
		var repoOK bool
		repoOK, err = submitAfterConfirmation(p, repoFields, "Create repository?", func() error {
			var e error
			repo, e = c.CreateRepository(ctx, project.ID, o.CreateRepositoryOptions)
			return e
		}, fmt.Sprintf("project WAS created (%s, %s); finish in the web app: Project > Repositories", project.Slug, project.ID))
		if !repoOK && err == nil {
			err = exit.Errorf(exit.Usage, "repository creation aborted")
		}
		if err != nil {
			return fmt.Errorf("project WAS created (%s, %s); repository creation failed: %w; finish in the web app: Project > Repositories", project.Slug, project.ID, err)
		}
	}
	if o.JSON {
		return printJSON(printer, map[string]any{"project": project, "repository": repo})
	}
	printer.Success("created project %s", project.Name)
	printer.Dim("slug: %s", project.Slug)
	printer.Dim("id: %s", project.ID)
	printer.Dim("url: %s/projects/%s", c.BaseURL(), project.ID)
	repositoryHint(printer, repo)
	printer.Dim("next: deployer envs create %s", project.Slug)
	return nil
}

type environmentCreateOptions struct {
	api.CreateEnvironmentOptions
	repositoryOptions
	Repo         string
	Servers      []string
	PipelineFrom string
	JSON         bool
}

func createEnvironment(ctx context.Context, c *api.Client, printer *ui.Printer, p *prompter, org string, project api.Project, o environmentCreateOptions) error {
	if p == nil && o.Name == "" {
		return exit.Errorf(exit.Usage, "missing required flag(s): --name")
	}
	if o.ReleaseRetention < 0 || o.ReleaseRetention > 100 {
		return exit.Errorf(exit.Usage, "--retention must be between 1 and 100")
	}
	if err := pathValid(o.BasePath); err != nil {
		return exit.With(exit.Usage, err)
	}
	fields := []intakeField{{key: "name", label: "Name", value: &o.Name, validate: required}, {key: "slug", label: "Slug", value: &o.Slug, def: func() string { return derivedSlug(o.Name) }, validate: slugValid}}
	if err := intake(p, fields); err != nil {
		return err
	}
	repos, err := c.Repositories(ctx, project.ID)
	if err != nil {
		return err
	}
	var pendingRepo bool
	var repoFields []intakeField
	if o.RepoURL != "" || len(repos) == 0 {
		if p == nil && o.RepoURL == "" {
			return exit.Errorf(exit.Usage, "project has no repositories: supply --repo-url and --provider (optional: --default-branch, --clone-token, --clone-username)")
		}
		repoFields, err = repositoryFields(p, &o.repositoryOptions)
		if err != nil {
			return err
		}
		if o.RepoURL == "" {
			return exit.Errorf(exit.Usage, "a repository is required: supply --repo-url and --provider")
		}
		pendingRepo = true
	} else {
		options := []choice{}
		for _, r := range repos {
			options = append(options, choice{r.ID, r.RepoURL})
		}
		if o.Repo != "" {
			o.RepositoryID, err = resolveChoice(o.Repo, options)
			if err != nil {
				return err
			}
		} else if len(repos) == 1 {
			o.RepositoryID = repos[0].ID
			if p != nil {
				fmt.Fprintf(p.out, "Repository: %s (auto-selected)\n\n", repos[0].RepoURL)
			}
		} else if p == nil {
			return exit.Errorf(exit.Usage, "missing required flag(s): --repo (valid values: %s)", choiceLabels(options))
		}
		rf := intakeField{key: "repository_id", label: "Repository", value: &o.RepositoryID, options: options}
		if len(repos) > 1 {
			if err = intake(p, []intakeField{rf}); err != nil {
				return err
			}
		}
		fields = append(fields, rf)
	}
	branchDefault := "main"
	if pendingRepo && o.DefaultBranch != "" {
		branchDefault = o.DefaultBranch
	}
	for _, r := range repos {
		if r.ID == o.RepositoryID && r.DefaultBranch != "" {
			branchDefault = r.DefaultBranch
		}
	}
	more := []intakeField{{key: "branch", label: "Branch", value: &o.Branch, def: func() string { return branchDefault }}, {key: "url", label: "URL (optional)", value: &o.URL, validate: urlValid}}
	if err = intake(p, more); err != nil {
		return err
	}
	fields = append(fields, more...)
	servers, err := c.Servers(ctx, org)
	if err != nil {
		return err
	}
	serverChoices := []choice{}
	for _, s := range servers {
		serverChoices = append(serverChoices, choice{s.ID, s.Name})
	}
	selected := []string{}
	for _, s := range o.Servers {
		v, e := resolveChoice(s, serverChoices)
		if e != nil {
			return e
		}
		if !contains(selected, v) {
			selected = append(selected, v)
		}
	}
	// Servers given as flags are the answer; prompt only when none were.
	if p != nil && len(o.Servers) == 0 && len(serverChoices) > 0 {
		selected, err = p.chooseMany("Servers", serverChoices)
		if err != nil {
			return err
		}
	}
	envs, err := c.Environments(ctx, project.ID)
	if err != nil {
		return err
	}
	envChoices := choices("none")
	for _, e := range envs {
		envChoices = append(envChoices, choice{e.Slug, e.Slug})
	}
	source := o.PipelineFrom
	if source == "" {
		source = "none"
	}
	if len(envs) > 0 || o.PipelineFrom != "" {
		if p != nil {
			source, err = p.choose("Pipeline from", envChoices, source)
		} else {
			source, err = resolveChoice(source, envChoices)
		}
		if err != nil {
			return err
		}
	}
	var pipelineID string
	if source != "none" {
		for _, e := range envs {
			if e.Slug == source {
				pipelines, e2 := c.Pipelines(ctx, e.ID)
				if e2 != nil {
					return e2
				}
				for _, pipeline := range pipelines {
					if pipeline.IsActive {
						pipelineID = pipeline.ID
						break
					}
				}
			}
		}
		if pipelineID == "" {
			return exit.Errorf(exit.Usage, "environment %q has no active pipeline", source)
		}
	}
	approval, auto := "no", "no"
	if o.RequiresApproval {
		approval = "yes"
	}
	if o.AutoDeploy {
		auto = "yes"
	}
	boolValid := func(s string) error {
		switch strings.ToLower(s) {
		case "y", "yes", "n", "no":
			return nil
		}
		return fmt.Errorf("enter y or n")
	}
	bools := []intakeField{{key: "requires_approval", label: "Requires approval (y/n)", value: &approval, validate: boolValid}, {key: "auto_deploy", label: "Auto deploy (y/n)", value: &auto, validate: boolValid}}
	if err = intake(p, bools); err != nil {
		return err
	}
	// Normalise y/n so the summary reads the same whatever was typed.
	for _, v := range []*string{&approval, &auto} {
		*v = map[bool]string{true: "yes", false: "no"}[strings.HasPrefix(strings.ToLower(*v), "y")]
	}
	fields = append(fields, bools...)
	// Flag-only fields are included for review, but never become interactive questions.
	retention := fmt.Sprint(o.ReleaseRetention)
	if o.ReleaseRetention == 0 {
		retention = "5"
	}
	summaryFields := append(append([]intakeField{}, fields...), repoFields...)
	serverNames := []string{}
	for _, id := range selected {
		for _, s := range servers {
			if s.ID == id {
				serverNames = append(serverNames, s.Name)
			}
		}
	}
	serverSummary := strings.Join(serverNames, ", ")
	summaryFields = append(summaryFields, intakeField{label: "Servers", value: &serverSummary}, intakeField{label: "Pipeline from", value: &source}, intakeField{label: "Base path", value: &o.BasePath}, intakeField{label: "Retention", value: &retention})
	var env *api.Environment
	var repo *api.Repository
	// Confirm the complete operation before creating either resource.
	ok, err := submit(p, summaryFields, fmt.Sprintf("Create environment %q?", o.Name), func() error {
		if pendingRepo {
			var e error
			repo, e = c.CreateRepository(ctx, project.ID, o.CreateRepositoryOptions)
			if e != nil {
				return e
			}
			if !o.JSON {
				repositoryHint(printer, repo)
			}
			o.RepositoryID = repo.ID
			pendingRepo = false
		}
		o.RequiresApproval = strings.EqualFold(approval, "yes") || strings.EqualFold(approval, "y")
		o.AutoDeploy = strings.EqualFold(auto, "yes") || strings.EqualFold(auto, "y")
		var e error
		env, e = c.CreateEnvironment(ctx, project.ID, o.CreateEnvironmentOptions)
		return e
	})
	if err != nil || !ok {
		if repo != nil {
			if err == nil {
				err = exit.Errorf(exit.Usage, "environment creation aborted")
			}
			return fmt.Errorf("repository WAS created (%s); environment was not created: %w; finish in the web app: Project > Environments", repo.ID, err)
		}
		return err
	}
	attachments := []api.ServerAttachment{}
	for i, id := range selected {
		attachments = append(attachments, api.ServerAttachment{ServerID: id, IsPrimary: i == 0})
	}
	finish := func(step string, e error) error {
		return fmt.Errorf("environment WAS created (%s, %s); %s failed: %w; finish in the web app: Project > Environments > %s", env.Slug, env.ID, step, e, env.Name)
	}
	if len(attachments) > 0 {
		if err = c.SyncEnvironmentServers(ctx, env.ID, attachments); err != nil {
			return finish("server attachment", err)
		}
	}
	var pipeline *api.Pipeline
	if pipelineID != "" {
		pipeline, err = c.CopyPipeline(ctx, pipelineID, api.CopyPipelineOptions{EnvironmentID: env.ID})
		if err != nil {
			return finish("pipeline copy", err)
		}
	}
	if o.JSON {
		return printJSON(printer, map[string]any{"environment": env, "servers": attachments, "pipeline": pipeline})
	}
	printer.Success("created environment %s in %s", env.Name, project.Name)
	printer.Dim("slug: %s", env.Slug)
	printer.Dim("id: %s", env.ID)
	printer.Dim("branch: %s", env.Branch)
	printer.Dim("url: %s/projects/%s/environments/%s", c.BaseURL(), project.ID, env.ID)
	printer.Dim("servers attached: %d", len(attachments))
	if pipeline != nil {
		printer.Dim("pipeline copied: %s", pipeline.Name)
	}
	printer.Dim("next: deployer deploy %s %s", project.Slug, env.Slug)
	return nil
}
func contains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}
func choiceLabels(options []choice) string {
	v := []string{}
	for _, o := range options {
		v = append(v, o.label+" ("+o.value+")")
	}
	return strings.Join(v, ", ")
}

func addRepositoryFlags(cmd *cobra.Command, o *repositoryOptions) {
	f := cmd.Flags()
	f.StringVar(&o.RepoURL, "repo-url", "", "repository clone URL (optional)")
	f.StringVar(&o.Provider, "provider", "", "github, gitlab, bitbucket, gitea or generic")
	f.StringVar(&o.DefaultBranch, "default-branch", "", "repository default branch (default main)")
	f.StringVar(&o.CloneToken, "clone-token", "", "HTTPS clone token (prefer DEPLOYER_CLONE_TOKEN)")
	f.StringVar(&o.CloneUsername, "clone-username", "", "HTTPS clone username (optional)")
}
func commandPrompter(cmd *cobra.Command, printer *ui.Printer, yes, noInput bool) *prompter {
	file, ok := cmd.InOrStdin().(*os.File)
	if yes || noInput || !ok || !term.IsTerminal(int(file.Fd())) {
		return nil
	}
	return &prompter{bufio.NewReader(file), cmd.ErrOrStderr(), printer, true}
}
func newProjectsCreateCommand(f *Factory) *cobra.Command {
	var o projectCreateOptions
	var yes, noInput bool
	cmd := &cobra.Command{Use: "create", Aliases: []string{"add", "new"}, Short: "Create a project and optional repository", Long: "Create a project with guided intake on a terminal. Use --yes or --no-input\nto apply flags and defaults without prompts or confirmation.", Example: "  deployer projects create\n  deployer projects create --name 'Billing API' --type laravel --yes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		printer := f.Printer()
		s, err := f.session()
		if err != nil {
			return err
		}
		s.warnFileToken(printer)
		org, err := f.organization(cmd.Context(), s)
		if err != nil {
			return err
		}
		o.JSON = f.JSON
		if o.CloneToken == "" {
			o.CloneToken = os.Getenv("DEPLOYER_CLONE_TOKEN")
		}
		return createProject(cmd.Context(), s.Client, printer, commandPrompter(cmd, printer, yes, noInput), org, o)
	}}
	flags := cmd.Flags()
	flags.StringVar(&o.Name, "name", "", "project name (required)")
	flags.StringVar(&o.Slug, "slug", "", "project slug (default derived from name)")
	flags.StringVar(&o.Description, "description", "", "description (optional)")
	flags.StringVar(&o.ProjectType, "type", "", "laravel, php, node, static or generic (default generic)")
	flags.StringVar(&o.Group, "group", "", "project group name or id")
	flags.StringVar(&o.PHPVersion, "php-version", "", "PHP major.minor version (optional)")
	flags.StringVar(&o.BasePath, "base-path", "", "absolute base path (optional)")
	flags.BoolVarP(&yes, "yes", "y", false, "create without prompts or confirmation")
	flags.BoolVar(&noInput, "no-input", false, "never prompt")
	addRepositoryFlags(cmd, &o.repositoryOptions)
	return cmd
}
func newEnvsCreateCommand(f *Factory) *cobra.Command {
	var o environmentCreateOptions
	var yes, noInput bool
	cmd := &cobra.Command{Use: "create <project>", Aliases: []string{"add", "new"}, Short: "Create an environment in a project", Long: "Create an environment, attach servers and optionally copy an active pipeline.\nIntake prompts appear only on a terminal; --yes and --no-input use flags and defaults.", Example: "  deployer envs create billing-api\n  deployer envs create billing-api --name Production --server web-1 --yes", Args: wantArgs(1, 1, "deployer envs create <project> — the project slug, as listed by `deployer projects ls`"), RunE: func(cmd *cobra.Command, args []string) error {
		printer := f.Printer()
		s, err := f.session()
		if err != nil {
			return err
		}
		s.warnFileToken(printer)
		org, err := f.organization(cmd.Context(), s)
		if err != nil {
			return err
		}
		target, err := s.Client.Resolve(cmd.Context(), org, args[0], "")
		if err != nil {
			return explainResolveFailure(printer, err, args[0], "")
		}
		o.JSON = f.JSON
		if o.CloneToken == "" {
			o.CloneToken = os.Getenv("DEPLOYER_CLONE_TOKEN")
		}
		if cmd.Flags().Changed("retention") && o.ReleaseRetention < 1 {
			return exit.Errorf(exit.Usage, "--retention must be between 1 and 100")
		}
		return createEnvironment(cmd.Context(), s.Client, printer, commandPrompter(cmd, printer, yes, noInput), org, api.Project{ID: target.Project.ID, Name: target.Project.Name, Slug: target.Project.Slug}, o)
	}}
	flags := cmd.Flags()
	flags.StringVar(&o.Name, "name", "", "environment name (required)")
	flags.StringVar(&o.Slug, "slug", "", "environment slug (default derived from name)")
	flags.StringVar(&o.Repo, "repo", "", "repository id or URL")
	flags.StringVar(&o.Branch, "branch", "", "branch (default repository's default branch)")
	flags.StringVar(&o.URL, "url", "", "http(s) application URL (optional)")
	flags.StringSliceVar(&o.Servers, "server", nil, "server name or id (repeatable)")
	flags.StringVar(&o.PipelineFrom, "pipeline-from", "", "environment slug whose active pipeline to copy")
	flags.BoolVar(&o.RequiresApproval, "requires-approval", false, "require deployment approval")
	flags.BoolVar(&o.AutoDeploy, "auto-deploy", false, "enable automatic deployments")
	flags.StringVar(&o.BasePath, "base-path", "", "absolute base path (optional)")
	flags.IntVar(&o.ReleaseRetention, "retention", 0, "releases to retain, 1..100 (default 5)")
	flags.BoolVarP(&yes, "yes", "y", false, "create without prompts or confirmation")
	flags.BoolVar(&noInput, "no-input", false, "never prompt")
	addRepositoryFlags(cmd, &o.repositoryOptions)
	return cmd
}

// The first confirmation covers the repository too; a second review is needed
// only if the API rejects a repository field after the project was created.
func submitAfterConfirmation(p *prompter, fields []intakeField, title string, post func() error, abort string) (bool, error) {
	err := post()
	if err == nil {
		return true, nil
	}
	var problem *api.Problem
	if p == nil || !errors.As(err, &problem) || problem.Status != 422 || len(problem.Errors) == 0 {
		return false, err
	}
	matching := map[string]int{}
	for i, f := range fields {
		matching[f.key] = i
	}
	for k := range problem.Errors {
		if _, ok := matching[k]; !ok {
			return false, err
		}
	}
	for _, f := range fields {
		if messages, ok := problem.Errors[f.key]; ok {
			for _, m := range messages {
				p.failure(fmt.Errorf("%s: %s", f.label, m))
			}
			if e := f.read(p); e != nil {
				return false, e
			}
		}
	}
	return submit(p, fields, title, post, abort)
}
