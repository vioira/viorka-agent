package state

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type Identity struct {
	PanelURL   string `json:"panel_url"`
	NodeID     string `json:"node_id"`
	PrivateKey []byte `json:"private_key"`
}
type Store struct {
	dir  string
	lock *os.File
}

func Open(dir string) (*Store, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	if leaf, e := os.Lstat(absolute); e != nil || leaf.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("state directory must not be a symlink")
	}
	absolute = resolved
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("state directory must have mode 0700")
	}
	fd, err := syscall.Open(filepath.Join(absolute, "agent.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "agent.lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state directory already in use: %w", err)
	}
	return &Store{absolute, f}, nil
}
func (s *Store) Close() error { return s.lock.Close() }
func (s *Store) Read(name string, v any) error {
	path := filepath.Join(s.dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4*1024*1024 {
		return fmt.Errorf("unsafe state file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 4*1024*1024+1))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return err
	}
	var rest any
	if d.Decode(&rest) != io.EOF {
		return fmt.Errorf("trailing state content")
	}
	return nil
}
func (s *Store) Write(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > 4*1024*1024 {
		return fmt.Errorf("state capacity exceeded")
	}
	f, err := os.CreateTemp(s.dir, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(s.dir, name)); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *Store) Identity(origin string, create bool) (Identity, error) {
	var v Identity
	err := s.Read("identity.json", &v)
	if errors.Is(err, os.ErrNotExist) && create {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return v, e
		}
		v = Identity{PanelURL: origin, PrivateKey: key}
		return v, s.Write("identity.json", v)
	}
	if err != nil {
		return v, err
	}
	if v.PanelURL != origin || len(v.PrivateKey) != ed25519.PrivateKeySize {
		return v, fmt.Errorf("identity does not match Panel or has invalid key")
	}
	return v, nil
}
