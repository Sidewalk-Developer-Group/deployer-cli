package follow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

// stage is one scripted poll round: the log chunks the server hands out and the
// status it reports while doing so.
type stage struct {
	status string
	chunks []api.LogChunk
}

// fakeInstance replays stages in order, honouring the `after` cursor exactly the
// way the real endpoint does (seq-ordered, exclusive).
type fakeInstance struct {
	t          *testing.T
	stages     []stage
	logCalls   int
	statCalls  int
	stageIndex int
}

func (f *fakeInstance) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/logs"):
			f.logCalls++

			var after int64
			if raw := r.URL.Query().Get("after"); raw != "" {
				if _, err := fmt.Sscanf(raw, "%d", &after); err != nil {
					f.t.Fatalf("bad after cursor %q", raw)
				}
			}

			var out []api.LogChunk
			for _, chunk := range f.current().chunks {
				if chunk.Seq > after {
					out = append(out, chunk)
				}
			}

			encoded, _ := json.Marshal(map[string]any{"data": out})
			_, _ = w.Write(encoded)

		default:
			f.statCalls++
			status := f.current().status

			// Advance only after the status read, so each stage is observed as
			// one complete (logs, status) round.
			if f.stageIndex < len(f.stages)-1 {
				f.stageIndex++
			}

			_, _ = fmt.Fprintf(w, `{"data":{"id":"d1","status":%q}}`, status)
		}
	})
}

func (f *fakeInstance) current() stage {
	return f.stages[f.stageIndex]
}

// serve wires a client and printer against the scripted instance.
func serve(t *testing.T, stages []stage) (*api.Client, *ui.Printer, *bytes.Buffer, *fakeInstance) {
	t.Helper()

	instance := &fakeInstance{t: t, stages: stages}
	server := httptest.NewServer(instance.handler())
	t.Cleanup(server.Close)

	client, err := api.New(server.URL, "token", "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	out := &bytes.Buffer{}
	printer := &ui.Printer{Out: out, Err: &bytes.Buffer{}}

	return client, printer, out, instance
}

// bounded gives every follow test a hard ceiling: a regression that stops
// detecting terminal status should fail the test, not hang the suite.
func bounded(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	return ctx
}

func fastOptions() Options {
	return Options{Interval: time.Millisecond, MaxInterval: 2 * time.Millisecond}
}

func TestRunStreamsToSuccessAndReportsFinalStatus(t *testing.T) {
	client, printer, out, _ := serve(t, []stage{
		{status: api.StatusRunning, chunks: []api.LogChunk{{Seq: 1, Chunk: "clone\n"}}},
		{status: api.StatusRunning, chunks: []api.LogChunk{{Seq: 1, Chunk: "clone\n"}, {Seq: 2, Chunk: "build\n"}}},
		{status: api.StatusSucceeded, chunks: []api.LogChunk{
			{Seq: 1, Chunk: "clone\n"}, {Seq: 2, Chunk: "build\n"}, {Seq: 3, Chunk: "activate\n"},
		}},
	})

	final, err := Run(bounded(t), client, printer, &api.Deployment{ID: "d1", Status: api.StatusQueued}, fastOptions())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if final.Status != api.StatusSucceeded {
		t.Fatalf("final status = %q", final.Status)
	}

	text := out.String()
	for _, want := range []string{"clone", "build", "activate"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}

	// Every chunk exactly once: an overlapping page must not double-print.
	if got := strings.Count(text, "clone"); got != 1 {
		t.Errorf("clone printed %d times, want 1:\n%s", got, text)
	}
}

func TestRunDrainsLogsWrittenAfterTheTerminalStatus(t *testing.T) {
	// The failure reason is written after the status flips — the common shape
	// for a failed deploy. Losing it would leave the operator with no cause.
	client, printer, out, _ := serve(t, []stage{
		{status: api.StatusRunning, chunks: []api.LogChunk{{Seq: 1, Chunk: "migrate\n"}}},
		{status: api.StatusFailed, chunks: []api.LogChunk{{Seq: 1, Chunk: "migrate\n"}}},
		{status: api.StatusFailed, chunks: []api.LogChunk{
			{Seq: 1, Chunk: "migrate\n"}, {Seq: 2, Chunk: "ERROR: migration aborted\n"},
		}},
	})

	final, err := Run(bounded(t), client, printer, &api.Deployment{ID: "d1", Status: api.StatusRunning}, fastOptions())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if final.Status != api.StatusFailed {
		t.Fatalf("final status = %q", final.Status)
	}

	if !strings.Contains(out.String(), "ERROR: migration aborted") {
		t.Errorf("drain lost the post-terminal output:\n%s", out.String())
	}
}

func TestRunStopsOnAnApprovalGate(t *testing.T) {
	client, printer, _, _ := serve(t, []stage{
		{status: api.StatusApprovalPending},
	})

	_, err := Run(bounded(t), client, printer, &api.Deployment{ID: "d1", Status: api.StatusPending}, fastOptions())

	if !errors.Is(err, ErrAwaitingApproval) {
		t.Fatalf("expected ErrAwaitingApproval, got %v", err)
	}
}

func TestRunWaitsThroughAnApprovalGateWhenAsked(t *testing.T) {
	client, printer, _, _ := serve(t, []stage{
		{status: api.StatusApprovalPending},
		{status: api.StatusRunning},
		{status: api.StatusSucceeded},
	})

	options := fastOptions()
	options.WaitApproval = true

	final, err := Run(bounded(t), client, printer, &api.Deployment{ID: "d1", Status: api.StatusPending}, options)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if final.Status != api.StatusSucceeded {
		t.Fatalf("final status = %q", final.Status)
	}
}

func TestRunReturnsInterruptedWhenCancelled(t *testing.T) {
	client, printer, _, _ := serve(t, []stage{
		{status: api.StatusRunning},
		{status: api.StatusRunning},
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	_, err := Run(ctx, client, printer, &api.Deployment{ID: "d1", Status: api.StatusRunning}, fastOptions())

	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
}

func TestJSONModeEmitsNewlineDelimitedEvents(t *testing.T) {
	client, printer, out, _ := serve(t, []stage{
		{status: api.StatusRunning, chunks: []api.LogChunk{{Seq: 1, Stream: "stdout", Chunk: "clone"}}},
		{status: api.StatusSucceeded, chunks: []api.LogChunk{{Seq: 1, Stream: "stdout", Chunk: "clone"}}},
	})

	options := fastOptions()
	options.JSON = true

	if _, err := Run(bounded(t), client, printer, &api.Deployment{ID: "d1", Status: api.StatusRunning}, options); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var sawLog, sawStatus bool

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line is not JSON: %q", line)
		}

		switch event["type"] {
		case "log":
			sawLog = true
			if event["chunk"] != "clone" {
				t.Errorf("unexpected log event: %v", event)
			}
		case "status":
			sawStatus = true
		}
	}

	if !sawLog || !sawStatus {
		t.Errorf("expected both log and status events, got:\n%s", out.String())
	}
}

func TestQuietStretchesBackOff(t *testing.T) {
	// Twelve status rounds with no output: the interval should have doubled to
	// the cap rather than hammering the instance at the base rate.
	stages := make([]stage, 0, 13)
	for i := 0; i < 12; i++ {
		stages = append(stages, stage{status: api.StatusRunning})
	}
	stages = append(stages, stage{status: api.StatusSucceeded})

	client, printer, _, instance := serve(t, stages)

	start := time.Now()
	if _, err := Run(bounded(t), client, printer, &api.Deployment{ID: "d1", Status: api.StatusRunning}, Options{
		Interval:    2 * time.Millisecond,
		MaxInterval: 20 * time.Millisecond,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 13 rounds at the base 2ms would be ~26ms; backoff makes it materially
	// longer while still finishing quickly.
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("expected backoff to slow polling, took %v", elapsed)
	}

	if instance.logCalls == 0 || instance.statCalls == 0 {
		t.Errorf("expected both endpoints to be polled (logs=%d status=%d)", instance.logCalls, instance.statCalls)
	}
}
