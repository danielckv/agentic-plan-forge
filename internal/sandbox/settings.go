//go:build client && !server

package sandbox

import (
	"errors"
	"strings"
)

// Settings is the configuration a provider adapter needs, resolved from the
// session configuration. It mirrors config.SandboxConfig without importing it:
// this package must not depend on the settings file.
type Settings struct {
	BaseURL        string
	APIKey         string
	Template       string
	TimeoutSeconds int
	VCPU           int
	MemoryMB       int
	DomainSuffix   string
}

// DefaultBaseURL is the provider's API root when none is configured.
const DefaultBaseURL = "https://api.e2b.dev"

// Validate reports the first problem that would make a launch fail after money
// was spent, so it can be reported before anything is created.
func (s Settings) Validate() error {
	if strings.TrimSpace(s.APIKey) == "" {
		return errors.New("sandbox: provider key is missing")
	}
	if s.Template == "" {
		return errors.New("sandbox: a template is required")
	}
	if s.TimeoutSeconds < MinTimeoutSeconds || s.TimeoutSeconds > MaxTimeoutSeconds {
		return errors.New("sandbox: the lifetime must be between 60 and 3600 seconds")
	}
	if s.VCPU < 1 || s.VCPU > 64 {
		return errors.New("sandbox: vcpu must be between 1 and 64")
	}
	if s.MemoryMB < 128 || s.MemoryMB > 262144 {
		return errors.New("sandbox: memory must be between 128 MB and 256 GiB")
	}
	return nil
}

// NewHTTPProvider builds the REST adapter from settings. The key stays inside
// the provider: no method logs it, returns it, or embeds it in an error.
func NewHTTPProvider(s Settings) *HTTPProvider {
	base := strings.TrimSpace(s.BaseURL)
	if base == "" {
		base = DefaultBaseURL
	}
	return &HTTPProvider{BaseURL: strings.TrimRight(base, "/"), APIKey: s.APIKey}
}

// DomainSuffixOf bounds which hosts a preview may proxy to.
func (s Settings) DomainSuffixOf() string {
	if strings.TrimSpace(s.DomainSuffix) == "" {
		return PreviewDomainSuffix
	}
	return s.DomainSuffix
}
