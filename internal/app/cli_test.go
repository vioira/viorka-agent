package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestUpdateCheckInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"update-check", "extra"}, {"update-check", "--config", "/missing/update-config"}, {"update-check", "--unknown"}} {
		var out, errOut bytes.Buffer
		if code := Main(args, &out, &errOut); code != 2 {
			t.Fatalf("%v: code=%d", args, code)
		}
	}
}
