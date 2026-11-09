package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/sidewalk-developer-group/deployer-cli/internal/follow"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

// lockedBuffer: the interrupt goroutine and the follow loop both print.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// fakeCommands scripts one environment command: each status poll advances one
// step; a cancel switches the remaining polls to `cancelled`.
type fakeCommands struct {
	mu        sync.Mutex
	statuses  []string
	chunks    []api.LogChunk
	exitCode  any
	polls     int
	cancelled bool
	started   map[string]any
	forbidden bool
	// mfaRequired: the org requires two-factor and the user has none.
	mfaRequired bool
	// stubborn: the cancel is accepted but the command never stops.
	stubborn    bool
	cancelCalls int
}

func (f *fakeCommands) serve(t *testing.T) *api.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/environments/env1/commands":
			if f.mfaRequired {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"type":"https://deployer.dev/problems/mfa-required","title":"Two-factor authentication required.","status":403,"detail":"Acme requires two-factor authentication. Turn it on in Settings → Security to continue."}`))

				return
			}
			if f.forbidden {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"type":"about:blank","title":"Forbidden","status":403,"detail":"This action is unauthorized."}`))

				return
			}
			_ = json.NewDecoder(r.Body).Decode(&f.started)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"data":{"id":"c1","status":"queued"},"meta":{"channel":"private-environment-command.c1"}}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/environment-commands/c1/cancel":
			f.cancelCalls++
			f.cancelled = !f.stubborn
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"data":{"id":"c1","status":"running","cancel_requested":true}}`))

		case strings.HasSuffix(r.URL.Path, "/environment-commands/c1/logs"):
			var after int64
			_, _ = fmt.Sscanf(r.URL.Query().Get("after"), "%d", &after)

			out := []api.LogChunk{}
			for _, chunk := range f.chunks {
				// Hand out one chunk per page so streaming spans several polls.
				if chunk.Seq > after {
					out = append(out, chunk)

					break
				}
			}
			encoded, _ := json.Marshal(map[string]any{"data": out})
			_, _ = w.Write(encoded)

		case r.URL.Path == "/api/v1/environment-commands/c1":
			status := f.statuses[min(f.polls, len(f.statuses)-1)]
			f.polls++
			if f.cancelled {
				status = api.StatusCancelled
			}

			body := map[string]any{"id": "c1", "status": status, "exit_code": nil, "error_summary": nil}
			if api.IsTerminal(status) && status != api.StatusCancelled {
				body["exit_code"] = f.exitCode
			}
			encoded, _ := json.Marshal(map[string]any{"data": body})
			_, _ = w.Write(encoded)

		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client, err := api.New(server.URL, "token", "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return client
}

func init() {
	commandPolling = follow.Options{Interval: time.Millisecond, MaxInterval: 5 * time.Millisecond}
}

func testPrinter() (*ui.Printer, *lockedBuffer, *lockedBuffer) {
	out, errOut := &lockedBuffer{}, &lockedBuffer{}

	return &ui.Printer{Out: out, Err: errOut}, out, errOut
}

func bounded(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	return ctx
}

func start(t *testing.T, client *api.Client) *api.EnvironmentCommand {
	t.Helper()

	command, err := client.RunCommand(bounded(t), "env1", api.CommandOptions{Command: "php artisan about"})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}

	return command
}

func TestRunStreamsOutputAndExitsZero(t *testing.T) {
	fake := &fakeCommands{
		statuses: []string{"running", "running", "succeeded"},
		chunks:   []api.LogChunk{{Seq: 1, Chunk: "Laravel 13\n"}, {Seq: 2, Chunk: "PHP 8.3\n"}, {Seq: 3, Chunk: "done\n"}},
		exitCode: 0,
	}
	client := fake.serve(t)
	printer, out, _ := testPrinter()

	err := followCommand(bounded(t), false, client, printer, start(t, client), make(chan os.Signal))
	if err != nil {
		t.Fatalf("followCommand: %v", err)
	}

	// The API takes one string, and zero-valued options are left to the server.
	if len(fake.started) != 1 || fake.started["command"] != "php artisan about" {
		t.Errorf("request body = %v", fake.started)
	}

	for _, want := range []string{"Laravel 13", "PHP 8.3", "done", "exited 0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunExitsWithTheRemoteExitCode(t *testing.T) {
	cases := []struct {
		name     string
		exitCode any
		want     int
	}{
		{"remote status passes through", 3, 3},
		{"timeout(1) code passes through", 124, 124},
		{"unknown outcome is a plain failure", nil, exit.DeploymentFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := (&fakeCommands{statuses: []string{"running", "failed"}, exitCode: tc.exitCode}).serve(t)
			printer, _, errOut := testPrinter()

			err := followCommand(bounded(t), false, client, printer, start(t, client), make(chan os.Signal))

			if got := exit.Code(err); got != tc.want {
				t.Fatalf("exit code = %d, want %d (err %v)", got, tc.want, err)
			}

			if !strings.Contains(errOut.String(), "failed") {
				t.Errorf("no failure summary:\n%s", errOut.String())
			}
		})
	}
}

func TestInterruptCancelsAndFollowsToTheEnd(t *testing.T) {
	fake := &fakeCommands{statuses: []string{"running"}}
	client := fake.serve(t)
	printer, _, errOut := testPrinter()

	interrupts := make(chan os.Signal, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		interrupts <- os.Interrupt
	}()

	err := followCommand(bounded(t), false, client, printer, start(t, client), interrupts)

	if got := exit.Code(err); got != interrupted {
		t.Fatalf("exit code = %d, want %d (err %v)", got, interrupted, err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	if !fake.cancelled {
		t.Fatal("Ctrl-C did not request cancellation")
	}

	// It waited for the server to confirm, rather than walking away.
	if !strings.Contains(errOut.String(), "cancelled — c1") {
		t.Errorf("expected the confirmed-cancel summary:\n%s", errOut.String())
	}
}

func TestSecondInterruptStopsWaiting(t *testing.T) {
	// The server never confirms the cancel: only a second Ctrl-C gets us out.
	fake := &fakeCommands{statuses: []string{"running"}, stubborn: true}
	client := fake.serve(t)
	printer, _, errOut := testPrinter()

	interrupts := make(chan os.Signal, 2)
	interrupts <- os.Interrupt
	go func() {
		time.Sleep(100 * time.Millisecond)
		interrupts <- os.Interrupt
	}()

	err := followCommand(bounded(t), false, client, printer, start(t, client), interrupts)

	if got := exit.Code(err); got != interrupted {
		t.Fatalf("exit code = %d, want %d (err %v)", got, interrupted, err)
	}

	fake.mu.Lock()
	calls := fake.cancelCalls
	fake.mu.Unlock()

	if calls != 1 {
		t.Errorf("cancel requested %d times, want 1", calls)
	}

	if !strings.Contains(errOut.String(), "not yet confirmed") {
		t.Errorf("expected the unconfirmed-cancel warning:\n%s", errOut.String())
	}
}

func TestForbiddenNamesTheExecAbility(t *testing.T) {
	client := (&fakeCommands{forbidden: true}).serve(t)
	printer, out, errOut := testPrinter()

	_, err := client.RunCommand(bounded(t), "env1", api.CommandOptions{Command: "ls"})
	err = explainExecDenied(printer, err)

	if got := exit.Code(err); got != exit.Auth {
		t.Fatalf("exit code = %d, want %d", got, exit.Auth)
	}

	if !errors.Is(err, errSilent) {
		t.Errorf("the hint is the message; the bare error must not be repeated: %v", err)
	}

	text := out.String() + errOut.String()
	for _, want := range []string{"environments:exec", "Settings → Security → Personal access tokens"} {
		if !strings.Contains(text, want) {
			t.Errorf("hint missing %q:\n%s", want, text)
		}
	}
}

func TestRunRejectsAMissingCommand(t *testing.T) {
	cases := map[string][]string{
		"nothing after --": {"run", "billing-api", "production", "--"},
		"blank after --":   {"run", "billing-api", "production", "--", "  "},
		"no -- at all":     {"run", "billing-api", "production", "ls"},
		"no environment":   {"run", "billing-api"},
	}

	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			root := NewRootCommand("test", "")
			stderr := &bytes.Buffer{}
			root.SetErr(stderr)
			root.SetOut(&bytes.Buffer{})
			root.SetArgs(args)

			err := root.Execute()

			if got := exit.Code(err); got != exit.Usage {
				t.Fatalf("exit code = %d, want %d (err %v)", got, exit.Usage, err)
			}

			if !strings.Contains(stderr.String(), "deployer run <project> <environment> -- <command...>") {
				t.Errorf("usage hint missing:\n%s", stderr.String())
			}
		})
	}
}

func TestCommandLineJoinsLikeSSH(t *testing.T) {
	if got := commandLine([]string{"p", "e", "php", "artisan", "about"}); got != "php artisan about" {
		t.Errorf("got %q", got)
	}

	if got := commandLine([]string{"p", "e", "tail -n 5 log | grep x"}); got != "tail -n 5 log | grep x" {
		t.Errorf("got %q", got)
	}
}

// Regression: an org two-factor requirement got the token/role hint.
func TestMfaRequiredNamesTwoFactorNotTheToken(t *testing.T) {
	client := (&fakeCommands{mfaRequired: true}).serve(t)
	printer, out, errOut := testPrinter()

	_, err := client.RunCommand(bounded(t), "env1", api.CommandOptions{Command: "ls"})
	err = explainExecDenied(printer, err)

	if got := exit.Code(err); got != exit.Auth {
		t.Fatalf("exit code = %d, want %d", got, exit.Auth)
	}

	text := out.String() + errOut.String()
	if !strings.Contains(text, "Your organization requires two-factor — enable it in Settings → Security") {
		t.Errorf("two-factor hint missing:\n%s", text)
	}

	if strings.Contains(text, "environments:exec") {
		t.Errorf("the token hint must not follow a two-factor refusal:\n%s", text)
	}
}
