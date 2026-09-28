// Package pythonexec runs CPython inside a capability-limited WebAssembly VM.
// Guest code never receives a host directory, environment, socket or process API.
package pythonexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing/fstest"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

const (
	Version        = "CPython 3.12.0 / WASI"
	MaxCodeBytes   = 16 << 10
	MaxInputBytes  = 8 << 20
	MaxOutputBytes = 64 << 10
	MaxStderrBytes = 4 << 10
	MemoryMB       = 128
	Timeout        = 20 * time.Second
)

// SHA256 values are pinned to the verified upstream distribution; changing a
// local path cannot silently replace the executable or its Python standard library.
var runtimeFiles = map[string]string{
	"bin/python-3.12.0.wasm":         "5ce0cbeb843e6e5abf2d50c7158002e8333c26a40fbe27a7a52e66ee48cf64a8",
	"usr/local/lib/python312.zip":    "74130c400ba5b818bf58bfc2f41fc075f4350cbb13e53099c84e9c0494ec5444",
	"usr/local/lib/python3.12/os.py": "e36b0a6d4feefa0ec1f15b84abd87f61da6f5db434586467869ff0e5f5e1dc24",
}

type Result struct {
	Status     string `json:"status"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"duration_ms"`
}
type Executor interface {
	Execute(context.Context, string, []byte) (Result, error)
}
type Runtime struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	stdlib   fstest.MapFS
	gate     chan struct{}
}

func DefaultDirectory() string {
	if dir := os.Getenv("Q4D_PYTHON_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return "/opt/q4d/python-wasi"
}

func New(ctx context.Context, directory string) (*Runtime, error) {
	files := map[string][]byte{}
	for name, checksum := range runtimeFiles {
		body, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			return nil, fmt.Errorf("python runtime asset %s: %w", name, err)
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != checksum {
			return nil, fmt.Errorf("python runtime asset checksum mismatch: %s", name)
		}
		files[name] = body
	}
	r := &Runtime{runtime: wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(MemoryMB*16).WithCloseOnContextDone(true)), stdlib: fstest.MapFS{}, gate: make(chan struct{}, 1)}
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r.runtime); err != nil {
		r.Close()
		return nil, err
	}
	var err error
	r.compiled, err = r.runtime.CompileModule(ctx, files["bin/python-3.12.0.wasm"])
	if err != nil {
		r.Close()
		return nil, err
	}
	for _, name := range []string{"usr/local/lib/python312.zip", "usr/local/lib/python3.12/os.py"} {
		r.stdlib[name] = &fstest.MapFile{Data: files[name], Mode: 0444}
	}
	return r, nil
}

func (r *Runtime) Close() error { return r.runtime.Close(context.Background()) }

type quotaWriter struct {
	buffer   bytes.Buffer
	limit    int
	exceeded *atomic.Bool
	cancel   context.CancelFunc
}

func (w *quotaWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.buffer.Len() {
		w.exceeded.Store(true)
		w.cancel()
		return 0, io.ErrShortWrite
	}
	return w.buffer.Write(p)
}

func (r *Runtime) Execute(ctx context.Context, source string, input []byte) (Result, error) {
	if len(source) == 0 || len(source) > MaxCodeBytes || len(input) > MaxInputBytes {
		return Result{}, errors.New("python input limit")
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	start := time.Now()
	var exceeded atomic.Bool
	out := &quotaWriter{limit: MaxOutputBytes, exceeded: &exceeded, cancel: cancel}
	errout := &quotaWriter{limit: MaxStderrBytes, exceeded: &exceeded, cancel: cancel}
	fs := fstest.MapFS{}
	for name, file := range r.stdlib {
		fs[name] = file
	}
	fs["data/input.json"] = &fstest.MapFile{Data: input, Mode: 0444}
	// WithFSMount exposes io/fs (read-only), not an os directory. CPython -I
	// ignores PYTHON* settings and user-site imports; no host env is inherited.
	_, err := r.runtime.InstantiateModule(ctx, r.compiled, wazero.NewModuleConfig().WithName("").
		WithSysWalltime().WithSysNanotime().
		WithArgs("python", "-I", "-B", "-c", source).WithStdout(out).WithStderr(errout).
		WithFSConfig(wazero.NewFSConfig().WithFSMount(fs, "/")))
	result := Result{Status: "succeeded", Stdout: out.buffer.String(), Stderr: errout.buffer.String(), DurationMS: time.Since(start).Milliseconds()}
	var exit *sys.ExitError
	switch {
	case exceeded.Load():
		result.Status = "output_limit"
	case ctx.Err() != nil:
		result.Status = "timed_out"
	case errors.As(err, &exit) && exit.ExitCode() != 0:
		result.Status = "python_error"
	case err != nil:
		result.Status = "runtime_error"
	}
	return result, nil
}
