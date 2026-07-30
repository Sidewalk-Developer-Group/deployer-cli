package config

import (
	"os"
	"path/filepath"
	"testing"
)

// isolate points the config package at a temp dir so tests never touch a real
// developer's ~/.config/deployer.
func isolate(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("DEPLOYER_CONFIG_DIR", dir)
	t.Setenv("DEPLOYER_CONTEXT", "")

	return dir
}

func TestLoadReturnsEmptyFileWhenMissing(t *testing.T) {
	isolate(t)

	file, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(file.Contexts) != 0 {
		t.Fatalf("expected no contexts, got %d", len(file.Contexts))
	}

	if _, _, err := file.Resolve(""); err != ErrNoContext {
		t.Fatalf("expected ErrNoContext, got %v", err)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	dir := isolate(t)

	file, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	file.Set("prod", &Context{APIURL: "https://deploy.example.com", Organization: "acme", OrganizationID: "org-uuid"})

	if err := file.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "config.yml"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("config mode = %v, want 0600", mode)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	name, context, err := reloaded.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if name != "prod" || context.Organization != "acme" || context.OrganizationID != "org-uuid" {
		t.Fatalf("unexpected context %q: %+v", name, context)
	}
}

func TestResolvePrefersExplicitNameThenCurrentThenOnly(t *testing.T) {
	isolate(t)

	file := &File{Contexts: map[string]*Context{
		"a": {APIURL: "https://a.example.com"},
		"b": {APIURL: "https://b.example.com"},
	}}

	if name, _, err := file.Resolve("b"); err != nil || name != "b" {
		t.Fatalf("explicit name: got %q, %v", name, err)
	}

	file.CurrentContext = "a"
	if name, _, err := file.Resolve(""); err != nil || name != "a" {
		t.Fatalf("current context: got %q, %v", name, err)
	}

	// Several contexts and none selected must be an error, never a guess: the
	// wrong pick would deploy to the wrong instance.
	file.CurrentContext = ""
	if _, _, err := file.Resolve(""); err == nil {
		t.Fatal("expected an error when several contexts and none selected")
	}

	delete(file.Contexts, "b")
	if name, _, err := file.Resolve(""); err != nil || name != "a" {
		t.Fatalf("single context: got %q, %v", name, err)
	}
}

func TestResolveHonoursContextEnvVar(t *testing.T) {
	isolate(t)
	t.Setenv("DEPLOYER_CONTEXT", "staging")

	file := &File{
		CurrentContext: "prod",
		Contexts: map[string]*Context{
			"prod":    {APIURL: "https://prod.example.com"},
			"staging": {APIURL: "https://staging.example.com"},
		},
	}

	name, _, err := file.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if name != "staging" {
		t.Fatalf("got %q, want staging (DEPLOYER_CONTEXT)", name)
	}
}

func TestResolveRejectsUnknownName(t *testing.T) {
	isolate(t)

	file := &File{Contexts: map[string]*Context{"prod": {APIURL: "https://prod.example.com"}}}

	if _, _, err := file.Resolve("nope"); err == nil {
		t.Fatal("expected an error for an unknown context")
	}
}

func TestRemoveClearsCurrentContext(t *testing.T) {
	isolate(t)

	file := &File{CurrentContext: "prod", Contexts: map[string]*Context{
		"prod":    {APIURL: "https://prod.example.com"},
		"staging": {APIURL: "https://staging.example.com"},
	}}

	if err := file.Remove("prod"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// One left, so it becomes current rather than leaving the CLI unusable.
	if file.CurrentContext != "staging" {
		t.Fatalf("current context = %q, want staging", file.CurrentContext)
	}

	if err := file.Remove("prod"); err == nil {
		t.Fatal("expected an error removing a context twice")
	}
}

func TestDefaultContextName(t *testing.T) {
	cases := map[string]string{
		"https://deploy.acme.io":            "deploy.acme.io",
		"https://deploy.acme.io/":           "deploy.acme.io",
		"http://deployer.test:8080/api/v1":  "deployer.test",
		"deployer.internal":                 "deployer.internal",
		"":                                  "default",
		"https://deploy.acme.io/sub/path/x": "deploy.acme.io",
	}

	for input, want := range cases {
		if got := DefaultContextName(input); got != want {
			t.Errorf("DefaultContextName(%q) = %q, want %q", input, got, want)
		}
	}
}
