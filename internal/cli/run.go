package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/sidewalk-developer-group/deployer-cli/internal/follow"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

const runHint = "deployer run <project> <environment> -- <command...>   — e.g. deployer run billing-api production -- php artisan migrate:status"

func newRunCommand(f *Factory) *cobra.Command {
	var (
		noFollow   bool
		list       bool
		server     string
		maxRuntime time.Duration
	)

	cmd := &cobra.Command{
		Use:   "run <project> <environment> -- <command...>",
		Short: "Run a shell command in an environment's live release",
		Long: "Runs an ad-hoc command as the deploy user inside the environment's current\n" +
			"release and streams its output until it finishes.\n\n" +
			"Everything after `--` is the command. Several words are joined with spaces,\n" +
			"as ssh does, and run by the remote shell — quote the whole command to keep\n" +
			"pipes, `&&` or globs away from your local shell.\n\n" +
			"The exit code is the remote command's own exit status (124 means it hit its\n" +
			"time limit). Ctrl-C cancels the command on the server and waits for it to stop,\n" +
			"then exits 130; a second Ctrl-C stops waiting at once. If the outcome is\n" +
			"unknown (the connection to the server dropped) it exits 1. Before the command\n" +
			"starts the usual CLI codes apply: 2 usage, 3 auth, 4 API.\n\n" +
			"Needs a token with the environments:exec ability (Settings → Security → Personal access tokens) and an\n" +
			"owner or admin role; one command runs per environment at a time.",
		Example: "  deployer run billing-api production -- php artisan migrate:status\n" +
			"  deployer run billing-api staging -- 'tail -n 50 storage/logs/laravel.log | grep ERROR'\n" +
			"  deployer run billing-api production --max-runtime 10m -- php artisan queue:retry all\n" +
			"  deployer run billing-api production --no-follow -- php artisan cache:clear\n" +
			"  deployer run --list billing-api production",
		Args: func(cmd *cobra.Command, args []string) error {
			if list {
				return wantArgs(2, 2, "deployer run --list <project> <environment>")(cmd, args)
			}

			if err := wantArgs(2, -1, runHint)(cmd, args); err != nil {
				return err
			}

			if cmd.ArgsLenAtDash() != 2 {
				return usageFailure(cmd, args, "Put the command after `--`, following the project and environment.", runHint)
			}

			if commandLine(args) == "" {
				return usageFailure(cmd, args, "No command given after `--`.", runHint)
			}

			return nil
		},
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

			if list {
				return listCommands(cmd.Context(), f, s.Client, printer, target.Environment.ID)
			}

			command, err := s.Client.RunCommand(cmd.Context(), target.Environment.ID, api.CommandOptions{
				Command:        commandLine(args),
				Server:         server,
				TimeoutSeconds: int(maxRuntime.Seconds()),
			})
			if err != nil {
				return explainExecDenied(printer, err)
			}

			if noFollow {
				if f.JSON {
					return printJSON(printer, command)
				}

				// Bare id on stdout so a script can capture it.
				printer.Raw(command.ID + "\n")

				return nil
			}

			if !f.JSON {
				printer.Printf("%s → %s  %s\n", target.Project.Slug, target.Environment.Slug, printer.Status(command.Status))
				printer.Dim("command %s", command.ID)
			}

			interrupts := make(chan os.Signal, 2)
			signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(interrupts)

			return followCommand(cmd.Context(), f.JSON, s.Client, printer, command, interrupts)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&noFollow, "no-follow", false, "return as soon as the command is accepted, printing its id")
	flags.BoolVar(&list, "list", false, "list the environment's recent commands instead of running one")
	flags.StringVar(&server, "server", "", "id of the server to run on (default: the environment's primary server)")
	flags.DurationVar(&maxRuntime, "max-runtime", 0, "stop the command after this long, e.g. 10m (server default 30m, max 55m)")

	return cmd
}

// commandLine is everything after `--`, joined the way ssh joins its trailing
// arguments. The API takes one string for the remote shell.
func commandLine(args []string) string {
	if len(args) <= 2 {
		return ""
	}

	return strings.TrimSpace(strings.Join(args[2:], " "))
}

// commandPolling matches deploy's cadence; a var so tests can run it fast.
var commandPolling = follow.Options{Interval: 700 * time.Millisecond, MaxInterval: 2 * time.Second}

// followCommand streams the command to completion. The first interrupt asks the
// server to cancel it and keeps following, so the operator sees it stop; the
// second stops watching. The process's root context is deliberately detached:
// it ends on the first Ctrl-C, which here must not end the follow.
func followCommand(parent context.Context, asJSON bool, client *api.Client, printer *ui.Printer, command *api.EnvironmentCommand, interrupts <-chan os.Signal) error {
	ctx, stopWatching := context.WithCancel(context.WithoutCancel(parent))
	defer stopWatching()

	var cancelRequested atomic.Bool

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-interrupts:
		}

		cancelRequested.Store(true)
		printer.Warn("cancelling %s — press Ctrl-C again to stop waiting", command.ID)

		request, done := context.WithTimeout(ctx, 30*time.Second)
		_, err := client.CancelCommand(request, command.ID)
		done()

		// A 409 here usually means it finished first; the follow reports that.
		if err != nil && ctx.Err() == nil {
			printer.Warn("cancel request failed: %v", err)
		}

		select {
		case <-ctx.Done():
		case <-interrupts:
			stopWatching()
		}
	}()

	options := commandPolling
	options.JSON = asJSON

	final, err := follow.RunCommand(ctx, client, printer, command, options)
	stopWatching()

	if errors.Is(err, follow.ErrInterrupted) {
		if cancelRequested.Load() {
			printer.Warn("stopped waiting; cancellation of %s was requested but not yet confirmed", command.ID)
		} else {
			printer.Warn("stopped watching %s", command.ID)
		}

		return exit.With(interrupted, errSilent)
	}

	if err != nil {
		return err
	}

	if asJSON {
		if err := printJSON(printer, final); err != nil {
			return err
		}
	} else {
		commandSummary(printer, final)
	}

	if code := commandExitCode(final); code != exit.OK {
		return exit.With(code, errSilent)
	}

	return nil
}

// commandExitCode is the remote exit status when the server knows it, so
// `deployer run ... -- test -f x` composes in scripts the way ssh does.
func commandExitCode(command *api.EnvironmentCommand) int {
	switch command.Status {
	case api.StatusSucceeded:
		return exit.OK
	case api.StatusCancelled:
		return interrupted
	}

	if command.ExitCode != nil && *command.ExitCode > 0 && *command.ExitCode < 256 {
		return *command.ExitCode
	}

	// Failed with no known status: it never started, or the connection or
	// worker died and the outcome is unknown.
	return exit.DeploymentFailed
}

func commandSummary(printer *ui.Printer, command *api.EnvironmentCommand) {
	switch command.Status {
	case api.StatusSucceeded:
		printer.Success("%s exited 0", command.ID)
	case api.StatusCancelled:
		printer.Warn("cancelled — %s", command.ID)
	default:
		code := "no exit status"
		if command.ExitCode != nil {
			code = "exit " + strconv.Itoa(*command.ExitCode)
		}

		printer.Fail("failed (%s) — %s: %s", code, command.ID, orDash(command.ErrorSummary))
	}

	if command.OutputTruncated {
		printer.Warn("output was truncated; the server keeps at most 5 MB per command")
	}
}

// explainExecDenied adds the likely fix to a 403 from the commands endpoints.
// The server does not say which check failed, so name all three.
func explainExecDenied(printer *ui.Printer, err error) error {
	var problem *api.Problem
	if !errors.As(err, &problem) || problem.Status != http.StatusForbidden {
		return err
	}

	printer.Fail("%s", problem.Error())

	// The org's two-factor requirement is the one 403 the server names; the
	// token and role advice below would send the user the wrong way.
	if strings.HasSuffix(problem.Type, "/problems/mfa-required") {
		printer.Dim("Your organization requires two-factor — enable it in Settings → Security.")

		return exit.With(exit.Auth, errSilent)
	}

	printer.Dim("Running commands needs a token with the environments:exec ability — create one in the web app under Settings → Security → Personal access tokens.")
	printer.Dim("Your role must also grant it (owner or admin), and on a protected environment you must be allowed to deploy.")

	return exit.With(exit.Auth, errSilent)
}

func listCommands(ctx context.Context, f *Factory, client *api.Client, printer *ui.Printer, environmentID string) error {
	commands, err := client.Commands(ctx, environmentID, 25)
	if err != nil {
		return explainExecDenied(printer, err)
	}

	if f.JSON {
		return printJSON(printer, commands)
	}

	if len(commands) == 0 {
		printer.Printf("no commands run here yet\n")

		return nil
	}

	rows := make([][]string, 0, len(commands))
	for _, command := range commands {
		code := "—"
		if command.ExitCode != nil {
			code = strconv.Itoa(*command.ExitCode)
		}

		text := command.Command
		if runes := []rune(text); len(runes) > 60 {
			text = string(runes[:59]) + "…"
		}

		rows = append(rows, []string{command.ID, printer.Status(command.Status), code, ui.Ago(command.CreatedAt), text})
	}

	printer.Table([]string{"ID", "STATUS", "EXIT", "WHEN", "COMMAND"}, rows)

	return nil
}
