package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestOpenRCDetection(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "run/openrc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sbin"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openrc-run", "rc-service", "rc-update", "supervise-daemon"} {
		if serviceManager(root) != "" {
			t.Fatal("incomplete OpenRC advertised")
		}
		if err := os.WriteFile(filepath.Join(root, "sbin", name), []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if serviceManager(root) != "openrc" {
		t.Fatal("OpenRC not detected")
	}
}

func TestOpenRCControl(t *testing.T) {
	m := &Managed{openrc: true, runlevels: t.TempDir()}
	var calls [][]string
	m.run = func(_ context.Context, path string, args ...string) error {
		calls = append(calls, append([]string{path}, args...))
		return nil
	}
	for _, level := range []string{"default", "boot"} {
		dir := filepath.Join(m.runlevels, level)
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/etc/init.d/viorka-engine-sing-box", filepath.Join(dir, "viorka-engine-sing-box")); err != nil {
			t.Fatal(err)
		}
	}
	for _, op := range []string{"daemon-reload", "enable", "start", "restart", "is-active", "stop", "disable"} {
		if err := m.ctl(context.Background(), "sing-box", op); err != nil {
			t.Fatal(err)
		}
	}
	want := [][]string{
		{"/sbin/rc-update", "add", "viorka-engine-sing-box", "default"},
		{"/sbin/rc-service", "viorka-engine-sing-box", "start"},
		{"/sbin/rc-service", "viorka-engine-sing-box", "restart"},
		{"/sbin/rc-service", "viorka-engine-sing-box", "status"},
		{"/sbin/rc-service", "--ifstarted", "viorka-engine-sing-box", "stop"},
		{"/sbin/rc-update", "del", "viorka-engine-sing-box", "boot"},
		{"/sbin/rc-update", "del", "viorka-engine-sing-box", "default"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls: %v", calls)
	}
	failure := errors.New("permission denied")
	m.run = func(context.Context, string, ...string) error { return failure }
	if err := m.ctl(context.Background(), "sing-box", "disable"); !errors.Is(err, failure) {
		t.Fatalf("lost rc-update failure: %v", err)
	}
	m.runlevels = t.TempDir()
	if err := m.ctl(context.Background(), "sing-box", "disable"); err != nil {
		t.Fatal("disabling absent service:", err)
	}
}

func TestOpenRCInstallAndConfigRollback(t *testing.T) {
	m, dir, c := singboxFixture(t)
	m.openrc = true
	if err := os.Chmod(m.root, 0700); err != nil {
		t.Fatal(err)
	}
	m.supported = func() bool { return true }
	m.detectExternal = func(string) bool { return false }
	m.download = func(context.Context, v1.EngineRelease) ([]byte, error) { return []byte("fixture"), nil }
	m.runlevels = t.TempDir()
	c.Services = nil
	c.Revision = 1
	r, err := m.Execute(context.Background(), "engine", "engine.install", c)
	if err != nil || r.ErrorCode != "" || r.Status != "installed" {
		t.Fatalf("install: %+v %v", r, err)
	}
	path := m.unitPath(c.Name)
	before, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(before), openrcHeader) {
		t.Fatalf("script: %s %v", before, err)
	}
	for _, name := range []string{"sing-box", "xray", "realm"} {
		script := string(m.unit(name, filepath.Join(m.root, name)))
		if !strings.Contains(script, "supervisor=supervise-daemon") || !strings.Contains(script, "respawn_max=3") {
			t.Fatal(script)
		}
	}
	rollback, cleanup, code := m.prepareConfig(context.Background(), dir, c)
	if cleanup != nil {
		defer cleanup()
	}
	if code != "" {
		t.Fatal(code)
	}
	if err := rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("script not restored", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("lost executable mode", err)
	}
	// Validation failure must preserve the installed script and config.
	m.run = func(context.Context, string, ...string) error { return errors.New("invalid config") }
	_, _, code = m.prepareConfig(context.Background(), dir, c)
	if code != "engine_invalid_config" {
		t.Fatal(code)
	}
	m.run = func(context.Context, string, ...string) error { return nil }
	c.Revision++
	r, err = m.Execute(context.Background(), "engine", "engine.uninstall", c)
	if err != nil || r.ErrorCode != "" || r.Status != "not_installed" {
		t.Fatalf("uninstall: %+v %v", r, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("service script retained", err)
	}
}

func TestOpenRCRejectForeignService(t *testing.T) {
	m, _, c := singboxFixture(t)
	c.Services = nil
	m.openrc = true
	if err := os.Chmod(m.root, 0700); err != nil {
		t.Fatal(err)
	}
	m.supported = func() bool { return true }
	if err := os.WriteFile(m.unitPath(c.Name), []byte("#!/sbin/openrc-run\ncommand=/usr/bin/sing-box\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r, err := m.Execute(context.Background(), "engine", "engine.install", c)
	if err != nil || r.ErrorCode != "engine_external" {
		t.Fatalf("foreign service: %+v %v", r, err)
	}
}
