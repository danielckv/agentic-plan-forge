//go:build client && !server

package main

import "testing"

// TestVersionDisplay covers the release path: the pipeline injects the tag
// itself, so a caller that prepends "v" printed "vv0.1.2".
func TestVersionDisplay(t *testing.T) {
	cases := map[string]string{
		"":                  "dev",
		"dev":               "dev",
		"0.1.2":             "v0.1.2",
		"v0.1.2":            "v0.1.2",
		"V0.1.2":            "V0.1.2",
		"0.0.0-snapshot":    "v0.0.0-snapshot",
		"0.0.0-main.abc123": "v0.0.0-main.abc123",
		"nightly":           "nightly",
	}
	for in, want := range cases {
		saved := version
		version = in
		got := versionDisplay()
		version = saved
		if got != want {
			t.Errorf("versionDisplay() with version=%q = %q, want %q", in, got, want)
		}
	}
}
