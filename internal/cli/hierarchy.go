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

// CmdBrowse — `kb browse (list|create|get|attach|detach|delete)`
func CmdBrowse(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: kb browse (list|create|get|attach|detach|delete)")
	}
	verb := args[0]
	rest := args[1:]
	switch verb {
	case "list":
		return cmdBrowseList(rest, stdout)
	case "create":
		return cmdBrowseCreate(rest, stdout)
	case "get":
		return cmdBrowseGet(rest, stdout)
	case "attach":
		return cmdBrowseAttach(rest, stdout)
	case "detach":
		return cmdBrowseDetach(rest, stdout)
	case "delete":
		return cmdBrowseDelete(rest, stdout)
	}
	return fmt.Errorf("unknown browse subcommand: %s", verb)
}

func cmdBrowseList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("browse-list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("project", "", "project filter")
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
		Nodes []map[string]any `json:"nodes"`
	}
	if err := cli.Do(http.MethodGet, "/v1/browse?"+q.Encode(), nil, nil, &out); err != nil {
		return err
	}
	for _, n := range out.Nodes {
		fmt.Fprintf(stdout, "%s\t%s\n", n["ID"], n["Name"])
	}
	return nil
}

func cmdBrowseCreate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("browse-create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "node name (required)")
	parent := fs.String("parent", "", "parent node ID")
	project := fs.String("project", "", "project ID")
	desc := fs.String("description", "", "description")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	body := map[string]any{"name": *name}
	if *parent != "" {
		body["parent_id"] = *parent
	}
	if *project != "" {
		body["project_id"] = *project
	}
	if *desc != "" {
		body["description"] = *desc
	}
	var out map[string]any
	if err := cli.Do(http.MethodPost, "/v1/browse", body, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

func cmdBrowseGet(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: kb browse get <node-id>")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	var out map[string]any
	if err := cli.Do(http.MethodGet, "/v1/browse/"+url.PathEscape(args[0]), nil, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}

func cmdBrowseAttach(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("browse-attach", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	node := fs.String("node", "", "node ID (required)")
	entry := fs.String("entry", "", "entry ID (required)")
	weight := fs.Float64("weight", 1.0, "weight")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *node == "" || *entry == "" {
		return errors.New("--node and --entry are required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	if err := cli.Do(http.MethodPost,
		"/v1/browse/"+url.PathEscape(*node)+"/entries",
		map[string]any{"entry_id": *entry, "weight": *weight}, nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "attached:", *entry, "→", *node)
	return nil
}

func cmdBrowseDetach(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("browse-detach", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	node := fs.String("node", "", "node ID (required)")
	entry := fs.String("entry", "", "entry ID (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *node == "" || *entry == "" {
		return errors.New("--node and --entry are required")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	if err := cli.Do(http.MethodDelete,
		"/v1/browse/"+url.PathEscape(*node)+"/entries/"+url.PathEscape(*entry),
		nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "detached:", *entry, "from", *node)
	return nil
}

func cmdBrowseDelete(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: kb browse delete <node-id>")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	if err := cli.Do(http.MethodDelete, "/v1/browse/"+url.PathEscape(args[0]), nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "deleted:", args[0])
	return nil
}

// CmdIndex — `kb index [--group-by tag|recent|hierarchy]`
func CmdIndex(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	groupBy := fs.String("group-by", "tag", "tag|recent|hierarchy")
	project := fs.String("project", "", "project filter")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("group_by", *groupBy)
	if *project != "" {
		q.Set("project_id", *project)
	}
	var out struct {
		GroupBy string `json:"group_by"`
		Buckets []struct {
			Key   string `json:"Key"`
			Label string `json:"Label"`
			Count int    `json:"Count"`
		} `json:"buckets"`
	}
	if err := cli.Do(http.MethodGet, "/v1/index?"+q.Encode(), nil, nil, &out); err != nil {
		return err
	}
	for _, b := range out.Buckets {
		fmt.Fprintf(stdout, "%s\t%d\n", b.Label, b.Count)
	}
	return nil
}

// CmdReflect — `kb reflect E-1 E-2 ... --prompt "..."`
func CmdReflect(args []string, stdout io.Writer) error {
	// Positional entry IDs first, then flags. Both `kb reflect T-X --prompt`
	// and `kb reflect --prompt X T-X` work because flag.Parse stops at the
	// first non-flag.
	ids := []string{}
	rest := []string{}
	for i, a := range args {
		if len(a) > 0 && a[0] == '-' {
			rest = args[i:]
			break
		}
		ids = append(ids, a)
	}
	fs := flag.NewFlagSet("reflect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	prompt := fs.String("prompt", "", "cross-entry question")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if len(ids) == 0 {
		return errors.New("usage: kb reflect <entry-id>... [--prompt …]")
	}
	cli, err := loadClient()
	if err != nil {
		return err
	}
	body := map[string]any{"entry_ids": ids}
	if *prompt != "" {
		body["prompt"] = *prompt
	}
	var out map[string]any
	if err := cli.Do(http.MethodPost, "/v1/reflect", body, nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(stdout, string(b))
	return nil
}
