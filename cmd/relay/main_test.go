//go:build server && !client

package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParsePortSelection(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		env        string
		setEnv     bool
		wantPort   int
		wantListen string
		wantErr    string
	}{
		{name: "default", wantPort: 8443},
		{name: "PORT env used", env: "8080", setEnv: true, wantPort: 8080},
		{name: "empty PORT ignored", env: "", setEnv: true, wantPort: 8443},
		{name: "flag port wins over PORT", args: []string{"--port", "9000"}, env: "8080", setEnv: true, wantPort: 9000},
		{name: "flag port equal to default wins over PORT", args: []string{"--port", "8443"}, env: "8080", setEnv: true, wantPort: 8443},
		{name: "listen wins over PORT", args: []string{"--listen", "127.0.0.1:9100"}, env: "8080", setEnv: true, wantPort: 8443, wantListen: "127.0.0.1:9100"},
		{name: "non-numeric PORT", env: "abc", setEnv: true, wantErr: "PORT"},
		{name: "out of range PORT", env: "70000", setEnv: true, wantErr: "PORT"},
		{name: "zero PORT", env: "0", setEnv: true, wantErr: "PORT"},
		{name: "invalid PORT ignored when --port given", args: []string{"--port", "9000"}, env: "abc", setEnv: true, wantPort: 9000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv("PORT", tc.env)
			} else {
				// Guard against an ambient PORT in the developer's shell.
				t.Setenv("PORT", "")
			}
			opts, err := parse(tc.args)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parse(%v) succeeded, want error mentioning %q", tc.args, tc.wantErr)
				}
				if !errors.Is(err, errUsage) {
					t.Errorf("error %v is not a usage error", err)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse(%v): %v", tc.args, err)
			}
			if opts.port != tc.wantPort {
				t.Errorf("port = %d, want %d", opts.port, tc.wantPort)
			}
			if opts.listen != tc.wantListen {
				t.Errorf("listen = %q, want %q", opts.listen, tc.wantListen)
			}
		})
	}
}
