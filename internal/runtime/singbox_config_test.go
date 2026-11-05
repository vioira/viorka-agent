package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func singboxFixture(t *testing.T) (*Managed, string, v1.EngineCommand) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := NewManaged()
	m.root = root
	m.units = filepath.Join(root, "units")
	dir := filepath.Join(root, "sing-box")
	for _, p := range []string{dir, m.units} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	m.run = func(context.Context, string, ...string) error { return nil }
	c := renderCommand(t, "sing-box")
	legacy := strings.Replace(string(m.unit(c.Name, dir)), "run -C "+filepath.Join(dir, "conf.d"), "run -c "+filepath.Join(dir, "config.json"), 1)
	if err := os.WriteFile(filepath.Join(m.units, "viorka-engine-sing-box.service"), []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	data, err := Render(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return m, dir, c
}

func TestSingboxFiles(t *testing.T) {
	c := renderCommand(t, "sing-box")
	second := *c.Services[0]
	second.ID = "reality-2"
	second.ListenPort++
	c.Services = append(c.Services, &second)
	files, err := renderSingboxFiles(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatal(files)
	}
	for name, data := range files {
		if !json.Valid(data) {
			t.Fatal(name)
		}
	}
	var egress struct {
		Route struct {
			Final string `json:"final"`
			Rules []struct {
				Inbound []string `json:"inbound"`
			} `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(files["00-egress.json"], &egress); err != nil {
		t.Fatal(err)
	}
	if egress.Route.Final != "sb-egress-direct" || len(egress.Route.Rules) != 1 || len(egress.Route.Rules[0].Inbound) != 2 {
		t.Fatal(egress)
	}
	if strings.Contains(string(files["01-service.json"]), "reality-2") {
		t.Fatal("mixed services")
	}
	c.Services[1].Enabled = false
	files, err = renderSingboxFiles(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["01-reality-2.json"]; ok {
		t.Fatal("disabled service retained")
	}
	c.Services[0].ID = "../../escape"
	if _, err = renderSingboxFiles(c); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestSingboxMigrationAndRollback(t *testing.T) {
	m, dir, c := singboxFixture(t)
	unitPath := filepath.Join(m.units, "viorka-engine-sing-box.service")
	oldUnit, _ := os.ReadFile(unitPath)
	checked := false
	m.run = func(_ context.Context, path string, args ...string) error {
		if path != systemctl() {
			if len(args) != 3 || args[0] != "check" || args[1] != "-C" {
				t.Fatal(args)
			}
			entries, err := os.ReadDir(args[2])
			if err != nil || len(entries) != 3 {
				t.Fatalf("stage %v %v", entries, err)
			}
			checked = true
		}
		return nil
	}
	rollback, cleanup, code := m.prepareConfig(context.Background(), dir, c)
	if code != "" {
		t.Fatal(code)
	}
	if !checked {
		t.Fatal("not validated")
	}
	if err := inspectSingbox(dir, c); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(unitPath)
	if !strings.Contains(string(unit), "run -C "+filepath.Join(dir, "conf.d")) {
		t.Fatal(string(unit))
	}
	if err := rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	cleanup()
	unit, _ = os.ReadFile(unitPath)
	if string(unit) != string(oldUnit) {
		t.Fatal("unit not restored")
	}
	if activeConfig(dir, c.Name) != filepath.Join(dir, "config.json") {
		t.Fatal("legacy config not restored")
	}
	entries, _ := filepath.Glob(filepath.Join(dir, ".conf-*"))
	if len(entries) != 0 {
		t.Fatal(entries)
	}
}

func TestSingboxReplacementValidationAndInspection(t *testing.T) {
	m, dir, c := singboxFixture(t)
	_, cleanup, code := m.prepareConfig(context.Background(), dir, c)
	if code != "" {
		t.Fatal(code)
	}
	cleanup()
	original, _ := singboxGeneration(dir)
	originalCommand := c
	c.Services = nil
	m.run = func(_ context.Context, path string, _ ...string) error {
		if path != systemctl() {
			return errors.New("invalid config")
		}
		return nil
	}
	_, cleanup, code = m.prepareConfig(context.Background(), dir, c)
	if code != "engine_invalid_config" {
		t.Fatal(code)
	}
	cleanup()
	active, _ := singboxGeneration(dir)
	if active != original {
		t.Fatal("validation switched configuration")
	}
	m.run = func(context.Context, string, ...string) error { return nil }
	rollback, cleanup, code := m.prepareConfig(context.Background(), dir, c)
	if code != "" {
		t.Fatal(code)
	}
	if err := inspectSingbox(dir, c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "conf.d", "01-service.json")); !os.IsNotExist(err) {
		t.Fatal("deleted service retained")
	}
	if err := rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if err := inspectSingbox(dir, originalCommand); err != nil {
		t.Fatal("original configuration not restored", err)
	}
	// Unexpected files and links must not be silently accepted by inspection.
	if err := os.Symlink(filepath.Join(dir, "config.json"), filepath.Join(dir, "conf.d", "injected.json")); err != nil {
		t.Fatal(err)
	}
	if err := inspectSingbox(dir, c); err == nil {
		t.Fatal("extra file accepted")
	}
}

func TestSingboxRejectsUnmanagedDirectoryAndEscapingLink(t *testing.T) {
	for _, kind := range []string{"directory", "link", "dangling"} {
		t.Run(kind, func(t *testing.T) {
			m, dir, c := singboxFixture(t)
			path := filepath.Join(dir, "conf.d")
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "link":
				err = os.Symlink("../outside", path)
			case "dangling":
				err = os.Symlink(".conf-missing", path)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, _, code := m.prepareConfig(context.Background(), dir, c)
			if code != "engine_io_failed" {
				t.Fatal(code)
			}
			if _, err := os.Lstat(filepath.Join(dir, ".conf-missing")); !os.IsNotExist(err) {
				t.Fatal("inspection created directory")
			}
		})
	}
}

func TestSingboxFailedApplyRestoresLegacy(t *testing.T) {
	m, dir, c := singboxFixture(t)
	c.Services = nil
	c.RunIntent = "stopped"
	old := managedState{EngineID: "engine", Command: c, Installed: true}
	if err := m.save(c.Name, old); err != nil {
		t.Fatal(err)
	}
	c.Revision++
	stops := 0
	m.run = func(_ context.Context, path string, args ...string) error {
		if path == systemctl() && args[0] == "stop" {
			stops++
			if stops == 1 {
				return errors.New("stop failure")
			}
		}
		return nil
	}
	r, err := m.apply(context.Background(), "engine", c, old, v1.EngineResult{Status: "stopped"})
	if err != nil || r.ErrorCode != "engine_start_failed" {
		t.Fatalf("%+v %v", r, err)
	}
	if activeConfig(dir, c.Name) != filepath.Join(dir, "config.json") {
		t.Fatal("legacy config not restored")
	}
	state, err := m.state(c.Name)
	if err != nil || state.Command.Revision != old.Command.Revision {
		t.Fatal("revision advanced on failure")
	}
}

func TestSingboxSuccessfulReplacementCleansOldGeneration(t *testing.T) {
	m, dir, c := singboxFixture(t)
	_, cleanup, code := m.prepareConfig(context.Background(), dir, c)
	if code != "" {
		t.Fatal(code)
	}
	cleanup()
	previous, _ := singboxGeneration(dir)
	c.Services = nil
	_, cleanup, code = m.prepareConfig(context.Background(), dir, c)
	if code != "" {
		t.Fatal(code)
	}
	cleanup()
	if _, err := os.Lstat(filepath.Join(dir, previous)); !os.IsNotExist(err) {
		t.Fatal("old generation retained")
	}
	if err := inspectSingbox(dir, c); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "conf.d", "00-base.json")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := inspectSingbox(dir, c); err == nil {
		t.Fatal("public config accepted")
	}
}

func TestSingboxReloadRollbackFailureReported(t *testing.T) {
	m, _, c := singboxFixture(t)
	c.Services = nil
	c.RunIntent = "stopped"
	old := managedState{EngineID: "engine", Command: c, Installed: true}
	m.run = func(_ context.Context, path string, args ...string) error {
		if path == systemctl() && args[0] == "daemon-reload" {
			return errors.New("reload failed")
		}
		return nil
	}
	result, err := m.apply(context.Background(), "engine", c, old, v1.EngineResult{Status: "stopped"})
	if err != nil || result.ErrorCode != "engine_rollback_failed" || result.Status != "degraded" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestSingboxStateWriteFailureRetainsPreviousGeneration(t *testing.T) {
	m, dir, c := singboxFixture(t)
	c.Services = nil
	c.RunIntent = "stopped"
	_, cleanup, code := m.prepareConfig(context.Background(), dir, c)
	if code != "" {
		t.Fatal(code)
	}
	cleanup()
	previous, _ := singboxGeneration(dir)
	old := managedState{EngineID: "engine", Command: c, Installed: true}
	// A non-regular state destination must reject persistence after deployment.
	if err := os.Mkdir(filepath.Join(dir, "state.json"), 0700); err != nil {
		t.Fatal(err)
	}
	c.Revision++
	result, err := m.apply(context.Background(), "engine", c, old, v1.EngineResult{Status: "stopped"})
	if err != nil || result.ErrorCode != "engine_io_failed" || result.Status != "unknown" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, previous)); err != nil {
		t.Fatal("recovery configuration removed", err)
	}
}

func TestSingboxSharedOutboundsAndOrderedRules(t *testing.T) {
	c := renderCommand(t, "sing-box")
	c.Outbounds = []*v1.EngineOutbound{{ID: "proxy", Name: "Proxy", Type: "socks", Server: "127.0.0.1", ServerPort: 1080, Username: "user", Password: "secret"}}
	c.Routing = &v1.EngineRouting{OutboundIds: []string{"proxy"}, Final: "reject", Sniff: true, Rules: []*v1.EngineRouteRule{
		{ID: "one", Name: "Domain and network", Enabled: true, DomainSuffix: []string{"example.com"}, Network: []string{"tcp"}, Outbound: "proxy"},
		{ID: "disabled", Name: "Disabled", Enabled: false, MatchAll: true, Outbound: "direct"},
		{ID: "two", Name: "Direct", Enabled: true, Port: []uint64{443}, Outbound: "direct"},
	}}
	files, err := renderSingboxFiles(c)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Outbounds []struct{ Tag, Type, Password string }
		Route     struct {
			Final string
			Rules []struct {
				Type, Mode, Action, Outbound string
				Rules                        []json.RawMessage
			}
		}
	}
	if err = json.Unmarshal(files["00-egress.json"], &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Outbounds) != 2 || doc.Outbounds[1].Tag != "egress-proxy" || doc.Outbounds[1].Password != "secret" {
		t.Fatal("outbound rendering failed")
	}
	if len(doc.Route.Rules) != 4 || doc.Route.Rules[0].Action != "sniff" || doc.Route.Rules[1].Type != "logical" || doc.Route.Rules[1].Mode != "and" || len(doc.Route.Rules[1].Rules) != 2 || doc.Route.Rules[1].Outbound != "egress-proxy" || doc.Route.Rules[2].Outbound != "sb-egress-direct" || doc.Route.Rules[3].Action != "reject" {
		t.Fatal("ordered routing semantics failed")
	}
}
