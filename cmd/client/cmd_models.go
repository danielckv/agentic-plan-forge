//go:build client && !server

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/danielckv/agentic-plan-forge/internal/host"
)

func init() {
	register("models", "list discovered models per authenticated host", runModels)
}

// runModels lists the models each detected host exposes. Output carries model
// identifiers only; nothing here reads credentials or account state.
func runModels(ctx context.Context, args []string) int {
	var asJSON bool
	fs, err := parseFlags("models", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&asJSON, "json", false, "machine-readable output")
	})
	if err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "models takes no arguments")
		return exitUsage
	}

	type hostModels struct {
		Host   string          `json:"host"`
		Found  bool            `json:"found"`
		Models []host.ModelRef `json:"models,omitempty"`
	}
	out := []hostModels{}
	for _, adapter := range host.Registered() {
		entry := hostModels{Host: string(adapter.Kind()), Models: []host.ModelRef{}}
		if _, err := adapter.Detect(ctx); err != nil {
			out = append(out, entry)
			continue
		}
		entry.Found = true
		if models, err := adapter.Models(ctx); err == nil {
			entry.Models = models
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })

	if asJSON {
		raw, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return exitRuntimeError
		}
		fmt.Println(string(raw))
		return exitOK
	}
	for _, entry := range out {
		if !entry.Found {
			fmt.Printf("%s not found\n", entry.Host)
			continue
		}
		fmt.Printf("%s:\n", entry.Host)
		if len(entry.Models) == 0 {
			fmt.Println("  (no models reported)")
			continue
		}
		for _, m := range entry.Models {
			fmt.Printf("  %s\t%s\n", m.ID, m.Label)
		}
	}
	return exitOK
}
