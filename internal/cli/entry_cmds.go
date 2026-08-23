// Entry write/read/mutate/query commands: post, get, update, delete,
// history, list, search.

package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

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
