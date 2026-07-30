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
			done:  "cancellation requested for %s",
		}),
		newActionCommand(f, actionSpec{
			name:  "retry",
			short: "Retry a failed deployment",
			done:  "retry queued as %s",
			// The API answers with the new deployment, so follow that one.
			follow: true,
		}),
		newActionCommand(f, actionSpec{
			name:   "rollback",
			short:  "Roll an environment back to its previous release",
			done:   "rollback queued as %s",
			follow: true,
		}),
		newActionCommand(f, actionSpec{
			name:    "approve",
			short:   "Approve a deployment waiting on an approval gate",
			done:    "approved %s",
			comment: commentOptional,
			follow:  true,
		}),
		newActionCommand(f, actionSpec{
			name:    "reject",
			short:   "Reject a deployment waiting on an approval gate",
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

	cmd := &cobra.Command{
		Use:   spec.name + " <deployment-id>",
		Short: spec.short,
		Args:  cobra.ExactArgs(1),
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
