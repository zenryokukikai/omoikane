// Command kb-mcp is the stdio MCP adapter for omoikane. It proxies tool
// calls to the Core HTTP API. All logic lives in internal/mcp; this file
// is a 3-line shim per docs/design.md §19 (cmd/ shims are exempt from
// coverage).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/zenryokukikai/omoikane/internal/mcp"
)

func main() {
	core := os.Getenv("KB_CORE_URL")
	token := os.Getenv("KB_INTERNAL_TOKEN")
	if token == "" {
		// Fall back to the user token so the same binary works as a CLI
		// adapter without needing an internal service account.
		token = os.Getenv("KB_TOKEN")
	}
	if core == "" || token == "" {
		fmt.Fprintln(os.Stderr, "kb-mcp: KB_CORE_URL and KB_INTERNAL_TOKEN (or KB_TOKEN) are required")
		os.Exit(2)
	}
	s := &mcp.Server{
		CoreURL:   core,
		Token:     token,
		ProjectID: os.Getenv("KB_PROJECT"),
	}
	if err := s.Run(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "kb-mcp:", err)
		os.Exit(1)
	}
}
