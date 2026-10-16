//go:build client && !server

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/host"
	"github.com/danielckv/agentic-plan-forge/internal/window"
)

func init() {
	register("doctor", "report prerequisites, host capabilities, models, browser and OCR status", runDoctor)
}

// doctorReport is the data behind both output formats. It carries only
// redacted values: host AuthState detail strings are the sign-in commands by
// contract, never credentials, account identifiers or raw command output.
type doctorReport struct {
	Version string            `json:"version"`
	Browser doctorBrowser     `json:"browser"`
	Hosts   []doctorHost      `json:"hosts"`
	OCR     map[string]string `json:"ocr"`
}

type doctorBrowser struct {
	Found  string `json:"found"`
	Name   string `json:"name,omitempty"`
	Path   string `json:"path,omitempty"`
	Remedy string `json:"remedy,omitempty"`
}

type doctorHost struct {
	Host    string     `json:"host"`
	Found   bool       `json:"found"`
	Version string     `json:"version,omitempty"`
	Auth    doctorAuth `json:"auth"`
	Models  []string   `json:"models,omitempty"`
}

type doctorAuth struct {
	Ready  bool   `json:"ready"`
	Method string `json:"method,omitempty"`
	SignIn string `json:"sign_in,omitempty"`
}

// runDoctor reports prerequisites: host detection, auth state and models per
// host, the browser the dedicated window will use (honoring browser_path),
// and the optional OCR tools. Secrets are redacted by construction: nothing
// here reads or prints credentials or raw probe output.
func runDoctor(ctx context.Context, args []string) int {
	var asJSON bool
	fs, err := parseFlags("doctor", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&asJSON, "json", false, "machine-readable output")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "doctor takes no arguments")
		return exitUsage
	}

	report := doctorReport{Version: version, Hosts: []doctorHost{}, OCR: map[string]string{}}

	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	if browser, berr := window.DefaultFinder().Find(cfg.BrowserPath); berr == nil {
		report.Browser = doctorBrowser{Found: "yes", Name: browser.Name, Path: browser.Path}
	} else {
		report.Browser = doctorBrowser{
			Found:  "none",
			Remedy: "install Chrome, Edge, Brave or Chromium, or set browser_path",
		}
	}

	for _, adapter := range host.Registered() {
		entry := doctorHost{Host: string(adapter.Kind()), Auth: doctorAuth{}}
		v, verr := adapter.Detect(ctx)
		if verr != nil {
			entry.Found = false
			report.Hosts = append(report.Hosts, entry)
			continue
		}
		entry.Found = true
		entry.Version = v
		if state, aerr := adapter.Auth(ctx); aerr == nil {
			entry.Auth = doctorAuth{Ready: state.Ready, Method: state.Method}
			if !state.Ready && state.Detail != "" {
				entry.Auth.SignIn = state.Detail
			}
		}
		if models, merr := adapter.Models(ctx); merr == nil && len(models) > 0 {
			ids := make([]string, 0, len(models))
			for _, m := range models {
				ids = append(ids, m.ID)
			}
			sort.Strings(ids)
			entry.Models = ids
		}
		report.Hosts = append(report.Hosts, entry)
	}

	for _, tool := range []string{"pdftoppm", "tesseract"} {
		if path, lerr := exec.LookPath(tool); lerr == nil {
			report.OCR[tool] = "found (" + path + ")"
		} else {
			report.OCR[tool] = "not found"
		}
	}

	if asJSON {
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return exitRuntimeError
		}
		fmt.Println(string(raw))
		return exitOK
	}

	fmt.Printf("plan-forge %s doctor\n\n", version)
	if report.Browser.Found == "yes" {
		fmt.Printf("browser:    %s (%s)\n", report.Browser.Name, report.Browser.Path)
	} else {
		fmt.Printf("browser:    none — %s\n", report.Browser.Remedy)
	}
	for _, entry := range report.Hosts {
		name := padRight(entry.Host, 10)
		if !entry.Found {
			fmt.Printf("%s not found\n", name)
			continue
		}
		fmt.Printf("%s found %s\n", name, entry.Version)
		if entry.Auth.Ready {
			fmt.Printf("  auth:   ready (%s)\n", entry.Auth.Method)
		} else if entry.Auth.SignIn != "" {
			fmt.Printf("  auth:   not signed in — run: %s\n", entry.Auth.SignIn)
		} else {
			fmt.Println("  auth:   unknown")
		}
		if len(entry.Models) > 0 {
			fmt.Printf("  models: %s\n", strings.Join(entry.Models, ", "))
		}
	}
	fmt.Println()
	fmt.Printf("ocr (optional, scanned PDFs only): pdftoppm %s; tesseract %s\n",
		report.OCR["pdftoppm"], report.OCR["tesseract"])
	return exitOK
}
