package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
	"golang.org/x/term"
)

type prompter struct {
	in      *bufio.Reader
	out     io.Writer
	printer *ui.Printer
	isTTY   bool
}
type choice struct{ value, label string }

func (p *prompter) label(label, def string) {
	if def != "" {
		label += " " + p.printer.Muted("("+def+")")
	}
	fmt.Fprintln(p.out, p.printer.Green(label+":"))
}
func (p *prompter) failure(err error) { fmt.Fprintln(p.out, p.printer.Red("✗ "+err.Error())) }
func (p *prompter) read() (string, error) {
	fmt.Fprint(p.out, "> ")
	s, err := p.in.ReadString('\n')
	if err != nil {
		return "", exit.Errorf(exit.Usage, "aborted, nothing created")
	}
	// The terminal already echoed the user's Enter; one more newline is the
	// blank line between fields.
	fmt.Fprint(p.out, "\n")
	return strings.TrimSpace(s), nil
}
func (p *prompter) ask(label, def string, validate func(string) error) (string, error) {
	for {
		p.label(label, def)
		s, err := p.read()
		if err != nil {
			return "", err
		}
		if s == "" {
			s = def
		}
		if validate != nil {
			if err = validate(s); err != nil {
				p.failure(err)
				continue
			}
		}
		return s, nil
	}
}
func (p *prompter) askSecret(label string) (string, error) {
	if !p.isTTY {
		return p.ask(label, "", nil)
	}
	p.label(label, "")
	fmt.Fprint(p.out, "> ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprint(p.out, "\n\n")
	if err != nil {
		return "", exit.Errorf(exit.Usage, "aborted, nothing created")
	}
	return string(b), nil
}
func selectChoice(s string, options []choice) (string, error) {
	if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= len(options) {
		return options[n-1].value, nil
	}
	for _, o := range options {
		if s == o.value || s == o.label {
			return o.value, nil
		}
	}
	values := make([]string, len(options))
	for i, o := range options {
		values[i] = o.label
	}
	return "", fmt.Errorf("unknown value %q (valid values: %s)", s, strings.Join(values, ", "))
}
func (p *prompter) choose(label string, options []choice, def string) (string, error) {
	for {
		p.label(label, def)
		for i, o := range options {
			mark := ""
			if o.value == def {
				mark = " (default)"
			}
			fmt.Fprintf(p.out, "  %d) %s%s\n", i+1, o.label, mark)
		}
		s, err := p.read()
		if err != nil {
			return "", err
		}
		if s == "" {
			s = def
		}
		v, err := selectChoice(s, options)
		if err != nil {
			p.failure(err)
			continue
		}
		return v, nil
	}
}
func (p *prompter) chooseMany(label string, options []choice) ([]string, error) {
	for {
		p.label(label+" (optional; comma-separated)", "")
		for i, o := range options {
			fmt.Fprintf(p.out, "  %d) %s\n", i+1, o.label)
		}
		s, err := p.read()
		if err != nil {
			return nil, err
		}
		if s == "" {
			return nil, nil
		}
		var result []string
		valid := true
		seen := map[string]bool{}
		for _, part := range strings.Split(s, ",") {
			v, e := selectChoice(strings.TrimSpace(part), options)
			if e != nil {
				p.failure(e)
				valid = false
				break
			}
			if !seen[v] {
				result = append(result, v)
				seen[v] = true
			}
		}
		if valid {
			return result, nil
		}
	}
}
func (p *prompter) confirm(label string) (bool, error) {
	s, err := p.ask(label+" [y/N]", "", nil)
	return strings.EqualFold(s, "y") || strings.EqualFold(s, "yes"), err
}
