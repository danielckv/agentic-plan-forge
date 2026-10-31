//go:build client && !server

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"golang.org/x/term"
)

func init() {
	register("config", "show or change settings, including the relay origin and models", runConfig)
}

func runConfig(ctx context.Context, args []string) int {
	// `config skills` is a subcommand, not a top-level command: one entry point
	// for settings, with a narrow verb under it.
	if len(args) > 0 && args[0] == "skills" {
		return runConfigSkills(args[1:])
	}
	var (
		show          bool
		asJSON        bool
		relayURL      string
		modelMain     string
		modelJudge    string
		mainHost      string
		judgeHost     string
		workdayHours  float64
		setRelayToken bool
		stageTimeout  int
		narrator      string
		roleFlags     roleModelFlags
	)
	fs, err := parseFlags("config", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&show, "show", false, "print the current configuration")
		fs.BoolVar(&asJSON, "json", false, "print the current configuration as JSON")
		fs.StringVar(&relayURL, "relay-url", "", "set the relay server URL")
		fs.StringVar(&modelMain, "model-main", "", "set the main generation model")
		fs.StringVar(&modelJudge, "model-judge", "", "set the critic model")
		fs.StringVar(&mainHost, "main-host", "", "set the main host (anthropic, gemini, openai, fireworks, antigravity, claude, agy, omp)")
		fs.StringVar(&judgeHost, "judge-host", "", "set the critic host (anthropic, gemini, openai, fireworks, antigravity, claude, agy, omp)")
		fs.Float64Var(&workdayHours, "workday-hours", 0, "set the human workday baseline in hours")
		fs.BoolVar(&setRelayToken, "set-relay-token", false, "store the relay token read from a hidden prompt")
		fs.IntVar(&stageTimeout, "stage-timeout", 0, "set what one deliberation call may take, in seconds (60..1800)")
		fs.StringVar(&narrator, "narrator", "", "turn the live status avatar on or off")
		fs.Var(&roleFlags, "role",
			"place one avatar role: <role>=<host>, <role>=<host>/<model>, <role>=/<model>, or <role>= to clear (repeatable)")
	})
	if err != nil {
		return exitUsage
	}
	_ = ctx
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected arguments: %v\n", fs.Args())
		return exitUsage
	}

	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}

	if setRelayToken {
		return storeRelayToken(cfg)
	}

	changed := false
	if relayURL != "" {
		if err := applyRelayURL(&cfg, relayURL); err != nil {
			return fail(err)
		}
		changed = true
	}
	if modelMain != "" {
		cfg.DefaultMainModel = modelMain
		changed = true
	}
	if modelJudge != "" {
		cfg.DefaultJudgeModel = modelJudge
		changed = true
	}
	if stageTimeout != 0 {
		cfg.StageTimeoutSeconds = stageTimeout
		changed = true
	}
	if narrator != "" {
		on, err := parseOnOff(narrator)
		if err != nil {
			return fail(err)
		}
		cfg.DisableNarrator = !on
		changed = true
	}
	if len(roleFlags) > 0 {
		if err := applyRoleModels(&cfg, roleFlags); err != nil {
			return fail(err)
		}
		changed = true
	}
	if mainHost != "" {
		if !config.IsValidHost(mainHost) {
			return fail(fmt.Errorf("main_host %q is not one of anthropic, gemini, openai, fireworks, antigravity, claude, agy", mainHost))
		}
		cfg.MainHost = mainHost
		changed = true
	}
	if judgeHost != "" {
		if !config.IsValidHost(judgeHost) {
			return fail(fmt.Errorf("judge_host %q is not one of anthropic, gemini, openai, fireworks, antigravity, claude, agy", judgeHost))
		}
		cfg.JudgeHost = judgeHost
		changed = true
	}
	if workdayHours != 0 {
		cfg.HumanWorkdayHours = workdayHours
		changed = true
	}

	if changed {
		if err := config.Validate(cfg); err != nil {
			return fail(err)
		}
		if _, err := config.EnsureHome(); err != nil {
			return fail(err)
		}
		if err := config.Save("", cfg); err != nil {
			return fail(err)
		}
		fmt.Fprintln(os.Stderr, "configuration saved")
	}

	if show || asJSON || !changed {
		if asJSON {
			if err := config.WriteJSON(os.Stdout, cfg.Redacted()); err != nil {
				return fail(err)
			}
			return exitOK
		}
		printConfig(cfg)
		if !changed && !show {
			// No flags and a terminal: the wizard runs. Without a terminal the
			// current values are the whole answer, so nothing hangs waiting for
			// input that will never arrive.
			if term.IsTerminal(int(os.Stdin.Fd())) {
				return wizard(&cfg)
			}
		}
	}
	return exitOK
}

// applyRelayURL guards the one setting that can exfiltrate the relay token:
// pointing the client at a different origin requires an interactive
// confirmation that names both origins, and invalidates the stored token.
func applyRelayURL(cfg *config.Config, raw string) error {
	if err := config.ValidateRelayURL(raw); err != nil {
		return err
	}
	previous := cfg.RelayServerURL
	if previous != "" && !config.SameOrigin(previous, raw) {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("changing relay_server_url from %s to %s requires an interactive terminal confirmation", previous, raw)
		}
		fmt.Fprintf(os.Stderr, "Changing relay origin:\n  from: %s\n  to:   %s\n", previous, raw)
		ok, err := confirm("Delete the stored relay token for the old origin and continue? [y/N] ")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("relay origin change declined")
		}
		if err := config.DeleteRelayToken(); err != nil {
			return err
		}
	}
	cfg.RelayServerURL = raw
	return nil
}

func storeRelayToken(cfg config.Config) int {
	if cfg.RelayServerURL == "" {
		return fail(fmt.Errorf("set relay_server_url before storing a relay token"))
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fail(fmt.Errorf("--set-relay-token needs an interactive terminal; it reads the token without echoing it"))
	}
	fmt.Fprintf(os.Stderr, "Relay token for %s: ", cfg.RelayServerURL)
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fail(err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return fail(fmt.Errorf("empty token, nothing stored"))
	}
	if _, err := config.EnsureHome(); err != nil {
		return fail(err)
	}
	if err := config.SaveRelayToken(config.RelayToken{Origin: cfg.RelayServerURL, Token: token}); err != nil {
		return fail(err)
	}
	fmt.Fprintln(os.Stderr, "relay token stored")
	return exitOK
}

// roleModelFlags collects repeated -role values in the order they are given.
type roleModelFlags []string

func (f *roleModelFlags) String() string { return strings.Join(*f, ",") }

func (f *roleModelFlags) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// applyRoleModels places avatar roles from -role values. Each value is
// <role>=<host>, <role>=<host>/<model>, <role>=/<model>, or <role>= to clear
// the pin and return the role to the spread across ready hosts.
func applyRoleModels(cfg *config.Config, values []string) error {
	for _, v := range values {
		role, binding, ok := strings.Cut(v, "=")
		if !ok {
			return fmt.Errorf("-role %q is not <role>=<host>[/<model>]", v)
		}
		role = strings.TrimSpace(role)
		if !slices.Contains(config.RoleNames(), role) {
			return fmt.Errorf("-role %q names no avatar role; known roles are %s",
				role, strings.Join(config.RoleNames(), ", "))
		}
		if strings.TrimSpace(binding) == "" {
			delete(cfg.RoleModels, role)
			continue
		}
		host, model, _ := strings.Cut(binding, "/")
		if !config.IsValidHost(host) {
			return fmt.Errorf("-role %s host %q is not one of anthropic, gemini, openai, fireworks, antigravity, claude, agy", role, host)
		}
		if cfg.RoleModels == nil {
			cfg.RoleModels = map[string]config.RoleBinding{}
		}
		cfg.RoleModels[role] = config.RoleBinding{Host: host, Model: model}
	}
	return nil
}

func parseOnOff(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("-narrator %q is not on or off", v)
}

func printConfig(cfg config.Config) {
	fmt.Printf("config file:        %s\n", configPathForDisplay())
	fmt.Printf("relay_server_url:   %s\n", orNone(cfg.RelayServerURL))
	fmt.Printf("main_host:          %s\n", orNone(cfg.MainHost))
	fmt.Printf("judge_host:         %s\n", orNone(cfg.JudgeHost))
	fmt.Printf("default_main_model: %s\n", orNone(cfg.DefaultMainModel))
	fmt.Printf("default_judge_model:%s\n", orNone(cfg.DefaultJudgeModel))
	fmt.Printf("human_workday_hours:%g\n", cfg.HumanWorkdayHours)
	fmt.Printf("max_cycles:         %d\n", cfg.MaxCycles)
	fmt.Printf("max_parallel:       %d\n", cfg.MaxParallel)
	fmt.Printf("stage_timeout_s:    %d\n", cfg.StageTimeout())
	fmt.Printf("browser_path:       %s\n", orNone(cfg.BrowserPath))
	fmt.Printf("ocr_language:       %s\n", orNone(cfg.OCRLanguage))
	fmt.Printf("narrator:           %s\n", onOff(!cfg.DisableNarrator))
	printRoleModels(cfg)
}

// printRoleModels lists where each avatar role runs. A role with no entry is
// spread across the ready hosts by the router, which is the default and the
// reason a run uses every host the machine is signed in to.
func printRoleModels(cfg config.Config) {
	fmt.Println("role_models:")
	for _, role := range config.RoleNames() {
		b, ok := cfg.RoleBindingFor(role)
		switch {
		case !ok || (b.Host == "" && b.Model == ""):
			fmt.Printf("  %-12s (spread across ready hosts)\n", role)
		case b.Host == "":
			fmt.Printf("  %-12s %s on any ready host\n", role, b.Model)
		case b.Model == "":
			fmt.Printf("  %-12s %s, host default model\n", role, b.Host)
		default:
			fmt.Printf("  %-12s %s on %s\n", role, b.Model, b.Host)
		}
	}
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func configPathForDisplay() string {
	path, err := config.DefaultPath()
	if err != nil {
		return "(unresolved)"
	}
	return path
}

func orNone(v string) string {
	if v == "" {
		return "(not set)"
	}
	return v
}

func confirm(prompt string) (bool, error) {
	fmt.Fprint(os.Stderr, prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// wizard walks the settings a user actually changes, one at a time, and leaves
// anything they skip untouched.
func wizard(cfg *config.Config) int {
	reader := bufio.NewReader(os.Stdin)
	changed := false

	ask := func(label, current string) string {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, orNone(current))
		line, err := reader.ReadString('\n')
		if err != nil {
			return ""
		}
		return strings.TrimSpace(line)
	}

	if v := ask("relay_server_url", cfg.RelayServerURL); v != "" {
		if err := applyRelayURL(cfg, v); err != nil {
			return fail(err)
		}
		changed = true
	}
	if v := ask("main_host (anthropic, gemini, openai, fireworks, antigravity, claude, agy, omp)", cfg.MainHost); v != "" {
		if !config.IsValidHost(v) {
			return fail(fmt.Errorf("main_host %q is not one of anthropic, gemini, openai, fireworks, antigravity, claude, agy", v))
		}
		cfg.MainHost = v
		changed = true
	}
	if v := ask("judge_host (anthropic, gemini, openai, fireworks, antigravity, claude, agy, omp)", cfg.JudgeHost); v != "" {
		if !config.IsValidHost(v) {
			return fail(fmt.Errorf("judge_host %q is not one of anthropic, gemini, openai, fireworks, antigravity, claude, agy", v))
		}
		cfg.JudgeHost = v
		changed = true
	}
	if v := ask("default_main_model", cfg.DefaultMainModel); v != "" {
		cfg.DefaultMainModel = v
		changed = true
	}
	if v := ask("default_judge_model", cfg.DefaultJudgeModel); v != "" {
		cfg.DefaultJudgeModel = v
		changed = true
	}
	if v := ask("human_workday_hours", fmt.Sprintf("%g", cfg.HumanWorkdayHours)); v != "" {
		var hours float64
		if _, err := fmt.Sscanf(v, "%g", &hours); err != nil {
			return fail(fmt.Errorf("human_workday_hours %q is not a number", v))
		}
		cfg.HumanWorkdayHours = hours
		changed = true
	}
	if v := ask("stage_timeout_seconds", strconv.Itoa(cfg.StageTimeout())); v != "" {
		seconds, err := strconv.Atoi(v)
		if err != nil {
			return fail(fmt.Errorf("stage_timeout_seconds %q is not a number", v))
		}
		cfg.StageTimeoutSeconds = seconds
		changed = true
	}
	// Each role takes "<host>", "<host>/<model>", "/<model>" or "-" to clear
	// it. An empty answer keeps what is there, which for most roles is the
	// spread across every ready host.
	for _, role := range config.RoleNames() {
		current := ""
		if b, ok := cfg.RoleBindingFor(role); ok {
			current = b.Host + "/" + b.Model
		}
		v := ask("role_models."+role+" (<host>, <host>/<model>, /<model>, - to clear)", current)
		if v == "" {
			continue
		}
		if cfg.RoleModels == nil {
			cfg.RoleModels = map[string]config.RoleBinding{}
		}
		if v == "-" {
			delete(cfg.RoleModels, role)
			changed = true
			continue
		}
		host, model, _ := strings.Cut(v, "/")
		if !config.IsValidHost(host) {
			return fail(fmt.Errorf("role_models.%s host %q is not one of anthropic, gemini, openai, fireworks, antigravity, claude, agy", role, host))
		}
		cfg.RoleModels[role] = config.RoleBinding{Host: host, Model: model}
		changed = true
	}

	if !changed {
		fmt.Fprintln(os.Stderr, "nothing changed")
		return exitOK
	}
	if err := config.Validate(*cfg); err != nil {
		return fail(err)
	}
	if err := config.Save("", *cfg); err != nil {
		return fail(err)
	}
	fmt.Fprintln(os.Stderr, "configuration saved")
	return exitOK
}
