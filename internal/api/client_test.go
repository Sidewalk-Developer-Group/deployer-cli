package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewNormalisesBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://deploy.example.com":         "https://deploy.example.com",
		"https://deploy.example.com/":        "https://deploy.example.com",
		"https://deploy.example.com/api":     "https://deploy.example.com",
		"https://deploy.example.com/api/v1":  "https://deploy.example.com",
		"https://deploy.example.com/api/v1/": "https://deploy.example.com",
		// A bare host is assumed https, never http: a token must not be sent
		// in the clear because someone omitted the scheme.
		"deploy.example.com": "https://deploy.example.com",
	}

	for input, want := range cases {
		client, err := New(input, "token", "1.0.0")
		if err != nil {
			t.Fatalf("New(%q): %v", input, err)
		}

		if got := client.BaseURL(); got != want {
			t.Errorf("New(%q).BaseURL() = %q, want %q", input, got, want)
		}
	}
}

func TestNewRejectsEmptyAndInvalidURLs(t *testing.T) {
	for _, input := range []string{"", "   ", "https://"} {
		if _, err := New(input, "token", "1.0.0"); err == nil {
			t.Errorf("New(%q) should have failed", input)
		}
	}
}

func TestRequestsCarryAuthorizationAndUserAgent(t *testing.T) {
	var gotAuth, gotAgent, gotPath, gotQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAgent = r.Header.Get("User-Agent")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"project":{"id":"p1","slug":"billing-api"},"environment":{"id":"e1","slug":"production"}}}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "secret-token", "1.2.3")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	target, err := client.Resolve(context.Background(), "org-uuid", "billing-api", "production")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}

	if !strings.HasPrefix(gotAgent, "deployer-cli/1.2.3 (") {
		t.Errorf("User-Agent = %q", gotAgent)
	}

	if gotPath != "/api/v1/organizations/org-uuid/resolve" {
		t.Errorf("path = %q", gotPath)
	}

	if !strings.Contains(gotQuery, "project=billing-api") || !strings.Contains(gotQuery, "environment=production") {
		t.Errorf("query = %q", gotQuery)
	}

	if target.Environment == nil || target.Environment.ID != "e1" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolveOmitsAnEmptyEnvironment(t *testing.T) {
	var gotQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":{"project":{"id":"p1"},"environment":null}}`))
	}))
	defer server.Close()

	client, _ := New(server.URL, "token", "test")

	if _, err := client.Resolve(context.Background(), "org", "billing-api", ""); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if strings.Contains(gotQuery, "environment") {
		t.Errorf("query should omit environment, got %q", gotQuery)
	}
}

func TestProblemResponsesBecomeTypedErrors(t *testing.T) {
	body := `{"type":"https://deployer.dev/problems/validation-failed","title":"Validation failed.","status":422,` +
		`"detail":"The given data was invalid.","errors":{"branch":["The branch field is invalid."]}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(422)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client, _ := New(server.URL, "token", "test")

	_, err := client.Deploy(context.Background(), "env-1", TriggerOptions{})

	var problem *Problem
	if !errors.As(err, &problem) {
		t.Fatalf("expected *Problem, got %T: %v", err, err)
	}

	if problem.Status != 422 || problem.Slug() != "validation-failed" {
		t.Fatalf("unexpected problem: %+v", problem)
	}

	// The field errors are the actionable part of a 422 and must reach the user.
	if !strings.Contains(problem.Error(), "branch: The branch field is invalid.") {
		t.Errorf("message lost the field errors: %q", problem.Error())
	}
}

func TestNonJsonErrorBodiesStillProduceAProblem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	defer server.Close()

	client, _ := New(server.URL, "token", "test")

	_, err := client.Deployment(context.Background(), "d1")

	var problem *Problem
	if !errors.As(err, &problem) {
		t.Fatalf("expected *Problem, got %T", err)
	}

	if problem.Status != 502 || problem.Title != "Bad Gateway" {
		t.Fatalf("unexpected problem: %+v", problem)
	}
}

func TestUnreachableInstanceIsATransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // nothing is listening now

	client, _ := New(url, "token", "test")

	_, err := client.Me(context.Background())

	var transport *TransportError
	if !errors.As(err, &transport) {
		t.Fatalf("expected *TransportError, got %T: %v", err, err)
	}
}

func TestDeployOmitsUnsetTriggerOptions(t *testing.T) {
	var body string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(raw)
		body = string(raw)

		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"data":{"id":"d1","status":"queued"}}`))
	}))
	defer server.Close()

	client, _ := New(server.URL, "token", "test")

	deployment, err := client.Deploy(context.Background(), "env-1", TriggerOptions{Branch: "main"})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// Sending is_dry_run=false or an empty commit_sha would override server-side
	// defaults with the CLI's zero values.
	for _, unwanted := range []string{"commit_sha", "is_dry_run", "comment", "scheduled_at", "include_optional_stages"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body should omit %s, got %s", unwanted, body)
		}
	}

	if deployment.Status != StatusQueued {
		t.Errorf("status = %q", deployment.Status)
	}
}

func TestLogsPassesTheSeqCursor(t *testing.T) {
	var gotQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":[{"seq":8,"stream":"stdout","chunk":"hello"}]}`))
	}))
	defer server.Close()

	client, _ := New(server.URL, "token", "test")

	chunks, err := client.Logs(context.Background(), "d1", 7, 500)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}

	if !strings.Contains(gotQuery, "after=7") || !strings.Contains(gotQuery, "per_page=500") {
		t.Errorf("query = %q", gotQuery)
	}

	if len(chunks) != 1 || chunks[0].Seq != 8 {
		t.Fatalf("unexpected chunks: %+v", chunks)
	}
}

func TestLogsOmitsTheCursorOnTheFirstPage(t *testing.T) {
	var gotQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client, _ := New(server.URL, "token", "test")

	if _, err := client.Logs(context.Background(), "d1", 0, 0); err != nil {
		t.Fatalf("Logs: %v", err)
	}

	if strings.Contains(gotQuery, "after=") {
		t.Errorf("query should omit after on the first page, got %q", gotQuery)
	}
}

func TestLatestDeploymentReturnsNilWhenNeverDeployed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client, _ := New(server.URL, "token", "test")

	deployment, err := client.LatestDeployment(context.Background(), "env-1")
	if err != nil {
		t.Fatalf("LatestDeployment: %v", err)
	}

	if deployment != nil {
		t.Fatalf("expected nil, got %+v", deployment)
	}
}

func TestTerminalAndSuccessClassification(t *testing.T) {
	terminal := map[string]bool{
		StatusPending: false, StatusApprovalPending: false, StatusQueued: false, StatusRunning: false,
		StatusSucceeded: true, StatusFailed: true, StatusDegraded: true, StatusCancelled: true, StatusRolledBack: true,
	}

	for status, want := range terminal {
		if got := IsTerminal(status); got != want {
			t.Errorf("IsTerminal(%q) = %v, want %v", status, got, want)
		}
	}

	// Only a clean success exits 0: degraded means some targets failed.
	if !Succeeded(StatusSucceeded) {
		t.Error("succeeded should count as success")
	}

	for _, status := range []string{StatusDegraded, StatusFailed, StatusCancelled, StatusRolledBack} {
		if Succeeded(status) {
			t.Errorf("%q must not count as success", status)
		}
	}
}
