//go:build client && !server

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"golang.org/x/term"
)

func init() {
	register("config", "show or change settings, including the relay origin and models", runConfig)
}

func runConfig(ctx context.Context, args []string) int {
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
	)
	fs, err := parseFlags("config", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&show, "show", false, "print the current configuration")
		fs.BoolVar(&asJSON, "json", false, "print the current configuration as JSON")
		fs.StringVar(&relayURL, "relay-url", "", "set the relay server URL")
		fs.StringVar(&modelMain, "model-main", "", "set the main generation model")
		fs.StringVar(&modelJudge, "model-judge", "", "set the critic model")
		fs.StringVar(&mainHost, "main-host", "", "set the main host (claude, omp, agy)")
		fs.StringVar(&judgeHost, "judge-host", "", "set the critic host (claude, omp, agy)")
		fs.Float64Var(&workdayHours, "workday-hours", 0, "set the human workday baseline in hours")
		fs.BoolVar(&setRelayToken, "set-relay-token", false, "store the relay token read from a hidden prompt")
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
	if mainHost != "" {
		if !config.IsValidHost(mainHost) {
			return fail(fmt.Errorf("main_host %q is not one of claude, omp, agy", mainHost))
		}
		cfg.MainHost = mainHost
		changed = true
	}
	if judgeHost != "" {
		if !config.IsValidHost(judgeHost) {
			return fail(fmt.Errorf("judge_host %q is not one of claude, omp, agy", judgeHost))
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
	fmt.Printf("browser_path:       %s\n", orNone(cfg.BrowserPath))
	fmt.Printf("ocr_language:       %s\n", orNone(cfg.OCRLanguage))
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
	if v := ask("main_host (claude, omp, agy)", cfg.MainHost); v != "" {
		if !config.IsValidHost(v) {
			return fail(fmt.Errorf("main_host %q is not one of claude, omp, agy", v))
		}
		cfg.MainHost = v
		changed = true
	}
	if v := ask("judge_host (claude, omp, agy)", cfg.JudgeHost); v != "" {
		if !config.IsValidHost(v) {
			return fail(fmt.Errorf("judge_host %q is not one of claude, omp, agy", v))
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
