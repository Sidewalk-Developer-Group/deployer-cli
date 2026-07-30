package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
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

		out := cmd.ErrOrStderr()

		fmt.Fprintf(out, "%s\n\n", describeArgCount(min, max, len(args)))
		fmt.Fprintf(out, "  %s\n\n", hint)
		fmt.Fprint(out, cmd.UsageString())

		// errSilent: everything worth saying has just been said.
		return exit.With(exit.Usage, errSilent)
	}
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
