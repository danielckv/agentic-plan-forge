//go:build client && !server

package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

// configField is one editable row: how to read it, how to write it. The write
// side validates what Validate cannot — a host name is wrong the moment it is
// typed, not at save time — so an editor session cannot carry a bad value
// forward by accident. A nil set makes the row read-only.
type configField struct {
	label string
	hint  string
	get   func(*config.Config) string
	set   func(*config.Config, string) error
}

// configTab is one screenful of rows. The editor starts with General, which
// carries the knobs a fresh install changes, and Models, which carries every
// model pin the configuration file can express.
type configTab struct {
	name string
	rows []configField
	// note explains an empty tab instead of leaving a blank screen.
	note string
}

// clearValue reports whether v asks to clear the field. An empty draft keeps
// the current value; clearing a pin takes an explicit "-".
func clearValue(v string) bool { return v == "-" }

// setString writes a free-form value, "-" clearing it.
func setString(dst func(*config.Config, string)) func(*config.Config, string) error {
	return func(c *config.Config, v string) error {
		if clearValue(v) {
			v = ""
		}
		dst(c, v)
		return nil
	}
}

// setHost writes a host name after checking it, "-" clearing the pin.
func setHost(dst func(*config.Config, string)) func(*config.Config, string) error {
	return func(c *config.Config, v string) error {
		if clearValue(v) {
			dst(c, "")
			return nil
		}
		if !config.IsValidHost(v) {
			return fmt.Errorf("%q is not one of anthropic, gemini, openai, fireworks, antigravity, claude, agy", v)
		}
		dst(c, v)
		return nil
	}
}

// generalTab holds the settings a fresh install actually changes. Everything
// else stays in `plan-forge config` and config.json.
func generalTab() configTab {
	return configTab{
		name: "General",
		rows: []configField{
			{
				label: "main_host",
				hint:  "anthropic, gemini, openai, fireworks, antigravity, claude, agy (- clears)",
				get:   func(c *config.Config) string { return c.MainHost },
				set:   setHost(func(c *config.Config, v string) { c.MainHost = v }),
			},
			{
				label: "judge_host",
				hint:  "host for the critic (- clears)",
				get:   func(c *config.Config) string { return c.JudgeHost },
				set:   setHost(func(c *config.Config, v string) { c.JudgeHost = v }),
			},
			{
				label: "stage_timeout_seconds",
				hint: fmt.Sprintf("%d..%d, - selects the default %d",
					config.MinStageTimeoutSeconds, config.MaxStageTimeoutSeconds, config.DefaultStageTimeoutSeconds),
				get: func(c *config.Config) string { return strconv.Itoa(c.StageTimeout()) },
				set: func(c *config.Config, v string) error {
					if clearValue(v) {
						c.StageTimeoutSeconds = 0
						return nil
					}
					n, err := strconv.Atoi(v)
					if err != nil {
						return fmt.Errorf("%q is not a number", v)
					}
					c.StageTimeoutSeconds = n
					return nil
				},
			},
			{
				label: "human_workday_hours",
				hint:  "the estimate baseline, > 0 and <= 24",
				get:   func(c *config.Config) string { return strconv.FormatFloat(c.HumanWorkdayHours, 'g', -1, 64) },
				set: func(c *config.Config, v string) error {
					h, err := strconv.ParseFloat(v, 64)
					if err != nil {
						return fmt.Errorf("%q is not a number", v)
					}
					c.HumanWorkdayHours = h
					return nil
				},
			},
			{
				label: "disable_narrator",
				hint:  "on or off",
				get: func(c *config.Config) string {
					if c.DisableNarrator {
						return "on"
					}
					return "off"
				},
				set: func(c *config.Config, v string) error {
					switch v {
					case "on", "true", "yes":
						c.DisableNarrator = true
					case "off", "false", "no":
						c.DisableNarrator = false
					default:
						return fmt.Errorf("%q is not on or off", v)
					}
					return nil
				},
			},
		},
	}
}

// roleModelField builds the row for one pinned role: the value shows the
// host/model pair, editing changes the model and keeps the host, and "-"
// clears the model so the host picks its own default. The binding lives in a
// map, so the closures re-read it every time rather than holding a copy.
func roleModelField(role string) configField {
	return configField{
		label: "role_models." + role,
		hint:  "model id for the " + role + " (- clears; the host pin is kept)",
		get: func(c *config.Config) string {
			b := c.RoleModels[role]
			switch {
			case b.Host == "" && b.Model == "":
				return ""
			case b.Host == "":
				return b.Model
			case b.Model == "":
				return b.Host + " (host default)"
			default:
				return b.Host + " / " + b.Model
			}
		},
		set: func(c *config.Config, v string) error {
			b := c.RoleModels[role]
			if clearValue(v) {
				v = ""
			}
			b.Model = v
			c.RoleModels[role] = b
			return nil
		},
	}
}

// modelsTab lists every model pin the configuration can express: the two
// defaults first, then one row per configured role binding in run order. A
// role with no binding is spread across the ready hosts by the router, which
// the note says rather than inventing rows the file does not have.
func modelsTab(cfg *config.Config) configTab {
	tab := configTab{
		name: "Models",
		rows: []configField{
			{
				label: "default_main_model",
				hint:  "model id for generation, empty lets main_host choose (- clears)",
				get:   func(c *config.Config) string { return c.DefaultMainModel },
				set:   setString(func(c *config.Config, v string) { c.DefaultMainModel = v }),
			},
			{
				label: "default_judge_model",
				hint:  "model id for the critic (- clears)",
				get:   func(c *config.Config) string { return c.DefaultJudgeModel },
				set:   setString(func(c *config.Config, v string) { c.DefaultJudgeModel = v }),
			},
		},
		note: "No role models pinned — every role is spread across the signed-in hosts.",
	}
	hasRoles := false
	for _, role := range config.RoleNames() {
		if _, ok := cfg.RoleModels[role]; ok {
			tab.rows = append(tab.rows, roleModelField(role))
			hasRoles = true
		}
	}
	if hasRoles {
		tab.note = ""
	}
	return tab
}

// configEditor is the state of one editing session.
type configEditor struct {
	path     string
	cfg      config.Config
	tabs     []configTab
	tab      int
	selected int
	editing  bool
	draft    string
	dirty    bool
	discard  bool // one q already asked about unsaved changes
	notice   string
}

// EditConfig opens a minimal arrow-key editor for the configuration at path
// (empty selects the default path) and returns when the reader quits. It
// loads and saves through the config package's own API, so the file format,
// the unknown-key preservation and the atomic write are exactly the CLI's.
func EditConfig(ctx context.Context, term Terminal, path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	e := &configEditor{
		path: path,
		cfg:  cfg,
		tabs: []configTab{generalTab(), modelsTab(&cfg)},
	}
	if s, ok := term.(startable); ok {
		s.start()
		defer term.Restore()
	}
	term.Draw(e.frame())
	for {
		k, err := term.ReadKey(ctx)
		if err != nil {
			return err
		}
		if e.editing {
			e.editKey(k)
		} else if e.navKey(k) {
			return nil
		}
		term.Draw(e.frame())
	}
}

// navKey handles one key outside edit mode and reports whether the session
// ends. Tab and Shift-Tab move between the tabs — the terminal decoder has
// no left and right arrows — and the movement keys stay inside the tab.
func (e *configEditor) navKey(k keyMsg) bool {
	e.notice = ""
	rows := e.tabs[e.tab].rows
	switch {
	case k.Special == "tab" || k.Special == "backtab":
		e.tab = (e.tab + 1) % len(e.tabs)
		if e.selected >= len(e.tabs[e.tab].rows) {
			e.selected = len(e.tabs[e.tab].rows) - 1
		}
		if e.selected < 0 {
			e.selected = 0
		}
	case k.Special == "up":
		if e.selected > 0 {
			e.selected--
		}
	case k.Special == "down":
		if e.selected < len(rows)-1 {
			e.selected++
		}
	case k.Special == "enter":
		if len(rows) == 0 {
			e.notice = e.tabs[e.tab].note
			break
		}
		if rows[e.selected].set == nil {
			e.notice = "That row is read-only."
			break
		}
		e.editing = true
		e.draft = ""
	case k.Rune == 's':
		e.save()
	case k.Special == "esc" || k.Rune == 'q':
		if e.dirty && !e.discard {
			e.discard = true
			e.notice = "Unsaved changes — s saves, q again discards."
			return false
		}
		return true
	}
	if k.Special != "esc" && k.Rune != 'q' {
		e.discard = false
	}
	return false
}

// editKey handles one key while a value is being typed.
func (e *configEditor) editKey(k keyMsg) {
	e.notice = ""
	switch {
	case k.Special == "esc":
		e.editing = false
	case k.Special == "enter":
		if e.draft == "" {
			// An empty answer keeps the current value, the same convention the
			// config wizard uses.
			e.editing = false
			return
		}
		f := e.tabs[e.tab].rows[e.selected]
		if err := f.set(&e.cfg, e.draft); err != nil {
			e.notice = err.Error()
			return
		}
		e.editing = false
		e.dirty = true
	case k.Special == "backspace":
		if r := []rune(e.draft); len(r) > 0 {
			e.draft = string(r[:len(r)-1])
		}
	case k.Rune != 0:
		e.draft += string(k.Rune)
	}
}

// save validates and writes the configuration. A validation failure is a
// notice, not an exit: the reader fixes the value and saves again.
func (e *configEditor) save() {
	if err := config.Validate(e.cfg); err != nil {
		e.notice = "Not saved: " + err.Error()
		return
	}
	if err := config.Save(e.path, e.cfg); err != nil {
		e.notice = "Not saved: " + err.Error()
		return
	}
	e.dirty = false
	e.discard = false
	e.notice = "Saved."
}

// frame renders the whole editor: the tab bar, the active tab's rows, the key
// help and the last notice. It is pure so a test can assert the layout and
// the notices without a terminal.
func (e *configEditor) frame() string {
	var b strings.Builder
	b.WriteString("\n  Plan Forge configuration\n\n")
	for i, tab := range e.tabs {
		if i == e.tab {
			fmt.Fprintf(&b, "  \x1b[7m %s \x1b[0m", tab.name)
		} else {
			fmt.Fprintf(&b, "  \x1b[2m %s \x1b[0m", tab.name)
		}
	}
	b.WriteString("\n\n")
	tab := e.tabs[e.tab]
	width := 0
	for _, f := range tab.rows {
		if len(f.label) > width {
			width = len(f.label)
		}
	}
	for i, f := range tab.rows {
		value := f.get(&e.cfg)
		if value == "" {
			value = "(unset)"
		}
		label := f.label + strings.Repeat(" ", width-len(f.label))
		if e.editing && i == e.selected {
			fmt.Fprintf(&b, "  \x1b[7m %s  %s \x1b[0m\n", label, e.draft+"█")
			continue
		}
		if i == e.selected {
			fmt.Fprintf(&b, "  \x1b[7m %s  %s \x1b[0m\n", label, value)
		} else {
			fmt.Fprintf(&b, "    %s  %s\n", label, value)
		}
	}
	if tab.note != "" {
		b.WriteString("\n    " + tab.note + "\n")
	}
	b.WriteString("\n")
	if e.editing {
		fmt.Fprintf(&b, "  %s\n  Enter sets (empty keeps, - clears) · Esc cancels\n", tab.rows[e.selected].hint)
	} else {
		b.WriteString("  Tab switch tab · ↑/↓ move · Enter edit · s save · q quit\n")
	}
	if e.notice != "" {
		b.WriteString("\n  " + e.notice + "\n")
	}
	return b.String()
}
