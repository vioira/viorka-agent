package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vioira/viorka-agent/internal/state"
)

func TestDoctorIsExplicitAboutStage(t *testing.T) {
	var out, errOut bytes.Buffer
	if status := Main([]string{"doctor"}, &out, &errOut); status != 0 {
		t.Fatalf("unexpected status %d: %s", status, errOut.String())
	}
	if !strings.Contains(out.String(), "not-checked") {
		t.Fatal("doctor must not claim live remote connectivity")
	}
}

func TestRunFailsWithoutUsableConfiguration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if status := Main([]string{"run"}, &out, &errOut); status == 0 {
		t.Fatalf("run must fail without usable configuration; got %s", out.String())
	}
}

func TestDefaultConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".viorka-agent")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent.json")
	valid := []byte(`{"panel_url":"https://panel.example.com","node_name":"node","state_dir":"/tmp/state"}`)
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Main([]string{"config-check"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, &errOut)
	}
	// An explicit path must take precedence over the valid default.
	if code := Main([]string{"config-check", "--config", filepath.Join(home, "missing.json")}, &out, &errOut); code != 2 {
		t.Fatalf("code %d", code)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"config-check"}, &out, &errOut); code != 2 {
		t.Fatalf("invalid default: code %d", code)
	}
}

func TestExplicitConfigWithoutHome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(`{"panel_url":"https://panel.example.com","node_name":"node","state_dir":"/tmp/state"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	if _, err := loadConfig(path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(""); err == nil {
		t.Fatal("missing home must fail")
	}
}

func TestConfigCheckRegistrationStatus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	stateDir := filepath.Join(dir, "state")
	const origin = "https://panel.example.com"
	config := fmt.Sprintf(`{"panel_url":%q,"node_name":"node","state_dir":%q}`, origin, stateDir)
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	check := func(wantCode int, wantOutput string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := Main([]string{"config-check", "--config", path, "--registration-status"}, &out, &errOut)
		if code != wantCode || strings.TrimSpace(out.String()) != wantOutput {
			t.Fatalf("status=%d, output=%q, error=%q", code, out.String(), errOut.String())
		}
	}
	check(0, "needs-registration")

	store, err := state.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	check(1, "") // A running Agent holds the state lock.
	id, err := store.Identity(origin, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	check(0, "needs-registration")

	identityPath := filepath.Join(stateDir, "identity.json")
	id.NodeID = "node-1"
	store, err = state.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write("identity.json", id); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	check(0, "registered")
	if err := os.WriteFile(identityPath, []byte(`{"node_id":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	check(1, "")
	if err := os.Rename(identityPath, filepath.Join(stateDir, "old-identity.json")); err != nil {
		t.Fatal(err)
	}
	check(1, "")
}

func TestUpdateCheckInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"update-check", "extra"}, {"update-check", "--config", "/missing/update-config"}, {"update-check", "--unknown"}} {
		var out, errOut bytes.Buffer
		if code := Main(args, &out, &errOut); code != 2 {
			t.Fatalf("%v: code=%d", args, code)
		}
	}
}

func TestBatchRegisterRejectsIncompatibleFlags(t *testing.T) {
	for _, args := range [][]string{{"register", "--replace-node", "node", "--name", "name"}, {"run", "--name", "node"}, {"run", "--token-stdin"}, {"register", "extra"}} {
		var out, errOut bytes.Buffer
		if code := Main(args, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Fatalf("%v: code=%d, output=%s", args, code, out.String())
		}
	}
}

func TestEnrollCommandRemoved(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Main([]string{"enroll"}, &out, &errOut); code != 2 {
		t.Fatalf("removed command returned %d", code)
	}
	if !strings.Contains(errOut.String(), "unknown command: enroll") {
		t.Fatal(errOut.String())
	}
}
