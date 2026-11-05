package cli

import (
	"github.com/spf13/cobra"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
)

// The non-CRUD deployment sub-resources (docs/API_SPECIFICATION.md): each is one
// POST, authorized server-side. The CLI adds no local rules about when they are
// allowed — a state conflict comes back as a 409 and exits 4.
func newActionCommands(f *Factory) []*cobra.Command {
	return []*cobra.Command{
		newActionCommand(f, actionSpec{
			name:  "cancel",
			short: "Cancel a running or queued deployment",
			long: "Asks the engine to stop a deployment that is pending, queued, running or\n" +
				"awaiting approval. Cancellation is cooperative: the current stage finishes\n" +
				"before the run stops, so the environment is never left mid-step. A deployment\n" +
				"that has already finished cannot be cancelled and returns a 409 (exit 4).",
			done: "cancellation requested for %s",
		}),
		newActionCommand(f, actionSpec{
			name:  "retry",
			short: "Retry a failed deployment",
			long: "Runs the same commit and branch again as a NEW deployment — the original run is\n" +
				"immutable history and is never rewritten. Useful after fixing a server-side\n" +
				"cause (disk, credentials, a flaky dependency mirror).",
			done: "retry queued as %s",
			// The API answers with the new deployment, so follow that one.
			follow: true,
		}),
		newActionCommand(f, actionSpec{
			name:  "rollback",
			short: "Roll an environment back to its previous release",
			long: "Re-points the environment at the release that preceded this deployment, as a new\n" +
				"audited run: no clone, no build — the target release is still on disk, so the\n" +
				"symlink flip is near-instant.\n\n" +
				"IMPORTANT: code is reverted; database migrations are NOT automatically\n" +
				"reversed. If the deployment being rolled back ran a destructive migration, you\n" +
				"must handle the data yourself.\n\n" +
				"Fails with 409 (exit 4) if the target release has already been pruned by the\n" +
				"environment's retention setting.",
			done:   "rollback queued as %s",
			follow: true,
		}),
		newActionCommand(f, actionSpec{
			name:  "approve",
			short: "Approve a deployment waiting on an approval gate",
			long: "Releases a deployment parked on an approval gate so it proceeds. Requires the\n" +
				"deployments:approve ability, and can_approve on a protected environment; the\n" +
				"decision and any --comment are recorded in the audit log.",
			done:    "approved %s",
			comment: commentOptional,
			follow:  true,
		}),
		newActionCommand(f, actionSpec{
			name:  "reject",
			short: "Reject a deployment waiting on an approval gate",
			long: "Declines a deployment parked on an approval gate; it never runs. A --comment is\n" +
				"required — the reason is what the person who requested the deploy will read.",
			done:    "rejected %s",
			comment: commentRequired,
		}),
	}
}

type commentMode int

const (
	commentNone commentMode = iota
	commentOptional
	commentRequired
)

type actionSpec struct {
	name    string
	short   string
	long    string
	done    string
	comment commentMode
	// follow means the action produces work worth streaming when the operator
	// is at a terminal.
	follow bool
}

func newActionCommand(f *Factory, spec actionSpec) *cobra.Command {
	var (
		comment  string
		noFollow bool
	)

	example := "  deployer status billing-api production\n  deployer " + spec.name + " <deployment-id>"
	if spec.comment != commentNone {
		example += " --comment 'Release window agreed'"
	}

	cmd := &cobra.Command{
		Use:     spec.name + " <deployment-id>",
		Short:   spec.short,
		Long:    spec.long + "\n\nDeployment ids come from `deployer status <project> <environment>`.",
		Example: example,
		Args:    wantArgs(1, 1, "deployer "+spec.name+" <deployment-id>   — ids come from `deployer status <project> <environment>`"),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := f.Printer()

			if spec.comment == commentRequired && comment == "" {
				return exit.Errorf(exit.Usage, "%s requires a reason: pass --comment", spec.name)
			}

			s, err := f.session()
			if err != nil {
				return err
			}
			s.warnFileToken(printer)

			body := map[string]string{}
			if spec.comment != commentNone && comment != "" {
				body["comment"] = comment
			}

			deployment, err := s.Client.Action(cmd.Context(), args[0], spec.name, body)
			if err != nil {
				return err
			}

			if f.JSON {
				return printJSON(printer, deployment)
			}

			printer.Success(spec.done, deployment.ID)
			printer.Printf("%s\n", printer.Status(deployment.Status))

			if spec.follow && !noFollow && shouldFollow(f, noFollow) && !api.IsTerminal(deployment.Status) {
				return followDeployment(cmd.Context(), f, s.Client, printer, deployment, true)
			}

			return nil
		},
	}

	if spec.comment != commentNone {
		usage := "reason recorded with the decision"
		if spec.comment == commentRequired {
			usage += " (required)"
		}
		cmd.Flags().StringVarP(&comment, "comment", "m", "", usage)
	}

	if spec.follow {
		cmd.Flags().BoolVar(&noFollow, "no-follow", false, "return without streaming the resulting deployment")
	}

	return cmd
}
