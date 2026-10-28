package sidecar

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnexpectedRuntimeExitReportsOOM(t *testing.T) {
	// Delay consumption until after the fixture exits. Cmd.Wait must still
	// preserve the OOM line at the end of stderr.
	oldPipe := sidecarStderrPipe
	sidecarStderrPipe = func(cmd *exec.Cmd) (io.ReadCloser, error) {
		reader, err := oldPipe(cmd)
		if err != nil {
			return nil, err
		}
		return &delayedDiagnosticReader{ReadCloser: reader}, nil
	}
	t.Cleanup(func() { sidecarStderrPipe = oldPipe })
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(old) })
	trigger := filepath.Join(t.TempDir(), "exit")
	venv := fakeVenv(t, `#!/bin/sh
cat >/dev/null
echo 'Websocket endpoint: ws://127.0.0.1:4321/token'
while test ! -f "$GOMOUFOX_EXIT_TRIGGER"; do sleep 0.01; done
printf '%10000s\n' startup-noise >&2
echo 'FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory' >&2
echo 'sensitive-page-content' >&2
exit 23
`)
	manager := New(Config{VenvDir: venv, Runtime: RuntimePython, ConnectTimeout: time.Second, ExtraEnv: []string{"GOMOUFOX_EXIT_TRIGGER=" + trigger}})
	if _, err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Stop(context.Background()) })
	if err := os.WriteFile(trigger, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-manager.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not exit")
	}
	if output := logs.String(); !strings.Contains(output, "reason=out_of_memory") || !strings.Contains(output, "exit status 23") {
		t.Fatalf("missing actionable runtime exit: %q", output)
	}
	if strings.Contains(logs.String(), "sensitive-page-content") {
		t.Fatal("runtime exit report exposed raw page diagnostics")
	}
}

type delayedDiagnosticReader struct {
	io.ReadCloser
	delayed bool
}

func (r *delayedDiagnosticReader) Read(p []byte) (int, error) {
	if !r.delayed {
		r.delayed = true
		time.Sleep(100 * time.Millisecond)
	}
	return r.ReadCloser.Read(p)
}
