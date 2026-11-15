package connection

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestLogLevelDynamicDurability(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c, err := New(config.Local{PanelURL: "https://panel.test", NodeName: "test", StateDir: t.TempDir()}, store)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var output bytes.Buffer
	c.SetLogOutput(&output)
	emit := func() string {
		output.Reset()
		c.logger.Debug("debug-line")
		c.logger.Info("info-line")
		c.logger.Warn("warn-line")
		c.logger.Error("error-line")
		return output.String()
	}
	if got := emit(); strings.Contains(got, "info-line") || !strings.Contains(got, "warn-line") {
		t.Fatal(got)
	}
	id := state.Identity{PanelURL: "https://panel.test", NodeID: "node"}
	settings, err := loadTelemetrySettings(store, id)
	if err != nil {
		t.Fatal(err)
	}
	settings.setLogLevel = c.applyLogLevel
	next := v1.DefaultTelemetrySettings()
	for i, value := range []string{"debug", "info", "error", "warn"} {
		next.Revision = uint64(i + 1)
		next.LogLevel = value
		if r := settings.apply(next); r.State != "applied" {
			t.Fatal(r)
		}
		got := emit()
		if strings.Contains(got, "debug-line") != (value == "debug") || strings.Contains(got, "info-line") != (value == "debug" || value == "info") || strings.Contains(got, "warn-line") != (value != "error") || !strings.Contains(got, "error-line") {
			t.Fatal(value, got)
		}
	}
	next.Revision++
	next.LogLevel = "debug"
	settings.write = func(string, any) error { return errors.New("disk failed") }
	if r := settings.apply(next); r.ErrorCode != "persist_failed" || c.logLevel.Level() != slog.LevelWarn {
		t.Fatal(r)
	}
	settings.write = store.Write
	bad := next
	bad.LogLevel = "trace"
	if r := settings.apply(bad); r.ErrorCode != "invalid_settings" {
		t.Fatal(r)
	}
	bad = next
	bad.Revision = 1
	if r := settings.apply(bad); r.ErrorCode != "stale_revision" {
		t.Fatal(r)
	}
	bad.Revision = settings.current.Revision
	if r := settings.apply(bad); r.ErrorCode != "revision_conflict" {
		t.Fatal(r)
	}
	if r := settings.apply(next); r.State != "applied" {
		t.Fatal(r)
	}
	restored, err := loadTelemetrySettings(store, id)
	if err != nil {
		t.Fatal(err)
	}
	c.applyLogLevel("warn")
	c.applyLogLevel(restored.current.LogLevel)
	if c.logLevel.Level() != slog.LevelDebug {
		t.Fatal("persisted level not restored")
	}
}

func TestRunFailureVisibleAtErrorLevel(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c, err := New(config.Local{PanelURL: "https://panel.test", NodeName: "test", StateDir: t.TempDir(), LogLevel: "error"}, store)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var output bytes.Buffer
	c.SetLogOutput(&output)
	if err := c.Run(context.Background()); err == nil {
		t.Fatal("unregistered run succeeded")
	}
	got := output.String()
	if !strings.Contains(got, "Agent starting") || !strings.Contains(got, "Agent run failed") || strings.Contains(got, "session established") || strings.Contains(got, "Agent stopped") {
		t.Fatal(got)
	}
	output.Reset()
	c.lifecycle().Info("Panel connected; session established")
	c.logger.Info("routine diagnostics")
	if !strings.Contains(output.String(), "session established") || strings.Contains(output.String(), "routine diagnostics") {
		t.Fatal(output.String())
	}
}
