package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestRecoverySnapshotsRetainIdentityAndConfig(t *testing.T) {
	for _, name := range []string{"sing-box", "realm"} {
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			m := NewManaged()
			m.root = filepath.Join(root, "engines")
			m.units = filepath.Join(root, "units")
			m.openrc = false
			m.supported = func() bool { return true }
			m.detectExternal = func(string) bool { return false }
			if err = os.MkdirAll(m.units, 0700); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(m.root, name)
			if err = os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			m.run = func(_ context.Context, path string, args ...string) error {
				if path == systemctl() {
					if args[0] != "is-active" {
						t.Fatalf("recovery changed service: %s", args[0])
					}
					return errors.New("stopped")
				}
				return nil
			}
			c := v1.EngineCommand{Name: name, Version: v1.EngineVersion(name), Revision: 7, RunIntent: "stopped", Services: []*v1.EngineService{}}
			if err = os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(m.unitPath(name), m.unit(name, dir), m.unitMode()); err != nil {
				t.Fatal(err)
			}
			if err = m.save(context.Background(), name, managedState{"original-engine", c, true}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			snapshots, err := m.RecoverySnapshots(context.Background())
			if err != nil || len(snapshots) != 1 {
				t.Fatal("snapshot", err)
			}
			if snapshots[0].EngineID != "original-engine" || snapshots[0].Command.Revision != 7 || snapshots[0].Status != "stopped" {
				t.Fatal("changed identity/revision")
			}
			after, err := os.ReadFile(filepath.Join(dir, "state.json"))
			if err != nil || string(before) != string(after) {
				t.Fatal("state mutated", err)
			}
			if err = os.WriteFile(m.unitPath(name), []byte("third party"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = m.RecoverySnapshots(context.Background()); err == nil {
				t.Fatal("foreign service accepted")
			}
		})
	}
}
