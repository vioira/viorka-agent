package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// RecoverySnapshots uses the same read-only observation as online recovery.
func (m *Managed) RecoverySnapshots(ctx context.Context) ([]*v1.EngineRecovery, error) {
	items := []*v1.EngineRecovery{}
	for _, name := range []string{"sing-box", "xray", "realm"} {
		snapshot, code := m.recoverySnapshot(ctx, name)
		if code != "" {
			return nil, fmt.Errorf("cannot recover %s: %s", name, code)
		}
		if snapshot != nil {
			items = append(items, snapshot)
		}
	}
	return items, nil
}

func (m *Managed) recoverySnapshot(ctx context.Context, name string) (*v1.EngineRecovery, string) {
	if ctx.Err() != nil {
		return nil, "deadline_exceeded"
	}
	dir := filepath.Join(m.root, name)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, ""
	} else if err != nil {
		return nil, "engine_io_failed"
	}
	for _, path := range []string{m.root, dir} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || !recoveryOwned(info) || info.Mode().Perm()&0077 != 0 {
			return nil, "engine_io_failed"
		}
	}
	stateFile, err := os.Lstat(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ""
	}
	if err != nil || !recoveryOwned(stateFile) {
		return nil, "engine_io_failed"
	}
	state, err := m.state(name)
	if err == nil && !state.Installed {
		return nil, ""
	}
	if err != nil || state.Command.Name != name {
		return nil, "engine_io_failed"
	}
	if !m.supported() {
		return nil, "engine_unsupported"
	}
	command := state.Command
	if _, ok := v1.EngineReleaseFor(name, command.Version, goruntime.GOARCH); !ok {
		return nil, "engine_unsupported"
	}
	snapshot := &v1.EngineRecovery{EngineID: state.EngineID, Command: &command, Status: "stopped"}
	if v1.ValidateEngineRecovery([]*v1.EngineRecovery{snapshot}) != nil {
		return nil, "engine_config_unavailable"
	}
	unitPath := m.unitPath(name)
	info, err := os.Lstat(unitPath)
	if err != nil || !info.Mode().IsRegular() || !recoveryOwned(info) || info.Mode().Perm()&0022 != 0 || info.Size() > 8192 {
		return nil, "engine_external"
	}
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		return nil, "engine_io_failed"
	}
	expected := m.unit(name, dir)
	config := activeConfig(dir, name)
	if name == "sing-box" && config == filepath.Join(dir, "config.json") {
		expected = bytes.Replace(expected, []byte("run -C "+filepath.Join(dir, "conf.d")), []byte("run -c "+config), 1)
		// A fresh install already has the split-config unit and no config yet.
		if _, err := os.Lstat(config); errors.Is(err, os.ErrNotExist) {
			expected = m.unit(name, dir)
		}
	}
	if !bytes.Equal(unit, expected) {
		return nil, "engine_external"
	}

	binary := filepath.Join(dir, name)
	info, err = os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || !recoveryOwned(info) || info.Mode().Perm() != 0700 {
		return nil, "engine_binary_unusable"
	}
	if code := m.verifyBinary(ctx, binary, command, false); code != "" {
		return nil, code
	}
	status, code := m.recoveryStatus(ctx, name)
	if code != "" {
		return nil, code
	}
	snapshot.Status = status

	snapshot.Unconfigured = len(command.Services) == 0 && command.Routing == nil && snapshot.Status == "stopped"
	for _, path := range []string{filepath.Join(dir, configName(name)), filepath.Join(dir, "conf.d")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			snapshot.Unconfigured = false
		}
	}
	if !snapshot.Unconfigured {
		if name == "sing-box" && config == filepath.Join(dir, "conf.d") {
			if err := inspectSingbox(dir, command); err != nil {
				return nil, "engine_config_unavailable"
			}
		} else {
			expected, err := Render(command)
			info, statErr := os.Lstat(config)
			if err != nil || statErr != nil || !info.Mode().IsRegular() || !recoveryOwned(info) || info.Mode().Perm() != 0600 || info.Size() != int64(len(expected)) {
				return nil, "engine_config_unavailable"
			}
			data, err := os.ReadFile(config)
			if err != nil || !bytes.Equal(expected, data) {
				return nil, "engine_config_unavailable"
			}
		}
		if snapshot.Status == "running" && m.healthy(ctx, command) != nil {
			return nil, "engine_result_unknown"
		}
		if err := m.loadEndpointBackup(snapshot); err != nil {
			return nil, "engine_io_failed"
		}
	}
	return snapshot, ""
}

func recoveryOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func (m *Managed) recoveryStatus(ctx context.Context, name string) (string, string) {
	if m.openrc {
		err := m.ctl(ctx, name, "is-active")
		if err == nil {
			return "running", ""
		}
		var exit interface{ ExitCode() int }
		if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 3 {
			return "stopped", ""
		}
		return "", "engine_result_unknown"
	}
	var output commandOutput
	if err := m.readSupervisor(ctx, &output, systemctl(), "show", "--all", "viorka-engine-"+name+".service", "-p", "ActiveState", "-p", "FragmentPath", "-p", "DropInPaths", "-p", "NeedDaemonReload"); err != nil || output.truncated {
		return "", "engine_result_unknown"
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(output.data), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			props[key] = value
		}
	}
	if drops, ok := props["DropInPaths"]; !ok || drops != "" || props["FragmentPath"] != m.unitPath(name) {
		return "", "engine_external"
	}
	if props["NeedDaemonReload"] != "no" {
		return "", "engine_result_unknown"
	}
	switch props["ActiveState"] {
	case "active":
		return "running", ""
	case "inactive":
		return "stopped", ""
	default:
		return "", "engine_result_unknown"
	}
}
