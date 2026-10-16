//go:build client && !server

package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

const (
	commandOutputLimit = 4 << 20
	commandErrorLimit  = 32 << 10
)

type cappedBuffer struct {
	buf []byte
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := b.max - len(b.buf); remaining > 0 {
		if len(p) < remaining {
			remaining = len(p)
		}
		b.buf = append(b.buf, p[:remaining]...)
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.buf) }
func (b *cappedBuffer) Bytes() []byte  { return append([]byte(nil), b.buf...) }

func newRunDir() (string, error) {
	cache, err := config.EnsureCache()
	if err != nil {
		return "", fmt.Errorf("prepare plan-forge cache: %w", err)
	}
	runRoot := filepath.Join(cache, "run")
	if err := os.MkdirAll(runRoot, 0o700); err != nil {
		return "", fmt.Errorf("prepare generation cache: %w", err)
	}
	dir, err := os.MkdirTemp(runRoot, "gen-*")
	if err != nil {
		return "", fmt.Errorf("create isolated generation directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("restrict isolated generation directory: %w", err)
	}
	return dir, nil
}

func writePrivate(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func lookPath(binary string) (string, error) {
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", &contracts.ErrCapability{
			Host:   binary,
			Reason: "CLI executable was not found on PATH",
			Remedy: "install " + binary + " and sign in",
		}
	}
	return path, nil
}

func runProbe(ctx context.Context, adapter Adapter, binary string, args ...string) ([]byte, []byte, error) {
	path, err := lookPath(binary)
	if err != nil {
		return nil, nil, err
	}
	dir, err := newRunDir()
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	return runCaptured(ctx, path, args, dir, childEnvironment(dir, adapter, nil), nil)
}

func runCaptured(ctx context.Context, executable string, args []string, cwd string, env []string, stdin []byte) ([]byte, []byte, error) {
	cmd := exec.Command(executable, args...)
	cmd.Dir = cwd
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	stdout := &cappedBuffer{max: commandOutputLimit}
	stderr := &cappedBuffer{max: commandErrorLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	group, err := startProcessGroup(cmd)
	if err != nil {
		return nil, nil, err
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	select {
	case err = <-wait:
		_ = group.Close()
	case <-ctx.Done():
		_ = group.Kill()
		err = <-wait
		_ = group.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

func runNDJSON(
	ctx context.Context,
	executable string,
	args []string,
	cwd string,
	env []string,
	stdin []byte,
	handle func(map[string]any) error,
) (string, error) {
	cmd := exec.Command(executable, args...)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(stdin)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr := &cappedBuffer{max: commandErrorLimit}
	cmd.Stderr = stderr

	group, err := startProcessGroup(cmd)
	if err != nil {
		return "", err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = group.Kill()
		case <-done:
		}
	}()

	var streamErr error
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), contracts.MaxStageOutputBytes*3+64<<10)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if !utf8.Valid(line) {
			streamErr = errors.New("host emitted invalid UTF-8")
			break
		}
		event, decodeErr := decodeJSONObject(line)
		if decodeErr != nil {
			streamErr = fmt.Errorf("decode host NDJSON event: %w", decodeErr)
			break
		}
		if streamErr = handle(event); streamErr != nil {
			break
		}
	}
	if scanErr := scanner.Err(); streamErr == nil && scanErr != nil {
		streamErr = fmt.Errorf("read host NDJSON stream: %w", scanErr)
	}
	if streamErr != nil {
		_ = group.Kill()
	}
	waitErr := cmd.Wait()
	close(done)
	_ = group.Close()

	if streamErr != nil {
		return stderr.String(), streamErr
	}
	if ctx.Err() != nil {
		return stderr.String(), ctx.Err()
	}
	if waitErr != nil {
		return stderr.String(), fmt.Errorf("host process failed: %w", waitErr)
	}
	return stderr.String(), nil
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("event must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return value, nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON token %v", token)
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}
