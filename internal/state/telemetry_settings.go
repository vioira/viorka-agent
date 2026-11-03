package state

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// ReadTelemetrySettings reads only the fixed v3 settings file. It does not
// relax legacy decoding or permit remotely supplied state paths.
func (s *Store) ReadTelemetrySettings(value any) error {
	directory, err := os.Lstat(s.dir)
	if err != nil {
		return err
	}
	if !directory.IsDir() || directory.Mode().Perm() != 0700 {
		return fmt.Errorf("unsafe telemetry settings directory")
	}
	fd, err := syscall.Open(filepath.Join(s.dir, "node-telemetry-settings.json"), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "node-telemetry-settings.json")
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		return fmt.Errorf("unsafe telemetry settings file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("unsafe telemetry settings ownership")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil {
		return err
	}
	if len(raw) > 16384 {
		return fmt.Errorf("telemetry settings file too large")
	}
	return v1.DecodeTelemetry(raw, value, false)
}
