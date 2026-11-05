package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/sidewalk-developer-group/deployer-cli/internal/follow"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

func newStatusCommand(f *Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "status <project> [environment]",
		Short: "Show the last deployment of an environment, or of every environment",
		Long: "With an environment, prints its most recent deployment. Without one, prints\n" +
			"every environment of the project and where each one stands.",
		Example: "  deployer status billing-api\n  deployer status billing-api production",
		Args:    wantArgs(1, 2, "deployer status <project> [environment]   — omit the environment to see every environment of the project"),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			environmentSlug := ""
			if len(args) == 2 {
				environmentSlug = args[1]
			}

			target, err := s.Client.Resolve(cmd.Context(), organization, args[0], environmentSlug)
			if err != nil {
				return explainResolveFailure(printer, err, args[0], environmentSlug)
			}

			if target.Environment != nil {
				deployment, err := s.Client.LatestDeployment(cmd.Context(), target.Environment.ID)
				if err != nil {
					return err
				}

				if f.JSON {
					return printJSON(printer, deployment)
				}

				if deployment == nil {
					printer.Printf("%s → %s: never deployed\n", target.Project.Slug, target.Environment.Slug)

					return nil
				}

				printDeployment(printer, target.Project.Slug+" → "+target.Environment.Slug, deployment)

				return nil
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

			// The environments listing carries no deployment summary, so ask per
			// environment. One request each is the price of not printing "never
			// deployed" over an environment that has in fact been deployed.
			rows := make([][]string, 0, len(environments))
			for _, environment := range environments {
				status, when := "never deployed", "—"

				latest, err := s.Client.LatestDeployment(cmd.Context(), environment.ID)
				if err != nil {
					return err
				}

				if latest != nil {
					status = printer.Status(latest.Status)
					when = ui.Ago(firstTimestamp(latest.FinishedAt, latest.CreatedAt))
				}

				rows = append(rows, []string{environment.Slug, environment.Branch, status, when, lockLabel(environment)})
			}

			printer.Table([]string{"ENVIRONMENT", "BRANCH", "LAST DEPLOYMENT", "WHEN", "NOTES"}, rows)

			return nil
		},
	}
}

func lockLabel(environment api.Environment) string {
	switch {
	case environment.IsLocked && environment.RequiresApprove:
		return "locked, approval required"
	case environment.IsLocked:
		return "locked"
	case environment.RequiresApprove:
		return "approval required"
	default:
		return ""
	}
}

func printDeployment(printer *ui.Printer, label string, deployment *api.Deployment) {
	printer.Printf("%s  %s\n", label, printer.Status(deployment.Status))

	rows := [][]string{
		{"deployment", deployment.ID},
		{"branch", orDash(deployment.Branch)},
		{"commit", orDash(ui.ShortSHA(deployment.CommitSHA))},
		{"trigger", orDash(deployment.TriggerSrc)},
		{"duration", ui.Duration(deployment.DurationMS)},
		{"finished", ui.Ago(deployment.FinishedAt)},
	}

	if deployment.Release != nil && deployment.Release.ReleaseRef != "" {
		rows = append(rows, []string{"release", deployment.Release.ReleaseRef})
	}

	if deployment.TriggeredBy != nil && deployment.TriggeredBy.Name != "" {
		rows = append(rows, []string{"by", deployment.TriggeredBy.Name})
	}

	if deployment.ErrorSummary != "" {
		rows = append(rows, []string{"error", deployment.ErrorSummary})
	}

	printer.Table(nil, rows)
}

func newLogsCommand(f *Factory) *cobra.Command {
	var followLog bool

	cmd := &cobra.Command{
		Use:   "logs <deployment-id | project environment>",
		Short: "Print a deployment's log",
		Long: "Prints the log of a deployment by id, or of the most recent deployment of an\n" +
			"environment. With --follow, keeps streaming until the deployment finishes.",
		Example: "  deployer logs billing-api production\n" +
			"  deployer logs billing-api staging --follow",
		Args: wantArgs(1, 2, "deployer logs <deployment-id>   or   deployer logs <project> <environment>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := f.Printer()

			s, err := f.session()
			if err != nil {
				return err
			}
			s.warnFileToken(printer)

			var deployment *api.Deployment

			if len(args) == 2 {
				organization, err := f.organization(cmd.Context(), s)
				if err != nil {
					return err
				}

				target, err := s.Client.Resolve(cmd.Context(), organization, args[0], args[1])
				if err != nil {
					return explainResolveFailure(printer, err, args[0], args[1])
				}

				if target.Environment == nil {
					return exit.Errorf(exit.Usage, "environment %q not found in project %q", args[1], args[0])
				}

				deployment, err = s.Client.LatestDeployment(cmd.Context(), target.Environment.ID)
				if err != nil {
					return err
				}

				if deployment == nil {
					return exit.Errorf(exit.Usage, "%s → %s has never been deployed", target.Project.Slug, target.Environment.Slug)
				}
			} else {
				deployment, err = s.Client.Deployment(cmd.Context(), args[0])
				if err != nil {
					return err
				}
			}

			if followLog && !api.IsTerminal(deployment.Status) {
				return followDeployment(cmd.Context(), f, s.Client, printer, deployment, true)
			}

			return dumpLogs(cmd.Context(), f, s.Client, printer, deployment)
		},
	}

	cmd.Flags().BoolVarP(&followLog, "follow", "f", false, "keep streaming until the deployment finishes")

	return cmd
}

// dumpLogs pages the whole log once, oldest first.
func dumpLogs(parent context.Context, f *Factory, client *api.Client, printer *ui.Printer, deployment *api.Deployment) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	var after int64

	for {
		chunks, err := client.Logs(ctx, deployment.ID, after, 1000)
		if err != nil {
			return err
		}

		if len(chunks) == 0 {
			break
		}

		for _, chunk := range chunks {
			if chunk.Seq <= after {
				continue
			}
			after = chunk.Seq

			if f.JSON {
				if err := printJSON(printer, chunk); err != nil {
					return err
				}

				continue
			}

			text := chunk.Chunk
			if len(text) == 0 || text[len(text)-1] != '\n' {
				text += "\n"
			}
			printer.Raw(text)
		}
	}

	if !f.JSON {
		follow.Summary(printer, deployment)
	}

	if api.IsTerminal(deployment.Status) && !api.Succeeded(deployment.Status) {
		return exit.With(exit.DeploymentFailed, errSilent)
	}

	return nil
}

func orDash(value string) string {
	if value == "" {
		return "—"
	}

	return value
}

// firstTimestamp prefers a finish time but falls back to creation, so an
// in-flight deployment still shows when it started rather than an em dash.
func firstTimestamp(values ...*time.Time) *time.Time {
	for _, value := range values {
		if value != nil {
			return value
		}
	}

	return nil
}
