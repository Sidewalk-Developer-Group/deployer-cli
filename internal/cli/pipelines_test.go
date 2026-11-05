package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/sidewalk-developer-group/deployer-cli/internal/ui"
)

func TestPipelineRoundTripAndConfirmation(t *testing.T) {
	original := []byte("name: Example\nstages:\n  - name: Clone\n    type: clone\n")
	for _, answer := range []string{"n", "y", "yes-flag"} {
		t.Run(answer, func(t *testing.T) {
			stored := append([]byte(nil), original...)
			posts, activations := 0, 0
			c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/export"):
					w.Write(stored)
				case strings.HasSuffix(r.URL.Path, "/import"):
					posts++
					var body struct {
						YAML string `json:"yaml"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					stored = []byte(body.YAML)
					w.WriteHeader(201)
					fmt.Fprint(w, `{"data":{"id":"p","name":"Example"},"meta":{"warnings":["review stage"]}}`)
				case strings.HasSuffix(r.URL.Path, "/activate"):
					activations++
					fmt.Fprint(w, `{"data":{"id":"p","name":"Example","is_active":true}}`)
				default:
					fmt.Fprint(w, `{"data":[{"id":"p","name":"Example","is_active":true,"version":1,"stages_count":1}]}`)
				}
			})
			var out, stderr bytes.Buffer
			printer := &ui.Printer{Out: &out, Err: &stderr}
			if err := exportPipeline(context.Background(), c, printer, "env", "", "", &out); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), original) {
				t.Fatal(out.String())
			}
			p, _ := scripted(answer + "\n")
			if answer == "yes-flag" {
				p = nil
			}
			err := importPipeline(context.Background(), c, printer, p, "env", "project/env", out.Bytes(), pipelineImportOptions{yes: answer == "yes-flag", activate: true, json: true})
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if answer == "n" {
				want = 0
			}
			if posts != want || activations != want {
				t.Fatal(posts, activations)
			}
			if want == 1 && (!strings.Contains(stderr.String(), "review stage") || !strings.Contains(stderr.String(), "active")) {
				t.Fatal(stderr.String())
			}
			path := filepath.Join(t.TempDir(), "pipeline.yaml")
			if err := exportPipeline(context.Background(), c, printer, "env", "p", path, &out); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(b, original) || !bytes.Equal(stored, original) {
				t.Fatal(string(b), err)
			}
		})
	}
}

func TestPipelineListAndImportValidation(t *testing.T) {
	var out bytes.Buffer
	p := &ui.Printer{Out: &out, Err: &out}
	count := 3
	if err := listPipelines(p, []api.Pipeline{{ID: "p", Name: "Example", IsActive: true, Version: 2, StagesCount: &count}}, false); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"ID", "NAME", "ACTIVE", "VERSION", "STAGES", "UPDATED", "✓", "Example"} {
		if !strings.Contains(out.String(), s) {
			t.Fatal(out.String())
		}
	}
	err := importPipeline(context.Background(), nil, p, nil, "env", "p/e", []byte("name: x"), pipelineImportOptions{})
	if exit.Code(err) != exit.Usage || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatal(err)
	}
}

func TestPipelineValidationProblemAndSelection(t *testing.T) {
	c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(422)
			fmt.Fprint(w, `{"title":"Invalid YAML","errors":{"stages.0.config.url":["Required"]}}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"inactive","is_active":false}]}`)
	})
	var out bytes.Buffer
	p := &ui.Printer{Out: &out, Err: &out}
	for _, id := range []string{"", "foreign"} {
		if err := exportPipeline(context.Background(), c, p, "e", id, "", &out); exit.Code(err) != exit.Usage {
			t.Fatal(err)
		}
	}
	err := importPipeline(context.Background(), c, p, nil, "e", "p/e", []byte("name: Test\nstages: []\n"), pipelineImportOptions{yes: true})
	if exit.Code(err) != exit.Usage || !strings.Contains(err.Error(), "stages.0.config.url: Required") {
		t.Fatal(err)
	}
}
