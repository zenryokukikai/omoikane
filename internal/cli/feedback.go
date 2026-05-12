package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// CmdFeedback handles `kb feedback (record|judge|signals|review-queue)`.
//
//   record       create a usage_case (the agent records what it considered)
//   judge        PATCH a usage_case with the post-hoc outcome/result
//   signals      print aggregated signals for an entry
//   review-queue list entries flagged for human review
func CmdFeedback(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: kb feedback (record|judge|signals|review-queue)")
	}
	verb := args[0]
	rest := args[1:]
	switch verb {
	case "record":
		return cmdFeedbackRecord(rest, stdout)
	case "judge":
		return cmdFeedbackJudge(rest, stdout)
	case "signals":
		return cmdFeedbackSignals(rest, stdout)
	case "review-queue":
		return cmdFeedbackReviewQueue(rest, stdout)
	}
	return fmt.Errorf("unknown feedback subcommand: %s", verb)
}

func cmdFeedbackRecord(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("feedback-record", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	entry := fs.String("entry", "", "entry ID (required)")
	trigger := fs.String("trigger", "", "trigger_query that surfaced the entry")
	outcome := fs.String("outcome", "", "applied|considered_rejected|ignored")
	result := fs.String("result", "", "helpful|partially_helpful|not_helpful|misleading|unknown")
	notes := fs.String("notes", "", "free-form notes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *entry == "" {
		return errors.New("--entry is required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	body := map[string]any{"entry_id": *entry}
	if *trigger != "" {
		body["trigger_query"] = *trigger
	}
	if *outcome != "" {
		body["outcome"] = *outcome
	}
	if *result != "" {
		body["result"] = *result
	}
	if *notes != "" {
		body["notes"] = *notes
	}
	var out map[string]any
	if err := cli.Do(http.MethodPost, "/v1/cases", body, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

func cmdFeedbackJudge(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("feedback-judge", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("case", "", "case_id (required)")
	outcome := fs.String("outcome", "", "applied|considered_rejected|ignored")
	result := fs.String("result", "", "helpful|partially_helpful|not_helpful|misleading|unknown")
	evidence := fs.String("evidence", "", "result_evidence text or URL")
	judgedBy := fs.String("by", "", "result_judged_by identifier")
	notes := fs.String("notes", "", "free-form notes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("--case is required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	patch := map[string]any{}
	if *outcome != "" {
		patch["outcome"] = *outcome
	}
	if *result != "" {
		patch["result"] = *result
	}
	if *evidence != "" {
		patch["result_evidence"] = *evidence
	}
	if *judgedBy != "" {
		patch["result_judged_by"] = *judgedBy
	}
	if *notes != "" {
		patch["notes"] = *notes
	}
	var out map[string]any
	if err := cli.Do(http.MethodPatch, "/v1/cases/"+url.PathEscape(*id), patch, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

func cmdFeedbackSignals(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: kb feedback signals <entry-id>")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	var out map[string]any
	if err := cli.Do(http.MethodGet, "/v1/entries/"+url.PathEscape(args[0])+"/signals", nil, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

func cmdFeedbackReviewQueue(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("feedback-review-queue", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	limit := fs.Int("limit", 50, "max rows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	var out struct {
		Queue []map[string]any `json:"queue"`
	}
	if err := cli.Do(http.MethodGet, fmt.Sprintf("/v1/review-queue?limit=%d", *limit), nil, nil, &out); err != nil {
		return err
	}
	for _, r := range out.Queue {
		fmt.Fprintf(stdout, "%s\tmisleading=%v\ttotal=%v\tscore=%v\t%s\n",
			r["id"], r["misleading_count"], r["total_uses"], r["helpfulness_score"], r["title"])
	}
	return nil
}

// ---- relations ----

func CmdRelations(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: kb relations (link|unlink|list)")
	}
	verb := args[0]
	rest := args[1:]
	switch verb {
	case "link":
		return cmdRelationsLink(rest, stdout)
	case "unlink":
		return cmdRelationsUnlink(rest, stdout)
	case "list":
		return cmdRelationsList(rest, stdout)
	}
	return fmt.Errorf("unknown relations subcommand: %s", verb)
}

func cmdRelationsLink(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relations-link", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	from := fs.String("from", "", "from_id (required)")
	to := fs.String("to", "", "to_id (required)")
	rel := fs.String("type", "", "rel_type (required)")
	conf := fs.Float64("confidence", 1.0, "confidence")
	source := fs.String("source", "human", "source identifier")
	notes := fs.String("notes", "", "notes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" || *rel == "" {
		return errors.New("--from, --to, --type are required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	body := map[string]any{
		"from_id":    *from,
		"to_id":      *to,
		"rel_type":   *rel,
		"confidence": *conf,
		"source":     *source,
	}
	if *notes != "" {
		body["notes"] = *notes
	}
	var out map[string]any
	if err := cli.Do(http.MethodPost, "/v1/relations", body, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

func cmdRelationsUnlink(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relations-unlink", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	from := fs.String("from", "", "from_id (required)")
	to := fs.String("to", "", "to_id (required)")
	rel := fs.String("type", "", "rel_type (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" || *rel == "" {
		return errors.New("--from, --to, --type are required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("from_id", *from)
	q.Set("to_id", *to)
	q.Set("rel_type", *rel)
	if err := cli.Do(http.MethodDelete, "/v1/relations?"+q.Encode(), nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "unlinked:", *from, "→", *to, "("+*rel+")")
	return nil
}

func cmdRelationsList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("relations-list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	entry := fs.String("entry", "", "entry ID (required)")
	direction := fs.String("direction", "outgoing", "outgoing|incoming|both")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *entry == "" {
		return errors.New("--entry is required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	var out struct {
		Relations []map[string]any `json:"relations"`
	}
	q := url.Values{}
	q.Set("direction", *direction)
	if err := cli.Do(http.MethodGet, "/v1/entries/"+url.PathEscape(*entry)+"/relations?"+q.Encode(), nil, nil, &out); err != nil {
		return err
	}
	for _, r := range out.Relations {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tconf=%v\n", r["FromID"], r["RelType"], r["ToID"], r["Confidence"])
	}
	return nil
}

// ---- situations ----

func CmdSituations(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: kb situations (create|list|get|link|delete|lookup)")
	}
	verb := args[0]
	rest := args[1:]
	switch verb {
	case "create":
		return cmdSituationCreate(rest, stdin, stdout)
	case "list":
		return cmdSituationList(rest, stdout)
	case "get":
		return cmdSituationGet(rest, stdout)
	case "link":
		return cmdSituationLink(rest, stdout)
	case "delete":
		return cmdSituationDelete(rest, stdout)
	case "lookup":
		return cmdSituationLookup(rest, stdout)
	}
	return fmt.Errorf("unknown situations subcommand: %s", verb)
}

func cmdSituationCreate(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("situations-create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	desc := fs.String("description", "", "description (required, or use --file)")
	file := fs.String("file", "", "description file (- for stdin)")
	project := fs.String("project", "", "optional project ID")
	domain := fs.String("domain", "", "optional domain")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := *desc
	if *file != "" {
		b, err := ReadFile(*file, stdin)
		if err != nil {
			return err
		}
		text = string(b)
	}
	if text == "" {
		return errors.New("--description or --file is required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	body := map[string]any{"description": text}
	if *project != "" {
		body["project_id"] = *project
	}
	if *domain != "" {
		body["domain"] = *domain
	}
	var out map[string]any
	if err := cli.Do(http.MethodPost, "/v1/situations", body, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

func cmdSituationList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("situations-list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("project", "", "filter by project")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	q := url.Values{}
	if *project != "" {
		q.Set("project_id", *project)
	}
	var out struct {
		Situations []map[string]any `json:"situations"`
	}
	if err := cli.Do(http.MethodGet, "/v1/situations?"+q.Encode(), nil, nil, &out); err != nil {
		return err
	}
	for _, s := range out.Situations {
		fmt.Fprintf(stdout, "%s\t%s\n", s["ID"], s["Description"])
	}
	return nil
}

func cmdSituationGet(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: kb situations get <id>")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	var out map[string]any
	if err := cli.Do(http.MethodGet, "/v1/situations/"+url.PathEscape(args[0]), nil, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

func cmdSituationLink(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("situations-link", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	situation := fs.String("situation", "", "situation ID (required)")
	entry := fs.String("entry", "", "entry ID (required)")
	relevance := fs.Float64("relevance", 1.0, "relevance score")
	notes := fs.String("notes", "", "notes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *situation == "" || *entry == "" {
		return errors.New("--situation and --entry are required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	body := map[string]any{
		"entry_id":  *entry,
		"relevance": *relevance,
	}
	if *notes != "" {
		body["notes"] = *notes
	}
	if err := cli.Do(http.MethodPost, "/v1/situations/"+url.PathEscape(*situation)+"/entries", body, nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "linked:", *situation, "→", *entry)
	return nil
}

func cmdSituationDelete(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: kb situations delete <id>")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	if err := cli.Do(http.MethodDelete, "/v1/situations/"+url.PathEscape(args[0]), nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "deleted:", args[0])
	return nil
}

func cmdSituationLookup(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("situations-lookup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	query := fs.String("query", "", "situation description (required)")
	topK := fs.Int("top-k", 10, "max results")
	project := fs.String("project", "", "optional project filter")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *query == "" {
		return errors.New("--query is required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	body := map[string]any{
		"situation_description": *query,
		"top_k":                 *topK,
	}
	if *project != "" {
		body["project_id"] = *project
	}
	return printLookupResult(cli, "/v1/lookup/by-situation", body, stdout)
}

// ---- clusters ----

func CmdCluster(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: kb cluster (list|get|promote|dismiss|rebuild)")
	}
	verb := args[0]
	rest := args[1:]
	switch verb {
	case "list":
		return cmdClusterList(rest, stdout)
	case "get":
		return cmdClusterGet(rest, stdout)
	case "promote":
		return cmdClusterPromote(rest, stdout)
	case "dismiss":
		return cmdClusterDismiss(rest, stdout)
	case "rebuild":
		return cmdClusterRebuild(rest, stdout)
	}
	return fmt.Errorf("unknown cluster subcommand: %s", verb)
}

func cmdClusterList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("cluster-list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("project", "", "filter by project")
	status := fs.String("status", "", "filter by status (OPEN|PROMOTED|DISMISSED)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	q := url.Values{}
	if *project != "" {
		q.Set("project_id", *project)
	}
	if *status != "" {
		q.Set("status", *status)
	}
	var out struct {
		Clusters []map[string]any `json:"clusters"`
	}
	if err := cli.Do(http.MethodGet, "/v1/clusters?"+q.Encode(), nil, nil, &out); err != nil {
		return err
	}
	for _, c := range out.Clusters {
		fmt.Fprintf(stdout, "%s\t%s\tmembers=%v\t%s\n",
			c["ID"], c["Status"], c["MemberCount"], c["Title"])
	}
	return nil
}

func cmdClusterGet(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: kb cluster get <id>")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	var out map[string]any
	if err := cli.Do(http.MethodGet, "/v1/clusters/"+url.PathEscape(args[0]), nil, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

func cmdClusterPromote(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("cluster-promote", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cluster := fs.String("cluster", "", "cluster ID (required)")
	entry := fs.String("entry", "", "promoted-to entry ID (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cluster == "" || *entry == "" {
		return errors.New("--cluster and --entry are required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	if err := cli.Do(http.MethodPost, "/v1/clusters/"+url.PathEscape(*cluster)+"/promote",
		map[string]any{"entry_id": *entry}, nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "promoted:", *cluster, "→", *entry)
	return nil
}

func cmdClusterDismiss(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: kb cluster dismiss <id>")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	if err := cli.Do(http.MethodPost, "/v1/clusters/"+url.PathEscape(args[0])+"/dismiss",
		nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "dismissed:", args[0])
	return nil
}

func cmdClusterRebuild(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("cluster-rebuild", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("project", "", "filter by project")
	threshold := fs.Float64("threshold", 0.4, "Jaccard threshold")
	minMembers := fs.Int("min-members", 2, "minimum group size")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	body := map[string]any{
		"threshold":   *threshold,
		"min_members": *minMembers,
	}
	if *project != "" {
		body["project_id"] = *project
	}
	var out map[string]any
	if err := cli.Do(http.MethodPost, "/v1/clusters/rebuild", body, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

// loadClient is a shortcut for the Load+NewClient dance that every Phase 3
// subcommand uses.
func loadClient() (*Client, error) {
	c, err := Load()
	if err != nil {
		return nil, err
	}
	return NewClient(c)
}
