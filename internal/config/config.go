// Package config loads runtime configuration from environment variables.
// Configuration is process-wide and immutable after Load — callers should
// pass *Config explicitly rather than reading env vars throughout the
// codebase.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// SecretsMode controls how the write-time secret/PII scanner behaves.
type SecretsMode string

const (
	SecretsEnforce SecretsMode = "enforce"
	SecretsWarn    SecretsMode = "warn"
	SecretsOff     SecretsMode = "off"
)

type Config struct {
	HTTPAddr            string
	DBPath              string
	DashboardOpen       bool
	RequestBodyMax      int64
	AttachmentMaxBytes  int64 // KB_ATTACHMENT_MAX_BYTES (default 50MB). Applied to /v1/attachments POST only.
	LLMProvider         string
	LLMModel            string
	LLMAPIKey           string
	LLMEndpoint         string
	LLMMonthlyBudgetUSD float64
	SecretsMode         SecretsMode
	PiiMode             SecretsMode   // PII (email/card) scan mode; default off
	TriggerRulesPath    string        // optional path to trigger_rules.yaml
	ClusterInterval     time.Duration // Phase 3: background incident clustering cadence; 0 disables
	ClusterThreshold    float64       // Phase 3: Jaccard threshold for clustering (default 0.4)
	ClusterMinMembers   int           // Phase 3: minimum group size to surface a cluster (default 2)

	// Phase A auth (Google OAuth)
	GoogleClientID     string        // KB_OAUTH_GOOGLE_CLIENT_ID
	GoogleClientSecret string        // KB_OAUTH_GOOGLE_CLIENT_SECRET
	OAuthRedirectBase  string        // KB_OAUTH_REDIRECT_BASE (e.g. https://kb.example.com)
	AuthAllowDomains   []string      // KB_AUTH_ALLOW_DOMAINS=foo.com,bar.com — domain allow-list for new signups
	AuthAllowEmails    []string      // KB_AUTH_ALLOW_EMAILS=alice@x.com,bob@y.com — explicit email allow-list (OR with domain list)
	HTTPSEnabled       bool          // KB_HTTPS=1 → mark cookies Secure, expect https redirects
	SessionTTL         time.Duration // KB_SESSION_TTL — default 24h

	// Agent registration policy
	RegisterOpen bool // KB_REGISTER_OPEN=1 disables invite-code requirement (default false)

	// Personal librarian provisioning (issue #73). OPENCRAB_URL is the
	// base URL of the opencrab agent runtime (internal network, no
	// auth); empty disables the whole feature (no /my/librarian page).
	// OPENCRAB_OWNER_ID is the runtime's trusted caller id written into
	// each provisioned agent's trust row (owner_discord_id).
	OpencrabURL     string // OPENCRAB_URL (empty = feature disabled)
	OpencrabOwnerID string // OPENCRAB_OWNER_ID

	// External gate admin registration (issue #104 slice G2).
	// GATE_ADMIN_URL is the gate admin plane's base URL; empty disables
	// gate registration entirely (same gating pattern as OPENCRAB_URL).
	// GATE_OPERATOR_TOKEN is the operator credential sent as a bearer
	// token on every admin call.
	GateAdminURL      string // GATE_ADMIN_URL (empty = gate registration disabled)
	GateOperatorToken string // GATE_OPERATOR_TOKEN

	// GateTalkRESTForce is the gateway-cutover kill switch (issue
	// #104): GATE_TALK_REST_FORCE non-empty forces every /talk message
	// back onto the REST dispatch path, ignoring gate bindings. Set it
	// to revert the cutover instantly (env change + restart of THIS
	// process only — no opencrab restart, no DB surgery); unset it to
	// resume gateway delivery.
	GateTalkRESTForce bool // GATE_TALK_REST_FORCE (non-empty = force REST dispatch)

	// Expiring signed attachment URLs (issue #104 slice G4). HMAC key
	// for presigning /v1/attachments/{id}/content; empty disables the
	// feature (issuance errors, signatures never grant). Never logged.
	URLSigningKey string // KB_URL_SIGNING_KEY (empty = signed URLs disabled)
}

// Load reads configuration from environment variables (see design.md §10 /
// README env-var table).
func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:         envDefault("KB_HTTP_ADDR", ":8080"),
		DBPath:           envDefault("KB_DB_PATH", "./kb.db"),
		DashboardOpen:    envBool("KB_DASHBOARD_OPEN", false),
		LLMProvider:      strings.ToLower(os.Getenv("KB_LLM_PROVIDER")),
		LLMModel:         os.Getenv("KB_LLM_MODEL"),
		LLMAPIKey:        os.Getenv("KB_LLM_API_KEY"),
		LLMEndpoint:      os.Getenv("KB_LLM_ENDPOINT"),
		TriggerRulesPath: os.Getenv("KB_TRIGGER_RULES_PATH"),
	}

	bodyMax, err := envInt64("KB_REQUEST_BODY_MAX", 1<<20)
	if err != nil {
		return nil, fmt.Errorf("KB_REQUEST_BODY_MAX: %w", err)
	}
	c.RequestBodyMax = bodyMax

	// 50MB default — large enough for typical screenshots / charts /
	// short clips, small enough that a malicious or buggy uploader
	// can't trivially exhaust disk in a single request.
	attMax, err := envInt64("KB_ATTACHMENT_MAX_BYTES", 50<<20)
	if err != nil {
		return nil, fmt.Errorf("KB_ATTACHMENT_MAX_BYTES: %w", err)
	}
	c.AttachmentMaxBytes = attMax

	budget, err := envFloat("KB_LLM_MONTHLY_BUDGET_USD", 0)
	if err != nil {
		return nil, fmt.Errorf("KB_LLM_MONTHLY_BUDGET_USD: %w", err)
	}
	c.LLMMonthlyBudgetUSD = budget

	mode := strings.ToLower(envDefault("KB_SECRETS_MODE", "enforce"))
	switch SecretsMode(mode) {
	case SecretsEnforce, SecretsWarn, SecretsOff:
		c.SecretsMode = SecretsMode(mode)
	default:
		return nil, fmt.Errorf("KB_SECRETS_MODE: must be enforce|warn|off, got %q", mode)
	}

	// PII (email / card numbers) is a SEPARATE switch, default OFF —
	// omoikane is shared inside one org and per-project scope is the
	// privacy boundary, so PII is not policed unless a deployment opts in.
	piiMode := strings.ToLower(envDefault("KB_PII_MODE", "off"))
	switch SecretsMode(piiMode) {
	case SecretsEnforce, SecretsWarn, SecretsOff:
		c.PiiMode = SecretsMode(piiMode)
	default:
		return nil, fmt.Errorf("KB_PII_MODE: must be enforce|warn|off, got %q", piiMode)
	}

	clusterInterval, err := envDuration("KB_CLUSTER_INTERVAL", 0)
	if err != nil {
		return nil, fmt.Errorf("KB_CLUSTER_INTERVAL: %w", err)
	}
	c.ClusterInterval = clusterInterval

	thr, err := envFloat("KB_CLUSTER_THRESHOLD", 0.4)
	if err != nil {
		return nil, fmt.Errorf("KB_CLUSTER_THRESHOLD: %w", err)
	}
	c.ClusterThreshold = thr

	minMembers, err := envInt64("KB_CLUSTER_MIN_MEMBERS", 2)
	if err != nil {
		return nil, fmt.Errorf("KB_CLUSTER_MIN_MEMBERS: %w", err)
	}
	c.ClusterMinMembers = int(minMembers)

	// Phase A auth — all optional. Google OAuth simply stays disabled
	// if client_id / client_secret are unset.
	c.GoogleClientID = os.Getenv("KB_OAUTH_GOOGLE_CLIENT_ID")
	c.GoogleClientSecret = os.Getenv("KB_OAUTH_GOOGLE_CLIENT_SECRET")
	c.OAuthRedirectBase = strings.TrimRight(os.Getenv("KB_OAUTH_REDIRECT_BASE"), "/")
	c.AuthAllowDomains = splitCSVConfig(os.Getenv("KB_AUTH_ALLOW_DOMAINS"))
	c.AuthAllowEmails = splitCSVConfig(os.Getenv("KB_AUTH_ALLOW_EMAILS"))
	c.HTTPSEnabled = envBool("KB_HTTPS", false)
	sessTTL, err := envDuration("KB_SESSION_TTL", 24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("KB_SESSION_TTL: %w", err)
	}
	c.SessionTTL = sessTTL
	c.RegisterOpen = envBool("KB_REGISTER_OPEN", false)

	c.OpencrabURL = strings.TrimRight(os.Getenv("OPENCRAB_URL"), "/")
	c.OpencrabOwnerID = strings.TrimSpace(os.Getenv("OPENCRAB_OWNER_ID"))

	c.GateAdminURL = strings.TrimRight(os.Getenv("GATE_ADMIN_URL"), "/")
	c.GateOperatorToken = strings.TrimSpace(os.Getenv("GATE_OPERATOR_TOKEN"))
	c.GateTalkRESTForce = strings.TrimSpace(os.Getenv("GATE_TALK_REST_FORCE")) != ""

	c.URLSigningKey = strings.TrimSpace(os.Getenv("KB_URL_SIGNING_KEY"))

	return c, nil
}

func splitCSVConfig(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	return time.ParseDuration(v)
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt64(key string, fallback int64) (int64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	return strconv.ParseInt(v, 10, 64)
}

func envFloat(key string, fallback float64) (float64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	return strconv.ParseFloat(v, 64)
}
