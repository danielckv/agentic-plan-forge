package config

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
)

// Sandbox providers. The set is closed: a provider names an adapter that has to
// exist in this binary, so an unknown value is a typo, never a plugin.
const (
	// SandboxProviderNone means no prepared VM. Documents, implementation and
	// acceptance still work; only the remote build and preview are unavailable.
	SandboxProviderNone = ""
	// SandboxProviderE2B is a private E2B sandbox.
	SandboxProviderE2B = "e2b"
)

// Sandbox lifecycle defaults. The lifetime is the provider's own timeout: a
// sandbox that outlives the owner's session burns money with nobody watching.
const (
	DefaultSandboxTimeoutSeconds = 3600
	MinSandboxTimeoutSeconds     = 60
	MaxSandboxTimeoutSeconds     = 3600
	DefaultSandboxVCPU           = 2
	DefaultSandboxMemoryMB       = 4096
	// DefaultSandboxTemplate is the toolchain image the recorded script
	// commands are expected to find. A project whose toolchain differs must
	// name its own template before a launch is allowed.
	DefaultSandboxTemplate = "base"
	// DefaultE2BDomainSuffix bounds which hosts a preview may be proxied to.
	DefaultE2BDomainSuffix = ".e2b.app"
)

// SandboxConfig configures the optional prepared VM. Every field is optional:
// the zero value disables launching without affecting document generation.
type SandboxConfig struct {
	Provider string `json:"provider,omitempty"`
	// APIKeyEnv names the environment variable that holds the provider key. It
	// is a name, never a value, so the config file stays safe to copy.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Template selects the toolchain image inside the provider.
	Template       string `json:"template,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	VCPU           int    `json:"vcpu,omitempty"`
	MemoryMB       int    `json:"memory_mb,omitempty"`
	// DomainSuffix bounds preview upstreams to the provider's own hosts.
	DomainSuffix string `json:"domain_suffix,omitempty"`
	// HourlyCPUCents and HourlyMemoryCents are the rates the window quotes for
	// an estimate. Zero means "unknown", and the window says so rather than
	// showing a made-up price.
	HourlyCPUCents    int `json:"hourly_cpu_cents,omitempty"`
	HourlyMemoryCents int `json:"hourly_memory_cents,omitempty"`
}

// Enabled reports whether a launch could be attempted at all.
func (s SandboxConfig) Enabled() bool { return s.Provider != SandboxProviderNone }

// Resolved returns the sandbox settings with defaults applied.
func (s SandboxConfig) Resolved() SandboxConfig {
	if s.Provider == SandboxProviderNone {
		return s
	}
	out := s
	if out.TimeoutSeconds == 0 {
		out.TimeoutSeconds = DefaultSandboxTimeoutSeconds
	}
	if out.VCPU == 0 {
		out.VCPU = DefaultSandboxVCPU
	}
	if out.MemoryMB == 0 {
		out.MemoryMB = DefaultSandboxMemoryMB
	}
	if out.Template == "" {
		out.Template = DefaultSandboxTemplate
	}
	if out.DomainSuffix == "" {
		out.DomainSuffix = DefaultE2BDomainSuffix
	}
	if out.APIKeyEnv == "" {
		out.APIKeyEnv = DefaultSandboxAPIKeyEnv
	}
	return out
}

// DefaultSandboxAPIKeyEnv is the environment variable read when the config names
// none. The value never enters the config file or the window.
const DefaultSandboxAPIKeyEnv = "E2B_API_KEY"

// EstimatedRuntimeCost returns the estimated cost of one full lifetime and
// whether both rates are known. Without published rates the window shows
// "estimate unavailable" instead of a number.
func (s SandboxConfig) EstimatedRuntimeCost() (time.Duration, float64, bool) {
	r := s.Resolved()
	if r.HourlyCPUCents <= 0 || r.HourlyMemoryCents <= 0 {
		return 0, 0, false
	}
	lifetime := time.Duration(r.TimeoutSeconds) * time.Second
	hours := lifetime.Hours()
	cost := hours * (float64(r.VCPU*r.HourlyCPUCents) + float64(r.MemoryMB)*float64(r.HourlyMemoryCents)/1024)
	return lifetime, cost, true
}

// ValidateRepositoryWorkRoots checks the extra roots of the work view. They are
// repository-relative directories: an absolute path or a traversal would let
// the window read outside the project.
func ValidateRepositoryWorkRoots(roots []string) error {
	for _, root := range roots {
		if root == "" {
			return errors.New("repository_work_roots has an empty entry")
		}
		if strings.Contains(root, `\`) {
			return fmt.Errorf("repository_work_roots entry %q must use forward slashes", root)
		}
		if path.IsAbs(root) || strings.HasPrefix(root, "/") {
			return fmt.Errorf("repository_work_roots entry %q must be repository-relative", root)
		}
		clean := path.Clean(root)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("repository_work_roots entry %q escapes the repository", root)
		}
		if strings.HasPrefix(clean, ".git/") || clean == ".git" {
			return fmt.Errorf("repository_work_roots entry %q names the Git directory", root)
		}
	}
	return nil
}

// ValidateSandbox checks the prepared-VM settings. A disabled sandbox is always
// valid; an enabled one must name a supported provider and a plain key variable.
func ValidateSandbox(s SandboxConfig) error {
	switch s.Provider {
	case SandboxProviderNone:
		return nil
	case SandboxProviderE2B:
	default:
		return fmt.Errorf("sandbox.provider %q is not supported; use %q or leave it empty", s.Provider, SandboxProviderE2B)
	}
	if s.APIKeyEnv == "" {
		return nil
	}
	if strings.ContainsAny(s.APIKeyEnv, "= \t\n") {
		return fmt.Errorf("sandbox.api_key_env %q is not a bare environment variable name", s.APIKeyEnv)
	}
	if s.TimeoutSeconds != 0 &&
		(s.TimeoutSeconds < MinSandboxTimeoutSeconds || s.TimeoutSeconds > MaxSandboxTimeoutSeconds) {
		return fmt.Errorf("sandbox.timeout_seconds must be between %d and %d, got %d",
			MinSandboxTimeoutSeconds, MaxSandboxTimeoutSeconds, s.TimeoutSeconds)
	}
	if s.VCPU < 0 || s.VCPU > 64 {
		return fmt.Errorf("sandbox.vcpu must be between 0 and 64, got %d", s.VCPU)
	}
	if s.MemoryMB < 0 || s.MemoryMB > 262144 {
		return fmt.Errorf("sandbox.memory_mb must be between 0 and 262144, got %d", s.MemoryMB)
	}
	if s.Template != "" && strings.ContainsAny(s.Template, " \t\n/") {
		return fmt.Errorf("sandbox.template %q is not a bare template name", s.Template)
	}
	if s.DomainSuffix != "" {
		suffix := s.DomainSuffix
		if !strings.HasPrefix(suffix, ".") {
			return fmt.Errorf("sandbox.domain_suffix %q must start with a dot", s.DomainSuffix)
		}
		if _, err := url.Parse("https://host" + suffix); err != nil {
			return fmt.Errorf("sandbox.domain_suffix %q is not a host suffix", s.DomainSuffix)
		}
	}
	if s.HourlyCPUCents < 0 || s.HourlyMemoryCents < 0 {
		return errors.New("sandbox rates must not be negative")
	}
	return nil
}
