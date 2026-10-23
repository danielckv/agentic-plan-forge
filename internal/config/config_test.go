package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

func isolateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PLAN_FORGE_HOME", dir)
	return dir
}

func TestHomeRequiresAbsoluteOverride(t *testing.T) {
	t.Setenv("PLAN_FORGE_HOME", "relative/path")
	if _, err := config.Home(); err == nil {
		t.Fatal("expected a relative PLAN_FORGE_HOME to be rejected")
	}
}

func TestEnsureHomeWritesMarker(t *testing.T) {
	dir := isolateHome(t)
	got, err := config.EnsureHome()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("EnsureHome = %q, want %q", got, dir)
	}
	info, err := os.Stat(filepath.Join(dir, ".plan-forge-home"))
	if err != nil {
		t.Fatalf("marker file missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("marker mode = %o, want 600", perm)
	}
	if err := config.VerifyOwned(dir); err != nil {
		t.Fatalf("VerifyOwned on our own home: %v", err)
	}
	if err := config.VerifyOwned(t.TempDir()); err == nil {
		t.Fatal("VerifyOwned accepted a directory without the marker")
	}
}

func TestLoadMissingFileYieldsDefaults(t *testing.T) {
	isolateHome(t)
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HumanWorkdayHours != config.DefaultWorkdayHours {
		t.Errorf("workday hours = %g, want %d", cfg.HumanWorkdayHours, config.DefaultWorkdayHours)
	}
	if cfg.MaxCycles != config.DefaultMaxCycles {
		t.Errorf("max cycles = %d, want %d", cfg.MaxCycles, config.DefaultMaxCycles)
	}
}

func TestLoadMalformedFileIsReportedAndNotOverwritten(t *testing.T) {
	dir := isolateHome(t)
	path := filepath.Join(dir, "config.json")
	original := []byte("{not json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected malformed JSON to be reported")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("malformed config was modified")
	}
}

func TestLoadRejectsUnknownSchemaVersion(t *testing.T) {
	dir := isolateHome(t)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected an unsupported schema_version to be rejected")
	}
}

func TestSavePreservesUnknownKeys(t *testing.T) {
	dir := isolateHome(t)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"future_setting":"keep me"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MainHost = "claude"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["future_setting"]; !ok {
		t.Fatal("unknown key was dropped on save")
	}
	var host string
	if err := json.Unmarshal(doc["main_host"], &host); err != nil {
		t.Fatal(err)
	}
	if host != "claude" {
		t.Fatalf("main_host = %q, want claude", host)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %o, want 600", perm)
	}
}

func TestValidateRelayURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		ok   bool
	}{
		{"https", "https://relay.example.com", true},
		{"wss", "wss://relay.example.com", true},
		{"loopback plaintext", "http://127.0.0.1:8080", true},
		{"plaintext off loopback", "http://relay.example.com", false},
		{"bad scheme", "ftp://relay.example.com", false},
		{"userinfo", "https://user:pass@relay.example.com", false},
		{"query", "https://relay.example.com/?token=abc", false},
		{"fragment", "https://relay.example.com/#token", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateRelayURL(tc.url)
			if tc.ok && err != nil {
				t.Fatalf("expected %q to be accepted: %v", tc.url, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected %q to be rejected", tc.url)
			}
		})
	}
}

func TestSameOrigin(t *testing.T) {
	if !config.SameOrigin("https://relay.example.com", "https://relay.example.com/path") {
		t.Fatal("same origin with a path should match")
	}
	if config.SameOrigin("https://relay.example.com", "https://evil.example.com") {
		t.Fatal("different hosts must not match")
	}
	if config.SameOrigin("https://relay.example.com", "http://relay.example.com") {
		t.Fatal("different schemes must not match")
	}
}

func TestValidateBounds(t *testing.T) {
	cfg := config.Default()
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	bad := []func(*config.Config){
		func(c *config.Config) { c.HumanWorkdayHours = 0 },
		func(c *config.Config) { c.HumanWorkdayHours = 25 },
		func(c *config.Config) { c.MaxCycles = 4 },
		func(c *config.Config) { c.MaxParallel = 9 },
		func(c *config.Config) { c.MainHost = "gemini" },
		func(c *config.Config) { c.HostEnvAllow = []string{"BAD=1"} },
		func(c *config.Config) { c.StageTimeoutSeconds = 30 },
		func(c *config.Config) { c.StageTimeoutSeconds = 3601 },
		func(c *config.Config) {
			c.RoleModels = map[string]config.RoleBinding{"designer": {Host: "claude"}}
		},
		func(c *config.Config) {
			c.RoleModels = map[string]config.RoleBinding{"architect": {Host: "gemini"}}
		},
	}
	for i, mutate := range bad {
		c := config.Default()
		mutate(&c)
		if err := config.Validate(c); err == nil {
			t.Errorf("case %d: expected validation failure", i)
		}
	}
}

// A role may be pinned to a host, to a model, or to neither. Pinning only a
// model is what lets an owner say "the narrator uses the cheap model" without
// also deciding which host serves it.
func TestRoleBindingsValidateAndResolve(t *testing.T) {
	cfg := config.Default()
	cfg.RoleModels = map[string]config.RoleBinding{
		"architect": {Host: "claude", Model: "opus"},
		"narrator":  {Model: "gemini-3.8-flash-low"},
		"critic":    {Host: "agy"},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("valid role bindings rejected: %v", err)
	}
	for _, role := range config.RoleNames() {
		if _, ok := cfg.RoleBindingFor(role); !ok && cfg.RoleModels[role] != (config.RoleBinding{}) {
			t.Errorf("RoleBindingFor(%q) found nothing for a pinned role", role)
		}
	}
	if b, ok := cfg.RoleBindingFor("narrator"); !ok || b.Host != "" || b.Model != "gemini-3.8-flash-low" {
		t.Errorf("narrator binding = %+v, %v", b, ok)
	}
	if _, ok := cfg.RoleBindingFor("pm"); ok {
		t.Error("an unpinned role reported a binding")
	}
}

// The stage budget falls back to the measured default rather than to zero: a
// zero would mean "no budget" to every caller downstream.
func TestStageTimeoutFallsBackToTheDefault(t *testing.T) {
	var cfg config.Config
	if got := cfg.StageTimeout(); got != config.DefaultStageTimeoutSeconds {
		t.Fatalf("unset stage timeout = %d, want %d", got, config.DefaultStageTimeoutSeconds)
	}
	cfg.StageTimeoutSeconds = 900
	if got := cfg.StageTimeout(); got != 900 {
		t.Fatalf("configured stage timeout = %d, want 900", got)
	}
}

func TestPurgeTargetsEnumerateKnownChildren(t *testing.T) {
	dir := isolateHome(t)
	targets, err := config.PurgeTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		t.Fatal("no purge targets")
	}
	for _, target := range targets {
		if target == dir {
			t.Fatalf("purge target must never be the home root itself: %s", target)
		}
	}
}

func TestIsUnsafePurgeTarget(t *testing.T) {
	isolateHome(t)
	if config.IsUnsafePurgeTarget("/") {
	} else {
		t.Fatal("filesystem root must be unsafe")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !config.IsUnsafePurgeTarget(home) {
		t.Fatal("the user's home directory must be unsafe")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if !config.IsUnsafePurgeTarget(wd) {
		t.Fatal("an ancestor of the working directory must be unsafe")
	}
}

func TestRelayTokenRoundTrip(t *testing.T) {
	isolateHome(t)
	if _, err := config.EnsureHome(); err != nil {
		t.Fatal(err)
	}
	const relay = "https://relay.example.com"
	if err := config.SaveRelayToken(config.RelayToken{Origin: relay, Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	tok, err := config.LoadRelayToken(relay)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Token != "secret" {
		t.Fatalf("token = %q, want secret", tok.Token)
	}
	if _, err := config.LoadRelayToken("https://other.example.com"); err == nil {
		t.Fatal("a token bound to another origin must be refused")
	}
	t.Setenv("PLAN_FORGE_RELAY_TOKEN", "from-env")
	tok, err = config.LoadRelayToken(relay)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Token != "from-env" {
		t.Fatalf("environment token did not take precedence, got %q", tok.Token)
	}
	t.Setenv("PLAN_FORGE_RELAY_TOKEN", "")
	if err := config.DeleteRelayToken(); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadRelayToken(relay); err == nil {
		t.Fatal("expected the token file to be gone")
	}
}

func TestRedactedNeverExposesCredentials(t *testing.T) {
	cfg := config.Default()
	cfg.RelayServerURL = "https://relay.example.com"
	out := cfg.Redacted()
	if _, ok := out["relay_token"]; ok {
		t.Fatal("Redacted must not include a token field")
	}
	for key, value := range out {
		if s, ok := value.(string); ok && s == "secret" {
			t.Fatalf("Redacted leaked a secret through %s", key)
		}
	}
}
