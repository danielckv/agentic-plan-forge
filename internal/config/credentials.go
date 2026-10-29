package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// API provider identifiers. These are generation hosts reached over HTTP with
// the owner's own API key, alongside the local agent CLIs.
//
// Each is the provider's own sanctioned developer credential. Plan Forge never
// asks for, stores or forwards a consumer subscription sign-in: Anthropic's
// terms reserve Claude.ai OAuth for Anthropic's own applications and direct
// developers to a Console API key, and a subscription is reached only by the
// unmodified `claude` binary, which stays a separate CLI host.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderFireworks = "fireworks"
)

// ProviderNames lists the API providers in the order the setup window offers
// them: most capable planning model first.
func ProviderNames() []string {
	return []string{ProviderAnthropic, ProviderOpenAI, ProviderFireworks}
}

// IsAPIProvider reports whether name identifies an HTTP generation provider.
func IsAPIProvider(name string) bool {
	switch name {
	case ProviderAnthropic, ProviderOpenAI, ProviderFireworks:
		return true
	}
	return false
}

// ProviderLabel is what the window calls a provider.
func ProviderLabel(name string) string {
	switch name {
	case ProviderAnthropic:
		return "Anthropic"
	case ProviderOpenAI:
		return "OpenAI"
	case ProviderFireworks:
		return "Fireworks"
	}
	return name
}

// ProviderKeyEnv is the environment variable a provider's key is read from
// when the owner would rather not store it on disk. The environment wins over
// the stored file, so a shell or CI export overrides a saved key.
func ProviderKeyEnv(name string) string {
	switch name {
	case ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case ProviderOpenAI:
		return "OPENAI_API_KEY"
	case ProviderFireworks:
		return "FIREWORKS_API_KEY"
	}
	return ""
}

// ProviderConsoleURL is where the owner creates a key for a provider.
func ProviderConsoleURL(name string) string {
	switch name {
	case ProviderAnthropic:
		return "https://platform.claude.com/settings/keys"
	case ProviderOpenAI:
		return "https://platform.openai.com/api-keys"
	case ProviderFireworks:
		return "https://app.fireworks.ai/settings/users/api-keys"
	}
	return ""
}

// ProviderCredential is one provider's stored key and the endpoint it is used
// against. BaseURL is empty for a provider used at its public endpoint; a
// gateway or a compatible deployment sets its own.
type ProviderCredential struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

// Credentials is the persisted API key file. It is deliberately separate from
// Config: the settings file is quoted in reports, printed by `doctor` and
// copied into bug reports, and a key must never travel with it.
type Credentials struct {
	SchemaVersion int                           `json:"schema_version"`
	Providers     map[string]ProviderCredential `json:"providers,omitempty"`
}

// CredentialsPath returns the key file path inside the Plan Forge home.
func CredentialsPath() (string, error) {
	dir, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// LoadCredentials reads the stored keys. A missing file is not an error: it is
// the state of an installation that has not finished setup, and the caller
// reports that as "no provider configured" rather than as a failure.
func LoadCredentials() (Credentials, error) {
	path, err := CredentialsPath()
	if err != nil {
		return Credentials{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{SchemaVersion: SchemaVersion}, nil
	}
	if err != nil {
		return Credentials{}, err
	}
	var creds Credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return Credentials{}, fmt.Errorf("%s: malformed credentials file: %w", path, err)
	}
	if creds.Providers == nil {
		creds.Providers = map[string]ProviderCredential{}
	}
	return creds, nil
}

// SaveCredentials writes the key file with owner-only permissions, through a
// temporary file in the same directory so an interrupted write never leaves a
// half-written credential behind.
func SaveCredentials(creds Credentials) error {
	dir, err := EnsureHome()
	if err != nil {
		return err
	}
	creds.SchemaVersion = SchemaVersion
	body, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, "credentials.json"))
}

// Key returns the usable key for a provider and where it came from. The
// environment variable wins over the stored file so an export in the shell, a
// secrets manager or a CI runner overrides what setup saved, without the owner
// having to delete anything.
func (c Credentials) Key(provider string) (key string, source string) {
	if env := ProviderKeyEnv(provider); env != "" {
		if value := strings.TrimSpace(os.Getenv(env)); value != "" {
			return value, env
		}
	}
	if stored, ok := c.Providers[provider]; ok {
		if value := strings.TrimSpace(stored.APIKey); value != "" {
			return value, "credentials.json"
		}
	}
	return "", ""
}

// BaseURL returns the configured endpoint override for a provider, or "" to
// use the provider's public endpoint.
func (c Credentials) BaseURL(provider string) string {
	if stored, ok := c.Providers[provider]; ok {
		return strings.TrimSpace(stored.BaseURL)
	}
	return ""
}

// Configured reports whether a provider has a usable key.
func (c Credentials) Configured(provider string) bool {
	key, _ := c.Key(provider)
	return key != ""
}

// ConfiguredProviders lists the providers holding a usable key, in menu order.
func (c Credentials) ConfiguredProviders() []string {
	out := make([]string, 0, len(ProviderNames()))
	for _, name := range ProviderNames() {
		if c.Configured(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Set records a provider's key. An empty key removes the entry, which is how
// the setup window disconnects a provider.
func (c *Credentials) Set(provider, key, baseURL string) error {
	if !IsAPIProvider(provider) {
		return fmt.Errorf("unknown API provider %q", provider)
	}
	if c.Providers == nil {
		c.Providers = map[string]ProviderCredential{}
	}
	key = strings.TrimSpace(key)
	baseURL = strings.TrimSpace(baseURL)
	if key == "" && baseURL == "" {
		delete(c.Providers, provider)
		return nil
	}
	if baseURL != "" {
		if err := ValidateProviderBaseURL(baseURL); err != nil {
			return fmt.Errorf("%s base URL: %w", provider, err)
		}
	}
	c.Providers[provider] = ProviderCredential{APIKey: key, BaseURL: baseURL}
	return nil
}

// ValidateProviderBaseURL rejects an endpoint override that would leak the key
// or send it somewhere unreadable. The rules match ValidateAgentBoxURL and
// ValidateRelayURL: a query string or fragment can carry a secret into a log,
// userinfo is never needed, and plaintext is allowed only on loopback, where
// there is no network to read the Authorization header off.
func ValidateProviderBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base_url is not a URL: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("base_url %q has no host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("base_url must not carry userinfo")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base_url must not carry a query string or fragment")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("base_url may use http only on loopback, got %q", raw)
	default:
		return fmt.Errorf("base_url scheme %q is not http or https", u.Scheme)
	}
}

// MaskKey renders a key for display. It keeps the provider's prefix and the
// last four characters, which is enough to tell two keys apart and useless to
// anyone who reads a screenshot.
func MaskKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	runes := []rune(key)
	if len(runes) <= 8 {
		return strings.Repeat("•", len(runes))
	}
	return string(runes[:4]) + "…" + string(runes[len(runes)-4:])
}
