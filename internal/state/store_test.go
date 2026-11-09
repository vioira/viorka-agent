package state

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestStateLockNotInheritedByChild(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Keep a real executed child alive after the parent releases its lock.
	cmd := exec.CommandContext(ctx, "sh", "-c", "printf 'ready\\n'; read reply")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		cmd.Wait()
	}()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child readiness: %q, %v", line, err)
	}
	if other, err := Open(dir); err == nil {
		other.Close()
		t.Fatal("parent lock no longer excludes another owner")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := Open(dir)
	if err != nil {
		t.Fatalf("child retained state lock after parent closed it: %v", err)
	}
	defer other.Close()
}

func TestIdentityPermissionsLockAndOrigin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if other, err := Open(dir); err == nil {
		other.Close()
		t.Fatal("two owners accepted")
	}
	id, err := s.Identity("https://panel", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(id.PrivateKey) != 64 {
		t.Fatal("invalid key")
	}
	info, _ := os.Stat(filepath.Join(dir, "identity.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if _, err = s.Identity("https://different", false); err == nil {
		t.Fatal("identity origin changed")
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err = os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(link); err == nil {
		other.Close()
		t.Fatal("symlink accepted")
	}
}
