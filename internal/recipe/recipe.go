// Package recipe derives a versioned, deterministic execution recipe from the
// build assets a repository actually has. It reuses existing tooling (Make
// targets, package scripts, task runners, plain Go commands) in a fixed
// precedence order and never invents a command: when no command covers a
// step, it returns a sentinel error so the caller renders "not configured"
// instead of a fabricated check.
package recipe

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// SchemaVersion is the schema identifier hashed into every recipe.
const SchemaVersion = "execution_recipe.v1"

// Sentinel errors returned by Derive. The caller renders "not configured"
// for the step; the package never fabricates a passing check.
var (
	ErrNoBuildCommand = errors.New("recipe: no existing build command found")
	ErrNoTestCommand  = errors.New("recipe: no existing test command found")
	ErrNoRunCommand   = errors.New("recipe: no existing run command found")
	// ErrFacts is returned when the facts contradict themselves; the recipe
	// refuses to guess which asset is real.
	ErrFacts = errors.New("recipe: contradictory facts")
	// ErrStale is returned by CheckFresh when a hashed input changed or is
	// missing. It always wraps the specific mismatch.
	ErrStale = errors.New("recipe: stale input")
)

// Command is one argv-array step. Commands are never shell strings.
type Command struct {
	ID             string   `json:"id"`
	Argv           []string `json:"argv"`
	Dir            string   `json:"dir"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Env            []string `json:"env,omitempty"`
	SourceRef      string   `json:"source_ref,omitempty"`
}

// Script records an existing file a command relies on. This package does not
// generate scripts; Generated is always false here.
type Script struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Generated bool   `json:"generated"`
	SourceRef string `json:"source_ref,omitempty"`
}

// SourceRef pins a repository-relative file to its content hash.
type SourceRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Recipe is the versioned execution knowledge stored with the plan artifacts.
// Its JSON is the stored form: the field names are part of the contract with a
// prepared sandbox, so they are explicit rather than derived from Go names.
type Recipe struct {
	SchemaVersion  string      `json:"schema_version"`
	Revision       string      `json:"revision"`
	ProjectRoot    string      `json:"project_root"`
	Sources        []SourceRef `json:"sources"`
	Instructions   []SourceRef `json:"instructions"`
	Toolchains     []string    `json:"toolchains"`
	Scripts        []Script    `json:"scripts"`
	Build          Command     `json:"build"`
	Test           Command     `json:"test"`
	Run            Command     `json:"run"`
	Lockfiles      []SourceRef `json:"lockfiles"`
	EnvNames       []string    `json:"env_names"`
	RuntimePort    int         `json:"runtime_port"`
	ReadinessProbe []string    `json:"readiness_probe"`
	ArtifactPaths  []string    `json:"artifact_paths"`
	Template       string      `json:"template"`
}

// MarshalJSON emits the stored form. Nil slices become empty arrays so a
// consumer never has to treat null and empty differently.
func (r Recipe) MarshalJSON() ([]byte, error) {
	type wire Recipe
	w := wire(r)
	if w.Sources == nil {
		w.Sources = []SourceRef{}
	}
	if w.Instructions == nil {
		w.Instructions = []SourceRef{}
	}
	if w.Toolchains == nil {
		w.Toolchains = []string{}
	}
	if w.Scripts == nil {
		w.Scripts = []Script{}
	}
	if w.Lockfiles == nil {
		w.Lockfiles = []SourceRef{}
	}
	if w.EnvNames == nil {
		w.EnvNames = []string{}
	}
	if w.ReadinessProbe == nil {
		w.ReadinessProbe = []string{}
	}
	if w.ArtifactPaths == nil {
		w.ArtifactPaths = []string{}
	}
	return json.Marshal(w)
}

// Facts is what Detect observed about a repository tree.
type Facts struct {
	Root             string
	Revision         string
	GoMod            bool
	PackageJSON      bool
	Makefile         bool
	Justfile         bool
	Taskfile         bool
	ComposeFile      bool
	PackageScripts   map[string]string
	Lockfiles        []string
	InstructionFiles []string
	SourceDocs       []string
	Port             int
}

const (
	maxDocSize    = 2 << 20 // 2 MB per read file
	maxWalkFiles  = 20000
	maxWalkDepth  = 4
	defaultPort   = 8080
	goModName     = "go.mod"
	pkgJSONName   = "package.json"
	makefileName  = "Makefile"
	justfileName  = "justfile"
	taskfileName  = "Taskfile.yml"
	composeName   = "docker-compose.yml"
	dockerfile    = "Dockerfile"
	toolchainName = "go1.27"
)

// skipDirs are generated trees and VCS internals the walk never enters.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true,
	"build": true, "vendor": true, ".omp": true, ".agents": true,
	"coverage": true, ".playwright": true, "test-results": true,
	"web/dist": true, "web/node_modules": true,
}

// Detect walks root (bounded, symlink-safe, never into .git or generated
// trees) and reports which build assets exist. docs lists repository-relative
// source documents the caller considers relevant; each must exist inside
// root. Revision is left to the caller: it is a VCS fact, not a tree fact.
func Detect(ctx context.Context, root string, docs []string) (Facts, error) {
	f := Facts{Root: root}
	or, err := os.OpenRoot(root)
	if err != nil {
		return Facts{}, fmt.Errorf("recipe: open root: %w", err)
	}
	defer or.Close()

	fileCount := 0
	err = fs.WalkDir(os.DirFS(root), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			if p != "." && strings.Count(p, "/") >= maxWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks and other non-regular files are never read
		}
		fileCount++
		if fileCount > maxWalkFiles {
			return fmt.Errorf("recipe: tree exceeds %d files", maxWalkFiles)
		}
		switch p {
		case goModName:
			f.GoMod = true
		case makefileName:
			f.Makefile = true
		case justfileName, "Justfile", "justfile.yml":
			f.Justfile = true
		case taskfileName, "Taskfile.yaml":
			f.Taskfile = true
		case composeName, "docker-compose.yaml", "compose.yml", "compose.yaml":
			f.ComposeFile = true
		case "go.sum", "package-lock.json", "web/package-lock.json",
			"npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml":
			f.Lockfiles = append(f.Lockfiles, p)
		case "AGENTS.md", "CONTRIBUTING.md":
			f.InstructionFiles = append(f.InstructionFiles, p)
		case pkgJSONName:
			f.PackageJSON = true
			f.PackageScripts, err = readPackageScripts(or, p)
			if err != nil {
				return err
			}
		case "web/" + pkgJSONName:
			if !f.PackageJSON {
				f.PackageJSON = true
				f.PackageScripts, err = readPackageScripts(or, p)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return Facts{}, err
	}
	sort.Strings(f.Lockfiles)
	sort.Strings(f.InstructionFiles)

	for _, doc := range docs {
		clean, err := safeRel(doc)
		if err != nil {
			return Facts{}, err
		}
		if _, err := hashUnderRoot(or, clean); err != nil {
			return Facts{}, err
		}
		f.SourceDocs = append(f.SourceDocs, clean)
	}
	sort.Strings(f.SourceDocs)
	return f, nil
}

// readPackageScripts parses the scripts block of a package.json inside root.
func readPackageScripts(or *os.Root, rel string) (map[string]string, error) {
	f, err := or.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("recipe: open %s: %w", rel, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxDocSize+1))
	if err != nil {
		return nil, fmt.Errorf("recipe: read %s: %w", rel, err)
	}
	if len(raw) > maxDocSize {
		return nil, fmt.Errorf("recipe: %s exceeds %d bytes", rel, maxDocSize)
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, fmt.Errorf("recipe: parse %s: %w", rel, err)
	}
	return pkg.Scripts, nil
}

// makeTargets returns the targets declared in the Makefile under root.
func makeTargets(or *os.Root, rel string) (map[string]bool, error) {
	f, err := or.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("recipe: open %s: %w", rel, err)
	}
	defer f.Close()
	targets := map[string]bool{}
	sc := bufio.NewScanner(io.LimitReader(f, maxDocSize))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, " ") {
			continue
		}
		if i := strings.Index(line, ":"); i > 0 {
			name := strings.TrimSpace(line[:i])
			if name != "" && !strings.Contains(name, " ") && !strings.Contains(name, "$") {
				targets[name] = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("recipe: scan %s: %w", rel, err)
	}
	return targets, nil
}

// safeRel rejects absolute paths, traversal, and non-clean components.
func safeRel(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("recipe: empty path")
	}
	if path.IsAbs(p) || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("recipe: absolute path %q", p)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("recipe: path %q escapes the repository", p)
	}
	return clean, nil
}

// hashUnderRoot reads one file through the root-constrained handle, refusing
// non-regular files, and returns its SHA-256 hex digest.
func hashUnderRoot(or *os.Root, rel string) (string, error) {
	clean, err := safeRel(rel)
	if err != nil {
		return "", err
	}
	f, err := or.Open(clean)
	if err != nil {
		return "", fmt.Errorf("recipe: open %s: %w", clean, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("recipe: stat %s: %w", clean, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("recipe: %s is not a regular file", clean)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxDocSize+1)); err != nil {
		return "", fmt.Errorf("recipe: read %s: %w", clean, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Derive turns observed facts into a recipe. Build and test reuse the
// highest-precedence command that exists: Makefile target, package script,
// Taskfile/justfile recipe, plain Go command. Run prefers a package start
// script, then a compose service, then a Dockerfile. Nothing is generated.
func Derive(f Facts) (Recipe, error) {
	if len(f.PackageScripts) > 0 && !f.PackageJSON {
		return Recipe{}, fmt.Errorf("%w: package scripts present without package.json", ErrFacts)
	}
	if f.PackageJSON && f.PackageScripts == nil {
		return Recipe{}, fmt.Errorf("%w: package.json present with unreadable scripts", ErrFacts)
	}
	r := Recipe{
		SchemaVersion: SchemaVersion,
		Revision:      f.Revision,
		// ProjectRoot is repository-relative. The stored recipe is served to the
		// window and sent to a sandbox, so it must never name a host path.
		ProjectRoot: ".",
	}
	if f.GoMod {
		r.Toolchains = append(r.Toolchains, toolchainName)
	}
	if f.PackageJSON {
		r.Toolchains = append(r.Toolchains, "node")
	}
	sort.Strings(r.Toolchains)

	or, err := os.OpenRoot(f.Root)
	if err != nil {
		return Recipe{}, fmt.Errorf("recipe: open root: %w", err)
	}
	defer or.Close()

	hashAll := func(paths []string) ([]SourceRef, error) {
		refs := make([]SourceRef, 0, len(paths))
		for _, p := range paths {
			h, err := hashUnderRoot(or, p)
			if err != nil {
				return nil, err
			}
			refs = append(refs, SourceRef{Path: p, SHA256: h})
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].Path < refs[j].Path })
		return refs, nil
	}
	if r.Sources, err = hashAll(f.SourceDocs); err != nil {
		return Recipe{}, err
	}
	if r.Lockfiles, err = hashAll(f.Lockfiles); err != nil {
		return Recipe{}, err
	}
	if r.Instructions, err = hashAll(f.InstructionFiles); err != nil {
		return Recipe{}, err
	}

	pkgDir := ""
	if !fileExists(or, pkgJSONName) {
		if fileExists(or, path.Join("web", pkgJSONName)) {
			pkgDir = "web"
		}
	}
	pkgRef := path.Join(emptyOr(pkgDir), pkgJSONName)

	// Build and test: highest precedence that actually exists wins.
	r.Build, err = pickCommand(f, or, "build", 600, pkgDir, pkgRef)
	if err != nil {
		if errors.Is(err, ErrNoBuildCommand) {
			return Recipe{}, ErrNoBuildCommand
		}
		return Recipe{}, err
	}
	r.Test, err = pickCommand(f, or, "test", 1800, pkgDir, pkgRef)
	if err != nil {
		if errors.Is(err, ErrNoTestCommand) {
			return Recipe{}, ErrNoTestCommand
		}
		return Recipe{}, err
	}

	r.RuntimePort = portFor(f)
	r.ReadinessProbe = []string{"tcp", strconv.Itoa(r.RuntimePort)}
	r.Template = "linux-2vcpu-4gib"

	// Run: package start script, then compose service, then Dockerfile.
	switch {
	case len(f.PackageScripts["start"]) > 0:
		r.Run = Command{ID: "run", Argv: []string{"npm", "run", "start"}, Dir: emptyOr(pkgDir),
			TimeoutSeconds: 600, SourceRef: path.Join(emptyOr(pkgDir), pkgJSONName) + " script 'start'"}
		r.Scripts = appendScriptIfNew(r.Scripts, scriptRef(or, pkgRef))
	case f.ComposeFile:
		r.Run = Command{ID: "run", Argv: []string{"docker", "compose", "up", "--build"}, Dir: ".", TimeoutSeconds: 600, SourceRef: composeName}
		r.Scripts = appendScriptIfNew(r.Scripts, Script{Path: composeName, SHA256: hashExisting(or, composeName), SourceRef: composeName + " service"})
	case fileExists(or, dockerfile):
		port := strconv.Itoa(portFor(f))
		r.Run = Command{ID: "run", Argv: []string{"docker", "run", "--rm", "-p", port + ":" + port, "app"}, Dir: ".", TimeoutSeconds: 600, SourceRef: dockerfile}
	default:
		// Build and test are still valid; the caller renders run as
		// "not configured" instead of fabricating a check.
		return r, ErrNoRunCommand
	}

	return r, nil
}

// pickCommand returns the highest-precedence existing command for step.
func pickCommand(f Facts, or *os.Root, step string, timeout int, pkgDir, pkgRef string) (Command, error) {
	if f.Makefile {
		targets, err := makeTargets(or, makefileName)
		if err != nil {
			return Command{}, err
		}
		if targets[step] {
			return Command{
				ID: step, Argv: []string{"make", step}, Dir: ".",
				TimeoutSeconds: timeout, SourceRef: makefileName + " target '" + step + "'",
			}, nil
		}
	}
	// The map holds script *bodies*; npm needs the script *name*, which is the
	// step. Passing the body would ask npm to run a command as a script name.
	if _, ok := f.PackageScripts[step]; ok {
		return packageCmd(step, pkgDir, timeout), nil
	}
	switch step {
	case "test":
		if f.Taskfile {
			return Command{ID: step, Argv: []string{"task", step}, Dir: ".", TimeoutSeconds: timeout, SourceRef: taskfileName}, nil
		}
		if f.Justfile {
			return Command{ID: step, Argv: []string{"just", step}, Dir: ".", TimeoutSeconds: timeout, SourceRef: justfileName}, nil
		}
		if f.GoMod {
			return Command{ID: step, Argv: []string{"go", "test", "./..."}, Dir: ".", TimeoutSeconds: timeout, SourceRef: goModName}, nil
		}
		return Command{}, ErrNoTestCommand
	default: // build
		if f.Taskfile {
			return Command{ID: step, Argv: []string{"task", step}, Dir: ".", TimeoutSeconds: timeout, SourceRef: taskfileName}, nil
		}
		if f.Justfile {
			return Command{ID: step, Argv: []string{"just", step}, Dir: ".", TimeoutSeconds: timeout, SourceRef: justfileName}, nil
		}
		if f.GoMod {
			return Command{ID: step, Argv: []string{"go", "build", "./..."}, Dir: ".", TimeoutSeconds: timeout, SourceRef: goModName}, nil
		}
		return Command{}, ErrNoBuildCommand
	}
}

// portFor reports the runtime port, falling back to the documented default
// when the facts name none.
func portFor(f Facts) int {
	if f.Port > 0 {
		return f.Port
	}
	return defaultPort
}

// packageCmd runs a package script. npm is given the directory as its working
// directory rather than a prefix flag, so the command means the same thing to a
// reader and to the sandbox, and the path is never applied twice.
func packageCmd(id, dir string, timeout int) Command {
	return Command{
		ID: id, Argv: []string{"npm", "run", id}, Dir: emptyOr(dir), TimeoutSeconds: timeout,
		SourceRef: path.Join(emptyOr(dir), pkgJSONName) + " script '" + id + "'",
	}
}

func emptyOr(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

func scriptRef(or *os.Root, rel string) Script {
	return Script{Path: rel, SHA256: hashExisting(or, rel), SourceRef: rel + " script"}
}

// hashExisting hashes a file the facts said exists. A tree race is reported by
// an empty hash, which Validate rejects, rather than by taking the process down.

func appendScriptIfNew(scripts []Script, s Script) []Script {
	for _, existing := range scripts {
		if existing.Path == s.Path {
			return scripts
		}
	}
	return append(scripts, s)
}

func hashExisting(or *os.Root, rel string) string {
	h, err := hashUnderRoot(or, rel)
	if err != nil {
		return ""
	}
	return h
}

func fileExists(or *os.Root, rel string) bool {
	f, err := or.Open(rel)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// Hash returns the SHA-256 hex digest of the canonical JSON encoding of the
// recipe, schema version included. Encoding is struct-order JSON with no map
// iteration, so the digest is stable for identical recipes.
func (r Recipe) Hash() (string, error) {
	if r.SchemaVersion == "" {
		return "", fmt.Errorf("recipe: hash requires a schema version (want %s)", SchemaVersion)
	}
	b, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("recipe: encode: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Validate checks the recipe's internal consistency. Env entries are names
// only: any entry containing '=' (a value) is rejected.
func (r Recipe) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("recipe: schema version %q, want %q", r.SchemaVersion, SchemaVersion)
	}
	if r.Revision == "" {
		return fmt.Errorf("recipe: empty revision")
	}
	if r.ProjectRoot != "" && r.ProjectRoot != "." {
		if err := validateRel(r.ProjectRoot); err != nil {
			return fmt.Errorf("recipe: project root %q is not repository-relative: %w", r.ProjectRoot, err)
		}
	}
	ids := map[string]bool{}
	for _, c := range []Command{r.Build, r.Test, r.Run} {
		if err := validateCommand(c); err != nil {
			return err
		}
		if c.ID != "" {
			if ids[c.ID] {
				return fmt.Errorf("recipe: duplicate command ID %q", c.ID)
			}
			ids[c.ID] = true
		}
		for _, e := range c.Env {
			if strings.Contains(e, "=") {
				return fmt.Errorf("recipe: command %q env entry %q is a value; store names only", c.ID, e)
			}
		}
	}
	if r.Run.ID != "" && len(r.ReadinessProbe) == 0 {
		return fmt.Errorf("recipe: run command set without a readiness probe")
	}
	if r.Run.ID != "" && r.RuntimePort == 0 {
		return fmt.Errorf("recipe: run command set with runtime port 0")
	}
	if r.RuntimePort != 0 && (r.RuntimePort < 1 || r.RuntimePort > 65535) {
		return fmt.Errorf("recipe: runtime port %d out of range 1..65535", r.RuntimePort)
	}
	for _, e := range r.EnvNames {
		if strings.Contains(e, "=") {
			return fmt.Errorf("recipe: env name %q is a value; store names only", e)
		}
		if strings.ContainsAny(e, " \t\n") {
			return fmt.Errorf("recipe: env name %q contains whitespace", e)
		}
	}
	for _, ref := range allRefs(r) {
		if err := validateRel(ref.Path); err != nil {
			return err
		}
		if ref.SHA256 == "" {
			return fmt.Errorf("recipe: source %q has empty hash", ref.Path)
		}
	}
	for _, s := range r.Scripts {
		if err := validateRel(s.Path); err != nil {
			return err
		}
		if s.Generated {
			return fmt.Errorf("recipe: script %q marked generated; generation happens outside the recipe", s.Path)
		}
		if s.SHA256 == "" {
			return fmt.Errorf("recipe: script %q has empty hash", s.Path)
		}
	}
	for _, p := range r.ArtifactPaths {
		if err := validateRel(p); err != nil {
			return err
		}
	}
	if err := validateSortedRefs(r.Sources); err != nil {
		return err
	}
	if err := validateSortedRefs(r.Lockfiles); err != nil {
		return err
	}
	if err := validateSortedRefs(r.Instructions); err != nil {
		return err
	}
	if !sort.SliceIsSorted(r.Scripts, func(i, j int) bool { return r.Scripts[i].Path < r.Scripts[j].Path }) {
		return fmt.Errorf("recipe: scripts not sorted by path")
	}
	if !sort.StringsAreSorted(r.EnvNames) {
		return fmt.Errorf("recipe: env names not sorted")
	}
	if !sort.StringsAreSorted(r.ArtifactPaths) {
		return fmt.Errorf("recipe: artifact paths not sorted")
	}
	if !sort.StringsAreSorted(r.Toolchains) {
		return fmt.Errorf("recipe: toolchains not sorted")
	}
	return nil
}

func allRefs(r Recipe) []SourceRef {
	refs := make([]SourceRef, 0, len(r.Sources)+len(r.Lockfiles)+len(r.Instructions))
	refs = append(refs, r.Sources...)
	refs = append(refs, r.Lockfiles...)
	refs = append(refs, r.Instructions...)
	return refs
}

func validateSortedRefs(refs []SourceRef) error {
	for i := 1; i < len(refs); i++ {
		if refs[i-1].Path > refs[i].Path {
			return fmt.Errorf("recipe: refs not sorted: %q before %q", refs[i-1].Path, refs[i].Path)
		}
		if refs[i-1].Path == refs[i].Path {
			return fmt.Errorf("recipe: duplicate source %q", refs[i].Path)
		}
	}
	return nil
}

func validateRel(p string) error {
	clean, err := safeRel(p)
	if err != nil {
		return err
	}
	if clean != p {
		return fmt.Errorf("recipe: path %q is not clean; use %q", p, clean)
	}
	return nil
}

func validateCommand(c Command) error {
	if c.ID == "" {
		return nil // unset step
	}
	if len(c.Argv) == 0 {
		return fmt.Errorf("recipe: command %q has empty argv", c.ID)
	}
	for _, a := range c.Argv {
		if a == "" {
			return fmt.Errorf("recipe: command %q has empty argument", c.ID)
		}
		if strings.ContainsAny(a, ";&|`$><\n\\") {
			return fmt.Errorf("recipe: command %q argument %q needs shell metacharacters; commands are argv arrays", c.ID, a)
		}
	}
	if c.Dir != "" && c.Dir != "." {
		if err := validateRel(c.Dir); err != nil {
			return fmt.Errorf("recipe: command %q dir: %w", c.ID, err)
		}
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 3600 {
		return fmt.Errorf("recipe: command %q timeout %d outside 1..3600", c.ID, c.TimeoutSeconds)
	}
	return nil
}

// CheckFresh re-hashes every pinned input under root and returns ErrStale
// naming the first mismatch. A missing file is stale.
func (r Recipe) CheckFresh(ctx context.Context, root string) error {
	or, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("%w: open root: %v", ErrStale, err)
	}
	defer or.Close()

	check := func(kind, p, want string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got, err := hashUnderRoot(or, p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: %s %s deleted", ErrStale, kind, p)
			}
			return fmt.Errorf("%w: %s %s: %v", ErrStale, kind, p, err)
		}
		if got != want {
			return fmt.Errorf("%w: %s %s changed", ErrStale, kind, p)
		}
		return nil
	}
	for _, ref := range r.Sources {
		if err := check("source", ref.Path, ref.SHA256); err != nil {
			return err
		}
	}
	for _, ref := range r.Instructions {
		if err := check("instruction", ref.Path, ref.SHA256); err != nil {
			return err
		}
	}
	for _, ref := range r.Lockfiles {
		if err := check("lockfile", ref.Path, ref.SHA256); err != nil {
			return err
		}
	}
	for _, s := range r.Scripts {
		if err := check("script", s.Path, s.SHA256); err != nil {
			return err
		}
	}
	return nil
}
