// Reverse-lookup commands (trigger/symptom/tags) and the incident
// convenience wrapper.

package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
)

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
