package cli

import (
	"github.com/spf13/cobra"

	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

func newProjectsCommand(f *Factory) *cobra.Command {
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the organization's projects",
		Long: "Lists every project in the organization with the status of its most recent\n" +
			"deployment. The PROJECT column is the slug to pass to `deployer deploy`.",
		Example: "  deployer projects ls\n  deployer projects ls --org acme\n  deployer projects ls --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			printer := f.Printer()

			s, err := f.session()
			if err != nil {
				return err
			}
			s.warnFileToken(printer)

			organization, err := f.organization(cmd.Context(), s)
			if err != nil {
				return err
			}

			projects, err := s.Client.Projects(cmd.Context(), organization)
			if err != nil {
				return err
			}

			if f.JSON {
				return printJSON(printer, projects)
			}

			if len(projects) == 0 {
				printer.Printf("no projects yet\n")

				return nil
			}

			rows := make([][]string, 0, len(projects))
			for _, project := range projects {
				status, when := "—", "—"
				if project.Latest != nil {
					status = printer.Status(project.Latest.Status)
					when = ui.Ago(project.Latest.CreatedAt)
				}

				rows = append(rows, []string{project.Slug, project.Name, project.ProjectType, status, when})
			}

			printer.Table([]string{"PROJECT", "NAME", "TYPE", "LAST DEPLOYMENT", "WHEN"}, rows)

			return nil
		},
	}

	projects := &cobra.Command{
		Use:   "projects [project]",
		Short: "Work with projects",
		Long: "List projects in the selected organization or create a project with an optional repository.\n" +
			"Passing a project slug lists its environments, same as `deployer envs ls <project>`.",
		Example: "  deployer projects ls\n  deployer projects billing-api\n  deployer projects create --name 'Billing API' --type laravel --yes",
		Aliases: []string{"project"},
		Args:    wantArgs(0, 1, "deployer projects <project>   — the project slug, as listed by `deployer projects ls`"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}

			return listEnvironments(cmd, f, args[0])
		},
	}
	projects.AddCommand(list, newProjectsCreateCommand(f))

	return projects
}

func newEnvsCommand(f *Factory) *cobra.Command {
	list := &cobra.Command{
		Use:     "ls <project>",
		Aliases: []string{"list"},
		Short:   "List a project's environments",
		Long: "Lists a project's environments with the branch each tracks and whether it is\n" +
			"locked or gated by approval. The ENVIRONMENT column is the second argument to\n" +
			"`deployer deploy`.",
		Example: "  deployer envs ls billing-api",
		Args:    wantArgs(1, 1, "deployer envs ls <project>   — the project slug, as listed by `deployer projects ls`"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return listEnvironments(cmd, f, args[0])
		},
	}

	envs := &cobra.Command{
		Use:     "envs",
		Short:   "Work with environments",
		Long:    "List a project's environments or create one with servers and an optional copied pipeline.\nUse the project slug shown by `deployer projects ls`.",
		Example: "  deployer envs ls billing-api\n  deployer envs create billing-api",
		Aliases: []string{"env", "environments"},
		Args:    cobra.NoArgs,
	}
	envs.AddCommand(list, newEnvsCreateCommand(f))

	return envs
}

func listEnvironments(cmd *cobra.Command, f *Factory, slug string) error {
	printer := f.Printer()

	s, err := f.session()
	if err != nil {
		return err
	}
	s.warnFileToken(printer)

	organization, err := f.organization(cmd.Context(), s)
	if err != nil {
		return err
	}

	target, err := s.Client.Resolve(cmd.Context(), organization, slug, "")
	if err != nil {
		return explainResolveFailure(printer, err, slug, "")
	}

	environments, err := s.Client.Environments(cmd.Context(), target.Project.ID)
	if err != nil {
		return err
	}

	if f.JSON {
		return printJSON(printer, environments)
	}

	if len(environments) == 0 {
		printer.Printf("%s has no environments yet\n", target.Project.Slug)

		return nil
	}

	rows := make([][]string, 0, len(environments))
	for _, environment := range environments {
		rows = append(rows, []string{environment.Slug, environment.Name, environment.Branch, orDash(environment.URL), lockLabel(environment)})
	}

	printer.Table([]string{"ENVIRONMENT", "NAME", "BRANCH", "URL", "NOTES"}, rows)

	return nil
}
