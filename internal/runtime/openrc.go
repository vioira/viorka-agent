package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const openrcHeader = "#!/sbin/openrc-run\n" + unitMarker

// Probe only fixed system paths; do not resolve privileged tools through PATH.
func serviceManager(root string) string {
	directory := func(path string) bool {
		i, err := os.Stat(filepath.Join(root, path))
		return err == nil && i.IsDir()
	}
	executable := func(path string) bool {
		i, err := os.Stat(filepath.Join(root, path))
		return err == nil && i.Mode().IsRegular() && i.Mode().Perm()&0111 != 0
	}
	if directory("/run/systemd/system") && (executable("/usr/bin/systemctl") || executable("/bin/systemctl")) {
		return "systemd"
	}
	if directory("/run/openrc") {
		for _, path := range []string{"/sbin/openrc-run", "/sbin/rc-service", "/sbin/rc-update", "/sbin/supervise-daemon"} {
			if !executable(path) {
				return ""
			}
		}
		return "openrc"
	}
	return ""
}

func (m *Managed) unitPath(name string) string {
	file := "viorka-engine-" + name
	if !m.openrc {
		file += ".service"
	}
	return filepath.Join(m.units, file)
}

func (m *Managed) unitMode() os.FileMode {
	if m.openrc {
		return 0755
	}
	return 0644
}

func (m *Managed) ownsUnit(data []byte) bool {
	header := unitMarker
	if m.openrc {
		header = openrcHeader
	}
	return strings.HasPrefix(string(data), header)
}

func (m *Managed) openrcCtl(ctx context.Context, name, operation string) error {
	service := "viorka-engine-" + name
	switch operation {
	case "daemon-reload":
		// OpenRC reads the service script on every invocation.
		return nil
	case "enable":
		return m.run(ctx, "/sbin/rc-update", "add", service, "default")
	case "disable":
		// rc-update del fails for absent membership. Inspect actual runlevels
		// to make stopping an installed-but-never-started engine idempotent.
		levels, err := os.ReadDir(m.runlevels)
		if err != nil {
			return err
		}
		for _, level := range levels {
			if !level.IsDir() {
				continue
			}
			_, err := os.Lstat(filepath.Join(m.runlevels, level.Name(), service))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if err := m.run(ctx, "/sbin/rc-update", "del", service, level.Name()); err != nil {
				return err
			}
		}
		return nil
	case "is-active":
		return m.run(ctx, "/sbin/rc-service", service, "status")
	case "stop":
		return m.run(ctx, "/sbin/rc-service", "--ifstarted", service, "stop")
	case "start", "restart":
		return m.run(ctx, "/sbin/rc-service", service, operation)
	default:
		return errors.New("unsupported OpenRC operation")
	}
}
