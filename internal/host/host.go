//go:build client && !server

// Package host discovers local agent CLIs and runs tool-free model generations.
package host

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Kind identifies a supported local agent CLI.
type Kind string

const (
	KindClaude Kind = "claude"
	KindOMP    Kind = "omp"
	KindAGY    Kind = "agy"
)

// AuthState reports whether an adapter can use the host's existing sign-in.
// Detail never contains credentials, account identifiers, or command output.
type AuthState struct {
	Ready  bool   `json:"ready"`
	Method string `json:"method,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ModelRef is a model identifier exposed by a local host CLI.
type ModelRef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Adapter is the per-host boundary. Implementations must start a fresh,
// tool-free process for every Generate call.
type Adapter interface {
	Kind() Kind
	Detect(ctx context.Context) (version string, err error)
	Auth(ctx context.Context) (AuthState, error)
	Models(ctx context.Context) ([]ModelRef, error)
	Preflight(ctx context.Context) error
	Generate(ctx context.Context, req contracts.GenerateRequest, emit func(contracts.GenerationEvent) error) (contracts.GenerationResult, error)
}

// ErrCapability is the shared capability failure returned by adapters.
type ErrCapability = contracts.ErrCapability

var registry = struct {
	sync.RWMutex
	adapters map[Kind]Adapter
}{adapters: make(map[Kind]Adapter)}

// Register adds or replaces an adapter for its kind. Replacement supports
// build-tagged adapters such as fakehost without changing the production set.
func Register(a Adapter) {
	if a == nil {
		panic("host: register nil adapter")
	}
	kind := a.Kind()
	if kind == "" {
		panic("host: register adapter with empty kind")
	}
	registry.Lock()
	registry.adapters[kind] = a
	registry.Unlock()
}

// Registered returns a stable snapshot sorted by kind.
func Registered() []Adapter {
	registry.RLock()
	out := make([]Adapter, 0, len(registry.adapters))
	for _, adapter := range registry.adapters {
		out = append(out, adapter)
	}
	registry.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Kind() < out[j].Kind() })
	return out
}

// Version detects one registered adapter's installed CLI version.
func Version(ctx context.Context, kind Kind) (string, error) {
	registry.RLock()
	adapter, ok := registry.adapters[kind]
	registry.RUnlock()
	if !ok {
		return "", fmt.Errorf("host adapter %q is not registered", kind)
	}
	return adapter.Detect(ctx)
}

func registeredMap(extraEnv []string) map[Kind]Adapter {
	result := make(map[Kind]Adapter)
	for _, adapter := range Registered() {
		if configurable, ok := adapter.(interface{ withEnvAllow([]string) Adapter }); ok {
			adapter = configurable.withEnvAllow(extraEnv)
		}
		result[adapter.Kind()] = adapter
	}
	return result
}

func init() {
	Register(NewClaude())
	Register(NewOMP())
	Register(NewAGY())
	Register(NewAnthropicAPI())
	Register(NewOpenAIAPI())
	Register(NewFireworksAPI())
}
