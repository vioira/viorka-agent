package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
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
			m.readSupervisor = func(_ context.Context, w io.Writer, path string, args ...string) error {
				if path != systemctl() || args[0] != "show" {
					t.Fatal("unexpected supervisor command", path, args)
				}
				_, err := fmt.Fprintf(w, "ActiveState=inactive\nFragmentPath=%s\nDropInPaths=\nNeedDaemonReload=no\n", m.unitPath(name))
				return err
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
			if name == "realm" {
				c.Services = []*v1.EngineService{{ID: "svc", Name: "svc", Enabled: true, ListenHost: "127.0.0.1", ListenPort: 5443, TargetHost: "example.org", TargetPort: 443}}
				data, err := Render(c)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, configName(name)), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
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
			backup := v1.EngineEndpointBackup{EngineID: "original-engine", Name: name, CommandSHA256: v1.EndpointCommandDigest(c), Endpoints: []*v1.EnginePublicEndpoint{}}
			if name == "realm" {
				backup.Endpoints = append(backup.Endpoints, &v1.EnginePublicEndpoint{ServiceID: "svc", Host: "entry.example.org", Port: 15443})
			}
			if err := m.StoreEndpointBackup(context.Background(), backup); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "published-endpoints.json")
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("backup permissions", err)
			}
			snapshots, err := m.RecoverySnapshots(context.Background())
			if err != nil || len(snapshots) != 1 {
				t.Fatal("snapshot", err)
			}
			if snapshots[0].EngineID != "original-engine" || snapshots[0].Command.Revision != 7 || snapshots[0].Status != "stopped" {
				t.Fatal("changed identity/revision")
			}
			stored := snapshots[0].EndpointBackup
			if name == "realm" && (stored == nil || !stored.Matches("original-engine", c)) || name == "sing-box" && (!snapshots[0].Unconfigured || stored != nil) {
				t.Fatal("missing backup on registration")
			}
			preview, err := m.Execute(context.Background(), "replacement-node", v1.EngineRecoverOperation, v1.EngineCommand{
				Name: name, Version: v1.EngineVersion(name), RunIntent: "running", Services: []*v1.EngineService{},
			})
			if err != nil || preview.ErrorCode != "" || preview.Recovery == nil || preview.Recovery.EngineID != "original-engine" {
				t.Fatalf("online preview lost local owner: %+v %v", preview, err)
			}
			if name == "realm" {
				if len(stored.Endpoints) != 1 || stored.Endpoints[0].Port != 15443 {
					t.Fatal("missing public mapping")
				}
				backup.Endpoints = []*v1.EnginePublicEndpoint{}
				if err := m.StoreEndpointBackup(context.Background(), backup); err != nil {
					t.Fatal(err)
				}
				snapshots, err = m.RecoverySnapshots(context.Background())
				if err != nil || snapshots[0].EndpointBackup == nil || len(snapshots[0].EndpointBackup.Endpoints) != 0 {
					t.Fatal("cleared endpoint returned", err)
				}
			}
			after, err := os.ReadFile(filepath.Join(dir, "state.json"))
			if err != nil || string(before) != string(after) {
				t.Fatal("state mutated", err)
			}
			m.readSupervisor = func(_ context.Context, w io.Writer, _ string, _ ...string) error {
				_, err := fmt.Fprintf(w, "ActiveState=inactive\nFragmentPath=%s\nDropInPaths=/etc/systemd/override.conf\nNeedDaemonReload=no\n", m.unitPath(name))
				return err
			}
			if _, err = m.RecoverySnapshots(context.Background()); err == nil {
				t.Fatal("override accepted")
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
