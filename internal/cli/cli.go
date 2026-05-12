// Package cli is the omoikane CLI logic. Extracted from cmd/kb so it can
// be tested directly — the cmd/kb shim is just
// `os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))`.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Version is overridden at link time.
var Version = "0.6.0"

// configPathFn is overridden in tests to redirect $HOME-derived paths to a
// temp dir. Tests should set it via SetConfigPath.
var configPathFn = defaultConfigPath

// SetConfigPath replaces the config-path resolver. Tests use this to point
// at a temp file. Pass nil to reset to default.
func SetConfigPath(fn func() (string, error)) {
	if fn == nil {
		configPathFn = defaultConfigPath
		return
	}
	configPathFn = fn
}

// httpClientFn returns the HTTP client to use. Overridable for tests that
// inject a transport (we mostly use httptest.Server so this is rarely
// needed, but it keeps options open).
var httpClientFn = func() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// userHomeDirFn is overridable in tests to exercise the UserHomeDir error
// branch — the real os.UserHomeDir is hard to fail deterministically.
var userHomeDirFn = os.UserHomeDir

func defaultConfigPath() (string, error) {
	home, err := userHomeDirFn()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "omoikane", "cli.json"), nil
}

// Config is the on-disk CLI configuration.
type Config struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

// Load reads the config, applying env-var overrides (KB_URL, KB_TOKEN).
func Load() (*Config, error) {
	p, err := configPathFn()
	if err != nil {
		return nil, err
	}
	c := &Config{}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Fall through with empty config + env overrides applied below.
		} else {
			return nil, err
		}
	} else if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if v := os.Getenv("KB_URL"); v != "" {
		c.URL = v
	}
	if v := os.Getenv("KB_TOKEN"); v != "" {
		c.Token = v
	}
	return c, nil
}

// Save persists the config (0600 mode). json.MarshalIndent of *Config (two
// string fields) cannot fail in practice, so the marshal error branch is
// elided — Go's encoding/json returns nil for plain structs.
func Save(c *Config) error {
	p, err := configPathFn()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

// Client is a thin HTTP wrapper.
type Client struct {
	URL    string
	Token  string
	client *http.Client
}

func NewClient(c *Config) (*Client, error) {
	if c.URL == "" {
		return nil, errors.New("server URL not set; run `kb config set url <url>`")
	}
	if c.Token == "" {
		return nil, errors.New("token not set; run `kb config set token <token>`")
	}
	return &Client{
		URL:    strings.TrimRight(c.URL, "/"),
		Token:  c.Token,
		client: httpClientFn(),
	}, nil
}

// Do sends a request and decodes the JSON response into `into` on success.
// On HTTP >= 400 it returns an error containing the raw response body.
func (c *Client) Do(method, path string, body any, headers map[string]string, into any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.URL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Client-Type", "cli")
	req.Header.Set("X-Client-Version", Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if into != nil && len(raw) > 0 {
		return json.Unmarshal(raw, into)
	}
	return nil
}

// Run dispatches the CLI. Returns the process exit code. stdin/stdout/stderr
// are abstracted so tests can verify output without touching the real
// process streams.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		Usage(stdout)
		return 2
	}
	var err error
	switch args[0] {
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, "kb", Version)
		return 0
	case "help", "-h", "--help":
		Usage(stdout)
		return 0
	case "config":
		err = CmdConfig(args[1:], stdout)
	case "projects":
		err = CmdProjects(args[1:], stdout)
	case "post":
		err = CmdPost(args[1:], stdin, stdout)
	case "get":
		err = CmdGet(args[1:], stdout)
	case "update":
		err = CmdUpdate(args[1:], stdin, stdout)
	case "delete":
		err = CmdDelete(args[1:], stdout)
	case "history":
		err = CmdHistory(args[1:], stdout)
	case "list":
		err = CmdList(args[1:], stdout, stderr)
	case "search":
		err = CmdSearch(args[1:], stdout, stderr)
	case "lookup":
		err = CmdLookup(args[1:], stdout, stderr)
	case "incident":
		err = CmdIncident(args[1:], stdin, stdout)
	case "feedback":
		err = CmdFeedback(args[1:], stdout)
	case "relations":
		err = CmdRelations(args[1:], stdout)
	case "situations":
		err = CmdSituations(args[1:], stdin, stdout)
	case "cluster":
		err = CmdCluster(args[1:], stdout)
	case "browse":
		err = CmdBrowse(args[1:], stdout)
	case "index":
		err = CmdIndex(args[1:], stdout)
	case "reflect":
		err = CmdReflect(args[1:], stdout)
	default:
		fmt.Fprintln(stderr, "unknown command:", args[0])
		Usage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "kb:", err)
		return 1
	}
	return 0
}

// Usage prints the kb help text.
func Usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `kb — omoikane CLI

usage:
  kb config set url <url>
  kb config set token <token>
  kb config show

  kb projects create --id <id> --name <name> [--desc <text>]
  kb projects list

  kb post   --project <id> --type <type> --title <title> --file <path>
  kb get    <entry-id> [--as-of <RFC3339>]
  kb update <entry-id> --expected-version <N> [--status <s>] [--file <path>]
  kb delete <entry-id>
  kb history <entry-id>
  kb list   [--project <id>] [--type <type>] [--status <status>] [--tag <tag>]
  kb search <query> [--project <id>] [--type <type>] [--top-k N]

  kb lookup trigger --query <text> [--domain <d>] [--top-k N] [--project <id>]
  kb lookup symptom --query <text> [--top-k N] [--project <id>]
  kb lookup tags    --tags a,b,c [--mode any|all] [--top-k N] [--project <id>]

  kb incident --project <id> --title <title> --file <path>
              [--attempted <path>] [--observed <path>] [--hypotheses <path>]

  kb feedback record  --entry <id> [--trigger <text>] [--outcome <…>] [--result <…>]
  kb feedback judge   --case <id> [--outcome <…>] [--result <…>] [--evidence <text>]
  kb feedback signals <entry-id>
  kb feedback review-queue [--limit N]

  kb relations link   --from <id> --to <id> --type <relType> [--confidence <f>] [--notes <text>]
  kb relations unlink --from <id> --to <id> --type <relType>
  kb relations list   --entry <id> [--direction outgoing|incoming|both]

  kb situations create --description <text> [--project <id>] [--domain <d>]
  kb situations list   [--project <id>]
  kb situations get    <id>
  kb situations link   --situation <id> --entry <id> [--relevance <f>]
  kb situations delete <id>
  kb situations lookup --query <text> [--top-k N] [--project <id>]

  kb cluster list    [--project <id>] [--status <s>]
  kb cluster get     <id>
  kb cluster promote --cluster <id> --entry <id>
  kb cluster dismiss <id>
  kb cluster rebuild [--project <id>] [--threshold <f>] [--min-members N]

  kb browse list   [--project <id>]
  kb browse create --name <name> [--parent <id>] [--project <id>] [--description <text>]
  kb browse get    <node-id>
  kb browse attach --node <id> --entry <id> [--weight <f>]
  kb browse detach --node <id> --entry <id>
  kb browse delete <node-id>

  kb index   [--group-by tag|recent|hierarchy] [--project <id>]
  kb reflect <entry-id>... [--prompt <text>]`)
}

// ---- config ----

func CmdConfig(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: kb config (set|show)")
	}
	switch args[0] {
	case "show":
		c, err := Load()
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(c, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return nil
	case "set":
		if len(args) != 3 {
			return errors.New("usage: kb config set (url|token) <value>")
		}
		c, err := Load()
		if err != nil {
			return err
		}
		switch args[1] {
		case "url":
			c.URL = args[2]
		case "token":
			c.Token = args[2]
		default:
			return fmt.Errorf("unknown config key: %s", args[1])
		}
		return Save(c)
	default:
		return fmt.Errorf("unknown config command: %s", args[0])
	}
}

// ---- projects ----

func CmdProjects(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: kb projects (create|list)")
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		var out struct {
			Projects []map[string]any `json:"projects"`
		}
		if err := cli.Do(http.MethodGet, "/v1/projects", nil, nil, &out); err != nil {
			return err
		}
		for _, p := range out.Projects {
			fmt.Fprintf(stdout, "%s\t%s\n", p["id"], p["name"])
		}
		return nil
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		id := fs.String("id", "", "project ID")
		name := fs.String("name", "", "project name")
		desc := fs.String("desc", "", "description")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *name == "" {
			return errors.New("--id and --name are required")
		}
		body := map[string]any{"id": *id, "name": *name}
		if *desc != "" {
			body["description"] = *desc
		}
		var out map[string]any
		if err := cli.Do(http.MethodPost, "/v1/projects", body, nil, &out); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return nil
	}
	return fmt.Errorf("unknown subcommand: %s", args[0])
}

// ---- post ----

func CmdPost(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("post", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("project", "", "project ID")
	typ := fs.String("type", "", "entry type")
	title := fs.String("title", "", "title")
	file := fs.String("file", "", "body markdown file (- for stdin)")
	status := fs.String("status", "", "status (default DRAFT)")
	tagsCSV := fs.String("tags", "", "comma-separated tags")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *typ == "" || *title == "" || *file == "" {
		return errors.New("--project, --type, --title, --file are required")
	}
	body, err := ReadFile(*file, stdin)
	if err != nil {
		return err
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"project_id": *project,
		"type":       *typ,
		"title":      *title,
		"body":       string(body),
	}
	if *status != "" {
		payload["status"] = *status
	}
	if *tagsCSV != "" {
		payload["tags"] = SplitCSV(*tagsCSV)
	}
	var out map[string]any
	if err := cli.Do(http.MethodPost, "/v1/entries", payload, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

// ReadFile reads `p`, or stdin when `p == "-"`.
func ReadFile(p string, stdin io.Reader) ([]byte, error) {
	if p == "-" {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(p)
}

// ---- get / update / delete / history ----

func CmdGet(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: kb get <entry-id> [--as-of <RFC3339>]")
	}
	id := args[0]
	if strings.HasPrefix(id, "-") {
		return errors.New("usage: kb get <entry-id> [--as-of <RFC3339>]")
	}
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asOf := fs.String("as-of", "", "RFC3339 timestamp for historical snapshot")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}
	path := "/v1/entries/" + url.PathEscape(id)
	if *asOf != "" {
		path += "?as_of=" + url.QueryEscape(*asOf)
	}
	var out map[string]any
	if err := cli.Do(http.MethodGet, path, nil, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

func CmdUpdate(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: kb update <entry-id> --expected-version <N> [...]")
	}
	id := args[0]
	if strings.HasPrefix(id, "-") {
		return errors.New("usage: kb update <entry-id> --expected-version <N> [...]")
	}
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	expected := fs.Int("expected-version", 0, "current version (required for OCC)")
	status := fs.String("status", "", "new status")
	title := fs.String("title", "", "new title")
	file := fs.String("file", "", "body markdown file (- for stdin)")
	tagsCSV := fs.String("tags", "", "comma-separated tags (replace)")
	summary := fs.String("summary", "", "change summary for history")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *expected <= 0 {
		return errors.New("--expected-version is required and must be > 0")
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}

	patch := map[string]any{}
	if *status != "" {
		patch["status"] = *status
	}
	if *title != "" {
		patch["title"] = *title
	}
	if *file != "" {
		b, err := ReadFile(*file, stdin)
		if err != nil {
			return err
		}
		patch["body"] = string(b)
	}
	if *tagsCSV != "" {
		patch["tags"] = SplitCSV(*tagsCSV)
	}
	if *summary != "" {
		patch["change_summary"] = *summary
	}
	headers := map[string]string{"If-Match": fmt.Sprintf("%d", *expected)}
	var out map[string]any
	if err := cli.Do(http.MethodPatch, "/v1/entries/"+url.PathEscape(id),
		patch, headers, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

func CmdDelete(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: kb delete <entry-id>")
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}
	if err := cli.Do(http.MethodDelete, "/v1/entries/"+url.PathEscape(args[0]), nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "archived:", args[0])
	return nil
}

func CmdHistory(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: kb history <entry-id>")
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}
	var out struct {
		History []map[string]any `json:"history"`
	}
	if err := cli.Do(http.MethodGet, "/v1/entries/"+url.PathEscape(args[0])+"/history",
		nil, nil, &out); err != nil {
		return err
	}
	for _, h := range out.History {
		fmt.Fprintf(stdout, "v%v\t%v\t%v\t%v\n", h["version"], h["changed_at"], h["status"], h["change_summary"])
	}
	return nil
}

// ---- list / search ----

func CmdList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("project", "", "filter by project")
	typ := fs.String("type", "", "filter by type")
	status := fs.String("status", "", "filter by status")
	tag := fs.String("tag", "", "filter by tag")
	limit := fs.Int("limit", 50, "max results")
	offset := fs.Int("offset", 0, "pagination offset")
	includeSuperseded := fs.Bool("include-superseded", false, "include SUPERSEDED/ARCHIVED/DUPLICATE")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}
	q := url.Values{}
	if *project != "" {
		q.Set("project_id", *project)
	}
	if *typ != "" {
		q.Set("type", *typ)
	}
	if *status != "" {
		q.Set("status", *status)
	}
	if *tag != "" {
		q.Set("tag", *tag)
	}
	if *includeSuperseded {
		q.Set("include_superseded", "true")
	}
	q.Set("limit", fmt.Sprint(*limit))
	q.Set("offset", fmt.Sprint(*offset))
	var out struct {
		Entries    []map[string]any `json:"entries"`
		Pagination map[string]any   `json:"pagination"`
	}
	if err := cli.Do(http.MethodGet, "/v1/entries?"+q.Encode(), nil, nil, &out); err != nil {
		return err
	}
	for _, e := range out.Entries {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tv%v\t%s\n",
			e["id"], e["type"], e["status"], e["version"], e["title"])
	}
	if out.Pagination != nil {
		fmt.Fprintf(stderr, "# page: limit=%v offset=%v total=%v has_more=%v\n",
			out.Pagination["limit"], out.Pagination["offset"],
			out.Pagination["total"], out.Pagination["has_more"])
	}
	return nil
}

func CmdSearch(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: kb search <query> [--project …]")
	}
	query := args[0]
	rest := args[1:]
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("project", "", "filter by project")
	typ := fs.String("type", "", "filter by type")
	topK := fs.Int("top-k", 20, "max results")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}
	q := PrepareFTSQuery(query)
	body := map[string]any{"query": q, "top_k": *topK}
	if *project != "" || *typ != "" {
		f := map[string]any{}
		if *project != "" {
			f["project"] = *project
		}
		if *typ != "" {
			f["type"] = *typ
		}
		body["filters"] = f
	}
	var out struct {
		Results []struct {
			Entry map[string]any `json:"entry"`
			Score float64        `json:"score"`
		} `json:"results"`
		Total int `json:"total"`
	}
	if err := cli.Do(http.MethodPost, "/v1/search", body, nil, &out); err != nil {
		return err
	}
	for _, r := range out.Results {
		fmt.Fprintf(stdout, "%s\t%.3f\t%s\t%s\n",
			r.Entry["id"], r.Score, r.Entry["type"], r.Entry["title"])
	}
	if out.Total > len(out.Results) {
		fmt.Fprintf(stderr, "# returned %d of %d total\n", len(out.Results), out.Total)
	}
	return nil
}

// PrepareFTSQuery wraps each token in double quotes and a prefix marker so a
// user-friendly "mask training" becomes the safe FTS5 expression
// `"mask"* "training"*`. strings.FieldsFunc never emits empty tokens, so we
// don't filter them.
func PrepareFTSQuery(q string) string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ',', ';', '.', '(', ')', '[', ']', '{', '}',
			'"', '\'', '`', ':', '/', '\\', '!', '?', '=', '<', '>', '|':
			return true
		}
		return false
	})
	toks := make([]string, 0, len(fields))
	for _, f := range fields {
		toks = append(toks, `"`+strings.ReplaceAll(f, `"`, `""`)+`"*`)
	}
	if len(toks) == 0 {
		return q
	}
	return strings.Join(toks, " ")
}

// ---- lookup ----

func CmdLookup(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: kb lookup (trigger|symptom|tags) [flags]")
	}
	verb := args[0]
	rest := args[1:]
	switch verb {
	case "trigger":
		return cmdLookupTrigger(rest, stdout)
	case "symptom":
		return cmdLookupSymptom(rest, stdout)
	case "tags":
		return cmdLookupTags(rest, stdout)
	default:
		return fmt.Errorf("unknown lookup subcommand: %s", verb)
	}
}

func cmdLookupTrigger(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("lookup-trigger", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	query := fs.String("query", "", "trigger description (required)")
	domain := fs.String("domain", "", "optional domain filter")
	topK := fs.Int("top-k", 10, "max results")
	project := fs.String("project", "", "optional project filter")
	includeProhibited := fs.Bool("include-prohibited", true, "include prohibited text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *query == "" {
		return errors.New("--query is required")
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}
	body := map[string]any{
		"trigger_description": *query,
		"top_k":               *topK,
		"include_prohibited":  *includeProhibited,
	}
	if *domain != "" {
		body["domain"] = *domain
	}
	if *project != "" {
		body["project_id"] = *project
	}
	return printLookupResult(cli, "/v1/lookup/by-trigger", body, stdout)
}

func cmdLookupSymptom(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("lookup-symptom", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	query := fs.String("query", "", "symptom description (required)")
	topK := fs.Int("top-k", 10, "max results")
	project := fs.String("project", "", "optional project filter")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *query == "" {
		return errors.New("--query is required")
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}
	body := map[string]any{
		"symptom_description": *query,
		"top_k":               *topK,
	}
	if *project != "" {
		body["project_id"] = *project
	}
	return printLookupResult(cli, "/v1/lookup/by-symptom", body, stdout)
}

func cmdLookupTags(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("lookup-tags", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	tagsCSV := fs.String("tags", "", "comma-separated tags (required)")
	mode := fs.String("mode", "any", "any|all")
	topK := fs.Int("top-k", 10, "max results")
	project := fs.String("project", "", "optional project filter")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tagsCSV == "" {
		return errors.New("--tags is required")
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}
	body := map[string]any{
		"tags":       SplitCSV(*tagsCSV),
		"match_mode": *mode,
		"top_k":      *topK,
	}
	if *project != "" {
		body["project_id"] = *project
	}
	return printLookupResult(cli, "/v1/lookup/by-tags", body, stdout)
}

func printLookupResult(cli *Client, path string, body any, stdout io.Writer) error {
	var out struct {
		Matches []map[string]any `json:"matches"`
	}
	if err := cli.Do(http.MethodPost, path, body, nil, &out); err != nil {
		return err
	}
	for _, m := range out.Matches {
		fmt.Fprintf(stdout, "%s\t%.3f\t%s\t%s\t%s\n",
			m["entry_id"], m["score"], m["type"], m["status"], m["title"])
		if p, ok := m["prohibited"].(string); ok && p != "" {
			fmt.Fprintf(stdout, "  PROHIBITED: %s\n", p)
		}
	}
	return nil
}

// ---- incident ----

// CmdIncident is a convenience wrapper around `post --type incident` with
// dedicated flags for the incident-specific fields. The body file is the
// main markdown narrative; --attempted / --observed / --hypotheses are
// optional structured fields.
func CmdIncident(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("incident", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("project", "", "project ID (required)")
	title := fs.String("title", "", "title (required)")
	file := fs.String("file", "", "body markdown file (- for stdin, required)")
	status := fs.String("status", "INVESTIGATING", "status")
	symptom := fs.String("symptom", "", "symptom one-liner")
	attempted := fs.String("attempted", "", "attempted_approaches file (optional)")
	observed := fs.String("observed", "", "observed_behavior file (optional)")
	hypotheses := fs.String("hypotheses", "", "hypotheses file (optional)")
	tagsCSV := fs.String("tags", "", "comma-separated tags")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *title == "" || *file == "" {
		return errors.New("--project, --title, --file are required")
	}
	body, err := ReadFile(*file, stdin)
	if err != nil {
		return err
	}
	c, err := Load()
	if err != nil {
		return err
	}
	cli, err := NewClient(c)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"project_id": *project,
		"type":       "incident",
		"title":      *title,
		"body":       string(body),
		"status":     *status,
	}
	if *symptom != "" {
		payload["symptom"] = *symptom
	}
	if *tagsCSV != "" {
		payload["tags"] = SplitCSV(*tagsCSV)
	}
	for k, p := range map[string]*string{
		"attempted_approaches": attempted,
		"observed_behavior":    observed,
		"hypotheses":           hypotheses,
	} {
		if *p == "" {
			continue
		}
		b, err := ReadFile(*p, stdin)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		payload[k] = string(b)
	}

	var out map[string]any
	if err := cli.Do(http.MethodPost, "/v1/entries", payload, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

// SplitCSV trims whitespace and drops empties.
func SplitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
