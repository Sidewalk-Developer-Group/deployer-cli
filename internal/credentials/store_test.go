package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fileStore exercises the fallback backend directly: CI images have no keyring,
// and that is exactly the path most likely to leak a token to disk badly.
func fileStore(t *testing.T) *Store {
	t.Helper()

	return &Store{FileDir: t.TempDir(), UseFile: true}
}

func TestFileBackendRoundTrips(t *testing.T) {
	store := fileStore(t)

	if _, _, err := store.Get("prod"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	usedFile, err := store.Set("prod", "token-abc")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !usedFile {
		t.Fatal("expected the file backend to report itself")
	}

	token, fromFile, err := store.Get("prod")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if token != "token-abc" || !fromFile {
		t.Fatalf("got %q (fromFile=%v)", token, fromFile)
	}
}

func TestCredentialsFileIsNotWorldReadable(t *testing.T) {
	store := fileStore(t)

	if _, err := store.Set("prod", "token-abc"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	info, err := os.Stat(filepath.Join(store.FileDir, "credentials"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// A live deploy credential: 0600 or the whole keychain story is theatre.
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("credentials mode = %v, want 0600", mode)
	}
}

func TestSeparateContextsKeepSeparateTokens(t *testing.T) {
	store := fileStore(t)

	if _, err := store.Set("prod", "token-prod"); err != nil {
		t.Fatalf("Set prod: %v", err)
	}
	if _, err := store.Set("staging", "token-staging"); err != nil {
		t.Fatalf("Set staging: %v", err)
	}

	if token, _, _ := store.Get("prod"); token != "token-prod" {
		t.Fatalf("prod token = %q", token)
	}

	if err := store.Delete("prod"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, _, err := store.Get("prod"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected prod to be gone, got %v", err)
	}

	if token, _, _ := store.Get("staging"); token != "token-staging" {
		t.Fatalf("staging token = %q after deleting prod", token)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	store := fileStore(t)

	if err := store.Delete("never-existed"); err != nil {
		t.Fatalf("Delete on a missing context should be a no-op, got %v", err)
	}
}
