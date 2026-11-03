package runtime

import (
	"context"
	"errors"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestManagedInstallApplyAndManualStop(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	units := filepath.Join(root, "units")
	if err = os.Mkdir(units, 0700); err != nil {
		t.Fatal(err)
	}
	m := NewManaged()
	m.root = filepath.Join(root, "engines")
	m.units = units
	m.supported = func() bool { return true }
	m.detectExternal = func(string) bool { return false }
	m.download = func(context.Context, v1.EngineRelease) ([]byte, error) { return []byte("test executable fixture"), nil }
	var listener net.Listener
	var starts int
	var boot bool
	t.Cleanup(func() {
		if listener != nil {
			listener.Close()
		}
	})
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetPort := uint64(target.Addr().(*net.TCPAddr).Port)
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenPort := uint64(free.Addr().(*net.TCPAddr).Port)
	free.Close()
	m.run = func(_ context.Context, path string, args ...string) error {
		if path != systemctl() {
			return nil
		}
		switch args[0] {
		case "is-active":
			if listener == nil {
				return errors.New("inactive")
			}
		case "enable":
			boot = true
		case "disable":
			boot = false
		case "start", "restart":
			starts++
			if listener != nil {
				listener.Close()
			}
			listener, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.FormatUint(listenPort, 10)))
			return err
		case "stop":
			if listener != nil {
				listener.Close()
				listener = nil
			}
		}
		return nil
	}
	ctx := context.Background()
	c := v1.EngineCommand{Name: "realm", Version: v1.EngineVersion("realm"), Revision: 1, RunIntent: "running", Services: []*v1.EngineService{}}
	result, err := m.Execute(ctx, "engine", "engine.install", c)
	if err != nil || result.Status != "installed" || result.ErrorCode != "" || starts != 0 {
		t.Fatalf("install %+v %v starts=%d", result, err, starts)
	}
	c.Revision = 2
	c.Services = []*v1.EngineService{{ID: "tcp", Name: "TCP", Enabled: true, ListenHost: "127.0.0.1", ListenPort: listenPort, TargetHost: "127.0.0.1", TargetPort: targetPort}}
	result, err = m.Execute(ctx, "engine", "engine.apply", c)
	if err != nil || result.Status != "running" || result.ErrorCode != "" || !boot {
		t.Fatalf("apply %+v %v", result, err)
	}
	stop := c
	stop.Revision = 3
	stop.Services = nil
	stop.RunIntent = "stopped"
	result, err = m.Execute(ctx, "engine", "engine.stop", stop)
	if err != nil || result.Status != "stopped" || boot || listener != nil {
		t.Fatalf("stop %+v %v", result, err)
	}
	c.Revision = 4
	c.RunIntent = "stopped"
	c.Services[0].Name = "edited"
	result, err = m.Execute(ctx, "engine", "engine.apply", c)
	if err != nil || result.ErrorCode != "" || result.Status != "stopped" || starts != 1 || boot {
		t.Fatalf("stopped apply %+v %v starts %d", result, err, starts)
	}
	actual, err := m.state("realm")
	if err != nil || actual.Command.Revision != 4 || actual.Command.RunIntent != "stopped" {
		t.Fatal("state", err)
	}
	// A second identity cannot take over the same managed directory.
	c.Revision = 5
	result, err = m.Execute(ctx, "other", "engine.apply", c)
	if err != nil || result.ErrorCode != "engine_external" {
		t.Fatalf("takeover %+v %v", result, err)
	}
}
