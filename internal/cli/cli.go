// Package cli is the omoikane CLI logic. Extracted from cmd/kb so it can
// be tested directly — the cmd/kb shim is just
// `os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))`.
//
// This file holds the dispatcher (Run/Usage), the config/projects
// commands, and small shared helpers. The other command families live in
// client.go, entry_cmds.go, lookup_cmds.go, feedback.go, hierarchy.go,
// and open_work.go.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

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
	case "open":
		err = CmdOpen(args[1:], stdout)
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
  kb reflect <entry-id>... [--prompt <text>]

  kb open list     [--role <role>] [--effort S|M|L]
  kb open claim    --entry <id> --role <role> --instance <id> [--effort <S|M|L>]
  kb open release  --entry <id> --instance <id>
  kb open merge    --entry <id> --instance <id> [--result <text>] [--impl <id>]`)
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
