// Package follow tails a running deployment or environment command: it pages
// log chunks by their monotonic seq and watches the status until it is terminal
// (docs/CLI_DESIGN.md §4). Polling — not a websocket — is deliberate for P1:
// the logs endpoint is already seq-cursored, and a poll loop cannot silently
// drop a chunk the way a dropped socket can.
package follow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

// ErrInterrupted is returned when the operator interrupts the follow. The
// deployment keeps running server-side — following is a read.
var ErrInterrupted = errors.New("interrupted")

// ErrAwaitingApproval is returned when the deployment parks on an approval gate
// and the caller did not ask to wait for it.
var ErrAwaitingApproval = errors.New("awaiting approval")

type Options struct {
	// Interval is the poll period while output is flowing; it backs off toward
	// MaxInterval during quiet stretches (a long `npm ci`, a queued run).
	Interval    time.Duration
	MaxInterval time.Duration
	// WaitApproval keeps following while the deployment sits on an approval
	// gate instead of returning ErrAwaitingApproval.
	WaitApproval bool
	// JSON switches to newline-delimited events for piping.
	JSON bool
}

func (o Options) withDefaults() Options {
	if o.Interval <= 0 {
		o.Interval = 700 * time.Millisecond
	}

	if o.MaxInterval < o.Interval {
		o.MaxInterval = 2 * time.Second
	}

	return o
}

type event struct {
	Type   string `json:"type"`
	Seq    int64  `json:"seq,omitempty"`
	Stream string `json:"stream,omitempty"`
	Chunk  string `json:"chunk,omitempty"`
	Status string `json:"status,omitempty"`
	ID     string `json:"id,omitempty"`
}

// source is what the loop tails: seq-cursored log chunks plus a status to poll.
// Deployments and ad-hoc environment commands share the shape, so they share
// the loop.
type source struct {
	id     string
	hint   string // what Ctrl-C does, shown when following starts
	logs   func(ctx context.Context, after int64) ([]api.LogChunk, error)
	status func(ctx context.Context) (string, error)
}

// Run follows deployment until it reaches a terminal status, printing log
// chunks as they arrive. It returns the final deployment — the caller maps its
// status onto the process exit code.
func Run(ctx context.Context, client *api.Client, printer *ui.Printer, deployment *api.Deployment, opts Options) (*api.Deployment, error) {
	current := deployment

	err := loop(ctx, printer, source{
		id:   deployment.ID,
		hint: "press Ctrl-C to stop watching (the deployment keeps running)",
		logs: func(ctx context.Context, after int64) ([]api.LogChunk, error) {
			return client.Logs(ctx, deployment.ID, after, 500)
		},
		status: func(ctx context.Context) (string, error) {
			refreshed, err := client.Deployment(ctx, deployment.ID)
			if err != nil {
				return "", err
			}
			current = refreshed

			return refreshed.Status, nil
		},
	}, deployment.Status, opts)

	return current, err
}

// RunCommand follows an ad-hoc environment command the same way. What Ctrl-C
// does is the caller's business (it cancels the command), so ctx should only
// end when the operator wants to stop watching.
func RunCommand(ctx context.Context, client *api.Client, printer *ui.Printer, command *api.EnvironmentCommand, opts Options) (*api.EnvironmentCommand, error) {
	current := command

	err := loop(ctx, printer, source{
		id:   command.ID,
		hint: "press Ctrl-C to cancel it",
		logs: func(ctx context.Context, after int64) ([]api.LogChunk, error) {
			return client.CommandLogs(ctx, command.ID, after, 500)
		},
		status: func(ctx context.Context) (string, error) {
			refreshed, err := client.Command(ctx, command.ID)
			if err != nil {
				return "", err
			}
			current = refreshed

			return refreshed.Status, nil
		},
	}, command.Status, opts)

	return current, err
}

// loop polls src until its status is terminal. Command statuses are a subset of
// deployment statuses with the same terminal set, so api.IsTerminal serves both.
func loop(ctx context.Context, printer *ui.Printer, src source, status string, opts Options) error {
	opts = opts.withDefaults()

	var (
		after    int64
		interval = opts.Interval
	)

	if opts.JSON {
		emit(printer, event{Type: "status", Status: status, ID: src.id})
	} else {
		printer.Info("following %s — %s", src.id, src.hint)
	}

	for {
		chunks, err := src.logs(ctx, after)
		if err != nil {
			if ctxDone(ctx) {
				return ErrInterrupted
			}

			return err
		}

		if len(chunks) > 0 {
			after = write(printer, chunks, after, opts.JSON)
			interval = opts.Interval
		} else if interval < opts.MaxInterval {
			// Quiet stretch: ease off so a 40-minute build isn't 3,400 requests.
			interval = min(interval*2, opts.MaxInterval)
		}

		refreshed, err := src.status(ctx)
		if err != nil {
			if ctxDone(ctx) {
				return ErrInterrupted
			}

			return err
		}

		if refreshed != status {
			status = refreshed

			if opts.JSON {
				emit(printer, event{Type: "status", Status: status, ID: src.id})
			} else {
				printer.Printf("%s\n", printer.Status(status))
			}
		}

		if status == api.StatusApprovalPending && !opts.WaitApproval {
			return ErrAwaitingApproval
		}

		if api.IsTerminal(status) {
			// Log writes trail the status transition, so drain what's left
			// before reporting an outcome — otherwise the last stage's output
			// (usually the failure reason) is lost.
			if err := drain(ctx, printer, src, after, opts.JSON); err != nil && !errors.Is(err, ErrInterrupted) {
				return err
			}

			return nil
		}

		select {
		case <-ctx.Done():
			return ErrInterrupted
		case <-time.After(interval):
		}
	}
}

// drain reads remaining chunks after a terminal status, stopping when a poll
// returns nothing or the small budget is spent.
func drain(ctx context.Context, printer *ui.Printer, src source, after int64, asJSON bool) error {
	for attempt := 0; attempt < 5; attempt++ {
		chunks, err := src.logs(ctx, after)
		if err != nil {
			if ctxDone(ctx) {
				return ErrInterrupted
			}

			return err
		}

		if len(chunks) == 0 {
			return nil
		}

		after = write(printer, chunks, after, asJSON)

		select {
		case <-ctx.Done():
			return ErrInterrupted
		case <-time.After(200 * time.Millisecond):
		}
	}

	return nil
}

// write prints chunks and returns the new high-water seq. Chunk text is written
// verbatim: the server has already redacted secrets and the bytes may carry
// ANSI the remote build emitted, which is the operator's actual output.
func write(printer *ui.Printer, chunks []api.LogChunk, after int64, asJSON bool) int64 {
	for _, chunk := range chunks {
		if chunk.Seq <= after {
			continue // defensive: never re-print on an overlapping page
		}

		after = chunk.Seq

		if asJSON {
			emit(printer, event{Type: "log", Seq: chunk.Seq, Stream: chunk.Stream, Chunk: chunk.Chunk})

			continue
		}

		text := chunk.Chunk
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}

		printer.Raw(text)
	}

	return after
}

func emit(printer *ui.Printer, e event) {
	encoded, err := json.Marshal(e)
	if err != nil {
		return
	}

	printer.Raw(string(encoded) + "\n")
}

func ctxDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// Summary is the one-line outcome printed after a follow.
func Summary(printer *ui.Printer, deployment *api.Deployment) {
	detail := fmt.Sprintf("%s in %s", deployment.ID, ui.Duration(deployment.DurationMS))

	if deployment.Release != nil && deployment.Release.ReleaseRef != "" {
		detail = fmt.Sprintf("%s (%s) in %s", deployment.ID, deployment.Release.ReleaseRef, ui.Duration(deployment.DurationMS))
	}

	switch {
	case api.Succeeded(deployment.Status):
		printer.Success("%s", detail)
	case deployment.Status == api.StatusDegraded:
		printer.Warn("degraded — %s: %s", detail, orDash(deployment.ErrorSummary))
	default:
		printer.Fail("%s — %s: %s", ui.StatusLabel(deployment.Status), detail, orDash(deployment.ErrorSummary))
	}
}

func orDash(text string) string {
	if strings.TrimSpace(text) == "" {
		return "no error summary"
	}

	return text
}
