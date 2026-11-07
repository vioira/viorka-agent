package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Run only in a disposable Alpine container: this exercises real OpenRC,
// supervise-daemon and runlevel links with a local engine process fixture.
func TestOpenRCNativeLifecycle(t *testing.T) {
	if os.Getenv("VIORKA_DISPOSABLE_SERVICE_TEST") != "yes" {
		t.Skip("requires disposable root OpenRC environment")
	}
	// A container does not run OpenRC boot initialization. Check its lock
	// directory explicitly: OpenRC reports ENOENT here as "already starting".
	if info, err := os.Stat("/run/openrc/exclusive"); err != nil || !info.IsDir() {
		t.Fatalf("OpenRC test environment requires /run/openrc/exclusive: %v", err)
	}
	m := NewManaged()
	// Keep the production environment, arguments and deadline while exposing
	// fixture command diagnostics in CI. No real engine credentials are used.
	m.run = func(ctx context.Context, path string, args ...string) error {
		var output bytes.Buffer
		err := runControlledOutput(ctx, &output, path, args...)
		t.Logf("command: %s %q; error: %v\n%s", path, args, err, output.String())
		return err
	}
	if !m.openrc || !Supported() {
		t.Fatal("OpenRC capability unavailable")
	}
	if _, err := os.Lstat(m.unitPath("sing-box")); !os.IsNotExist(err) {
		t.Fatal("test service already exists", err)
	}
	m.root = t.TempDir()
	if err := os.Chmod(m.root, 0700); err != nil {
		t.Fatal(err)
	}
	m.detectExternal = func(string) bool { return false }
	pidPath := filepath.Join(m.root, "engine.pid")
	m.download = func(_ context.Context, _ v1.EngineRelease, dir string) (string, error) {
		return stageBinaryFixture(dir, []byte("#!/bin/sh\n[ \"$1\" = check ] && exit 0\necho $$ > "+pidPath+"\nexec /bin/sleep 600\n"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		_ = m.ctl(cleanup, "sing-box", "stop")
		_ = m.ctl(cleanup, "sing-box", "disable")
		_ = os.Remove(m.unitPath("sing-box"))
	})
	c := v1.EngineCommand{Name: "sing-box", Version: v1.EngineVersion("sing-box"), Revision: 1, RunIntent: "stopped"}
	result, err := m.Execute(ctx, "native-openrc", "engine.install", c)
	if err != nil || result.ErrorCode != "" || result.Status != "installed" {
		t.Fatalf("install: %+v %v", result, err)
	}
	c.Revision++
	result, err = m.Execute(ctx, "native-openrc", "engine.apply", c)
	if err != nil || result.ErrorCode != "" || result.Status != "stopped" {
		t.Fatalf("stopped apply: %+v %v", result, err)
	}
	for _, op := range []string{"enable", "start"} {
		if err := m.ctl(ctx, c.Name, op); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
	}
	if err := m.healthy(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(m.runlevels, "default", "viorka-engine-sing-box")); err != nil {
		t.Fatal("missing boot membership", err)
	}
	readPID := func() int {
		b, _ := os.ReadFile(pidPath)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return pid
	}
	deadline := time.Now().Add(5 * time.Second)
	for readPID() == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	pid := readPID()
	if pid <= 1 {
		t.Fatal("missing process pid")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for readPID() == pid && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if next := readPID(); next <= 1 || next == pid {
		t.Fatal("supervise-daemon did not respawn engine")
	}
	if err := m.healthy(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := m.ctl(ctx, c.Name, "restart"); err != nil {
		t.Fatal("restart:", err)
	}
	if err := m.healthy(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.Revision++
	result, err = m.Execute(ctx, "native-openrc", "engine.uninstall", c)
	if err != nil || result.ErrorCode != "" || result.Status != "not_installed" {
		t.Fatalf("uninstall: %+v %v", result, err)
	}
	for _, path := range []string{m.unitPath(c.Name), filepath.Join(m.runlevels, "default", "viorka-engine-sing-box")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("service or boot link retained", path, err)
		}
	}
}
