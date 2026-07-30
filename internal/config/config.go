// Package config owns the on-disk CLI configuration: named contexts (one per
// Deployer instance) and the flag/env/file precedence between them. It never
// stores credentials — those live in the credentials package.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Context is one Deployer instance plus the organization to act in. Deployer is
// self-hosted, so a user may legitimately have several (work, staging, a
// customer's instance) — hence contexts rather than a single global URL.
type Context struct {
	APIURL string `yaml:"api_url"`
	// Organization is the human slug the operator typed; OrganizationID is its
	// resolved public id, cached so a deploy needs no lookup round trip.
	Organization   string `yaml:"organization,omitempty"`
	OrganizationID string `yaml:"organization_id,omitempty"`
}

type File struct {
	CurrentContext string              `yaml:"current_context"`
	Contexts       map[string]*Context `yaml:"contexts"`

	path string `yaml:"-"`
}

// ErrNoContext means nothing is configured yet — the caller should point the
// user at `deployer login`.
var ErrNoContext = errors.New("no context configured")

// Dir is the config directory: $DEPLOYER_CONFIG_DIR, else
// $XDG_CONFIG_HOME/deployer, else ~/.config/deployer.
func Dir() (string, error) {
	if custom := os.Getenv("DEPLOYER_CONFIG_DIR"); custom != "" {
		return custom, nil
	}

	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "deployer"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}

	return filepath.Join(home, ".config", "deployer"), nil
}

func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "config.yml"), nil
}

// Load reads the config file, returning an empty one when it does not exist.
func Load() (*File, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}

	file := &File{Contexts: map[string]*Context{}, path: path}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	if err := yaml.Unmarshal(raw, file); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if file.Contexts == nil {
		file.Contexts = map[string]*Context{}
	}
	file.path = path

	return file, nil
}

// Save writes the config atomically. 0700 on the directory and 0600 on the file
// even though no secret lives here: the instance URLs and org slugs of a
// customer's estate are not for other local accounts to read.
func (f *File) Save() error {
	if f.path == "" {
		path, err := Path()
		if err != nil {
			return err
		}
		f.path = path
	}

	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	encoded, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	temp := f.path + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	if err := os.Rename(temp, f.path); err != nil {
		return fmt.Errorf("replacing config: %w", err)
	}

	return nil
}

func (f *File) Names() []string {
	names := make([]string, 0, len(f.Contexts))
	for name := range f.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// Resolve picks the context to act in: an explicit name (flag or
// $DEPLOYER_CONTEXT) wins, else current_context, else the only one defined.
func (f *File) Resolve(name string) (string, *Context, error) {
	if name == "" {
		name = os.Getenv("DEPLOYER_CONTEXT")
	}

	if name != "" {
		context, ok := f.Contexts[name]
		if !ok {
			return "", nil, fmt.Errorf("unknown context %q (have: %s)", name, strings.Join(f.Names(), ", "))
		}

		return name, context, nil
	}

	if f.CurrentContext != "" {
		if context, ok := f.Contexts[f.CurrentContext]; ok {
			return f.CurrentContext, context, nil
		}
	}

	if len(f.Contexts) == 1 {
		for only, context := range f.Contexts {
			return only, context, nil
		}
	}

	if len(f.Contexts) == 0 {
		return "", nil, ErrNoContext
	}

	return "", nil, fmt.Errorf("several contexts configured (%s) and none selected: run `deployer context use <name>` or pass --context", strings.Join(f.Names(), ", "))
}

func (f *File) Set(name string, context *Context) {
	if f.Contexts == nil {
		f.Contexts = map[string]*Context{}
	}

	f.Contexts[name] = context

	if f.CurrentContext == "" {
		f.CurrentContext = name
	}
}

func (f *File) Remove(name string) error {
	if _, ok := f.Contexts[name]; !ok {
		return fmt.Errorf("unknown context %q", name)
	}

	delete(f.Contexts, name)

	if f.CurrentContext == name {
		f.CurrentContext = ""
		if len(f.Contexts) == 1 {
			f.CurrentContext = f.Names()[0]
		}
	}

	return nil
}

// DefaultContextName derives a context name from an instance URL, so
// `deployer login --api https://deploy.acme.io` needs no --context.
func DefaultContextName(apiURL string) string {
	trimmed := apiURL
	for _, prefix := range []string{"https://", "http://"} {
		trimmed = strings.TrimPrefix(trimmed, prefix)
	}
	trimmed = strings.TrimSuffix(trimmed, "/")

	if i := strings.IndexAny(trimmed, "/:"); i > 0 {
		trimmed = trimmed[:i]
	}

	if trimmed == "" {
		return "default"
	}

	return trimmed
}
