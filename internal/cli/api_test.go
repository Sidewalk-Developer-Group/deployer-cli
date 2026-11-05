package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
)

func TestAPIPathsAndHeaders(t *testing.T) {
	requests := 0
	c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Host != "deployer.test" || r.URL.Path != "/api/v1/projects" || r.URL.Query().Get("per_page") != "5" {
			t.Fatal(r.URL)
		}
		if r.Header.Get("X-Custom") != "yes" {
			t.Fatal(r.Header)
		}
		fmt.Fprint(w, `{}`)
	})
	for _, p := range []string{"projects", "/projects", "api/v1/projects", "/api/v1/projects"} {
		path, q, err := apiPath(p + "?per_page=5")
		if err != nil {
			t.Fatal(err)
		}
		h, err := apiHeaders([]string{"X-Custom: yes"})
		if err != nil {
			t.Fatal(err)
		}
		if err = runAPI(context.Background(), c, "GET", path, q, h, nil, apiOptions{}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"https://evil.example/x", "//evil.example/x", "../x", "a/../../x", "%2e%2e/x", "a/%2e/x", `a\b`, "", "/api/v1/"} {
		if _, _, err := apiPath(p); exit.Code(err) != exit.Usage {
			t.Fatalf("%q: %v", p, err)
		}
	}
	if requests != 4 {
		t.Fatal(requests)
	}
	for _, key := range []string{"Authorization", "cookie", "HOST"} {
		if _, err := apiHeaders([]string{key + ": secret"}); exit.Code(err) != exit.Usage || !strings.Contains(err.Error(), key) {
			t.Fatal(err)
		}
	}
}

func TestAPIFields(t *testing.T) {
	file := filepath.Join(t.TempDir(), "field")
	if err := os.WriteFile(file, []byte("contents\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fields, q, err := apiFields(apiOptions{fields: []string{"string=true", "a[b]=1", "tags[]=x", "tags[]=y"}, typed: []string{"bool=true", "nil=null", "integer=12", "big=1000000", "float=1.5", "file=@" + file, "stdin=@-"}}, strings.NewReader("stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if fields["string"] != "true" || fields["bool"] != true || fields["nil"] != nil || fields["integer"] != json.Number("12") || fields["float"] != json.Number("1.5") || fields["file"] != "contents\n" || fields["stdin"] != "stdin" || fields["a"].(map[string]any)["b"] != "1" || len(fields["tags"].([]any)) != 2 || q.Get("integer") != "12" || q.Get("big") != "1000000" {
		t.Fatal(fields, q)
	}
}

func TestAPIResponseAndPagination(t *testing.T) {
	for _, cursor := range []bool{false, true} {
		count := 0
		c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
			count++
			if count == 1 {
				if cursor {
					fmt.Fprint(w, `{"data":[1],"meta":{"next_cursor":"next"}}`)
				} else {
					fmt.Fprint(w, `{"data":[1],"meta":{"current_page":1,"last_page":2}}`)
				}
			} else {
				if cursor && r.URL.Query().Get("cursor") != "next" || !cursor && r.URL.Query().Get("page") != "2" {
					t.Fatal(r.URL)
				}
				fmt.Fprint(w, `{"data":[2],"meta":{}}`)
			}
		})
		var out bytes.Buffer
		if err := runAPI(context.Background(), c, "GET", "projects", url.Values{}, nil, nil, apiOptions{paginate: true}, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		if out.String() != `{"data":[1,2]}` || count != 2 {
			t.Fatal(out.String(), count)
		}
	}
	c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "ok")
		w.WriteHeader(404)
		fmt.Fprint(w, `{"title":"missing"}`)
	})
	var out, stderr bytes.Buffer
	err := runAPI(context.Background(), c, "GET", "x", url.Values{}, nil, nil, apiOptions{include: true}, &out, &stderr)
	if exit.Code(err) != exit.Code(api.ParseProblem(404, stderr.Bytes())) || err.Error() != "" || stderr.String() != `{"title":"missing"}` || !strings.Contains(out.String(), "HTTP/1.1 404 Not Found\n") || !strings.Contains(out.String(), "X-Test: ok") {
		t.Fatal(err, &out, &stderr)
	}
}

func TestAPICommandValidationAndOrg(t *testing.T) {
	t.Setenv("DEPLOYER_CONFIG_DIR", t.TempDir())
	t.Setenv("DEPLOYER_TOKEN", "token")
	t.Setenv("DEPLOYER_API_URL", "https://deployer.test")
	t.Setenv("DEPLOYER_ORG", "")
	requests := 0
	createClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/api/v1/users/me":
			fmt.Fprint(w, `{"data":{"organizations":[{"id":"org-id","slug":"acme"}]}}`)
		case "/api/v1/organizations/org-id/projects":
			if r.URL.Query().Get("n") != "2" || r.Body != nil && r.ContentLength > 0 {
				t.Fatal(r)
			}
			fmt.Fprint(w, `{}`)
		default:
			t.Fatal(r.URL)
		}
	})
	for _, args := range [][]string{{"GET", "projects", "--input", "-", "-f", "x=y"}, {"projects", "-H", "Authorization: evil"}, {"https://evil.example/x"}} {
		cmd := newAPICommand(&Factory{})
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); exit.Code(err) != exit.Usage {
			t.Fatal(err)
		}
	}
	if requests != 0 {
		t.Fatal(requests)
	}
	cmd := newAPICommand(&Factory{})
	cmd.SetArgs([]string{"GET", "organizations/{org}/projects", "-F", "n=2"})
	cmd.SetOut(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatal(requests)
	}
}

func TestAPIRawRefusesRedirectTypedFollows(t *testing.T) {
	c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/users/me" {
			http.Redirect(w, r, "/api/v1/destination", 302)
			return
		}
		fmt.Fprint(w, `{"data":{"email":"ok"}}`)
	})
	raw, err := c.Raw(context.Background(), "GET", "users/me", nil, http.Header{"Authorization": []string{"evil"}}, nil)
	if err != nil || raw.Status != 302 {
		t.Fatal(raw, err)
	}
	user, err := c.Me(context.Background())
	if err != nil || user.Email != "ok" {
		t.Fatal(user, err)
	}
}

func TestAPICommandBodiesAndDefaultMethods(t *testing.T) {
	t.Setenv("DEPLOYER_CONFIG_DIR", t.TempDir())
	t.Setenv("DEPLOYER_TOKEN", "token")
	t.Setenv("DEPLOYER_API_URL", "https://deployer.test")
	var method, body, contentType string
	c := createClient(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			body = string(b)
		} else {
			body = ""
		}
		contentType = r.Header.Get("Content-Type")
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatal("client authentication was replaced")
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	for _, tc := range []struct {
		args                                  []string
		input, wantMethod, wantBody, wantType string
	}{
		{[]string{"projects"}, "", "GET", "", ""},
		{[]string{"projects", "-F", "enabled=true", "-f", "name=test"}, "", "POST", `{"enabled":true,"name":"test"}`, "application/json"},
		{[]string{"projects", "--input", "-"}, "{ \"x\": 1 }\n", "POST", "{ \"x\": 1 }\n", "application/json"},
		{[]string{"PUT", "projects", "--input", "-", "-H", "Content-Type: text/plain"}, "untouched\n", "PUT", "untouched\n", "text/plain"},
	} {
		cmd := newAPICommand(&Factory{})
		cmd.SetArgs(tc.args)
		cmd.SetIn(strings.NewReader(tc.input))
		cmd.SetOut(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if method != tc.wantMethod || body != tc.wantBody || contentType != tc.wantType {
			t.Fatalf("%s %q %q", method, body, contentType)
		}
	}
	if _, err := c.Raw(context.Background(), "GET", "projects", nil, http.Header{"authorization": []string{"evil"}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAPISuccessIncludeSilentAndMissingData(t *testing.T) {
	c := createClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true}`) })
	var out bytes.Buffer
	if err := runAPI(context.Background(), c, "GET", "x", url.Values{}, nil, nil, apiOptions{include: true}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "HTTP/1.1 200 OK\n") || !strings.HasSuffix(out.String(), `{"ok":true}`) {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := runAPI(context.Background(), c, "GET", "x", url.Values{}, nil, nil, apiOptions{silent: true}, &out, io.Discard); err != nil || out.Len() != 0 {
		t.Fatal(err, out.String())
	}
	if err := runAPI(context.Background(), c, "GET", "x", url.Values{}, nil, nil, apiOptions{paginate: true}, &out, io.Discard); err == nil || !strings.Contains(err.Error(), "no data array") {
		t.Fatal(err)
	}
}
