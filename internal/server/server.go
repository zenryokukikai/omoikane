// Package server holds the kb-server entry-point logic. It is split out of
// cmd/kb-server so it can be tested directly — the cmd/ shim is just
// `os.Exit(server.Run(os.Args[1:], os.Stdout, os.Stderr))`.
package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/api"
	"github.com/kojira/omoikane/internal/auth/oauth"
	"github.com/kojira/omoikane/internal/config"
	"github.com/kojira/omoikane/internal/dashboard"
	"github.com/kojira/omoikane/internal/enrich"
	"github.com/kojira/omoikane/internal/store"
)

// BuildVersion is overridden at link time. Exposed so cmd/kb-server can set
// it before calling Run().
var BuildVersion = "dev"

// Run is the program entry point. Returns a process exit code:
//   - 0 on graceful shutdown
//   - 1 on configuration / startup error
//   - 2 on usage error
//
// stdout/stderr are abstracted so tests can capture output without touching
// the real process file descriptors.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "admin-token":
			return AdminToken(args[1:], stdout, stderr)
		case "version":
			fmt.Fprintln(stdout, "kb-server", BuildVersion)
			return 0
		case "-h", "--help", "help":
			Usage(stdout)
			return 0
		}
	}
	if err := runServer(stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "fatal:", err)
		return 1
	}
	return 0
}

// Usage prints the kb-server help text.
func Usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `kb-server — omoikane Agent Knowledge Base server

usage:
  kb-server                              start the HTTP server
  kb-server admin-token [flags]          issue an API token
  kb-server version                      print build info

run with no arguments to start the server. See README.md for env-var config.`)
}

// runServer wires the dependency graph and listens. Returns when the process
// receives SIGINT/SIGTERM or the listener errors. Exposed for tests via
// RunServerForTest below.
func runServer(stdout, _ io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	if cfg.TriggerRulesPath != "" {
		n, err := st.LoadTriggerRulesYAML(ctx, cfg.TriggerRulesPath)
		if err != nil {
			return fmt.Errorf("trigger rules: %w", err)
		}
		logger.Info("trigger rules loaded", "path", cfg.TriggerRulesPath, "count", n)
	}

	root, err := BuildRouter(st, cfg, logger)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	stop, stopNotify := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopNotify()
	go func() {
		<-stop.Done()
		logger.Info("shutting down")
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(shutdownCtx)
		cancel()
	}()

	if cfg.ClusterInterval > 0 {
		go runClusterLoop(ctx, st, cfg, logger)
	}

	logger.Info("listening",
		"addr", cfg.HTTPAddr, "db", cfg.DBPath,
		"llm_provider", cfg.LLMProvider, "secrets_mode", cfg.SecretsMode,
		"dashboard_open", cfg.DashboardOpen,
		"cluster_interval", cfg.ClusterInterval)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// newDashboard is overridable by tests so the otherwise-unreachable
// dashboard.New error path can be covered.
var newDashboard = dashboard.New

// runClusterLoop is the Phase 3 background incident-clustering goroutine.
// It runs every cfg.ClusterInterval until ctx is cancelled. We don't add
// jitter — the only contention is with the same SQLite DB the API uses,
// and SQLite's busy_timeout makes this safe.
func runClusterLoop(ctx context.Context, st *store.Store, cfg *config.Config, logger *slog.Logger) {
	t := time.NewTicker(cfg.ClusterInterval)
	defer t.Stop()
	// Fire once at startup so a fresh boot doesn't wait a full interval.
	runClusterOnce(ctx, st, cfg, logger)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runClusterOnce(ctx, st, cfg, logger)
		}
	}
}

func runClusterOnce(ctx context.Context, st *store.Store, cfg *config.Config, logger *slog.Logger) {
	created, added, err := st.BuildIncidentClusters(ctx, "", cfg.ClusterThreshold, cfg.ClusterMinMembers)
	if err != nil {
		logger.Warn("cluster pass failed", "err", err)
		return
	}
	if created > 0 || added > 0 {
		logger.Info("cluster pass", "clusters", created, "members", added)
	}
}

// BuildRouter assembles the API + dashboard + middleware stack. Extracted
// so tests can exercise the wiring without binding a TCP port.
func BuildRouter(st *store.Store, cfg *config.Config, logger *slog.Logger) (http.Handler, error) {
	enr := enrich.New(cfg.LLMProvider, cfg.LLMModel, cfg.LLMAPIKey, cfg.LLMEndpoint, logger)

	apiH := &api.Handler{
		Store:       st,
		Enricher:    enr,
		SecretsMode: cfg.SecretsMode,
		Logger:      logger,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		BuildInfo:   BuildVersion,

		AuthAllowDomains: cfg.AuthAllowDomains,
		AuthAllowEmails:  cfg.AuthAllowEmails,
		HTTPSEnabled:     cfg.HTTPSEnabled,
		SessionTTL:       cfg.SessionTTL,
	}
	if cfg.GoogleClientID != "" && cfg.GoogleClientSecret != "" && cfg.OAuthRedirectBase != "" {
		apiH.OAuthGoogle = &oauth.Google{
			ClientID:     cfg.GoogleClientID,
			ClientSecret: cfg.GoogleClientSecret,
			RedirectURI:  cfg.OAuthRedirectBase + "/v1/auth/google/callback",
		}
		apiH.OAuthRedirectBase = cfg.OAuthRedirectBase
		logger.Info("oauth.google configured", "redirect", apiH.OAuthGoogle.(*oauth.Google).RedirectURI)
	}
	apiH.RegisterOpen = cfg.RegisterOpen
	apiH.AttachmentMaxBytes = cfg.AttachmentMaxBytes
	dashH, err := newDashboard(st, cfg.DashboardOpen)
	if err != nil {
		return nil, fmt.Errorf("dashboard: %w", err)
	}
	dashH.GoogleEnabled = apiH.OAuthGoogle != nil

	root := chi.NewRouter()
	root.Use(api.RequestID)
	root.Use(api.Recoverer(logger))
	root.Use(api.AccessLog(logger))
	root.Use(api.Audit(st, logger))
	if cfg.RequestBodyMax > 0 {
		// /v1/attachments has its own (larger) per-route cap; exempt
		// it from the root-level small-body cap so the two don't fight.
		// MaxBytesReader composes by taking the minimum, so without
		// the exemption the larger per-route cap would have no effect.
		root.Use(api.LimitBody(cfg.RequestBodyMax, "/v1/attachments"))
	}
	apiH.Mount(root)
	dashH.Mount(root)
	return root, nil
}

// AdminStorer is the subset of *store.Store that AdminToken needs. The
// interface is package-private (exported only by name pattern) so tests can
// inject a mock that triggers the defensive error branches; production code
// keeps using *store.Store directly.
type AdminStorer interface {
	GetUser(ctx context.Context, id string) (*store.User, error)
	CreateUser(ctx context.Context, u *store.User) error
	CreateToken(ctx context.Context, userID, name string, scopes []string, expiresAt *time.Time) (string, error)
	SetUserEmail(ctx context.Context, userID, email string) error
	Close() error
}

// openAdminStore is overridable by tests to inject a failing store.
var openAdminStore = func(ctx context.Context, path string) (AdminStorer, error) {
	return store.Open(ctx, path)
}

// AdminToken issues an API token. Exposed for direct unit testing.
func AdminToken(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin-token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	user := fs.String("user", "admin", "user ID (created if missing)")
	name := fs.String("name", "default", "human-readable token name")
	scopes := fs.String("scopes", "read,write,admin", "comma-separated scopes")
	role := fs.String("role", "admin", "user role when creating the user")
	email := fs.String("email", "", "set or update user's email (enables Google OAuth login matching)")
	ttl := fs.Duration("ttl", 0, "token TTL (0 = no expiry)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return 1
	}
	ctx := context.Background()
	st, err := openAdminStore(ctx, cfg.DBPath)
	if err != nil {
		fmt.Fprintln(stderr, "open store:", err)
		return 1
	}
	defer st.Close()

	return runAdminToken(ctx, st, *user, *name, *role, *scopes, *email, *ttl, stdout, stderr)
}

func runAdminToken(ctx context.Context, st AdminStorer, user, name, role, scopes, email string, ttl time.Duration, stdout, stderr io.Writer) int {
	if _, err := st.GetUser(ctx, user); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			fmt.Fprintln(stderr, "lookup user:", err)
			return 1
		}
		if err := st.CreateUser(ctx, &store.User{ID: user, Name: user, Role: role, Email: email}); err != nil {
			fmt.Fprintln(stderr, "create user:", err)
			return 1
		}
	} else if email != "" {
		// Existing user — update / set email so a future Google login can
		// be matched to this account.
		if err := st.SetUserEmail(ctx, user, email); err != nil {
			fmt.Fprintln(stderr, "set email:", err)
			return 1
		}
	}

	var expiresAt *time.Time
	if ttl > 0 {
		e := time.Now().Add(ttl)
		expiresAt = &e
	}
	plain, err := st.CreateToken(ctx, user, name, splitCSV(scopes), expiresAt)
	if err != nil {
		fmt.Fprintln(stderr, "create token:", err)
		return 1
	}
	fmt.Fprintln(stdout, "# kb-server admin-token")
	fmt.Fprintln(stdout, "# Store this token securely; it is shown only once.")
	fmt.Fprintln(stdout, "USER  :", user)
	fmt.Fprintln(stdout, "NAME  :", name)
	fmt.Fprintln(stdout, "SCOPES:", scopes)
	if email != "" {
		fmt.Fprintln(stdout, "EMAIL :", email)
	}
	if expiresAt != nil {
		fmt.Fprintln(stdout, "EXPIRY:", expiresAt.Format(time.RFC3339))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, plain)
	return 0
}

// splitCSV is a comma-split that trims whitespace and drops empties.
func splitCSV(s string) []string {
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

// ListenAndServe is a thin wrapper around http.Server.ListenAndServe that
// the test harness can call with its own listener. Returns nil on graceful
// shutdown, the original error otherwise.
func ListenAndServe(srv *http.Server, ln net.Listener) error {
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
