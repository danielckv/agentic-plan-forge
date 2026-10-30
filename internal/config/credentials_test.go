package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

// planForgeHome points the config package at a scratch directory so a test
// never reads or writes the developer's real credentials.
func planForgeHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PLAN_FORGE_HOME", dir)
	return dir
}

// A missing file is the state of an installation that has not finished setup,
// not a failure: reporting it as an error would make a first run look broken.
func TestLoadCredentialsWithNoFile(t *testing.T) {
	planForgeHome(t)
	creds, err := config.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if creds.Configured(config.ProviderAnthropic) {
		t.Error("no file must mean no provider is configured")
	}
	if len(creds.ConfiguredProviders()) != 0 {
		t.Errorf("want no configured providers, got %v", creds.ConfiguredProviders())
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := planForgeHome(t)
	var creds config.Credentials
	if err := creds.Set(config.ProviderAnthropic, "sk-ant-secret-value", ""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := config.SaveCredentials(creds); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	loaded, err := config.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	key, source := loaded.Key(config.ProviderAnthropic)
	if key != "sk-ant-secret-value" {
		t.Errorf("key did not survive the round trip, got %q", key)
	}
	if source != "credentials.json" {
		t.Errorf("want the file as the source, got %q", source)
	}

	// A key file readable by other local accounts is a leak.
	info, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("want mode 0600, got %o", perm)
	}
}

// The environment wins, so an export in the shell or a CI runner overrides a
// saved key without the owner having to delete anything.
func TestEnvironmentKeyOverridesTheStoredFile(t *testing.T) {
	planForgeHome(t)
	var creds config.Credentials
	if err := creds.Set(config.ProviderAnthropic, "stored-key", ""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := config.SaveCredentials(creds); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "environment-key")

	loaded, err := config.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	key, source := loaded.Key(config.ProviderAnthropic)
	if key != "environment-key" {
		t.Errorf("want the environment key, got %q", key)
	}
	if source != "ANTHROPIC_API_KEY" {
		t.Errorf("the source must name the variable, got %q", source)
	}
}

// Setting an empty key is how the setup window disconnects a provider.
func TestSetEmptyKeyRemovesTheProvider(t *testing.T) {
	var creds config.Credentials
	if err := creds.Set(config.ProviderOpenAI, "key", ""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !creds.Configured(config.ProviderOpenAI) {
		t.Fatal("the provider should be configured")
	}
	if err := creds.Set(config.ProviderOpenAI, "", ""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if creds.Configured(config.ProviderOpenAI) {
		t.Error("an empty key must disconnect the provider")
	}
}

func TestSetRejectsAnUnknownProvider(t *testing.T) {
	var creds config.Credentials
	if err := creds.Set("gemini", "key", ""); err == nil {
		t.Error("an unknown provider must be rejected, not stored")
	}
	if err := creds.Set(config.ProviderGoogle, "key", ""); err != nil {
		t.Errorf("google is a registered provider: %v", err)
	}
}

// A base URL carrying a query string or userinfo would push the key into
// somewhere it gets logged.
func TestProviderBaseURLRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		ok   bool
	}{
		{"https", "https://gateway.example.com/v1", true},
		{"loopback http", "http://127.0.0.1:8080/v1", true},
		{"plaintext off box", "http://gateway.example.com/v1", false},
		{"query string", "https://gateway.example.com/v1?key=abc", false},
		{"userinfo", "https://user:pw@gateway.example.com/v1", false},
		{"no host", "/v1", false},
		{"wrong scheme", "ftp://gateway.example.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateProviderBaseURL(tc.url)
			if tc.ok && err != nil {
				t.Errorf("want %q accepted, got %v", tc.url, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("want %q rejected", tc.url)
			}
		})
	}
}

// A masked key is for telling two keys apart on screen; it must not be enough
// to reconstruct one.
func TestMaskKey(t *testing.T) {
	masked := config.MaskKey("sk-ant-api03-abcdefghijklmnop9f2c")
	if strings.Contains(masked, "abcdefghij") {
		t.Errorf("mask leaks the body of the key: %q", masked)
	}
	if !strings.HasSuffix(masked, "9f2c") {
		t.Errorf("mask should keep the last four characters, got %q", masked)
	}
	if config.MaskKey("") != "" {
		t.Error("an absent key masks to nothing")
	}
	if strings.Contains(config.MaskKey("short"), "short") {
		t.Error("a short key must not be shown in full")
	}
}

func TestAPIProviderMetadata(t *testing.T) {
	for _, name := range config.ProviderNames() {
		if !config.IsAPIProvider(name) {
			t.Errorf("%s is listed but not recognised", name)
		}
		if config.ProviderKeyEnv(name) == "" {
			t.Errorf("%s has no environment variable", name)
		}
		if config.ProviderConsoleURL(name) == "" {
			t.Errorf("%s has no console URL, so the window cannot say where to get a key", name)
		}
	}
	if config.IsAPIProvider("claude") {
		t.Error("the Claude Code CLI host is not an API provider")
	}
}
