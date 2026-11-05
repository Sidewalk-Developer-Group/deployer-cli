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
	"sort"
	"strconv"
	"strings"

	"github.com/sidewalk-developer-group/deployer-cli/internal/api"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type apiOptions struct {
	fields, typed, headers    []string
	input                     string
	include, silent, paginate bool
}

// apiPath validates before any authenticated request can be made.
func apiPath(value string) (string, url.Values, error) {
	bad := func() (string, url.Values, error) {
		return "", nil, exit.Errorf(exit.Usage, "invalid API path %q: use a path relative to /api/v1/", value)
	}
	u, err := url.Parse(value)
	if err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(value, "//") || strings.Contains(value, `\`) || u.Fragment != "" {
		return bad()
	}
	path, err := url.PathUnescape(u.EscapedPath())
	if err != nil || strings.Contains(path, `\`) {
		return bad()
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return bad()
		}
	}
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimPrefix(path, "api/v1/")
	if strings.Trim(path, "/") == "" {
		return bad()
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return bad()
	}
	return path, q, nil
}

func apiHeaders(values []string) (http.Header, error) {
	h := http.Header{}
	for _, value := range values {
		key, val, ok := strings.Cut(value, ":")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, exit.Errorf(exit.Usage, "header must be Key: Value")
		}
		switch strings.ToLower(key) {
		case "authorization", "cookie", "host":
			return nil, exit.Errorf(exit.Usage, "header %s cannot be overridden", key)
		}
		h.Add(key, strings.TrimSpace(val))
	}
	return h, nil
}

func apiFieldValue(value string, typed bool, stdin io.Reader) (any, error) {
	if !typed {
		return value, nil
	}
	if strings.HasPrefix(value, "@") {
		var b []byte
		var err error
		if value == "@-" {
			b, err = io.ReadAll(stdin)
		} else {
			b, err = os.ReadFile(value[1:])
		}
		return string(b), err
	}
	// json.Number keeps the literal, so 1000000 is not re-rendered as 1e+06.
	var v any
	d := json.NewDecoder(strings.NewReader(value))
	d.UseNumber()
	if d.Decode(&v) == nil && !d.More() {
		switch v.(type) {
		case nil, bool, json.Number:
			return v, nil
		}
	}
	return value, nil
}

// apiFields supports one object level and array append without inventing a schema.
func apiFields(o apiOptions, stdin io.Reader) (map[string]any, url.Values, error) {
	result := map[string]any{}
	query := url.Values{}
	for _, group := range []struct {
		values []string
		typed  bool
	}{{o.fields, false}, {o.typed, true}} {
		for _, field := range group.values {
			key, value, ok := strings.Cut(field, "=")
			if !ok || key == "" {
				return nil, nil, exit.Errorf(exit.Usage, "field must be key=value")
			}
			v, err := apiFieldValue(value, group.typed, stdin)
			if err != nil {
				return nil, nil, err
			}
			text := fmt.Sprint(v)
			if v == nil {
				text = "null"
			}
			query.Add(key, text)
			if i := strings.IndexByte(key, '['); i >= 0 {
				if i == 0 || !strings.HasSuffix(key, "]") || strings.ContainsAny(key[i+1:len(key)-1], "[]") {
					return nil, nil, exit.Errorf(exit.Usage, "unsupported nested field %q", key)
				}
				base, sub := key[:i], key[i+1:len(key)-1]
				if sub == "" {
					a, ok := result[base].([]any)
					if result[base] != nil && !ok {
						return nil, nil, exit.Errorf(exit.Usage, "conflicting field %q", key)
					}
					result[base] = append(a, v)
				} else {
					m, ok := result[base].(map[string]any)
					if result[base] != nil && !ok {
						return nil, nil, exit.Errorf(exit.Usage, "conflicting field %q", key)
					}
					if m == nil {
						m = map[string]any{}
					}
					m[sub] = v
					result[base] = m
				}
			} else {
				if _, exists := result[key]; exists {
					return nil, nil, exit.Errorf(exit.Usage, "duplicate field %q", key)
				}
				result[key] = v
			}
		}
	}
	return result, query, nil
}

func apiWrite(w io.Writer, body []byte, pretty bool) error {
	if pretty {
		var b bytes.Buffer
		if json.Indent(&b, body, "", "  ") == nil {
			body = append(b.Bytes(), '\n')
		}
	}
	_, err := w.Write(body)
	return err
}

func writerTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// runAPI retains response bytes and lets the shared problem parser classify errors.
func runAPI(ctx context.Context, c *api.Client, method, path string, query url.Values, h http.Header, body io.Reader, o apiOptions, out, stderr io.Writer) error {
	all := []json.RawMessage{}
	seen := map[string]bool{}
	for {
		marker := query.Encode()
		if seen[marker] {
			return fmt.Errorf("pagination did not advance")
		}
		seen[marker] = true
		response, err := c.Raw(ctx, method, path, query, h, body)
		if err != nil {
			return err
		}
		if o.include {
			fmt.Fprintf(out, "HTTP/1.1 %d %s\n", response.Status, http.StatusText(response.Status))
			keys := make([]string, 0, len(response.Headers))
			for k := range response.Headers {
				if !strings.EqualFold(k, "Authorization") {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				for _, v := range response.Headers[k] {
					fmt.Fprintf(out, "%s: %s\n", k, v)
				}
			}
			fmt.Fprintln(out)
		}
		if location := response.Headers.Get("Location"); response.Status >= 300 && response.Status < 400 && location != "" {
			fmt.Fprintf(stderr, "refused to follow %d redirect to %s\n", response.Status, location)
		}
		if response.Status < 200 || response.Status >= 300 {
			if err := apiWrite(stderr, response.Body, writerTTY(stderr)); err != nil {
				return err
			}
			return exit.Errorf(exit.Code(api.ParseProblem(response.Status, response.Body)), "")
		}
		if !o.paginate {
			if o.silent {
				return nil
			}
			return apiWrite(out, response.Body, writerTTY(out))
		}
		var page struct {
			Data json.RawMessage `json:"data"`
			Meta struct {
				Current int     `json:"current_page"`
				Last    int     `json:"last_page"`
				Cursor  *string `json:"next_cursor"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(response.Body, &page); err != nil {
			return fmt.Errorf("decoding pagination: %w", err)
		}
		var data []json.RawMessage
		if len(page.Data) == 0 || page.Data[0] != '[' || json.Unmarshal(page.Data, &data) != nil {
			return fmt.Errorf("pagination response has no data array")
		}
		all = append(all, data...)
		if page.Meta.Current > 0 && page.Meta.Current < page.Meta.Last {
			query.Set("page", strconv.Itoa(page.Meta.Current+1))
			continue
		}
		if page.Meta.Cursor != nil {
			query.Set("cursor", *page.Meta.Cursor)
			continue
		}
		break
	}
	if o.silent {
		return nil
	}
	encoded, err := json.Marshal(map[string]any{"data": all})
	if err != nil {
		return err
	}
	return apiWrite(out, encoded, writerTTY(out))
}

func newAPICommand(f *Factory) *cobra.Command {
	var o apiOptions
	cmd := &cobra.Command{
		Use:   "api [METHOD] <path>",
		Short: "Make an authenticated request to this instance",
		Long: "Requests a path relative to /api/v1/ on the configured instance. {org} resolves\n" +
			"the selected organization. Absolute URLs, traversal and redirects are refused.\n" +
			"\n" +
			"The method defaults to GET, or POST with fields or --input. Use -f key=value\n" +
			"for strings and -F key=value for typed numbers, booleans, null or @file contents\n" +
			"(@- reads stdin). GET and DELETE fields become query parameters. --input sends\n" +
			"raw bytes from a file (or - for stdin) and cannot be combined with fields.\n" +
			"\n" +
			"-H adds headers; Authorization, Cookie and Host cannot be overridden. --include\n" +
			"prints response status and headers. --paginate combines GET data arrays across\n" +
			"pages. Pipe to jq to filter the response.\n" +
			"\n" +
			"A non-2xx response prints its body to stderr and exits 2 (400/404/422),\n" +
			"3 (401/403) or 4 (anything else, including a refused redirect).",
		Example: "  deployer api organizations/{org}/projects --paginate | jq '.data[].slug'\n" +
			"  deployer api POST organizations/{org}/projects -f name='Billing API' -f slug=billing-api\n" +
			"  deployer api POST organizations/{org}/projects --input payload.json\n" +
			"  deployer api organizations/{org}/projects -F per_page=5 --include\n" +
			"  deployer api organizations/{org}/projects -H 'Accept: application/json'",
		Args: wantArgs(1, 2, "deployer api [METHOD] <path>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			method, path := "GET", args[0]
			hasBody := len(o.fields)+len(o.typed) > 0 || cmd.Flags().Changed("input")
			if hasBody {
				method = "POST"
			}
			if len(args) == 2 {
				method = strings.ToUpper(args[0])
				path = args[1]
			}
			switch method {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
			default:
				return exit.Errorf(exit.Usage, "unsupported method %q", method)
			}
			path, q, err := apiPath(path)
			if err != nil {
				return err
			}
			if o.paginate && method != "GET" {
				return exit.Errorf(exit.Usage, "--paginate requires GET")
			}
			if cmd.Flags().Changed("input") && len(o.fields)+len(o.typed) > 0 {
				return exit.Errorf(exit.Usage, "--input cannot be combined with -f or -F")
			}
			h, err := apiHeaders(o.headers)
			if err != nil {
				return err
			}
			fields, query, err := apiFields(o, cmd.InOrStdin())
			if err != nil {
				return err
			}
			var body io.Reader
			if method == "GET" || method == "DELETE" {
				for k, v := range query {
					q[k] = append(q[k], v...)
				}
			} else if len(fields) > 0 {
				b, e := json.Marshal(fields)
				if e != nil {
					return e
				}
				body = bytes.NewReader(b)
			}
			if cmd.Flags().Changed("input") {
				var b []byte
				if o.input == "-" {
					b, err = io.ReadAll(cmd.InOrStdin())
				} else {
					b, err = os.ReadFile(o.input)
				}
				if err != nil {
					return err
				}
				body = bytes.NewReader(b)
			}
			s, err := f.session()
			if err != nil {
				return err
			}
			s.warnFileToken(f.Printer())
			if strings.Contains(path, "{org}") {
				org, e := f.organization(cmd.Context(), s)
				if e != nil {
					return e
				}
				path = strings.ReplaceAll(path, "{org}", org)
			}
			return runAPI(cmd.Context(), s.Client, method, path, q, h, body, o, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	flags := cmd.Flags()
	flags.StringArrayVarP(&o.fields, "raw-field", "f", nil, "string field key=value")
	flags.StringArrayVarP(&o.typed, "field", "F", nil, "typed field key=value; @file reads contents")
	flags.StringArrayVarP(&o.headers, "header", "H", nil, "request header Key: Value")
	flags.StringVar(&o.input, "input", "", "raw request body file, or - for stdin")
	flags.BoolVarP(&o.include, "include", "i", false, "include response status and headers")
	flags.BoolVar(&o.silent, "silent", false, "suppress response body")
	flags.BoolVar(&o.paginate, "paginate", false, "combine paginated data arrays (GET only)")
	return cmd
}
