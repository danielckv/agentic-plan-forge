//go:build client && !server

package host

import (
	"os"
	"runtime"
	"sort"
	"strings"
)

var baseEnvironmentNames = map[string]struct{}{
	"PATH": {}, "HOME": {}, "USER": {}, "LOGNAME": {}, "SHELL": {},
	"LANG": {}, "TERM": {}, "TMPDIR": {}, "TZ": {},
	"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "NO_PROXY": {},
	"SSL_CERT_FILE": {}, "SSL_CERT_DIR": {}, "NODE_EXTRA_CA_CERTS": {},
}

var windowsEnvironmentNames = map[string]struct{}{
	"SystemRoot": {}, "USERPROFILE": {}, "APPDATA": {}, "LOCALAPPDATA": {},
	"PATHEXT": {}, "ComSpec": {},
}

var removedEnvironmentNames = map[string]struct{}{
	"CLAUDECODE": {}, "CLAUDE_CODE_ENTRYPOINT": {}, "OMP_AGENT_ACTIVE": {},
	"ANTIGRAVITY_AGENT": {}, "AGENTS_RUNTIME": {}, "OLDPWD": {}, "INIT_CWD": {},
}

// BuildEnvironment filters parent through the common allowlist, the adapter's
// credential/config families, and explicitly configured variable names. It
// always removes nesting and repository markers, then pins PWD to cwd.
func BuildEnvironment(parent []string, cwd string, adapter Adapter, extraNames []string) []string {
	patterns := make([]string, 0, len(extraNames)+8)
	if provider, ok := adapter.(interface{ EnvAllow() []string }); ok {
		patterns = append(patterns, provider.EnvAllow()...)
	}
	for _, name := range extraNames {
		if validEnvironmentName(name) {
			patterns = append(patterns, name)
		}
	}

	values := make(map[string]string)
	spellings := make(map[string]string)
	for _, entry := range parent {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !validEnvironmentName(name) || mustRemoveEnvironment(name) {
			continue
		}
		if !baseEnvironmentAllowed(name) && !matchesEnvironmentPattern(name, patterns) {
			continue
		}
		key := environmentKey(name)
		values[key] = value
		spellings[key] = name
	}

	pwdKey := environmentKey("PWD")
	values[pwdKey] = cwd
	spellings[pwdKey] = "PWD"

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := strings.ToUpper(spellings[keys[i]]), strings.ToUpper(spellings[keys[j]])
		if left == right {
			return spellings[keys[i]] < spellings[keys[j]]
		}
		return left < right
	})

	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, spellings[key]+"="+values[key])
	}
	return result
}

func childEnvironment(cwd string, adapter Adapter, extraNames []string) []string {
	return BuildEnvironment(os.Environ(), cwd, adapter, extraNames)
}

func environmentKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

func baseEnvironmentAllowed(name string) bool {
	if strings.HasPrefix(name, "LC_") || strings.HasPrefix(name, "XDG_") {
		return true
	}
	if runtime.GOOS != "windows" {
		_, ok := baseEnvironmentNames[name]
		return ok
	}
	for allowed := range baseEnvironmentNames {
		if strings.EqualFold(name, allowed) {
			return true
		}
	}
	for allowed := range windowsEnvironmentNames {
		if strings.EqualFold(name, allowed) {
			return true
		}
	}
	return false
}

func matchesEnvironmentPattern(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.HasSuffix(pattern, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(pattern, "*")) {
				return true
			}
			continue
		}
		if name == pattern || (runtime.GOOS == "windows" && strings.EqualFold(name, pattern)) {
			return true
		}
	}
	return false
}

func mustRemoveEnvironment(name string) bool {
	if strings.HasPrefix(name, "PLAN_FORGE_") || strings.HasPrefix(name, "GIT_") {
		return true
	}
	_, ok := removedEnvironmentNames[name]
	if ok || runtime.GOOS != "windows" {
		return ok
	}
	for removed := range removedEnvironmentNames {
		if strings.EqualFold(name, removed) {
			return true
		}
	}
	return false
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
