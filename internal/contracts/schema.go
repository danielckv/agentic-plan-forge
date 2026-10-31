package contracts

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaFS holds the canonical schemas. They are the single source of truth:
// the Go validator and the frontend's Ajv instance load the same files, and the
// fixture corpus in tests/fixtures/schema exercises both.
//
//go:embed schema/*.json
var schemaFS embed.FS

// SchemaDir is the embedded directory prefix used by SchemaPath and SchemaNames.
const SchemaDir = "schema"

var (
	schemaOnce     sync.Once
	compiledSchema map[string]*jsonschema.Schema
	schemaErr      error
)

// SchemaNames lists the embedded schema IDs (file names without extension), in
// sorted order.
func SchemaNames() []string {
	entries, err := fs.ReadDir(schemaFS, SchemaDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(names)
	return names
}

// SchemaBytes returns the raw bytes of one embedded schema.
func SchemaBytes(schemaID string) ([]byte, error) {
	name, err := schemaFileName(schemaID)
	if err != nil {
		return nil, err
	}
	return schemaFS.ReadFile(name)
}

func schemaFileName(schemaID string) (string, error) {
	if schemaID == "" {
		return "", Coded(CodeInvalidPayload, "empty schema id")
	}
	if strings.ContainsAny(schemaID, `/\`) || strings.Contains(schemaID, "..") {
		return "", Coded(CodeInvalidPayload, "invalid schema id %q", schemaID)
	}
	return SchemaDir + "/" + schemaID + ".json", nil
}

func loadSchemas() {
	compiledSchema = map[string]*jsonschema.Schema{}
	for _, id := range SchemaNames() {
		raw, err := SchemaBytes(id)
		if err != nil {
			schemaErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
		if err != nil {
			schemaErr = fmt.Errorf("schema %s: %w", id, err)
			return
		}
		compiler := jsonschema.NewCompiler()
		// Network $ref loading is disabled by construction: the only resource
		// the compiler ever sees is the embedded document itself.
		if err := compiler.AddResource(id+".json", doc); err != nil {
			schemaErr = fmt.Errorf("schema %s: %w", id, err)
			return
		}
		sch, err := compiler.Compile(id + ".json")
		if err != nil {
			schemaErr = fmt.Errorf("schema %s: %w", id, err)
			return
		}
		compiledSchema[id] = sch
	}
}

// Validate checks raw JSON against an embedded schema. Model output is never
// trusted merely because it parses: it must satisfy the schema, and every
// schema rejects unknown properties.
func Validate(schemaID string, raw []byte) error {
	schemaOnce.Do(loadSchemas)
	if schemaErr != nil {
		return Wrap(CodeInternal, schemaErr, "load schemas")
	}
	sch, ok := compiledSchema[schemaID]
	if !ok {
		return Coded(CodeInvalidPayload, "unknown schema %q", schemaID)
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return Wrap(CodeInvalidPayload, err, "schema %s: decode instance", schemaID)
	}
	if err := sch.Validate(inst); err != nil {
		return Wrap(CodeInvalidPayload, err, "schema %s: validation failed", schemaID)
	}
	return nil
}

// SchemaPath writes the embedded schema to a 0600 temporary file and returns its
// path. Host CLIs that accept a schema file (Antigravity's --json-schema, for
// example) need a real path, and the file must not be world-readable because
// schemas can encode internal field names.
func SchemaPath(schemaID string) (string, error) {
	raw, err := SchemaBytes(schemaID)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "plan-forge-schema-")
	if err != nil {
		return "", Wrap(CodeInternal, err, "create schema temp dir")
	}
	path := filepath.Join(dir, schemaID+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", Wrap(CodeInternal, err, "write schema %s", schemaID)
	}
	return path, nil
}

// MermaidDenylistTokens returns the shared Mermaid statement denylist. The same
// file is imported by the frontend, so the compiler and the renderer cannot
// drift apart.
func MermaidDenylistTokens() ([]string, error) {
	raw, err := SchemaBytes("mermaid-denylist")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Tokens []string `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, Wrap(CodeInternal, err, "decode mermaid denylist")
	}
	return doc.Tokens, nil
}
