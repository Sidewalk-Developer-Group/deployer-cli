package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Problem is an RFC 7807 error body as returned by every non-2xx Deployer
// response (docs/API_SPECIFICATION.md "Errors").
type Problem struct {
	Type   string              `json:"type"`
	Title  string              `json:"title"`
	Status int                 `json:"status"`
	Detail string              `json:"detail"`
	Errors map[string][]string `json:"errors"`

	// Extensions carries problem-specific members (e.g. the quota fields on a
	// 402) that the typed fields above don't cover.
	Extensions map[string]any `json:"-"`
}

func (p *Problem) Error() string {
	var b strings.Builder

	switch {
	case p.Detail != "":
		b.WriteString(p.Detail)
	case p.Title != "":
		b.WriteString(p.Title)
	default:
		b.WriteString(http.StatusText(p.Status))
	}

	// Validation failures are the one case where the field errors, not the
	// detail line, are the actionable part.
	if len(p.Errors) > 0 {
		fields := make([]string, 0, len(p.Errors))
		for field := range p.Errors {
			fields = append(fields, field)
		}
		sort.Strings(fields)

		for _, field := range fields {
			for _, message := range p.Errors[field] {
				b.WriteString("\n  " + field + ": " + message)
			}
		}
	}

	return b.String()
}

// Slug is the trailing path segment of the problem type URI
// (https://deployer.dev/problems/<slug>), or "" for about:blank.
func (p *Problem) Slug() string {
	if p.Type == "" || p.Type == "about:blank" {
		return ""
	}

	if i := strings.LastIndex(p.Type, "/"); i >= 0 {
		return p.Type[i+1:]
	}

	return p.Type
}

func parseProblem(status int, body []byte) *Problem {
	problem := &Problem{Status: status}

	if err := json.Unmarshal(body, problem); err != nil || (problem.Title == "" && problem.Detail == "") {
		// A proxy, a maintenance page, or an HTML error — surface something
		// truthful rather than pretending we got a problem document.
		problem.Title = http.StatusText(status)
		problem.Detail = firstLine(body)
	}

	if problem.Status == 0 {
		problem.Status = status
	}

	var extensions map[string]any
	if err := json.Unmarshal(body, &extensions); err == nil {
		for _, known := range []string{"type", "title", "status", "detail", "errors"} {
			delete(extensions, known)
		}
		if len(extensions) > 0 {
			problem.Extensions = extensions
		}
	}

	return problem
}

func firstLine(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}

	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}

	const max = 200
	if len(text) > max {
		text = text[:max] + "…"
	}

	return text
}

// TransportError is a failure to reach the API at all, as opposed to an API
// response that says no.
type TransportError struct {
	URL string
	Err error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("cannot reach %s: %v", e.URL, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }
