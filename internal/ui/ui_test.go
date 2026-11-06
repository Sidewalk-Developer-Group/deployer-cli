package ui

import (
	"bytes"
	"testing"
)

func TestWarnStripsControlCharacters(t *testing.T) {
	var stderr bytes.Buffer
	p := &Printer{Err: &stderr}
	p.Warn("%s", "stage '\x1b]52;c;cGF5bG9hZA==\x07evil\x1b[2J\u009b31m\x7f\x00ok'\tnext\nline")
	want := "! stage ']52;c;cGF5bG9hZA==evil[2J31mok'\tnext\nline\n"
	if got := stderr.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
