// Package ui renders CLI output. Status is always glyph + label (+ colour where
// available) and never colour alone — the same rule the product's UI follows
// (CLAUDE.md, WCAG 2.1 AA), and the only version that survives a pipe.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
)

type Printer struct {
	Out   io.Writer
	Err   io.Writer
	Color bool
	Quiet bool
}

// New decides colour once: an explicit --no-color, NO_COLOR (the de-facto
// standard), or a non-terminal stdout all disable it.
func New(out, errOut io.Writer, colorFlag bool) *Printer {
	color := colorFlag

	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		color = false
	}

	if color && !IsTerminal(out) {
		color = false
	}

	return &Printer{Out: out, Err: errOut, Color: color}
}

// IsTerminal reports whether w is an interactive terminal.
func IsTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}

	info, err := file.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

const (
	reset  = "\x1b[0m"
	dim    = "\x1b[2m"
	bold   = "\x1b[1m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	blue   = "\x1b[34m"
	grey   = "\x1b[90m"
)

func (p *Printer) paint(code, text string) string {
	if !p.Color || code == "" {
		return text
	}

	return code + text + reset
}

func (p *Printer) Printf(format string, args ...any) {
	if p.Quiet {
		return
	}

	fmt.Fprintf(p.Out, format, args...)
}

// Raw writes to stdout even in quiet mode: it is the payload (log text, JSON),
// not commentary.
func (p *Printer) Raw(text string) {
	fmt.Fprint(p.Out, text)
}

func (p *Printer) Dim(format string, args ...any) {
	p.Printf("%s\n", p.paint(dim, fmt.Sprintf(format, args...)))
}

// Warn often carries server-supplied text (stage names, export warnings), so
// control characters are stripped: an ESC or OSC must not reach the terminal.
func (p *Printer) Warn(format string, args ...any) {
	fmt.Fprintf(p.Err, "%s %s\n", p.paint(yellow, "!"), stripControl(fmt.Sprintf(format, args...)))
}

func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		// C0, DEL and C1 (U+009B is a one-byte CSI on some terminals).
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// Fail writes the terminal error. Always stderr, so `deployer logs ... > file`
// keeps the log clean.
func (p *Printer) Fail(format string, args ...any) {
	fmt.Fprintf(p.Err, "%s %s\n", p.paint(red, "✗"), fmt.Sprintf(format, args...))
}

func (p *Printer) Success(format string, args ...any) {
	p.Printf("%s %s\n", p.paint(green, "✓"), fmt.Sprintf(format, args...))
}

func (p *Printer) Info(format string, args ...any) {
	p.Printf("%s %s\n", p.paint(blue, "›"), fmt.Sprintf(format, args...))
}

// Status renders a deployment status as glyph + label.
func (p *Printer) Status(status string) string {
	glyph, color := statusGlyph(status)

	return p.paint(color, glyph+" "+StatusLabel(status))
}

func statusGlyph(status string) (glyph, color string) {
	switch status {
	case api.StatusSucceeded:
		return "✓", green
	case api.StatusFailed:
		return "✗", red
	case api.StatusDegraded:
		return "!", yellow
	case api.StatusCancelled:
		return "⊘", grey
	case api.StatusRolledBack:
		return "↩", yellow
	case api.StatusRunning:
		return "●", blue
	case api.StatusQueued, api.StatusPending:
		return "◌", grey
	case api.StatusApprovalPending:
		return "⏸", yellow
	default:
		return "·", ""
	}
}

// StatusLabel turns a wire status into a human label ("approval_pending" ->
// "awaiting approval").
func StatusLabel(status string) string {
	switch status {
	case api.StatusApprovalPending:
		return "awaiting approval"
	case api.StatusRolledBack:
		return "rolled back"
	case "":
		return "unknown"
	default:
		return strings.ReplaceAll(status, "_", " ")
	}
}

// Duration formats a millisecond duration the way the log console does.
func Duration(ms *int64) string {
	if ms == nil {
		return "—"
	}

	d := time.Duration(*ms) * time.Millisecond

	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// Ago renders a timestamp as a compact relative age.
func Ago(t *time.Time) string {
	if t == nil {
		return "—"
	}

	d := time.Since(*t)

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// ShortSHA trims a commit sha for display without losing identity.
func ShortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}

	return sha[:7]
}

// Table writes aligned columns. Header cells are dimmed rather than boxed —
// this output is meant to be greppable.
func (p *Printer) Table(headers []string, rows [][]string) {
	writer := tabwriter.NewWriter(p.Out, 0, 4, 3, ' ', 0)

	if len(headers) > 0 {
		fmt.Fprintln(writer, p.paint(bold, strings.Join(headers, "\t")))
	}

	for _, row := range rows {
		fmt.Fprintln(writer, strings.Join(row, "\t"))
	}

	_ = writer.Flush()
}

// Green, Red and Muted let intake share the printer's terminal color policy.
func (p *Printer) Green(s string) string { return p.paint(green, s) }
func (p *Printer) Red(s string) string   { return p.paint(red, s) }
func (p *Printer) Muted(s string) string { return p.paint(dim, s) }
