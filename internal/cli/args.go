package cli

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

// wantArgs validates positional-argument count and, when it is wrong, prints
// what the command expects followed by its usage block.
//
// Cobra's own message ("accepts 2 arg(s), received 0") is useless on its own, and
// the root command sets SilenceUsage so that a *failed deployment* doesn't dump a
// wall of flags. That silence is right for runtime errors and wrong for usage
// errors, so the distinction is made here: a bad invocation always shows how to
// invoke it.
//
// max < 0 means unbounded.
func wantArgs(min, max int, hint string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= min && (max < 0 || len(args) <= max) {
			return nil
		}

		return usageFailure(cmd, args, describeArgCount(min, max, len(args)), hint)
	}
}

// usageFailure prints what was wrong, the expected form, what was typed and the
// usage block, and returns a silent usage error.
func usageFailure(cmd *cobra.Command, args []string, problem, hint string) error {
	out := cmd.ErrOrStderr()

	fmt.Fprintf(out, "%s\n\n", problem)
	fmt.Fprintf(out, "  %s\n\n", hint)

	if got := strings.Join(args, " "); got != "" {
		fmt.Fprintf(out, "You typed: %s %s\n\n", cmd.CommandPath(), got)
	}

	fmt.Fprint(out, cmd.UsageString())

	// errSilent: everything worth saying has just been said.
	return exit.With(exit.Usage, errSilent)
}

// looksLikeUUID catches the commonest wrong turn: passing a public id where a
// slug belongs (they are all over the web app's URLs), or the reverse. Cobra
// would accept the argument and let the API answer 404, which tells the operator
// nothing about which of the two things they got wrong.
func looksLikeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}

	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}

	return true
}

func describeArgCount(min, max, got int) string {
	var expected string

	switch {
	case max < 0:
		expected = fmt.Sprintf("at least %d", min)
	case min == max:
		expected = fmt.Sprintf("%d", min)
	default:
		expected = fmt.Sprintf("%d or %d", min, max)
	}

	if got == 0 {
		return fmt.Sprintf("This command needs %s argument(s); none were given.", expected)
	}

	return fmt.Sprintf("This command needs %s argument(s); %d were given.", expected, got)
}

// explainResolveFailure turns the API's deliberately terse 404 into guidance.
//
// The endpoint cannot say which half of `<project> <environment>` was wrong, or
// whether the caller simply may not see it — saying so would leak the existence
// of other organizations' resources. The CLI has no such constraint locally: it
// knows what the operator typed and which command lists the valid values.
func explainResolveFailure(printer *ui.Printer, err error, project, environment string) error {
	var problem *api.Problem
	if !errors.As(err, &problem) || problem.Status != http.StatusNotFound {
		return err
	}

	target := project
	if environment != "" {
		target = project + " " + environment
	}

	printer.Fail("No %s in this organization (or your token cannot see it).", target)

	switch {
	case looksLikeUUID(project):
		printer.Dim("%q looks like an id. These arguments are slugs — the short names in `deployer projects ls`.", project)
	case environment != "" && looksLikeUUID(environment):
		printer.Dim("%q looks like an id. These arguments are slugs — see `deployer envs ls %s`.", environment, project)
	case environment != "":
		printer.Dim("Check both halves: `deployer projects ls`, then `deployer envs ls %s`.", project)
	default:
		printer.Dim("Run `deployer projects ls` to see the project slugs you can use.")
	}

	printer.Dim("Wrong organization? Pass --org <slug>; `deployer whoami` lists the ones you belong to.")

	return exit.With(exit.Usage, errSilent)
}
