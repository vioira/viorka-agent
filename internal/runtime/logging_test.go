package runtime

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineCommandLoggingLevelAndSecrets(t *testing.T) {
	var output bytes.Buffer
	var level slog.LevelVar
	logger := slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: &level}))
	m := NewManaged(logger)
	binary := filepath.Join(t.TempDir(), "validator")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho secret-password\necho private-key >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, current := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		level.Set(current)
		output.Reset()
		if err := m.run(context.Background(), binary, "check", "secret-argument"); err == nil {
			t.Fatal("expected failure")
		}
		got := output.String()
		if !strings.Contains(got, "Engine command failed") {
			t.Fatal(got)
		}
		if strings.Contains(got, "Engine command started") != (current == slog.LevelDebug) {
			t.Fatal(got)
		}
		for _, secret := range []string{"secret-password", "private-key", "secret-argument"} {
			if strings.Contains(got, secret) {
				t.Fatalf("secret leaked: %s", got)
			}
		}
	}
}

func TestCommandOutputBound(t *testing.T) {
	var output commandOutput
	input := bytes.Repeat([]byte("x"), 12000)
	if n, err := output.Write(input); n != len(input) || err != nil {
		t.Fatal(n, err)
	}
	if n, err := output.Write(input); n != len(input) || err != nil {
		t.Fatal(n, err)
	}
	if len(output.data) != 8192 || !output.truncated {
		t.Fatal("unbounded output")
	}
}
