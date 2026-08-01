package cli

import (
	"context"
	"errors"
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

// interrupted is the conventional shell code for SIGINT. Following is a read,
// so Ctrl-C stops watching and leaves the deployment running.
const interrupted = 130

func newDeployCommand(f *Factory) *cobra.Command {
	var (
		branch       string
		commit       string
		comment      string
		scheduledAt  string
		stages       []string
		dryRun       bool
		noFollow     bool
		waitApproval bool
	)

	cmd := &cobra.Command{
		Use:   "deploy <project> <environment>",
		Short: "Deploy a project's environment",
		Long: "Triggers a deployment and streams its log until it finishes.\n\n" +
			"The exit code is the outcome: 0 deployed, 1 the deployment failed or was\n" +
			"cancelled, 5 it is waiting on an approval gate. Both arguments are slugs, as\n" +
			"shown by `deployer projects ls` and `deployer envs ls <project>`.",
		Example: "  deployer deploy billing-api production\n" +
			"  deployer deploy billing-api staging --branch fix/timeout\n" +
			"  deployer deploy billing-api production --dry-run\n" +
			"  deployer deploy billing-api production --no-follow --json",
		Args: wantArgs(2, 2, "deployer deploy <project> <environment>   — slugs, as listed by `deployer projects ls` and `deployer envs ls <project>`"),
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

			target, err := s.Client.Resolve(cmd.Context(), organization, args[0], args[1])
			if err != nil {
				return explainResolveFailure(printer, err, args[0], args[1])
			}

			if target.Environment == nil {
				return exit.Errorf(exit.Usage, "environment %q not found in project %q", args[1], args[0])
			}

			options := api.TriggerOptions{
				Branch:                branch,
				CommitSHA:             commit,
				Comment:               comment,
				ScheduledAt:           scheduledAt,
				IsDryRun:              dryRun,
				IncludeOptionalStages: stages,
			}

			deployment, err := s.Client.Deploy(cmd.Context(), target.Environment.ID, options)
			if err != nil {
				return err
			}

			if !f.JSON {
				label := target.Project.Slug + " → " + target.Environment.Slug
				if dryRun {
					label += " (dry run)"
				}
				printer.Printf("%s  %s\n", label, printer.Status(deployment.Status))
				printer.Dim("deployment %s  branch %s", deployment.ID, firstNonEmpty(deployment.Branch, target.Environment.Branch, "default"))
			}

			// Only an explicit --no-follow returns early. A queued deployment is
			// not a failure, so this exits 0 — the caller asked not to wait.
			if !shouldFollow(f, noFollow) {
				if f.JSON {
					return printJSON(printer, deployment)
				}

				printer.Dim("not following; run `deployer logs %s` to watch it", deployment.ID)

				return nil
			}

			return followDeployment(cmd.Context(), f, s.Client, printer, deployment, waitApproval)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&branch, "branch", "b", "", "branch to deploy (default: the environment's branch)")
	flags.StringVar(&commit, "commit", "", "exact commit sha to deploy")
	flags.StringVarP(&comment, "comment", "m", "", "note recorded with the deployment")
	flags.StringVar(&scheduledAt, "at", "", "schedule for an ISO-8601 timestamp instead of deploying now")
	flags.StringArrayVar(&stages, "stage", nil, "include an optional pipeline stage (repeatable)")
	flags.BoolVar(&dryRun, "dry-run", false, "run the pipeline without activating a release")
	flags.BoolVar(&noFollow, "no-follow", false, "return as soon as the deployment is accepted, without waiting for the outcome")
	flags.BoolVar(&waitApproval, "wait-approval", false, "keep waiting while the deployment sits on an approval gate")

	// -f reads as "follow" to anyone who has used tail; keep it accepted but
	// hidden, since following is already the default.
	var followFlag bool
	flags.BoolVarP(&followFlag, "follow", "f", true, "stream the log until the deployment finishes")
	_ = flags.MarkHidden("follow")

	return cmd
}

// shouldFollow: following is the default everywhere, terminal or not. Deciding
// by TTY was worse than it looked — a CI job would exit 0 the moment the API
// accepted the request, reporting success for a deployment that was still
// queued, or parked on an approval gate, or about to fail.
func shouldFollow(_ *Factory, noFollow bool) bool {
	return !noFollow
}

// followDeployment streams to completion and maps the outcome onto an exit code.
func followDeployment(parent context.Context, f *Factory, client *api.Client, printer *ui.Printer, deployment *api.Deployment, waitApproval bool) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	final, err := follow.Run(ctx, client, printer, deployment, follow.Options{
		Interval:     700 * time.Millisecond,
		MaxInterval:  2 * time.Second,
		WaitApproval: waitApproval,
		JSON:         f.JSON,
	})

	switch {
	case errors.Is(err, follow.ErrInterrupted):
		printer.Warn("stopped watching; the deployment is still running")
		printer.Dim("resume with `deployer logs %s`, or stop it with `deployer cancel %s`", deployment.ID, deployment.ID)

		// The guidance above is the message; don't repeat the bare error.
		return exit.With(interrupted, errSilent)

	case errors.Is(err, follow.ErrAwaitingApproval):
		printer.Warn("deployment %s is waiting for approval", deployment.ID)
		printer.Dim("approve it in the web app, with `deployer approve %s`, or wait here using --wait-approval", deployment.ID)

		return exit.With(exit.AwaitingApproval, errSilent)

	case err != nil:
		return err
	}

	if f.JSON {
		return printJSON(printer, final)
	}

	follow.Summary(printer, final)

	if !api.Succeeded(final.Status) {
		// The deployment, not the CLI, failed — exit 1 so CI stops.
		return exit.With(exit.DeploymentFailed, errSilent)
	}

	return nil
}

// errSilent marks an error whose message has already been printed.
var errSilent = errors.New("")
