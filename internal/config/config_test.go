package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValid(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev:
    site: datadoghq.eu
    api-key-env: DDEZ_DEV_API_KEY
    app-key-env: DDEZ_DEV_APP_KEY
  prod:
    api-key-env: DDEZ_PROD_API_KEY
    app-key-env: DDEZ_PROD_APP_KEY
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.CurrentContext != "dev" {
		t.Errorf("current = %q", c.CurrentContext)
	}
	if got := c.Contexts["prod"].Site; got != DefaultSite {
		t.Errorf("prod site should default to %s, got %q", DefaultSite, got)
	}
	if got := c.Names(); len(got) != 2 || got[0] != "dev" || got[1] != "prod" {
		t.Errorf("names = %v", got)
	}
}

func TestRejectPlaintextSecrets(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev:
    site: datadoghq.eu
    api-key: deadbeef
    app-key-env: X
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("plaintext api-key must be rejected")
	}
	if !strings.Contains(err.Error(), "secrets never go in the config file") {
		t.Errorf("error should explain the env indirection rule, got: %v", err)
	}
}

func TestUnknownCurrentContext(t *testing.T) {
	p := write(t, `
current-context: nope
contexts:
  dev: {api-key-env: A, app-key-env: B}
`)
	if _, err := Load(p); err == nil {
		t.Fatal("unknown current-context must be rejected")
	}
}

func TestMissingKeyEnvNames(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev: {site: datadoghq.eu}
`)
	if _, err := Load(p); err == nil {
		t.Fatal("context without api-key-env/app-key-env must be rejected")
	}
}

func TestMissingFileFallsBackToImplicit(t *testing.T) {
	t.Setenv("DD_SITE", "us5.datadoghq.com")
	c, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	def, ok := c.Contexts["default"]
	if !ok || c.CurrentContext != "default" {
		t.Fatalf("implicit config expected, got %+v", c)
	}
	if def.Site != "us5.datadoghq.com" || def.APIKeyEnv != "DD_API_KEY" {
		t.Errorf("implicit context wrong: %+v", def)
	}
}

func TestTokenEnvContext(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev:
    site: datadoghq.eu
    token-env: DDEZ_DEV_TOKEN
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DDEZ_DEV_TOKEN", "tok-123")
	tok, err := c.Contexts["dev"].ResolveToken()
	if err != nil || tok != "tok-123" {
		t.Fatalf("token resolve: %v %q", err, tok)
	}
}

func TestUnknownSiteRejected(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev:
    site: evil.example.com
    api-key-env: A
    app-key-env: B
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("unknown site must be rejected — credentials would be sent to it")
	}
	if !strings.Contains(err.Error(), "refusing to send credentials") {
		t.Errorf("error should explain the exfiltration risk, got: %v", err)
	}
}

func TestImplicitIgnoresInvalidDDSite(t *testing.T) {
	t.Setenv("DD_SITE", "evil.example.com")
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Contexts["default"].Site; got != DefaultSite {
		t.Errorf("invalid DD_SITE must fall back to %s, got %q", DefaultSite, got)
	}
}

func TestKeychainContextNeedsNoEnvNames(t *testing.T) {
	p := write(t, `
current-context: prod
contexts:
  prod:
    site: datadoghq.com
    keychain: true
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Contexts["prod"].Keychain {
		t.Error("keychain flag lost")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.yaml")
	c := &Config{
		CurrentContext: "dev",
		Contexts: map[string]Context{
			"dev":  {Site: "datadoghq.eu", APIKeyEnv: "A", AppKeyEnv: "B"},
			"prod": {Site: "datadoghq.com", Keychain: true},
		},
	}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentContext != "dev" || len(got.Contexts) != 2 || !got.Contexts["prod"].Keychain {
		t.Errorf("round trip mismatch: %+v", got)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestResolve(t *testing.T) {
	t.Setenv("T_API", "aaa")
	t.Setenv("T_APP", "bbb")
	api, app, err := (Context{APIKeyEnv: "T_API", AppKeyEnv: "T_APP"}).Resolve()
	if err != nil || api != "aaa" || app != "bbb" {
		t.Fatalf("resolve: %v %q %q", err, api, app)
	}
	if _, _, err := (Context{APIKeyEnv: "T_API", AppKeyEnv: "T_UNSET"}).Resolve(); err == nil {
		t.Fatal("missing env var must error")
	}
}
