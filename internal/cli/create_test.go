package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

func scripted(input string) (*prompter, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &prompter{in: bufio.NewReader(strings.NewReader(input)), out: out, printer: &ui.Printer{Out: out, Err: out}}, out
}

// handlerTransport uses httptest responses without a listener, so intake tests
// also run in sandboxes that prohibit binding local TCP ports.
type handlerTransport struct{ handler http.Handler }

func (h handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w.Result(), nil
}
func createClient(t *testing.T, h http.HandlerFunc) *api.Client {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = handlerTransport{h}
	t.Cleanup(func() { http.DefaultTransport = previous })
	c, e := api.New("https://deployer.test", "token", "test")
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func TestPromptDefaultsChoicesAndEOF(t *testing.T) {
	p, _ := scripted("\n2\nphp\n")
	if v, e := p.ask("Branch", "main", nil); v != "main" || e != nil {
		t.Fatalf("%q %v", v, e)
	}
	for i := 0; i < 2; i++ {
		if v, e := p.choose("Type", choices("laravel", "php"), "laravel"); v != "php" || e != nil {
			t.Fatalf("%q %v", v, e)
		}
	}
	if _, e := p.ask("Name", "", nil); exit.Code(e) != exit.Usage || !strings.Contains(e.Error(), "aborted, nothing created") {
		t.Fatal(e)
	}
}
func TestPromptValidationReasks(t *testing.T) {
	p, out := scripted("Bad slug\ngood-slug\n")
	v, e := p.ask("Slug", "", slugValid)
	if e != nil || v != "good-slug" || strings.Count(out.String(), "Slug:") != 2 || !strings.Contains(out.String(), "✗") {
		t.Fatalf("%q %v %s", v, e, out)
	}
}
func TestCreateProjectIntake(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		retry       bool
		posts       int
	}{
		{"default slug", "Billing API\n\n\n3\n\n\ny\n", false, 1},
		{"422 slug", "Billing API\n\n\n3\n\n\ny\nbilling-fixed\ny\n", true, 2},
		{"decline", "Billing API\n\n\n3\n\n\nn\n", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			var body map[string]any
			c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "GET" {
					fmt.Fprint(w, `{"data":[]}`)
					return
				}
				count++
				json.NewDecoder(r.Body).Decode(&body)
				if tc.retry && count == 1 {
					w.WriteHeader(422)
					fmt.Fprint(w, `{"title":"Invalid","errors":{"slug":["Already taken"]}}`)
					return
				}
				w.WriteHeader(201)
				fmt.Fprint(w, `{"data":{"id":"p1","name":"Billing API","slug":"billing-api"}}`)
			})
			p, output := scripted(tc.input)
			printer, _, _ := testPrinter()
			e := createProject(context.Background(), c, printer, p, "o", projectCreateOptions{})
			if e != nil {
				t.Fatal(e)
			}
			if count != tc.posts {
				t.Fatalf("posts=%d", count)
			}
			if tc.posts > 0 {
				if body["project_type"] != "node" {
					t.Fatal(body)
				}
				want := "billing-api"
				if tc.retry {
					want = "billing-fixed"
				}
				if body["slug"] != want {
					t.Fatal(body)
				}
			}
			// Scripted stdin is not echoed, so the user's Enter is missing here;
			// on a terminal it lands after "> " and the "\n" is the blank line.
			if !strings.Contains(output.String(), "Name:\n> \nSlug (billing-api):\n> \n") {
				t.Fatal(output.String())
			}
			if tc.retry {
				if strings.Count(output.String(), "Name:\n") != 1 || !strings.Contains(output.String(), "Already taken") {
					t.Fatal(output.String())
				}
			}
		})
	}
}
func TestCreateProjectFlags(t *testing.T) {
	var body map[string]any
	var repoBody map[string]any
	c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET":
			fmt.Fprint(w, `{"data":[{"id":"g1","name":"Apps"}]}`)
		case strings.HasSuffix(r.URL.Path, "repositories"):
			json.NewDecoder(r.Body).Decode(&repoBody)
			fmt.Fprint(w, `{"data":{"id":"r1"}}`)
		default:
			json.NewDecoder(r.Body).Decode(&body)
			fmt.Fprint(w, `{"data":{"id":"p1","name":"App","slug":"app"}}`)
		}
	})
	printer, _, errOut := testPrinter()
	o := projectCreateOptions{CreateProjectOptions: api.CreateProjectOptions{Name: "App", Description: "desc", ProjectType: "php", PHPVersion: "8.3", BasePath: "/app"}, Group: "Apps", repositoryOptions: repositoryOptions{api.CreateRepositoryOptions{RepoURL: "https://github.com/a/b", CloneToken: "secret", CloneUsername: "user", DefaultBranch: "develop"}}}
	if e := createProject(context.Background(), c, printer, nil, "o", o); e != nil {
		t.Fatal(e)
	}
	for k, v := range map[string]any{"name": "App", "slug": "app", "description": "desc", "project_type": "php", "php_version": "8.3", "base_path": "/app", "project_group_id": "g1"} {
		if body[k] != v {
			t.Fatalf("%s: %v", k, body)
		}
	}
	if repoBody["provider"] != "github" || repoBody["clone_token"] != "secret" || repoBody["default_branch"] != "develop" {
		t.Fatal(repoBody)
	}
	if errOut.String() != "" {
		t.Fatal(errOut.String())
	}
	if e := createProject(context.Background(), c, printer, nil, "o", projectCreateOptions{}); exit.Code(e) != exit.Usage || !strings.Contains(e.Error(), "--name") {
		t.Fatal(e)
	}
}
func TestCreateEnvironmentAttachmentsAndPipeline(t *testing.T) {
	var envBody, putBody, copyBody map[string]any
	c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "PUT":
			json.NewDecoder(r.Body).Decode(&putBody)
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/copy"):
			if r.URL.Path != "/api/v1/pipelines/active/copy" {
				t.Error(r.URL.Path)
			}
			json.NewDecoder(r.Body).Decode(&copyBody)
			fmt.Fprint(w, `{"data":{"id":"copied"}}`)
		case r.Method == "POST":
			json.NewDecoder(r.Body).Decode(&envBody)
			fmt.Fprint(w, `{"data":{"id":"e2","slug":"production","name":"Production","branch":"develop"}}`)
		case strings.HasSuffix(r.URL.Path, "repositories"):
			fmt.Fprint(w, `{"data":[{"id":"r1","repo_url":"git@github.com:a/b","default_branch":"develop"}]}`)
		case strings.HasSuffix(r.URL.Path, "servers"):
			fmt.Fprint(w, `{"data":[{"id":"s1","name":"One"},{"id":"s2","name":"Two"}],"meta":{"last_page":1}}`)
		case strings.HasSuffix(r.URL.Path, "pipelines"):
			fmt.Fprint(w, `{"data":[{"id":"inactive","is_active":false},{"id":"active","is_active":true}]}`)
		default:
			fmt.Fprint(w, `{"data":[{"id":"e1","slug":"staging"}]}`)
		}
	})
	p, _ := scripted("Production\n\n\n\n1,2\nstaging\n\n\ny\n")
	printer, _, _ := testPrinter()
	if e := createEnvironment(context.Background(), c, printer, p, "o", api.Project{ID: "p1", Slug: "app"}, environmentCreateOptions{}); e != nil {
		t.Fatal(e)
	}
	if envBody["repository_id"] != "r1" || envBody["branch"] != "develop" {
		t.Fatal(envBody)
	}
	attached := putBody["servers"].([]any)
	if len(attached) != 2 || attached[0].(map[string]any)["server_id"] != "s1" || attached[0].(map[string]any)["is_primary"] != true || attached[1].(map[string]any)["server_id"] != "s2" {
		t.Fatal(putBody)
	}
	if copyBody["environment_id"] != "e2" {
		t.Fatal(copyBody)
	}
}

// Regression: interactive mode used to discard --server and re-prompt.
func TestCreateEnvironmentServerFlagSkipsPrompt(t *testing.T) {
	var putBody map[string]any
	c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "PUT":
			json.NewDecoder(r.Body).Decode(&putBody)
			fmt.Fprint(w, `{}`)
		case r.Method == "POST":
			fmt.Fprint(w, `{"data":{"id":"e2","slug":"production","name":"Production"}}`)
		case strings.HasSuffix(r.URL.Path, "repositories"):
			fmt.Fprint(w, `{"data":[{"id":"r1","repo_url":"git@github.com:a/b"}]}`)
		case strings.HasSuffix(r.URL.Path, "servers"):
			fmt.Fprint(w, `{"data":[{"id":"s1","name":"One"},{"id":"s2","name":"Two"}],"meta":{"last_page":1}}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	})
	p, out := scripted("Production\n\n\n\n\n\ny\n")
	printer, _, _ := testPrinter()
	if e := createEnvironment(context.Background(), c, printer, p, "o", api.Project{ID: "p1", Slug: "app"}, environmentCreateOptions{Servers: []string{"Two"}}); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out.String(), "Servers (optional") {
		t.Fatal("prompted for servers despite --server")
	}
	attached := putBody["servers"].([]any)
	if len(attached) != 1 || attached[0].(map[string]any)["server_id"] != "s2" {
		t.Fatal(putBody)
	}
}

func TestCreateEnvironmentRequiresRepositoryChoice(t *testing.T) {
	c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"r1","repo_url":"one"},{"id":"r2","repo_url":"two"}]}`)
	})
	printer, _, _ := testPrinter()
	e := createEnvironment(context.Background(), c, printer, nil, "o", api.Project{ID: "p"}, environmentCreateOptions{CreateEnvironmentOptions: api.CreateEnvironmentOptions{Name: "Production"}})
	if exit.Code(e) != exit.Usage || !strings.Contains(e.Error(), "--repo") {
		t.Fatal(e)
	}
}

func TestCreateProjectJSONAndPartialSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET":
					fmt.Fprint(w, `{"data":[]}`)
				case strings.HasSuffix(r.URL.Path, "repositories"):
					if fail {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"title":"Unavailable"}`)
					} else {
						fmt.Fprint(w, `{"data":{"id":"r1","clone_auth":"deploy_key","deploy_key":{"public_key":"ssh-ed25519 public"}}}`)
					}
				default:
					fmt.Fprint(w, `{"data":{"id":"p1","slug":"app","name":"App"}}`)
				}
			})
			printer, out, _ := testPrinter()
			e := createProject(context.Background(), c, printer, nil, "o", projectCreateOptions{CreateProjectOptions: api.CreateProjectOptions{Name: "App"}, repositoryOptions: repositoryOptions{api.CreateRepositoryOptions{RepoURL: "https://github.com/a/b", CloneToken: "private-secret"}}, JSON: true})
			if fail {
				if e == nil || !strings.Contains(e.Error(), "project WAS created (app, p1)") || !strings.Contains(e.Error(), "Project > Repositories") {
					t.Fatal(e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			var result map[string]any
			if e = json.Unmarshal([]byte(out.String()), &result); e != nil {
				t.Fatal(e, out.String())
			}
			if result["project"] == nil || result["repository"] == nil || strings.Contains(out.String(), "private-secret") {
				t.Fatal(out.String())
			}
		})
	}
}
func TestSubmitRejectsUnknownFieldsAndMasksSecret(t *testing.T) {
	p, out := scripted("y\n")
	secret := "sensitive"
	_, err := submit(p, []intakeField{{key: "clone_token", label: "Clone token", value: &secret, secret: true}}, "Create?", func() error {
		return &api.Problem{Status: 422, Errors: map[string][]string{"unknown": {"No matching input"}}}
	})
	if err == nil || strings.Contains(out.String(), secret) || !strings.Contains(out.String(), "set") {
		t.Fatal(err, out.String())
	}
}
func TestCreateEnvironmentFollowupFailure(t *testing.T) {
	c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT":
			w.WriteHeader(500)
			fmt.Fprint(w, `{"title":"Unavailable"}`)
		case r.Method == "POST":
			fmt.Fprint(w, `{"data":{"id":"e1","slug":"production","name":"Production"}}`)
		case strings.HasSuffix(r.URL.Path, "repositories"):
			fmt.Fprint(w, `{"data":[{"id":"r1"}]}`)
		case strings.HasSuffix(r.URL.Path, "servers"):
			fmt.Fprint(w, `{"data":[{"id":"s1","name":"web"}]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	})
	printer, _, _ := testPrinter()
	err := createEnvironment(context.Background(), c, printer, nil, "o", api.Project{ID: "p1"}, environmentCreateOptions{CreateEnvironmentOptions: api.CreateEnvironmentOptions{Name: "Production"}, Servers: []string{"web"}})
	if err == nil || !strings.Contains(err.Error(), "environment WAS created (production, e1)") || !strings.Contains(err.Error(), "server attachment failed") {
		t.Fatal(err)
	}
}
