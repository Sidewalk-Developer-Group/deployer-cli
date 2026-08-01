package cli

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

// A bad invocation must say what to type next. Cobra's bare "accepts 2 arg(s),
// received 0" is what this replaces.
func TestWantArgsPrintsTheExpectedFormAndUsage(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := &cobra.Command{Use: "deploy <project> <environment>"}
	cmd.SetErr(out)
	cmd.SetOut(out)

	err := wantArgs(2, 2, "deployer deploy <project> <environment>   — slugs")(cmd, []string{})

	if got := exit.Code(err); got != exit.Usage {
		t.Fatalf("exit code = %d, want %d", got, exit.Usage)
	}

	text := out.String()
	for _, want := range []string{"needs 2 argument(s); none were given", "deployer deploy <project> <environment>", "Usage:"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
}

func TestWantArgsEchoesWhatWasTyped(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := &cobra.Command{Use: "deploy"}
	cmd.SetErr(out)

	_ = wantArgs(2, 2, "hint")(cmd, []string{"billing-api"})

	if !strings.Contains(out.String(), "You typed: deploy billing-api") {
		t.Errorf("expected the typed arguments echoed back:\n%s", out.String())
	}
}

func TestWantArgsAcceptsValidCounts(t *testing.T) {
	cmd := &cobra.Command{Use: "status"}

	for _, args := range [][]string{{"a"}, {"a", "b"}} {
		if err := wantArgs(1, 2, "hint")(cmd, args); err != nil {
			t.Errorf("wantArgs(1,2) rejected %v: %v", args, err)
		}
	}

	if err := wantArgs(1, -1, "hint")(cmd, []string{"a", "b", "c"}); err != nil {
		t.Errorf("unbounded max rejected 3 args: %v", err)
	}
}

func printerTo(buf *bytes.Buffer) *ui.Printer {
	return &ui.Printer{Out: buf, Err: buf}
}

func TestExplainResolveFailureNamesTheLikelyMistake(t *testing.T) {
	cases := []struct {
		name        string
		project     string
		environment string
		want        string
	}{
		{"uuid as project", "019f50c5-33a8-735c-92c7-1295bccadfaa", "production", "looks like an id"},
		{"uuid as environment", "billing-api", "019f50c5-33a8-735c-92c7-1295bccadfaa", "looks like an id"},
		{"both halves given", "billing-api", "prod", "envs ls billing-api"},
		{"project only", "billing-api", "", "projects ls"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			buf := &bytes.Buffer{}

			err := explainResolveFailure(printerTo(buf), &api.Problem{Status: http.StatusNotFound, Title: "Not found."}, testCase.project, testCase.environment)

			if got := exit.Code(err); got != exit.Usage {
				t.Fatalf("exit code = %d, want %d", got, exit.Usage)
			}

			if !strings.Contains(buf.String(), testCase.want) {
				t.Errorf("guidance missing %q:\n%s", testCase.want, buf.String())
			}

			// The org is the other thing that is commonly wrong.
			if !strings.Contains(buf.String(), "--org") {
				t.Errorf("guidance should mention --org:\n%s", buf.String())
			}
		})
	}
}

func TestExplainResolveFailureLeavesOtherErrorsAlone(t *testing.T) {
	buf := &bytes.Buffer{}

	// A 403 is not a "you typed the wrong slug" problem; it must not be dressed
	// up as one, and its own message has to survive.
	original := &api.Problem{Status: http.StatusForbidden, Title: "Forbidden."}
	err := explainResolveFailure(printerTo(buf), original, "billing-api", "production")

	if !errors.Is(err, error(original)) {
		t.Fatalf("expected the original error back, got %v", err)
	}

	if buf.Len() != 0 {
		t.Errorf("nothing should have been printed:\n%s", buf.String())
	}
}

func TestLooksLikeUUID(t *testing.T) {
	valid := []string{"019f50c5-33a8-735c-92c7-1295bccadfaa", "0198F3C1-6B2F-7A41-9C88-3F1C0D5E7A22"}
	invalid := []string{"billing-api", "", "production", "019f50c5-33a8-735c-92c7-1295bccadfa", "019f50c5x33a8x735cx92c7x1295bccadfaa", "zzzzzzzz-33a8-735c-92c7-1295bccadfaa"}

	for _, value := range valid {
		if !looksLikeUUID(value) {
			t.Errorf("looksLikeUUID(%q) = false, want true", value)
		}
	}

	for _, value := range invalid {
		if looksLikeUUID(value) {
			t.Errorf("looksLikeUUID(%q) = true, want false", value)
		}
	}
}
