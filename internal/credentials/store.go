// Package credentials stores API tokens in the OS keychain, falling back to a
// 0600 file only where no keyring exists (headless Linux, some CI images).
// A token here is a live deploy credential — treat it like an SSH key
// (docs/SECURITY_GUIDE.md, docs/CLI_DESIGN.md §5).
package credentials

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
	"gopkg.in/yaml.v3"
)

// service is the keyring service name; the account is the context name, so one
// machine can hold tokens for several instances.
const service = "deployer-cli"

// ErrNotFound means no token is stored for that context.
var ErrNotFound = errors.New("no stored credentials")

// Store reads and writes tokens. FileDir is the directory used by the fallback;
// callers pass the config dir so credentials sit beside config.yml.
type Store struct {
	FileDir string
	// UseFile forces the file backend, skipping the keyring probe. Set by
	// $DEPLOYER_NO_KEYRING for environments where the keyring blocks on a
	// GUI prompt that will never be answered.
	UseFile bool
}

func New(fileDir string) *Store {
	return &Store{
		FileDir: fileDir,
		UseFile: os.Getenv("DEPLOYER_NO_KEYRING") != "",
	}
}

// Get returns the token for a context. UsedFile reports whether the value came
// from the plaintext fallback, so commands can warn.
func (s *Store) Get(context string) (token string, usedFile bool, err error) {
	if !s.UseFile {
		token, err = keyring.Get(service, context)
		switch {
		case err == nil:
			return token, false, nil
		case errors.Is(err, keyring.ErrNotFound):
			// Fall through: an earlier version, or another machine's synced
			// config, may have left a file.
		default:
			// Keyring present but unhappy (locked, no D-Bus). Try the file
			// rather than failing outright.
		}
	}

	token, err = s.fileGet(context)
	if err != nil {
		return "", false, err
	}

	return token, true, nil
}

func (s *Store) Set(context, token string) (usedFile bool, err error) {
	if !s.UseFile {
		if err := keyring.Set(service, context, token); err == nil {
			// Belt and braces: if a token for this context was previously in
			// the fallback file, don't leave a stale plaintext copy behind.
			_ = s.fileDelete(context)

			return false, nil
		}
	}

	if err := s.fileSet(context, token); err != nil {
		return false, err
	}

	return true, nil
}

func (s *Store) Delete(context string) error {
	var kerr error
	if !s.UseFile {
		kerr = keyring.Delete(service, context)
		if errors.Is(kerr, keyring.ErrNotFound) {
			kerr = nil
		}
	}

	ferr := s.fileDelete(context)
	if errors.Is(ferr, ErrNotFound) {
		ferr = nil
	}

	if kerr != nil {
		return kerr
	}

	return ferr
}

func (s *Store) filePath() string {
	return filepath.Join(s.FileDir, "credentials")
}

func (s *Store) fileLoad() (map[string]string, error) {
	raw, err := os.ReadFile(s.filePath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading credentials: %w", err)
	}

	tokens := map[string]string{}
	if err := yaml.Unmarshal(raw, &tokens); err != nil {
		return nil, fmt.Errorf("parsing credentials: %w", err)
	}

	return tokens, nil
}

func (s *Store) fileGet(context string) (string, error) {
	tokens, err := s.fileLoad()
	if err != nil {
		return "", err
	}

	token, ok := tokens[context]
	if !ok || token == "" {
		return "", ErrNotFound
	}

	return token, nil
}

func (s *Store) fileSet(context, token string) error {
	tokens, err := s.fileLoad()
	if err != nil {
		return err
	}

	tokens[context] = token

	return s.fileWrite(tokens)
}

func (s *Store) fileDelete(context string) error {
	tokens, err := s.fileLoad()
	if err != nil {
		return err
	}

	if _, ok := tokens[context]; !ok {
		return ErrNotFound
	}

	delete(tokens, context)

	return s.fileWrite(tokens)
}

func (s *Store) fileWrite(tokens map[string]string) error {
	if err := os.MkdirAll(s.FileDir, 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	encoded, err := yaml.Marshal(tokens)
	if err != nil {
		return fmt.Errorf("encoding credentials: %w", err)
	}

	// Write-then-rename with 0600 throughout: the token must never exist on
	// disk world-readable, not even for the instant before a chmod.
	temp := s.filePath() + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}

	if err := os.Rename(temp, s.filePath()); err != nil {
		return fmt.Errorf("replacing credentials: %w", err)
	}

	return nil
}
