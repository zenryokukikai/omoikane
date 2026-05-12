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
	LLMProvider         string
	LLMModel            string
	LLMAPIKey           string
	LLMEndpoint         string
	LLMMonthlyBudgetUSD float64
	SecretsMode         SecretsMode
	TriggerRulesPath    string        // optional path to trigger_rules.yaml
	ClusterInterval     time.Duration // Phase 3: background incident clustering cadence; 0 disables
	ClusterThreshold    float64       // Phase 3: Jaccard threshold for clustering (default 0.4)
	ClusterMinMembers   int           // Phase 3: minimum group size to surface a cluster (default 2)
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

	return c, nil
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
