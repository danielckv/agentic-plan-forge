// Package config owns Plan Forge's on-disk settings and the resolution of its
// state roots. Nothing else in the tree calls os.UserConfigDir or
// os.UserCacheDir, so every path a user can inspect is defined in one place.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// SchemaVersion is the config schema this build understands.
const SchemaVersion = 1

// Defaults for the tunable settings.
const (
	DefaultWorkdayHours = 9
	DefaultMaxCycles    = 3
	DefaultMaxParallel  = 4
	// DefaultMaxGapAgents admits up to two expertise-gap agents per run. The
	// plan bounds the setting from zero through three (MaxGapAgents).
	DefaultMaxGapAgents = 2
	DefaultOCRLanguage  = "eng"
	// DefaultStageTimeoutSeconds is the budget one deliberation call gets. The
	// architect design part was measured at 110 seconds on opus with a minimal
	// payload, so the old 180-second cap left a real cycle no room (ADR 0009).
	DefaultStageTimeoutSeconds = 600
	// MinStageTimeoutSeconds and MaxStageTimeoutSeconds bound the setting. The
	// floor keeps a typo from failing every stage; the ceiling keeps a hung
	// host from holding a run open indefinitely.
	MinStageTimeoutSeconds = 60
	MaxStageTimeoutSeconds = 1800
)

// Deploy targets for a built plan. The set is closed: a target names code that
// has to exist in this binary, so an unknown value is a typo, never a plugin.
const (
	// DeployTargetNone means a built plan is previewed and never deployed.
	DeployTargetNone = "none"
	// DeployTargetDocker runs the built image on the local Docker daemon.
	DeployTargetDocker = "docker"
	// DeployTargetAgentBox pushes the built image to the Agent Box service.
	DeployTargetAgentBox = "agent-box"
)

// DefaultAgentBoxURL is the Agent Box endpoint used when none is configured.
const DefaultAgentBoxURL = "https://agent-box.sh/"

var validDeployTargets = map[string]bool{
	"": true, DeployTargetNone: true, DeployTargetDocker: true, DeployTargetAgentBox: true,
}

// DeployTargetNames lists the choices the window offers, in menu order.
func DeployTargetNames() []string {
	return []string{DeployTargetAgentBox, DeployTargetDocker, DeployTargetNone}
}

// IsValidDeployTarget reports whether t names a supported deploy target.
func IsValidDeployTarget(t string) bool { return validDeployTargets[t] }

// AgentBoxEndpoint is the configured Agent Box URL, or the default.
func (c Config) AgentBoxEndpoint() string {
	if strings.TrimSpace(c.AgentBoxURL) == "" {
		return DefaultAgentBoxURL
	}
	return c.AgentBoxURL
}

// Avatar roles that can be pinned to a host and a model. The set is closed and
// matches contracts.RoleID; config stays free of other packages, so the names
// are repeated here rather than imported.
var validRoles = map[string]bool{
	"pm": true, "architect": true, "specialist": true,
	"critic": true, "synthesizer": true, "narrator": true,
	"tech_lead": true, "code_reviewer": true, "gap_agent": true,
}

// RoleNames lists the pinnable roles in the order a run uses them.
func RoleNames() []string {
	return []string{"pm", "architect", "specialist", "critic", "synthesizer", "narrator", "tech_lead", "code_reviewer", "gap_agent"}
}

// RoleBinding pins one avatar role to a host, a model, or both. An empty Host
// lets the router spread the role across whatever hosts are ready; an empty
// Model lets the chosen host pick its own default.
type RoleBinding struct {
	Host  string `json:"host,omitempty"`
	Model string `json:"model,omitempty"`
}

// Config is the persisted settings file. Unknown keys survive a load/save round
// trip so a newer build can add settings without an older build erasing them.
type Config struct {
	SchemaVersion     int     `json:"schema_version"`
	RelayServerURL    string  `json:"relay_server_url,omitempty"`
	DefaultMainModel  string  `json:"default_main_model,omitempty"`
	DefaultJudgeModel string  `json:"default_judge_model,omitempty"`
	MainHost          string  `json:"main_host,omitempty"`
	JudgeHost         string  `json:"judge_host,omitempty"`
	HumanWorkdayHours float64 `json:"human_workday_hours"`
	MaxCycles         int     `json:"max_cycles"`
	MaxParallel       int     `json:"max_parallel"`
	// MaxGapAgents bounds how many expertise-gap agents one run may admit,
	// beyond the required workstream owners. Zero through three; zero
	// disables the gap selection entirely.
	MaxGapAgents int `json:"max_gap_agents"`
	// StageTimeoutSeconds bounds one deliberation call. Zero selects
	// DefaultStageTimeoutSeconds.
	StageTimeoutSeconds int `json:"stage_timeout_seconds"`
	// DisableNarrator turns off the live status avatar. It defaults to true so
	// deliberation does not spend provider calls on periodic status summaries.
	DisableNarrator bool `json:"disable_narrator"`
	// RoleModels pins avatar roles to hosts and models, keyed by role name. A
	// role with no entry is spread across the ready hosts by the router, so a
	// default install uses every host it is signed in to rather than one.
	RoleModels   map[string]RoleBinding `json:"role_models,omitempty"`
	BrowserPath  string                 `json:"browser_path,omitempty"`
	OCRLanguage  string                 `json:"ocr_language,omitempty"`
	HostEnvAllow []string               `json:"host_env_allow,omitempty"`
	// DeployTarget is where an approved, built plan is deployed. Empty means
	// the owner has not chosen yet, which is what makes the window ask once.
	// A deliberate "never deploy" is DeployTargetNone, not empty.
	DeployTarget string `json:"deploy_target,omitempty"`
	// AgentBoxURL is the Agent Box endpoint. Empty selects DefaultAgentBoxURL.
	AgentBoxURL string `json:"agent_box_url,omitempty"`
	// RepositoryWorkRoots lists extra repository-relative directories the work
	// view indexes, for a project that keeps its plans outside the usual
	// docs/ hierarchy. The standard roots are always read.
	RepositoryWorkRoots []string `json:"repository_work_roots,omitempty"`
	// Sandbox is the prepared-VM settings for the optional remote build and
	// preview. An empty provider disables launching and leaves every document
	// and implementation path available.
	Sandbox SandboxConfig `json:"sandbox,omitempty"`
	// GitHubAPIURL is the review API root. Empty selects the public API; a
	// GitHub Enterprise install sets its own.
	GitHubAPIURL string `json:"github_api_url,omitempty"`

	// unknown preserves keys written by another build.
	unknown map[string]json.RawMessage
}

// Valid host identifiers. An empty value means "not configured yet". The CLI
// hosts claude and agy stay for generation only through an explicit pin;
// omp is removed as a generation host. The SDK hosts are the defaults.
var validHosts = map[string]bool{
	"": true, "claude": true, "agy": true,
	ProviderAnthropic: true, ProviderOpenAI: true, ProviderFireworks: true,
	"gemini": true, "antigravity": true,
}

// IsValidHost reports whether h names a supported host adapter.
func IsValidHost(h string) bool { return validHosts[h] }

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{
		SchemaVersion:       SchemaVersion,
		HumanWorkdayHours:   DefaultWorkdayHours,
		MaxCycles:           DefaultMaxCycles,
		MaxParallel:         DefaultMaxParallel,
		MaxGapAgents:        DefaultMaxGapAgents,
		DisableNarrator:     true,
		OCRLanguage:         DefaultOCRLanguage,
		StageTimeoutSeconds: DefaultStageTimeoutSeconds,
	}
}

// DefaultPath returns the config file path inside the Plan Forge home.
func DefaultPath() (string, error) {
	dir, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the configuration at path. A missing file yields defaults; a
// malformed file is reported with its path and never overwritten.
func Load(path string) (Config, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return Config{}, err
		}
		path = p
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		cfg.unknown = map[string]json.RawMessage{}
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	cfg := Default()
	cfg.unknown = map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		var offset int64 = -1
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			offset = syntaxErr.Offset
		}
		return Config{}, fmt.Errorf("%s: malformed JSON at offset %d: %w", path, offset, err)
	}

	// Collect keys this build does not know so Save can preserve them.
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	for k, v := range all {
		if !knownKeys[k] {
			cfg.unknown[k] = v
		}
	}

	if cfg.SchemaVersion != 0 && cfg.SchemaVersion != SchemaVersion {
		return Config{}, fmt.Errorf("%s: unsupported config schema_version %d (this build understands %d)", path, cfg.SchemaVersion, SchemaVersion)
	}
	cfg.SchemaVersion = SchemaVersion
	return cfg, nil
}

var knownKeys = map[string]bool{
	"schema_version": true, "relay_server_url": true, "default_main_model": true,
	"default_judge_model": true, "main_host": true, "judge_host": true,
	"human_workday_hours": true, "max_cycles": true, "max_parallel": true,
	"max_gap_agents": true,
	"browser_path":   true, "ocr_language": true, "host_env_allow": true,
	"stage_timeout_seconds": true, "role_models": true, "disable_narrator": true,
	"deploy_target": true, "agent_box_url": true,
	"repository_work_roots": true, "sandbox": true, "github_api_url": true,
}

// Save writes the configuration atomically: same-directory temporary file,
// fsync, then rename, so an interrupted write cannot truncate the settings.
func Save(path string, cfg Config) error {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return err
		}
		path = p
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	cfg.SchemaVersion = SchemaVersion

	merged := make(map[string]json.RawMessage, len(cfg.unknown)+len(knownKeys))
	for k, v := range cfg.unknown {
		merged[k] = v
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &encoded); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	for k, v := range encoded {
		merged[k] = v
	}
	final, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	final = append(final, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(final); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

// hostError explains an unsupported host. A host that used to work gets its own
// message: removing it without saying what to do instead left a saved config
// failing at startup with a list of names and no path forward.
func hostError(field, host string) error {
	const supported = "anthropic, gemini, openai, fireworks, antigravity, claude, agy"
	switch host {
	case "omp":
		return fmt.Errorf("%s is %q, which is no longer a generation host: this release generates through "+
			"provider APIs, not a local agent CLI. Set %s to anthropic or gemini and add that provider's API key "+
			"(plan-forge config). The supported hosts are %s", field, host, field, supported)
	default:
		return fmt.Errorf("%s is %q, which is not a supported host. The supported hosts are %s", field, host, supported)
	}
}

// Validate reports the first configuration problem. It never mutates cfg.
func Validate(cfg Config) error {
	if cfg.HumanWorkdayHours <= 0 || cfg.HumanWorkdayHours > 24 {
		return fmt.Errorf("human_workday_hours must be > 0 and <= 24, got %g", cfg.HumanWorkdayHours)
	}
	if cfg.MaxCycles < 1 || cfg.MaxCycles > 3 {
		return fmt.Errorf("max_cycles must be between 1 and 3, got %d", cfg.MaxCycles)
	}
	if cfg.MaxParallel < 1 || cfg.MaxParallel > 4 {
		return fmt.Errorf("max_parallel must be between 1 and 4, got %d", cfg.MaxParallel)
	}
	if cfg.MaxGapAgents < 0 || cfg.MaxGapAgents > 3 {
		return fmt.Errorf("max_gap_agents must be between 0 and 3, got %d", cfg.MaxGapAgents)
	}
	if !IsValidHost(cfg.MainHost) {
		return hostError("main_host", cfg.MainHost)
	}
	if !IsValidHost(cfg.JudgeHost) {
		return hostError("judge_host", cfg.JudgeHost)
	}
	if cfg.StageTimeoutSeconds != 0 &&
		(cfg.StageTimeoutSeconds < MinStageTimeoutSeconds || cfg.StageTimeoutSeconds > MaxStageTimeoutSeconds) {
		return fmt.Errorf("stage_timeout_seconds must be between %d and %d, got %d",
			MinStageTimeoutSeconds, MaxStageTimeoutSeconds, cfg.StageTimeoutSeconds)
	}
	for role, binding := range cfg.RoleModels {
		if !validRoles[role] {
			return fmt.Errorf("role_models has unknown role %q; known roles are %s",
				role, strings.Join(RoleNames(), ", "))
		}
		if !IsValidHost(binding.Host) {
			return hostError(fmt.Sprintf("role_models[%q].host", role), binding.Host)
		}
	}
	if cfg.RelayServerURL != "" {
		if err := ValidateRelayURL(cfg.RelayServerURL); err != nil {
			return err
		}
	}
	for _, name := range cfg.HostEnvAllow {
		if name == "" || strings.ContainsAny(name, "= \t\n") {
			return fmt.Errorf("host_env_allow entry %q is not a bare environment variable name", name)
		}
	}
	if !IsValidDeployTarget(cfg.DeployTarget) {
		return fmt.Errorf("deploy_target %q is not one of %s",
			cfg.DeployTarget, strings.Join(DeployTargetNames(), ", "))
	}
	if cfg.AgentBoxURL != "" {
		if err := ValidateAgentBoxURL(cfg.AgentBoxURL); err != nil {
			return err
		}
	}
	if err := ValidateRepositoryWorkRoots(cfg.RepositoryWorkRoots); err != nil {
		return err
	}
	if err := ValidateSandbox(cfg.Sandbox); err != nil {
		return err
	}
	if cfg.GitHubAPIURL != "" {
		if !strings.HasPrefix(cfg.GitHubAPIURL, "https://") {
			return fmt.Errorf("github_api_url %q must be an https URL", cfg.GitHubAPIURL)
		}
	}
	return nil
}

// ValidateAgentBoxURL rejects endpoints that would leak the built image or its
// credentials. The rules match ValidateRelayURL: a query string or fragment
// can carry a secret into a log, userinfo is never needed, and plaintext is
// allowed only on loopback, where there is no network to read it off.
func ValidateAgentBoxURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("agent_box_url is not a URL: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("agent_box_url %q has no host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("agent_box_url must not carry userinfo")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("agent_box_url must not carry a query string or fragment")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("agent_box_url may use http only on loopback, got %q", raw)
	default:
		return fmt.Errorf("agent_box_url scheme %q is not http or https", u.Scheme)
	}
}

// ValidateRelayURL rejects relay endpoints that would leak or mislead. Query
// strings and fragments can carry secrets into logs, and userinfo is never
// needed; plaintext is allowed only on loopback for local development.
func ValidateRelayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("relay_server_url is not a URL: %w", err)
	}
	switch u.Scheme {
	case "https", "wss":
	case "http", "ws":
		if !isLoopback(u.Hostname()) {
			return fmt.Errorf("relay_server_url %q uses plaintext outside loopback; use https or wss", raw)
		}
	default:
		return fmt.Errorf("relay_server_url %q must use https or wss", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("relay_server_url %q has no host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("relay_server_url %q must not contain userinfo", raw)
	}
	if u.RawQuery != "" {
		return fmt.Errorf("relay_server_url %q must not contain a query string", raw)
	}
	if u.Fragment != "" {
		return fmt.Errorf("relay_server_url %q must not contain a fragment", raw)
	}
	return nil
}

func isLoopback(host string) bool {
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

// SameOrigin reports whether two relay URLs share scheme, host and port. The
// relay token is bound to one origin: pointing the client at a different origin
// must be a deliberate, confirmed act, never a side effect of a config write.
func SameOrigin(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

// --- relay token ----------------------------------------------------------

// RelayToken is the stored credential for one relay origin.
type RelayToken struct {
	Origin string `json:"origin"`
	Token  string `json:"token"`
}

// RelayTokenPath returns the credential file path inside the Plan Forge home.
func RelayTokenPath() (string, error) {
	dir, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "relay-token.json"), nil
}

// LoadRelayToken reads the stored relay credential. The environment variable
// PLAN_FORGE_RELAY_TOKEN takes precedence and is bound to the configured origin.
func LoadRelayToken(relayURL string) (RelayToken, error) {
	if env := os.Getenv("PLAN_FORGE_RELAY_TOKEN"); env != "" {
		return RelayToken{Origin: originOf(relayURL), Token: env}, nil
	}
	path, err := RelayTokenPath()
	if err != nil {
		return RelayToken{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return RelayToken{}, os.ErrNotExist
	}
	if err != nil {
		return RelayToken{}, err
	}
	var tok RelayToken
	if err := json.Unmarshal(raw, &tok); err != nil {
		return RelayToken{}, fmt.Errorf("%s: malformed relay token file: %w", path, err)
	}
	if tok.Origin != "" && !SameOrigin(tok.Origin, relayURL) {
		return RelayToken{}, fmt.Errorf("stored relay token belongs to %s, not %s", tok.Origin, relayURL)
	}
	return tok, nil
}

// SaveRelayToken stores the credential with owner-only permissions.
func SaveRelayToken(tok RelayToken) error {
	dir, err := EnsureHome()
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "relay-token.json"), append(body, '\n'), 0o600)
}

// DeleteRelayToken removes the stored credential, ignoring a missing file.
func DeleteRelayToken() error {
	path, err := RelayTokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Path, u.RawQuery, u.Fragment, u.User = "", "", "", nil
	return strings.TrimSuffix(u.String(), "/")
}

// Redacted renders the configuration for display. It never contains relay
// credentials: those live in a separate file that is never printed.
func (c Config) Redacted() map[string]any {
	out := map[string]any{
		"schema_version":        c.SchemaVersion,
		"relay_server_url":      c.RelayServerURL,
		"default_main_model":    c.DefaultMainModel,
		"default_judge_model":   c.DefaultJudgeModel,
		"main_host":             c.MainHost,
		"judge_host":            c.JudgeHost,
		"human_workday_hours":   c.HumanWorkdayHours,
		"max_cycles":            c.MaxCycles,
		"max_parallel":          c.MaxParallel,
		"max_gap_agents":        c.MaxGapAgents,
		"stage_timeout_seconds": c.StageTimeout(),
		"role_models":           c.RoleModels,
		"disable_narrator":      c.DisableNarrator,
		"browser_path":          c.BrowserPath,
		"ocr_language":          c.OCRLanguage,
		"host_env_allow":        c.HostEnvAllow,
	}
	return out
}

// StageTimeout returns the configured deliberation call budget in seconds,
// or DefaultStageTimeoutSeconds when none is set.
func (c Config) StageTimeout() int {
	if c.StageTimeoutSeconds <= 0 {
		return DefaultStageTimeoutSeconds
	}
	return c.StageTimeoutSeconds
}

// RoleBindingFor returns the binding pinned to a role, and whether one exists.
func (c Config) RoleBindingFor(role string) (RoleBinding, bool) {
	b, ok := c.RoleModels[role]
	return b, ok
}

// WriteJSON renders v as indented JSON with a trailing newline.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
