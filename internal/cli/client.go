// Config load/save and the HTTP client shared by every subcommand, plus
// the test seams (config path, HTTP client, home dir) that cli_test.go
// overrides.

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Version is overridden at link time.
var Version = "0.6.0"

// configPathFn is overridden in tests to redirect $HOME-derived paths to a
// temp dir. Tests should set it via SetConfigPath.
var configPathFn = defaultConfigPath

// SetConfigPath replaces the config-path resolver. Tests use this to point
// at a temp file. Pass nil to reset to default.
func SetConfigPath(fn func() (string, error)) {
	if fn == nil {
		configPathFn = defaultConfigPath
		return
	}
	configPathFn = fn
}

// httpClientFn returns the HTTP client to use. Overridable for tests that
// inject a transport (we mostly use httptest.Server so this is rarely
// needed, but it keeps options open).
var httpClientFn = func() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// userHomeDirFn is overridable in tests to exercise the UserHomeDir error
// branch — the real os.UserHomeDir is hard to fail deterministically.
var userHomeDirFn = os.UserHomeDir

func defaultConfigPath() (string, error) {
	home, err := userHomeDirFn()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "omoikane", "cli.json"), nil
}

// Config is the on-disk CLI configuration.
type Config struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

// Load reads the config, applying env-var overrides (KB_URL, KB_TOKEN).
func Load() (*Config, error) {
	p, err := configPathFn()
	if err != nil {
		return nil, err
	}
	c := &Config{}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Fall through with empty config + env overrides applied below.
		} else {
			return nil, err
		}
	} else if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if v := os.Getenv("KB_URL"); v != "" {
		c.URL = v
	}
	if v := os.Getenv("KB_TOKEN"); v != "" {
		c.Token = v
	}
	return c, nil
}

// Save persists the config (0600 mode). json.MarshalIndent of *Config (two
// string fields) cannot fail in practice, so the marshal error branch is
// elided — Go's encoding/json returns nil for plain structs.
func Save(c *Config) error {
	p, err := configPathFn()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

// Client is a thin HTTP wrapper.
type Client struct {
	URL    string
	Token  string
	client *http.Client
}

func NewClient(c *Config) (*Client, error) {
	if c.URL == "" {
		return nil, errors.New("server URL not set; run `kb config set url <url>`")
	}
	if c.Token == "" {
		return nil, errors.New("token not set; run `kb config set token <token>`")
	}
	return &Client{
		URL:    strings.TrimRight(c.URL, "/"),
		Token:  c.Token,
		client: httpClientFn(),
	}, nil
}

// Do sends a request and decodes the JSON response into `into` on success.
// On HTTP >= 400 it returns an error containing the raw response body.
func (c *Client) Do(method, path string, body any, headers map[string]string, into any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.URL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Client-Type", "cli")
	req.Header.Set("X-Client-Version", Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if into != nil && len(raw) > 0 {
		return json.Unmarshal(raw, into)
	}
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
