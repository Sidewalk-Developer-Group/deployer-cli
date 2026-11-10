package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"
)

func TestProjectsCommandEnvironmentsAndHelp(t *testing.T) {
	t.Setenv("DEPLOYER_CONFIG_DIR", t.TempDir())
	t.Setenv("DEPLOYER_TOKEN", "token")
	t.Setenv("DEPLOYER_API_URL", "https://deployer.test")
	t.Setenv("DEPLOYER_ORG", "")
	requests, environmentRequests := 0, 0
	createClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/api/v1/users/me":
			fmt.Fprint(w, `{"data":{"organizations":[{"id":"org-id","slug":"acme"}]}}`)
		case "/api/v1/organizations/org-id/resolve":
			if r.URL.Query().Get("project") != "billing-api" || r.URL.Query().Has("environment") {
				t.Fatal(r.URL)
			}
			fmt.Fprint(w, `{"data":{"project":{"id":"project-id","slug":"billing-api"}}}`)
		case "/api/v1/projects/project-id/environments":
			environmentRequests++
			fmt.Fprint(w, `{"data":[{"id":"env-id","slug":"production","name":"Production","branch":"main"}]}`)
		default:
			t.Fatal(r.URL)
		}
	})
	var out bytes.Buffer
	cmd := newProjectsCommand(&Factory{})
	cmd.SetArgs([]string{"billing-api"})
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	// Factory.Printer writes to os.Stdout, independently of cmd.SetOut.
	if environmentRequests != 1 {
		t.Fatalf("environment requests = %d, want 1", environmentRequests)
	}
	requests = 0
	cmd = newProjectsCommand(&Factory{})
	cmd.SetArgs([]string{})
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("help made %d HTTP requests", requests)
	}
}
