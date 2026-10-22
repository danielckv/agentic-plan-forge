//go:build client && !server

package main

import (
	"bytes"
	"strings"
	"testing"
)

// Hooks from a 0.3.0 install call `center ensure` at every session start and
// `board`/`swarm` from the OMP hook. Each must exit 0 with one explanatory line,
// and none may appear as a working command.
func TestRetiredCommandsExitCleanly(t *testing.T) {
	for name, note := range retiredCommands {
		if _, registered := registry[name]; registered {
			t.Errorf("%s is retired but still registered", name)
		}
		var stderr bytes.Buffer
		if code := runRetired(&stderr, name, note); code != exitOK {
			t.Errorf("%s exit code = %d, want %d", name, code, exitOK)
		}
		out := stderr.String()
		if strings.Count(out, "\n") != 1 || !strings.Contains(out, "plan-forge install") {
			t.Errorf("%s output = %q, want one line naming the reinstall", name, out)
		}
	}
	for _, name := range []string{"center", "board", "swarm"} {
		if _, ok := retiredCommands[name]; !ok {
			t.Errorf("%s is missing from retiredCommands", name)
		}
	}
}
