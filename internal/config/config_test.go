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
