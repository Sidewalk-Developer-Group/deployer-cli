package exit

import (
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
)

// The exit codes are a public contract that CI branches on
// (docs/CLI_DESIGN.md §3.5), so each mapping is pinned here.
func TestCodeMapsProblemStatuses(t *testing.T) {
	cases := []struct {
		status int
		want   int
	}{
		{401, Auth},
		{403, Auth},
		{404, Usage},
		{422, Usage},
		{400, Usage},
		{409, API},
		{402, API},
		{429, API},
		{500, API},
		{503, API},
	}

	for _, testCase := range cases {
		got := Code(&api.Problem{Status: testCase.status, Title: "x"})
		if got != testCase.want {
			t.Errorf("status %d -> %d, want %d", testCase.status, got, testCase.want)
		}
	}
}

func TestCodeUnwrapsWrappedProblems(t *testing.T) {
	wrapped := fmt.Errorf("while deploying: %w", &api.Problem{Status: 403, Title: "Forbidden."})

	if got := Code(wrapped); got != Auth {
		t.Fatalf("wrapped 403 -> %d, want %d", got, Auth)
	}
}

func TestClassifiedErrorWins(t *testing.T) {
	// A failed deployment is exit 1 even though the API call itself was a 202
	// success — the deployment's outcome is what CI cares about.
	err := With(DeploymentFailed, errors.New("deployment failed"))

	if got := Code(err); got != DeploymentFailed {
		t.Fatalf("got %d, want %d", got, DeploymentFailed)
	}

	// Even when it wraps a problem with a different natural mapping.
	nested := With(AwaitingApproval, &api.Problem{Status: 500})
	if got := Code(nested); got != AwaitingApproval {
		t.Fatalf("got %d, want %d", got, AwaitingApproval)
	}
}

func TestTransportFailureIsApiCode(t *testing.T) {
	err := &api.TransportError{URL: "https://deploy.example.com", Err: &net.OpError{Op: "dial"}}

	if got := Code(err); got != API {
		t.Fatalf("got %d, want %d", got, API)
	}
}

func TestNilAndUnknownErrors(t *testing.T) {
	if got := Code(nil); got != OK {
		t.Fatalf("nil -> %d, want %d", got, OK)
	}

	if got := Code(errors.New("something else")); got != API {
		t.Fatalf("unknown -> %d, want %d", got, API)
	}
}

func TestCodesAreStable(t *testing.T) {
	// Reassigning any of these would silently change the meaning of a pipeline's
	// failure. Adding is fine; changing is not.
	for name, pair := range map[string][2]int{
		"OK":               {OK, 0},
		"DeploymentFailed": {DeploymentFailed, 1},
		"Usage":            {Usage, 2},
		"Auth":             {Auth, 3},
		"API":              {API, 4},
		"AwaitingApproval": {AwaitingApproval, 5},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %d, want %d", name, pair[0], pair[1])
		}
	}
}
