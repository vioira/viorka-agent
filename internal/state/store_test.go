package state

import (
	"os"
	"path/filepath"
	"testing"
)

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
