//go:build client && !server

package planstate

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

// shown makes an untrusted path safe to put in a message.
func shown(rel string) string {
	return fmt.Sprintf("%q", sanitize.Truncate(sanitize.Terminal(rel), 120))
}

// checkLocalPath enforces the rule for every manifest path (a target path or a
// verification cwd): it must satisfy filepath.IsLocal, be clean and
// forward-slash, contain no symbolic-link component (an Lstat walk) and so
// resolve inside the repository root. real must already be symlink-resolved. It
// never opens the path: a component that does not exist yet ends the walk, which
// is how a target for a file the plan will add passes.
func checkLocalPath(real, rel string, allowEmpty bool) error {
	if rel == "" {
		if allowEmpty {
			return nil
		}
		return errors.New("an empty path")
	}
	switch {
	case strings.ContainsAny(rel, "\\\x00"):
		return fmt.Errorf("path %s contains a backslash or NUL", shown(rel))
	case path.IsAbs(rel) || filepath.IsAbs(rel):
		return fmt.Errorf("path %s is absolute", shown(rel))
	case !filepath.IsLocal(filepath.FromSlash(rel)):
		return fmt.Errorf("path %s is not local to the repository", shown(rel))
	case path.Clean(rel) != rel:
		return fmt.Errorf("path %s is not clean", shown(rel))
	}
	cur := real
	for _, comp := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, comp)
		info, err := os.Lstat(cur)
		if isNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("path %s cannot be inspected", shown(rel))
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fmt.Errorf("path %s has a symbolic-link component", shown(rel))
		}
	}
	return nil
}

// validateManifestPaths checks every target path and verification cwd the
// manifest names, at the top level and inside tasks. The first violation is
// returned and nothing is opened.
func validateManifestPaths(real string, m contracts.PlanManifest) error {
	targets := append([]contracts.Target(nil), m.Targets...)
	cmds := append([]contracts.VerificationCommand(nil), m.VerificationCommands...)
	for _, t := range m.TaskDAG {
		targets = append(targets, t.Targets...)
		cmds = append(cmds, t.Verification...)
	}
	for _, t := range targets {
		if err := checkLocalPath(real, t.Path, false); err != nil {
			return fmt.Errorf("target %w", err)
		}
	}
	for _, c := range cmds {
		if err := checkLocalPath(real, c.Cwd, true); err != nil {
			return fmt.Errorf("verification cwd %w", err)
		}
	}
	return nil
}
