//go:build client && !server

package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var mandatoryOMPDisabledProviders = []string{
	"native", "claude", "codex", "gemini", "github", "opencode", "cursor", "agents-md",
}

func buildOMPOverlay(cwd string) ([]byte, error) {
	userDisabled, err := effectiveOMPDisabledProviders(cwd)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	providers := make([]string, 0, len(mandatoryOMPDisabledProviders)+len(userDisabled))
	for _, provider := range append(append([]string(nil), mandatoryOMPDisabledProviders...), userDisabled...) {
		provider = strings.TrimSpace(provider)
		if provider == "" || seen[provider] {
			continue
		}
		seen[provider] = true
		providers = append(providers, provider)
	}
	var overlay strings.Builder
	overlay.WriteString("disabledProviders:\n")
	for _, provider := range providers {
		overlay.WriteString("  - ")
		overlay.WriteString(strconv.Quote(provider))
		overlay.WriteByte('\n')
	}
	overlay.WriteString("mcp:\n  enableProjectConfig: false\n")
	return []byte(overlay.String()), nil
}

func effectiveOMPDisabledProviders(cwd string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	global, globalSet, err := readFirstOMPConfig(
		filepath.Join(home, ".omp", "agent", "config.yml"),
		filepath.Join(home, ".omp", "agent", "config.yaml"),
		filepath.Join(home, ".omp", "agent", "settings.json"),
	)
	if err != nil {
		return nil, err
	}
	project, projectSet, err := readLayeredOMPProjectConfig(filepath.Join(cwd, ".omp"))
	if err != nil {
		return nil, err
	}
	if projectSet {
		return project, nil
	}
	if globalSet {
		return global, nil
	}
	return nil, nil
}

func readFirstOMPConfig(paths ...string) ([]string, bool, error) {
	for _, path := range paths {
		values, set, exists, err := readOMPDisabledProviders(path)
		if err != nil {
			return nil, false, err
		}
		if exists {
			return values, set, nil
		}
	}
	return nil, false, nil
}

func readLayeredOMPProjectConfig(dir string) ([]string, bool, error) {
	var values []string
	set := false
	for _, name := range []string{"settings.json", "config.yml", "config.yaml"} {
		current, currentSet, exists, err := readOMPDisabledProviders(filepath.Join(dir, name))
		if err != nil {
			return nil, false, err
		}
		if exists && currentSet {
			values, set = current, true
		}
	}
	return values, set, nil
}

func readOMPDisabledProviders(path string) ([]string, bool, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, true, fmt.Errorf("OMP config is not a regular file: %s", path)
	}
	if info.Size() > 1<<20 {
		return nil, false, true, fmt.Errorf("OMP config exceeds 1 MiB: %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, true, err
	}
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		values, set, err := parseOMPJSONDisabledProviders(raw)
		return values, set, true, err
	}
	values, set, err := parseOMPYAMLDisabledProviders(raw)
	return values, set, true, err
}

func parseOMPJSONDisabledProviders(raw []byte) ([]string, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, false, err
	}
	value, ok := root["disabledProviders"]
	if !ok {
		return nil, false, nil
	}
	return collectProviderStrings(value), true, nil
}

func collectProviderStrings(value any) []string {
	var result []string
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case string:
			result = append(result, typed)
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				if key == "providers" || key == "values" || key == "items" || key == "models" {
					walk(item)
				}
			}
		}
	}
	walk(value)
	return result
}

func parseOMPYAMLDisabledProviders(raw []byte) ([]string, bool, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	inSetting := false
	settingIndent := 0
	var result []string
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		leading := len(line) - len(strings.TrimLeft(line, " \t"))
		if strings.Contains(line[:leading], "\t") {
			return nil, false, errors.New("tabs are not accepted in OMP YAML indentation")
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if !inSetting {
			if indent != 0 || !strings.HasPrefix(trimmed, "disabledProviders:") {
				continue
			}
			inSetting = true
			settingIndent = indent
			remainder := strings.TrimSpace(strings.TrimPrefix(trimmed, "disabledProviders:"))
			if remainder != "" {
				values, err := parseYAMLInlineStrings(remainder)
				return values, true, err
			}
			continue
		}
		if indent <= settingIndent {
			break
		}
		withoutComment := strings.TrimSpace(strings.SplitN(trimmed, " #", 2)[0])
		if strings.HasPrefix(withoutComment, "providers:") || strings.HasPrefix(withoutComment, "values:") || strings.HasPrefix(withoutComment, "items:") {
			remainder := strings.TrimSpace(strings.SplitN(withoutComment, ":", 2)[1])
			if remainder != "" {
				values, err := parseYAMLInlineStrings(remainder)
				if err != nil {
					return nil, false, err
				}
				result = append(result, values...)
			}
			continue
		}
		if !strings.HasPrefix(withoutComment, "-") {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(withoutComment, "-"))
		if strings.Contains(item, ":") {
			continue
		}
		value, err := parseYAMLScalar(item)
		if err != nil {
			return nil, false, err
		}
		if value != "" {
			result = append(result, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	return result, inSetting, nil
}

func parseYAMLInlineStrings(value string) ([]string, error) {
	if value == "null" || value == "[]" {
		return nil, nil
	}
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return nil, fmt.Errorf("unsupported disabledProviders YAML value %q", value)
	}
	body := strings.TrimSpace(value[1 : len(value)-1])
	if body == "" {
		return nil, nil
	}
	parts := strings.Split(body, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		item, err := parseYAMLScalar(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func parseYAMLScalar(value string) (string, error) {
	if value == "" {
		return "", errors.New("empty disabledProviders entry")
	}
	if value[0] == '"' {
		var decoded string
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return "", err
		}
		return decoded, nil
	}
	if value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", errors.New("unterminated YAML string")
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("*._-", r) {
			continue
		}
		return "", fmt.Errorf("unsupported character in disabledProviders entry %q", value)
	}
	return value, nil
}
