package config

import "testing"

func TestDefaults(t *testing.T) {
	for _, k := range []string{"KB_HTTP_ADDR", "KB_DB_PATH", "KB_LLM_PROVIDER",
		"KB_DASHBOARD_OPEN", "KB_REQUEST_BODY_MAX", "KB_LLM_MONTHLY_BUDGET_USD",
		"KB_SECRETS_MODE"} {
		t.Setenv(k, "")
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr: %q", c.HTTPAddr)
	}
	if c.DBPath != "./kb.db" {
		t.Fatalf("DBPath: %q", c.DBPath)
	}
	if c.SecretsMode != SecretsEnforce {
		t.Fatalf("SecretsMode default should be enforce, got %q", c.SecretsMode)
	}
	if c.RequestBodyMax != 1<<20 {
		t.Fatalf("RequestBodyMax: %d", c.RequestBodyMax)
	}
}

func TestSecretsModeOverrides(t *testing.T) {
	for _, mode := range []string{"enforce", "warn", "off"} {
		t.Setenv("KB_SECRETS_MODE", mode)
		c, err := Load()
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if string(c.SecretsMode) != mode {
			t.Fatalf("%s: got %s", mode, c.SecretsMode)
		}
	}
}

func TestBadSecretsModeRejected(t *testing.T) {
	t.Setenv("KB_SECRETS_MODE", "bogus")
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}

func TestBadIntRejected(t *testing.T) {
	t.Setenv("KB_REQUEST_BODY_MAX", "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}

func TestBadFloatRejected(t *testing.T) {
	t.Setenv("KB_LLM_MONTHLY_BUDGET_USD", "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}

func TestOpencrabConfig(t *testing.T) {
	// Default: unset → feature disabled (empty URL).
	t.Setenv("OPENCRAB_URL", "")
	t.Setenv("OPENCRAB_OWNER_ID", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.OpencrabURL != "" || c.OpencrabOwnerID != "" {
		t.Fatalf("defaults: %q %q", c.OpencrabURL, c.OpencrabOwnerID)
	}
	// Set: trailing slash trimmed, owner id whitespace trimmed.
	t.Setenv("OPENCRAB_URL", "http://crab.internal:3000/")
	t.Setenv("OPENCRAB_OWNER_ID", " owner-1 ")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.OpencrabURL != "http://crab.internal:3000" {
		t.Fatalf("OpencrabURL: %q", c.OpencrabURL)
	}
	if c.OpencrabOwnerID != "owner-1" {
		t.Fatalf("OpencrabOwnerID: %q", c.OpencrabOwnerID)
	}
}

func TestGateTalkRESTForce(t *testing.T) {
	// Default: unset → suppression active (gateway path claims).
	t.Setenv("GATE_TALK_REST_FORCE", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.GateTalkRESTForce {
		t.Fatal("unset → false")
	}
	// Whitespace-only counts as unset.
	t.Setenv("GATE_TALK_REST_FORCE", "  ")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.GateTalkRESTForce {
		t.Fatal("whitespace → false")
	}
	// Any non-empty value forces REST dispatch (kill switch on).
	t.Setenv("GATE_TALK_REST_FORCE", "1")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.GateTalkRESTForce {
		t.Fatal("non-empty → true")
	}
}

func TestEnvBoolBranches(t *testing.T) {
	t.Setenv("KB_DASHBOARD_OPEN", "1")
	c, _ := Load()
	if !c.DashboardOpen {
		t.Fatal("1 → true")
	}
	t.Setenv("KB_DASHBOARD_OPEN", "false")
	c, _ = Load()
	if c.DashboardOpen {
		t.Fatal("false → false")
	}
	t.Setenv("KB_DASHBOARD_OPEN", "weird-value")
	c, _ = Load()
	if c.DashboardOpen {
		t.Fatal("weird → false")
	}
}
