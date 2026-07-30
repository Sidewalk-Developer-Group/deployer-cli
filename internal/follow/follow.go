// Package follow tails a running deployment: it pages log chunks by their
// monotonic seq and watches the deployment's status until it is terminal
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

// Run follows deployment until it reaches a terminal status, printing log
// chunks as they arrive. It returns the final deployment — the caller maps its
// status onto the process exit code.
func Run(ctx context.Context, client *api.Client, printer *ui.Printer, deployment *api.Deployment, opts Options) (*api.Deployment, error) {
	opts = opts.withDefaults()

	var (
		after    int64
		interval = opts.Interval
		status   = deployment.Status
		current  = deployment
	)

	if opts.JSON {
		emit(printer, event{Type: "status", Status: status, ID: current.ID})
	} else {
		printer.Info("following %s — press Ctrl-C to stop watching (the deployment keeps running)", current.ID)
	}

	for {
		chunks, err := client.Logs(ctx, current.ID, after, 500)
		if err != nil {
			if ctxDone(ctx) {
				return current, ErrInterrupted
			}

			return current, err
		}

		if len(chunks) > 0 {
			after = write(printer, chunks, after, opts.JSON)
			interval = opts.Interval
		} else if interval < opts.MaxInterval {
			// Quiet stretch: ease off so a 40-minute build isn't 3,400 requests.
			interval = min(interval*2, opts.MaxInterval)
		}

		refreshed, err := client.Deployment(ctx, current.ID)
		if err != nil {
			if ctxDone(ctx) {
				return current, ErrInterrupted
			}

			return current, err
		}
		current = refreshed

		if refreshed.Status != status {
			status = refreshed.Status

			if opts.JSON {
				emit(printer, event{Type: "status", Status: status, ID: current.ID})
			} else {
				printer.Printf("%s\n", printer.Status(status))
			}
		}

		if status == api.StatusApprovalPending && !opts.WaitApproval {
			return current, ErrAwaitingApproval
		}

		if api.IsTerminal(status) {
			// Log writes trail the status transition, so drain what's left
			// before reporting an outcome — otherwise the last stage's output
			// (usually the failure reason) is lost.
			final, err := drain(ctx, client, printer, current.ID, after, opts.JSON)
			if err != nil && !errors.Is(err, ErrInterrupted) {
				return current, err
			}
			_ = final

			return current, nil
		}

		select {
		case <-ctx.Done():
			return current, ErrInterrupted
		case <-time.After(interval):
		}
	}
}

// drain reads remaining chunks after a terminal status, stopping when a poll
// returns nothing or the small budget is spent.
func drain(ctx context.Context, client *api.Client, printer *ui.Printer, deploymentID string, after int64, asJSON bool) (int64, error) {
	for attempt := 0; attempt < 5; attempt++ {
		chunks, err := client.Logs(ctx, deploymentID, after, 500)
		if err != nil {
			if ctxDone(ctx) {
				return after, ErrInterrupted
			}

			return after, err
		}

		if len(chunks) == 0 {
			return after, nil
		}

		after = write(printer, chunks, after, asJSON)

		select {
		case <-ctx.Done():
			return after, ErrInterrupted
		case <-time.After(200 * time.Millisecond):
		}
	}

	return after, nil
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
