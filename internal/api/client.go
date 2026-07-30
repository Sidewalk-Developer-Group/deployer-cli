// Package api is a thin typed client over the Deployer REST API. It holds no
// deployment logic: every method maps onto one documented endpoint
// (docs/API_SPECIFICATION.md).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

// Client talks to one Deployer instance. Deployer is self-hosted, so the base
// URL is per-install and always explicit — there is no default host.
type Client struct {
	baseURL *url.URL
	token   string
	version string
	http    *http.Client
	debug   io.Writer
}

type Option func(*Client)

// WithTimeout bounds a single request. The follow loop makes many short
// requests rather than one long one, so this stays small.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http.Timeout = d }
}

// WithDebug dumps request/response metadata. Never the Authorization header:
// the whole point of keychain storage is that the token doesn't leak into
// terminal scrollback or CI logs.
func WithDebug(w io.Writer) Option {
	return func(c *Client) { c.debug = w }
}

// New builds a client for baseURL ("https://deployer.example.com", with or
// without a trailing /api/v1). An empty token is allowed: unauthenticated
// endpoints (cli/version) work, and authenticated ones fail with 401.
func New(baseURL, token, version string, opts ...Option) (*Client, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return nil, fmt.Errorf("no API URL configured: pass --api or run `deployer login --api <url>`")
	}

	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid API URL %q: %w", baseURL, err)
	}

	if parsed.Host == "" {
		return nil, fmt.Errorf("invalid API URL %q: no host", baseURL)
	}

	// Accept a URL that already points at the API root so a pasted value from
	// the browser's network tab works.
	parsed.Path = strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), "/api/v1"), "/api")
	parsed.RawQuery = ""
	parsed.Fragment = ""

	client := &Client{
		baseURL: parsed,
		token:   token,
		version: version,
		http:    &http.Client{Timeout: 30 * time.Second},
	}

	for _, opt := range opts {
		opt(client)
	}

	return client, nil
}

// BaseURL is the instance root (no /api/v1), for printing and for building
// browser links.
func (c *Client) BaseURL() string {
	return c.baseURL.String()
}

func (c *Client) HasToken() bool { return c.token != "" }

func (c *Client) endpoint(path string, query url.Values) string {
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/" + strings.TrimLeft(path, "/")

	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}

	return u.String()
}

// do performs a request and decodes a successful JSON body into out (which may
// be nil). Errors are *Problem for an API refusal and *TransportError for a
// network failure, so callers can map them onto exit codes.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	endpoint := c.endpoint(path, query)

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}

	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", fmt.Sprintf("deployer-cli/%s (%s/%s)", c.version, runtime.GOOS, runtime.GOARCH))

	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}

	if c.debug != nil {
		fmt.Fprintf(c.debug, "> %s %s\n", method, endpoint)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return &TransportError{URL: endpoint, Err: err}
	}
	defer response.Body.Close()

	// Bounded read: a runaway body must not exhaust memory in CI.
	raw, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return &TransportError{URL: endpoint, Err: err}
	}

	if c.debug != nil {
		fmt.Fprintf(c.debug, "< %d (%d bytes)\n", response.StatusCode, len(raw))
	}

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return parseProblem(response.StatusCode, raw)
	}

	if out == nil || len(raw) == 0 {
		return nil
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return notJSON(c.baseURL.String(), path, response.Header.Get("Content-Type"), raw, err)
	}

	return nil
}

// notJSON explains a 2xx that isn't JSON. Overwhelmingly this is an instance
// whose SPA catch-all answered a route it doesn't have: older Deployer builds
// served the Vue shell for unknown /api paths, so the client saw `200 text/html`
// and failed on "invalid character '<'" — which reads like a client bug and hides
// the real cause. Say the real cause instead.
func notJSON(baseURL, path, contentType string, body []byte, err error) error {
	looksLikeHTML := strings.Contains(strings.ToLower(contentType), "html") ||
		strings.HasPrefix(strings.TrimSpace(string(body)), "<")

	if looksLikeHTML {
		return fmt.Errorf(
			"%s answered with a web page instead of JSON (path: %s).\n"+
				"That instance most likely has no such endpoint — its Deployer build is older than this CLI.\n"+
				"Confirm --api points at the right instance, run `deployer version --check`, then update the instance.",
			baseURL, path,
		)
	}

	return fmt.Errorf("decoding %s response from %s (content-type %q): %w", path, baseURL, contentType, err)
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, body, out)
}
