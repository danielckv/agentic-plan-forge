//go:build client && fakehost && !server

// Package fakehost provides deterministic fixture-backed generations for tests.
package fakehost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/host"
)

// KindFake is registered by the fakehost-enabled client entrypoint.
const KindFake host.Kind = "fake"

// Adapter replays one <role>.ndjson fixture per generation.
type Adapter struct{}

var _ host.Adapter = (*Adapter)(nil)

// New constructs a fixture adapter. Callers explicitly register it with
// host.Register so production builds never contain or select it.
func New() host.Adapter { return &Adapter{} }

func (a *Adapter) Kind() host.Kind { return KindFake }

func (a *Adapter) Detect(context.Context) (string, error) {
	if _, err := fixtureDirectory(); err != nil {
		return "", err
	}
	return "fixture-v1", nil
}

func (a *Adapter) Auth(context.Context) (host.AuthState, error) {
	if _, err := fixtureDirectory(); err != nil {
		return host.AuthState{Ready: false, Method: "fixture directory", Detail: err.Error()}, nil
	}
	return host.AuthState{Ready: true, Method: "fixture directory", Detail: "ready"}, nil
}

func (a *Adapter) Models(context.Context) ([]host.ModelRef, error) {
	return []host.ModelRef{{ID: "fixture", Label: "Fixture replay"}}, nil
}

func (a *Adapter) Preflight(context.Context) error {
	_, err := fixtureDirectory()
	return err
}

func (a *Adapter) Generate(ctx context.Context, req contracts.GenerateRequest, emit func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	name := string(req.Role)
	result := contracts.GenerationResult{Receipt: contracts.ToolFreeReceipt{
		Host:    string(KindFake),
		Version: "fixture-v1",
		Argv:    []string{"fake", "<" + name + ".ndjson>"},
	}}
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return result, fmt.Errorf("invalid fakehost role %q", name)
	}
	dir, err := fixtureDirectory()
	if err != nil {
		return result, err
	}
	path := filepath.Join(dir, name+".ndjson")
	info, err := os.Stat(path)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, fmt.Errorf("fakehost fixture is not a regular file: %s", name)
	}
	if info.Size() > int64(contracts.MaxStageOutputBytes*3) {
		return result, &contracts.ErrBound{Name: "fakehost fixture", Limit: int64(contracts.MaxStageOutputBytes * 3), Observed: info.Size()}
	}
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()

	var streamed bytes.Buffer
	var final string
	var usage contracts.Usage
	seenFinal := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), contracts.MaxStageOutputBytes*3)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event contracts.GenerationEvent
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			return result, fmt.Errorf("decode fakehost fixture %s: %w", name, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("trailing JSON value")
			}
			return result, fmt.Errorf("decode fakehost fixture %s: %w", name, err)
		}
		switch event.Kind {
		case contracts.EventStatus, contracts.EventPublicSummary:
			if emit != nil {
				if err := emit(event); err != nil {
					return result, err
				}
			}
		case contracts.EventTextDelta:
			if streamed.Len()+len(event.Text) > req.ClampMaxOutput() {
				return result, &contracts.ErrBound{Name: "generation output", Limit: int64(req.ClampMaxOutput()), Observed: int64(streamed.Len() + len(event.Text))}
			}
			_, _ = streamed.WriteString(event.Text)
			if emit != nil {
				if err := emit(event); err != nil {
					return result, err
				}
			}
		case contracts.EventUsage:
			if event.Usage != nil {
				usage = *event.Usage
			}
			if emit != nil {
				if err := emit(event); err != nil {
					return result, err
				}
			}
		case contracts.EventFinal:
			final = event.Text
			if final == "" {
				final = streamed.String()
			}
			seenFinal = true
		case contracts.EventError:
			return result, errors.New(event.Err)
		default:
			return result, fmt.Errorf("unsupported fakehost event kind %q", event.Kind)
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if !seenFinal {
		return result, errors.New("fakehost fixture has no final event")
	}
	raw := bytes.TrimSpace([]byte(final))
	if len(raw) > req.ClampMaxOutput() {
		return result, &contracts.ErrBound{Name: "generation output", Limit: int64(req.ClampMaxOutput()), Observed: int64(len(raw))}
	}
	if req.SchemaID != "" {
		if err := contracts.Validate(req.SchemaID, raw); err != nil {
			return result, err
		}
	}
	result.Content = append(json.RawMessage(nil), raw...)
	result.Usage = usage
	if emit != nil {
		if err := emit(contracts.GenerationEvent{Kind: contracts.EventFinal, Text: string(raw)}); err != nil {
			return result, err
		}
	}
	return result, nil
}

func fixtureDirectory() (string, error) {
	dir := os.Getenv("PLAN_FORGE_FAKE_FIXTURES")
	if dir == "" {
		return "", errors.New("PLAN_FORGE_FAKE_FIXTURES is not set")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("PLAN_FORGE_FAKE_FIXTURES is not a directory")
	}
	return dir, nil
}
