package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

const managedRoot = "/var/lib/viorka-engines"
const unitRoot = "/etc/systemd/system"
const unitMarker = "# Managed exclusively by Viorka Agent\n"

type managedState struct {
	EngineID  string           `json:"engine_id"`
	Command   v1.EngineCommand `json:"command"`
	Installed bool             `json:"installed"`
}
type Managed struct {
	logger         *slog.Logger
	supported      func() bool
	detectExternal func(string) bool
	root, units    string
	openrc         bool
	runlevels      string
	run            func(context.Context, string, ...string) error
	download       func(context.Context, v1.EngineRelease) ([]byte, error)
}

func NewManaged(loggers ...*slog.Logger) *Managed {
	m := &Managed{root: managedRoot, units: unitRoot, run: runControlled, download: Download, supported: Supported}
	m.openrc = serviceManager("/") == "openrc"
	if m.openrc {
		m.units = "/etc/init.d"
		m.runlevels = "/etc/runlevels"
	}
	m.logger = slog.New(slog.DiscardHandler)
	if len(loggers) > 0 && loggers[0] != nil {
		m.logger = loggers[0]
	}
	m.run = m.runLogged
	m.download = func(ctx context.Context, release v1.EngineRelease) ([]byte, error) {
		return downloadLogged(ctx, release, m.logger)
	}
	m.detectExternal = m.external
	return m
}
func Supported() bool {
	if goruntime.GOOS != "linux" || os.Geteuid() != 0 || (goruntime.GOARCH != "amd64" && goruntime.GOARCH != "arm64") {
		return false
	}
	return serviceManager("/") != ""
}
func runControlled(ctx context.Context, path string, args ...string) error {
	return runControlledOutput(ctx, io.Discard, path, args...)
}

func runControlledOutput(ctx context.Context, output io.Writer, path string, args ...string) error {
	timeout, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c := exec.CommandContext(timeout, path, args...)
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "HOME=/"}
	c.Stdout = output
	c.Stderr = output
	return c.Run()
}
func systemctl() string {
	if _, err := os.Stat("/usr/bin/systemctl"); err == nil {
		return "/usr/bin/systemctl"
	}
	return "/bin/systemctl"
}
func (m *Managed) ctl(ctx context.Context, name, operation string) error {
	if m.openrc {
		return m.openrcCtl(ctx, name, operation)
	}
	if operation == "daemon-reload" {
		return m.run(ctx, systemctl(), operation)
	}
	return m.run(ctx, systemctl(), operation, "viorka-engine-"+name+".service")
}
func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return errors.New("unsafe engine directory")
	}
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !i.IsDir() || i.Mode().Perm()&0077 != 0 {
		return errors.New("engine directory must be private")
	}
	return nil
}
func atomicEngineFile(path string, data []byte, mode os.FileMode) error {
	if i, err := os.Lstat(path); err == nil && !i.Mode().IsRegular() {
		return errors.New("unsafe managed file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func readPrivate(path string, v any) error {
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 || i.Size() > 1<<20 {
		return errors.New("unsafe state")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func (m *Managed) state(name string) (managedState, error) {
	var s managedState
	err := readPrivate(filepath.Join(m.root, name, "state.json"), &s)
	return s, err
}
func (m *Managed) save(name string, s managedState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	m.logger.Debug("Engine state persist", "installed", s.Installed, "observed_revision", s.Command.Revision)
	err = atomicEngineFile(filepath.Join(m.root, name, "state.json"), b, 0600)
	if err != nil {
		m.logger.Error("Engine state persist failed", "error", err)
	}
	return err
}
func (m *Managed) external(name string) bool {
	for _, dir := range []string{"/usr/bin", "/usr/local/bin", "/bin", "/opt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	if _, err := os.Lstat(filepath.Join("/etc/init.d", name)); err == nil {
		return true
	}
	for _, unit := range []string{name + ".service", name + "@.service"} {
		for _, dir := range []string{"/etc/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"} {
			if _, err := os.Lstat(filepath.Join(dir, unit)); err == nil {
				return true
			}
		}
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if err == nil && strings.TrimSpace(string(b)) == name {
			return true
		}
	}
	return false
}
func (m *Managed) unit(name, dir string) []byte {
	binary := filepath.Join(dir, name)
	config := filepath.Join(dir, "config.json")
	args := "run -c " + config
	if name == "sing-box" {
		args = "run -C " + filepath.Join(dir, "conf.d")
	}
	if name == "realm" {
		config = filepath.Join(dir, "config.toml")
		args = "-c " + config
	}
	if m.openrc {
		return []byte(openrcHeader + "description=\"Viorka managed " + name + "\"\nsupervisor=supervise-daemon\ncommand=" + binary + "\ncommand_args=\"" + args + "\"\npidfile=/run/viorka-engine-" + name + ".pid\numask=0077\nrespawn_delay=10\nrespawn_max=3\nrespawn_period=120\nretry=\"TERM/10/KILL/5\"\noutput_log=/dev/null\nerror_log=/dev/null\ndepend() {\n    use net\n}\n")
	}
	return []byte(unitMarker + "[Unit]\nDescription=Viorka managed " + name + "\nAfter=network-online.target\nWants=network-online.target\nStartLimitIntervalSec=120\nStartLimitBurst=3\n[Service]\nType=simple\nExecStart=" + binary + " " + args + "\nRestart=on-failure\nRestartSec=10\nTimeoutStopSec=15\nUMask=0077\nNoNewPrivileges=true\nLimitNOFILE=65536\nStandardOutput=null\nStandardError=null\n[Install]\nWantedBy=multi-user.target\n")
}
func (m *Managed) Execute(ctx context.Context, id, operation string, c v1.EngineCommand) (v1.EngineResult, error) {
	r := v1.EngineResult{Name: c.Name, Version: c.Version, Revision: c.Revision, Status: "unknown"}
	fail := func(code string) (v1.EngineResult, error) {
		m.logger.Error("Engine operation failed", "error_code", code, "status", r.Status)
		r.ErrorCode = code
		return r, nil
	}
	if c.Validate(operation) != nil || !m.supported() {
		return fail("engine_unsupported")
	}
	release, ok := v1.EngineReleaseFor(c.Name, c.Version, goruntime.GOARCH)
	if !ok {
		return fail("engine_unsupported")
	}
	if err := privateDirectory(m.root); err != nil {
		return fail("engine_io_failed")
	}
	dir := filepath.Join(m.root, c.Name)
	if err := privateDirectory(dir); err != nil {
		return fail("engine_io_failed")
	}
	old, err := m.state(c.Name)
	if err != nil && !os.IsNotExist(err) {
		return fail("engine_io_failed")
	}
	if old.EngineID != "" && old.EngineID != id {
		return fail("engine_external")
	}
	if old.Installed {
		r.Status = "stopped"
		if m.ctl(ctx, c.Name, "is-active") == nil {
			r.Status = "running"
		}
	} else {
		r.Status = "not_installed"
	}
	if old.Command.Revision >= c.Revision {
		return fail("engine_result_unknown")
	}
	unitPath := m.unitPath(c.Name)
	if unit, err := os.ReadFile(unitPath); err == nil && !m.ownsUnit(unit) {
		return fail("engine_external")
	}
	if operation == "engine.install" {
		if old.Installed || m.detectExternal(c.Name) {
			return fail("engine_external")
		}
		binary, err := m.download(ctx, release)
		if err != nil {
			code := err.Error()
			if code != "engine_checksum" {
				code = "engine_download_failed"
			}
			return fail(code)
		}
		m.logger.Debug("Engine binary install", "bytes", len(binary))
		if err = atomicEngineFile(filepath.Join(dir, c.Name), binary, 0700); err != nil {
			m.logger.Error("Engine binary install failed", "error", err)
			return fail("engine_io_failed")
		}
		m.logger.Debug("Engine service install", "openrc", m.openrc)
		if err = atomicEngineFile(unitPath, m.unit(c.Name, dir), m.unitMode()); err != nil {
			m.logger.Error("Engine service install failed", "error", err)
			return fail("engine_io_failed")
		}
		if err = m.ctl(ctx, c.Name, "daemon-reload"); err != nil {
			return fail("engine_io_failed")
		}
		if err = m.save(c.Name, managedState{id, c, true}); err != nil {
			return fail("engine_io_failed")
		}
		r.Status = "installed"
		return r, nil
	}
	if !old.Installed {
		if operation == "engine.inspect" {
			return r, nil
		}
		return fail("engine_unsupported")
	}
	if operation == "engine.upgrade" {
		if old.Command.Version == c.Version {
			return fail("engine_unsupported")
		}
		binary, err := m.download(ctx, release)
		if err != nil {
			return fail("engine_download_failed")
		}
		path := filepath.Join(dir, c.Name)
		previous, err := os.ReadFile(path)
		if err != nil {
			return fail("engine_io_failed")
		}
		if err = atomicEngineFile(filepath.Join(dir, "previous-binary"), previous, 0700); err != nil {
			return fail("engine_io_failed")
		}
		if err = atomicEngineFile(path, binary, 0700); err != nil {
			return fail("engine_io_failed")
		}
		next := old.Command
		next.Version = c.Version
		next.Revision = c.Revision
		if enabled(next) > 0 {
			err = m.validate(ctx, dir, next, activeConfig(dir, c.Name))
		}
		if err == nil && r.Status == "running" {
			err = m.ctl(ctx, c.Name, "restart")
			if err == nil {
				err = m.healthy(ctx, next)
			}
		}
		if err != nil {
			recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rollback := atomicEngineFile(path, previous, 0700)
			if rollback == nil && r.Status == "running" {
				rollback = m.ctl(recovery, c.Name, "restart")
				if rollback == nil {
					rollback = m.healthy(recovery, old.Command)
				}
			}
			if rollback != nil {
				r.Status = "degraded"
				return fail("engine_rollback_failed")
			}
			return fail("engine_start_failed")
		}
		if err = m.save(c.Name, managedState{id, next, true}); err != nil {
			r.Status = "unknown"
			return fail("engine_io_failed")
		}
		return r, nil
	}
	if operation == "engine.uninstall" {
		if len(old.Command.Services) > 0 {
			return fail("engine_invalid_config")
		}
		if m.ctl(ctx, c.Name, "stop") != nil || m.ctl(ctx, c.Name, "disable") != nil {
			return fail("engine_io_failed")
		}
		if err = os.Remove(unitPath); err != nil {
			return fail("engine_io_failed")
		}
		if m.ctl(ctx, c.Name, "daemon-reload") != nil {
			return fail("engine_io_failed")
		}
		if err = os.Remove(filepath.Join(dir, c.Name)); err != nil {
			return fail("engine_io_failed")
		}
		if err = m.save(c.Name, managedState{id, c, false}); err != nil {
			return fail("engine_io_failed")
		}
		r.Status = "not_installed"
		return r, nil
	}
	if operation == "engine.inspect" {
		splitConfig := c.Name == "sing-box" && activeConfig(dir, c.Name) == filepath.Join(dir, "conf.d")
		if len(old.Command.Services) > 0 || splitConfig {
			if splitConfig {
				if err := inspectSingbox(dir, old.Command); err != nil {
					return fail("engine_result_unknown")
				}
			} else {
				expected, err := Render(old.Command)
				if err != nil {
					return fail("engine_result_unknown")
				}
				actual, err := os.ReadFile(filepath.Join(dir, configName(c.Name)))
				if err != nil || !bytes.Equal(expected, actual) {
					return fail("engine_result_unknown")
				}
			}
			if r.Status == "running" && m.healthy(ctx, old.Command) != nil {
				r.Status = "degraded"
				return fail("engine_result_unknown")
			}
		}
		return r, nil
	}
	if operation == "engine.apply" {
		return m.apply(ctx, id, c, old, r)
	}
	current := old.Command
	current.Revision = c.Revision
	current.RunIntent = c.RunIntent
	if operation == "engine.stop" {
		if m.ctl(ctx, c.Name, "disable") != nil || m.ctl(ctx, c.Name, "stop") != nil {
			return fail("engine_io_failed")
		}
		r.Status = "stopped"
	} else if operation == "engine.start" {
		if enabled(current) == 0 {
			return fail("engine_invalid_config")
		}
		if m.ctl(ctx, c.Name, "enable") != nil || m.ctl(ctx, c.Name, "start") != nil || m.healthy(ctx, current) != nil {
			recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if m.ctl(recovery, c.Name, "disable") != nil || m.ctl(recovery, c.Name, "stop") != nil {
				r.Status = "degraded"
				return fail("engine_rollback_failed")
			}
			r.Status = "stopped"
			return fail("engine_start_failed")
		}
		r.Status = "running"
	}
	if err = m.save(c.Name, managedState{id, current, true}); err != nil {
		return fail("engine_io_failed")
	}
	return r, nil
}
func enabled(c v1.EngineCommand) int {
	n := 0
	for _, s := range c.Services {
		if s.Enabled {
			n++
		}
	}
	return n
}
func configName(name string) string {
	if name == "realm" {
		return "config.toml"
	}
	return "config.json"
}
func (m *Managed) validate(ctx context.Context, dir string, c v1.EngineCommand, path string) error {
	binary := filepath.Join(dir, c.Name)
	switch c.Name {
	case "sing-box":
		if filepath.Base(path) == "conf.d" {
			if _, err := singboxGeneration(dir); err != nil {
				return err
			}
		}
		flag := "-c"
		if filepath.Base(path) != "config.json" {
			flag = "-C"
		}
		return m.run(ctx, binary, "check", flag, path)
	case "xray":
		return m.run(ctx, binary, "run", "-test", "-c", path)
	case "realm":
		// Realm 2.9.6 has no check/test command. Accept only the exact
		// deterministic config rendered from the validated structured command.
		expected, err := Render(c)
		if err != nil {
			return err
		}
		actual, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(expected, actual) {
			return errors.New("configuration changed")
		}
		return nil
	}
	return errors.New("unsupported engine")
}
func (m *Managed) healthy(ctx context.Context, c v1.EngineCommand) error {
	for attempt := 0; attempt < 10; attempt++ {
		good := m.ctl(ctx, c.Name, "is-active") == nil
		for _, s := range c.Services {
			if !s.Enabled {
				continue
			}
			host := s.ListenHost
			if host == "0.0.0.0" {
				host = "127.0.0.1"
			}
			if host == "::" {
				host = "::1"
			}
			d := net.Dialer{Timeout: 300 * time.Millisecond}
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.FormatUint(s.ListenPort, 10)))
			if err != nil {
				good = false
			} else {
				conn.Close()
			}
		}
		if good {
			return nil
		}
		timer := time.NewTimer(300 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("health failed")
}
func (m *Managed) apply(ctx context.Context, id string, c v1.EngineCommand, old managedState, r v1.EngineResult) (v1.EngineResult, error) {
	fail := func(code string) (v1.EngineResult, error) {
		m.logger.Error("Engine operation failed", "error_code", code, "status", r.Status)
		r.ErrorCode = code
		return r, nil
	}
	dir := filepath.Join(m.root, c.Name)
	if err := preflight(ctx, c, old.Command, r.Status == "running"); err != nil {
		return fail(err.Error())
	}
	m.logger.Debug("Engine configuration prepare")
	rollbackConfig, cleanup, code := m.prepareConfig(ctx, dir, c)
	if cleanup != nil {
		defer func() {
			if r.ErrorCode != "engine_rollback_failed" && !(code == "" && r.ErrorCode == "engine_io_failed") {
				cleanup()
			}
		}()
	}
	if code != "" {
		if code == "engine_rollback_failed" {
			r.Status = "degraded"
		}
		return fail(code)
	}
	var err error
	running := c.RunIntent == "running" && enabled(c) > 0
	if running {
		err = m.ctl(ctx, c.Name, "enable")
		if err == nil {
			err = m.ctl(ctx, c.Name, "restart")
		}
		if err == nil {
			err = m.healthy(ctx, c)
		}
	} else {
		err = m.ctl(ctx, c.Name, "disable")
		if err == nil {
			err = m.ctl(ctx, c.Name, "stop")
		}
	}
	if err != nil {
		recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m.logger.Warn("Engine configuration rollback started")
		rollback := rollbackConfig(recovery)
		if rollback == nil && r.Status == "running" {
			rollback = m.ctl(recovery, c.Name, "restart")
			if rollback == nil {
				rollback = m.healthy(recovery, old.Command)
			}
		} else if rollback == nil {
			rollback = m.ctl(recovery, c.Name, "stop")
		}
		if rollback == nil {
			if old.Command.RunIntent == "running" && enabled(old.Command) > 0 {
				rollback = m.ctl(recovery, c.Name, "enable")
			} else {
				rollback = m.ctl(recovery, c.Name, "disable")
			}
		}
		if rollback != nil {
			r.Status = "degraded"
			return fail("engine_rollback_failed")
		}
		return fail("engine_start_failed")
	}
	if err = m.save(c.Name, managedState{id, c, true}); err != nil {
		r.Status = "unknown"
		return fail("engine_io_failed")
	}
	r.Status = "stopped"
	if running {
		r.Status = "running"
	}
	return r, nil
}

func (m *Managed) prepareSingle(ctx context.Context, dir string, c v1.EngineCommand) (func(context.Context) error, func(), string) {
	target := filepath.Join(dir, configName(c.Name))
	staging := filepath.Join(dir, "pending-"+configName(c.Name))
	data, err := Render(c)
	if err != nil {
		return nil, nil, "engine_invalid_config"
	}
	if err = atomicEngineFile(staging, data, 0600); err != nil {
		return nil, nil, "engine_io_failed"
	}
	defer os.Remove(staging)
	if enabled(c) > 0 && m.validate(ctx, dir, c, staging) != nil {
		return nil, nil, "engine_invalid_config"
	}
	previous, readErr := os.ReadFile(target)
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, nil, "engine_io_failed"
	}
	if readErr == nil {
		if err = atomicEngineFile(filepath.Join(dir, "previous-"+configName(c.Name)), previous, 0600); err != nil {
			return nil, nil, "engine_io_failed"
		}
	}
	if err = atomicEngineFile(target, data, 0600); err != nil {
		return nil, nil, "engine_io_failed"
	}

	return func(context.Context) error {
		if readErr == nil {
			return atomicEngineFile(target, previous, 0600)
		}
		return os.Remove(target)
	}, nil, ""
}
