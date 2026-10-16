//go:build client && !server

package host

import (
	"reflect"
	"testing"
)

func TestBuildEnvironmentExact(t *testing.T) {
	parent := []string{
		"PATH=/bin",
		"HOME=/home/example",
		"USER=example",
		"LANG=en_US.UTF-8",
		"LC_ALL=C",
		"XDG_CACHE_HOME=/cache",
		"ANTHROPIC_API_KEY=secret",
		"CLAUDE_CONFIG_DIR=/claude",
		"CLAUDE_CODE_USE_BEDROCK=1",
		"CUSTOM_ALLOWED=kept",
		"CUSTOM_DROPPED=gone",
		"CLAUDECODE=1",
		"CLAUDE_CODE_ENTRYPOINT=nested",
		"OMP_AGENT_ACTIVE=1",
		"ANTIGRAVITY_AGENT=1",
		"AGENTS_RUNTIME=1",
		"PLAN_FORGE_RELAY_TOKEN=secret",
		"GIT_ASKPASS=helper",
		"OLDPWD=/old",
		"INIT_CWD=/init",
		"PWD=/parent",
	}
	got := BuildEnvironment(parent, "/isolated", &Claude{}, []string{"CUSTOM_ALLOWED", "PLAN_FORGE_RELAY_TOKEN", "GIT_ASKPASS"})
	want := []string{
		"ANTHROPIC_API_KEY=secret",
		"CLAUDE_CODE_USE_BEDROCK=1",
		"CLAUDE_CONFIG_DIR=/claude",
		"CUSTOM_ALLOWED=kept",
		"HOME=/home/example",
		"LANG=en_US.UTF-8",
		"LC_ALL=C",
		"PATH=/bin",
		"PWD=/isolated",
		"USER=example",
		"XDG_CACHE_HOME=/cache",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("child environment mismatch\n got: %#v\nwant: %#v", got, want)
	}
}
