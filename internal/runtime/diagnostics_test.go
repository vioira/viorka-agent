package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestDownloadDiagnosticAndCheckpoint(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "stage")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stage, err := os.ReadFile(journal)
		if err != nil || string(stage) != "download" {
			t.Errorf("stage not durable before request: %s %v", stage, err)
		}
		w.WriteHeader(403)
		w.Write([]byte("secret-token"))
	}))
	defer server.Close()
	ctx, diagnostics := TrackDiagnostics(context.Background(), func(stage string) error {
		return atomicEngineFile(journal, []byte(stage), 0600)
	})
	_, err := Download(ctx, v1.EngineRelease{Name: "sing-box", URL: server.URL + "/?token=secret-token"}, dir)
	if err == nil {
		t.Fatal("expected failure")
	}
	d := diagnostics.Result("engine_download_failed")
	if d.Stage != "download" || d.HttpStatus == nil || *d.HttpStatus != 403 || strings.Contains(d.Message, "secret") || !v1.ValidEngineDiagnostic(d) {
		t.Fatalf("%+v", d)
	}
}

func TestCheckpointFailurePreventsNetworkEffects(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	ctx, d := TrackDiagnostics(context.Background(), func(string) error { return syscall.ENOSPC })
	if _, err := Download(ctx, v1.EngineRelease{URL: server.URL}, t.TempDir()); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	if called || !strings.Contains(d.Result("engine_io_failed").Message, "磁盘") {
		t.Fatal("effect occurred before persistence")
	}
}

func TestSafeCommandDiagnostic(t *testing.T) {
	ctx, d := TrackDiagnostics(context.Background(), func(string) error { return nil })
	if err := checkpoint(ctx, "validate"); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "validator")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho private-key >&2\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := NewManaged().run(ctx, binary, "secret"); err == nil {
		t.Fatal("expected failure")
	}
	result := d.Result("engine_invalid_config")
	if result.Stage != "validate" || result.ExitCode == nil || *result.ExitCode != 7 || strings.Contains(result.Message, "private-key") {
		t.Fatalf("%+v", result)
	}
}

func TestDiagnosticSafeErrorCategories(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&os.PathError{Op: "write", Path: "secret-path", Err: syscall.ENOSPC}, "磁盘"},
		{os.ErrPermission, "权限"},
		{context.DeadlineExceeded, "截止"},
		{errors.New("secret-token"), "节点运行日志"},
	} {
		if got := safeFailure(tc.err); !strings.Contains(got, tc.want) || strings.Contains(got, "secret") {
			t.Fatal(got)
		}
	}
}
