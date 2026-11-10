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
	m.download = func(_ context.Context, _ v1.EngineRelease, dir string) (string, error) {
		return stageBinaryFixture(dir, []byte("test executable fixture"))
	}
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
	snapshots, err := m.RecoverySnapshots(ctx)
	if err != nil || len(snapshots) != 1 || snapshots[0].EngineID != "engine" || snapshots[0].Command.Services[0].Name != "edited" || snapshots[0].Command.Revision != 4 || snapshots[0].Command.RunIntent != "stopped" || starts != 1 {
		t.Fatal("recovery lost applied config or restarted engine", err)
	}
	// A second identity cannot take over the same managed directory.
	c.Revision = 5
	result, err = m.Execute(ctx, "other", "engine.apply", c)
	if err != nil || result.ErrorCode != "engine_external" {
		t.Fatalf("takeover %+v %v", result, err)
	}

	// Uninstall a running engine with applied services, without an empty apply first.
	start := c
	start.Services = nil
	start.RunIntent = "running"
	result, err = m.Execute(ctx, "engine", "engine.start", start)
	if err != nil || result.ErrorCode != "" || listener == nil || !boot {
		t.Fatalf("restart before uninstall: %+v %v", result, err)
	}
	start.Revision++
	result, err = m.Execute(ctx, "engine", "engine.uninstall", start)
	if err != nil || result.ErrorCode != "" || result.Status != "not_installed" || listener != nil || boot {
		t.Fatalf("uninstall with services: %+v %v", result, err)
	}
	for _, path := range []string{m.unitPath(c.Name), filepath.Join(m.root, c.Name, c.Name)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("uninstall retained %s: %v", path, err)
		}
	}
	actual, err = m.state(c.Name)
	if err != nil || actual.Installed || len(actual.Command.Services) != 0 {
		t.Fatalf("uninstalled state: %+v %v", actual, err)
	}

}

func TestManagedInspectNotInstalled(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(strconv.FormatBool(saved), func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			m := NewManaged()
			m.root = filepath.Join(root, "engines")
			m.units = filepath.Join(root, "units")
			m.supported = func() bool { return true }
			m.run = func(context.Context, string, ...string) error {
				t.Fatal("inspect of absent engine must not invoke supervisor")
				return nil
			}
			c := v1.EngineCommand{Name: "sing-box", Version: v1.EngineVersion("sing-box"), Revision: 2, RunIntent: "stopped"}
			if saved {
				if err := privateDirectory(filepath.Join(m.root, c.Name)); err != nil {
					t.Fatal(err)
				}
				previous := c
				previous.Revision = 1
				if err := m.save(context.Background(), c.Name, managedState{"engine", previous, false}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := m.Execute(context.Background(), "engine", "engine.inspect", c)
			if err != nil || result.ErrorCode != "" || result.Status != "not_installed" {
				t.Fatalf("inspect: %+v %v", result, err)
			}
			// Inspect must not turn an absent engine into an installed one.
			actual, err := m.state(c.Name)
			if saved {
				if err != nil || actual.Installed || actual.Command.Revision != 1 {
					t.Fatalf("state changed: %+v %v", actual, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("unexpected state: %+v %v", actual, err)
			}
			result, err = m.Execute(context.Background(), "engine", "engine.start", c)
			if err != nil || result.ErrorCode != "engine_unsupported" {
				t.Fatalf("start without installation: %+v %v", result, err)
			}
		})
	}
}
