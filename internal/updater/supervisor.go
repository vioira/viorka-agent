package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func serviceMatches(ctx context.Context) bool {
	expected, err := os.ReadFile(installDir + "/.agent-service-sha256")
	if err != nil {
		return false
	}
	file := "/etc/init.d/viorka-agent"
	if _, err = os.Stat("/run/systemd/system"); err == nil {
		file = "/etc/systemd/system/viorka-agent.service"
		out, err := exec.CommandContext(ctx, "systemctl", "show", "--all", "viorka-agent.service", "-p", "FragmentPath", "-p", "DropInPaths").Output()
		if err != nil {
			return false
		}
		fields := strings.Fields(string(out))
		if len(fields) != 2 || !slices.Contains(fields, "FragmentPath="+file) || !slices.Contains(fields, "DropInPaths=") {
			return false
		}
	} else if _, err = os.Lstat("/etc/conf.d/viorka-agent"); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return false
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	h := sha256.Sum256(b)
	return strings.TrimSpace(string(expected)) == hex.EncodeToString(h[:])
}

var workerAttributes syscall.SysProcAttr

type worker struct {
	cmd  *exec.Cmd
	done chan error
}

func startWorker(dir string) (*worker, error) {
	if err := os.Remove(filepath.Join(dir, ".agent-ready")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	cmd := exec.Command(filepath.Join(dir, "viorka-agent"), "run", "--config", filepath.Join(dir, "agent.json"))
	cmd.SysProcAttr = &workerAttributes
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "VIORKA_SUPERVISED=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	w := &worker{cmd: cmd, done: make(chan error, 1)}
	go func() { w.done <- cmd.Wait(); close(w.done) }()
	return w, nil
}
func (w *worker) stop() {
	if w == nil {
		return
	}
	_ = w.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-w.done:
	case <-time.After(12 * time.Second):
		_ = w.cmd.Process.Kill()
		<-w.done
	}
}
func (w *worker) healthy(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var since time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.done:
			return errors.New("worker exited")
		case <-tick.C:
			b, err := os.ReadFile(filepath.Join(dir, ".agent-ready"))
			if err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(w.cmd.Process.Pid) {
				if since.IsZero() {
					since = time.Now()
				} else if time.Since(since) >= 3*time.Second {
					return nil
				}
			}
		}
	}
}

// The symlink is an exclusive installer lock with a recognizable owner after power loss.
// An ordinary installer owns a directory at the same pathname.
func ownLock(dir string) error {
	lock := filepath.Join(dir, ".install-lock")
	err := os.Symlink(journalName, lock)
	if errors.Is(err, os.ErrExist) {
		if dst, e := os.Readlink(lock); e == nil && dst == journalName {
			return nil
		}
	}
	return err
}
func releaseLock(dir string) error {
	lock := filepath.Join(dir, ".install-lock")
	dst, err := os.Readlink(lock)
	if err == nil && dst == journalName {
		return os.Remove(lock)
	}
	return nil
}
func linkReplace(src, dst string) error {
	tmp := dst + ".link"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Link(src, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

type supervisor struct {
	dir    string
	worker *worker
}

func (s *supervisor) start(check bool, ctx context.Context) error {
	w, err := startWorker(s.dir)
	s.worker = w
	if err == nil && check {
		err = w.healthy(ctx, s.dir)
	}
	return err
}
func (s *supervisor) restore(ctx context.Context, r receipt) error {
	if err := r.save(s.dir, "restoring"); err != nil {
		return err
	}
	s.worker.stop()
	err := linkReplace(filepath.Join(s.dir, "viorka-agent.previous"), filepath.Join(s.dir, "viorka-agent"))
	if err == nil {
		err = s.start(true, ctx)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		if e := r.save(s.dir, "update_rollback_failed"); e != nil {
			return errors.Join(err, e)
		}
		return errors.Join(fmt.Errorf("restore previous Agent: %w", err), releaseLock(s.dir))
	}
	return r.save(s.dir, "update_rolled_back")
}
func (s *supervisor) apply(ctx context.Context, r receipt) error {
	if err := ownLock(s.dir); err != nil {
		return r.save(s.dir, "update_failed")
	}
	checked, cancel := context.WithTimeout(ctx, 5*time.Second)
	allowed := serviceMatches(checked)
	cancel()
	if !allowed || !r.Target.NewerThan(runningVersion(s.dir)) || !time.Now().Before(r.Deadline) {
		return r.save(s.dir, "update_failed")
	}
	bounded, cancel := context.WithDeadline(ctx, r.Deadline)
	err := prepare(bounded, s.dir, r)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		slog.Warn("Agent update preparation failed", "error", err)
		return r.save(s.dir, "update_failed")
	}
	return s.activate(ctx, r)
}
func (s *supervisor) activate(ctx context.Context, r receipt) error {
	var err error
	active := filepath.Join(s.dir, "viorka-agent")
	if err = linkReplace(active, active+".previous"); err != nil {
		slog.Warn("Agent update backup failed", "error", err)
		return r.save(s.dir, "update_failed")
	}
	if err = r.save(s.dir, "trial"); err != nil {
		return err
	}
	s.worker.stop()
	if err = os.Rename(filepath.Join(s.dir, ".agent-next"), active); err == nil {
		err = s.start(true, ctx)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return s.restore(ctx, r)
	}
	return r.save(s.dir, "healthy")
}
func runningVersion(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, filepath.Join(dir, "viorka-agent"), "version").Output()
	parts := strings.Fields(string(b))
	if err != nil || len(parts) != 3 {
		return ""
	}
	return parts[1]
}
func Supervise(ctx context.Context) error {
	// Linux delivers Pdeathsig when the thread that created the child exits.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if os.Geteuid() != 0 {
		return errors.New("managed supervisor requires root")
	}
	lock, err := os.OpenFile(filepath.Join(installDir, ".supervisor-lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("supervisor already active: %w", err)
	}
	s := supervisor{dir: installDir}
	defer func() { s.worker.stop() }()
	if err = s.recover(ctx); err != nil {
		return err
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		r, err := readReceipt(s.dir)
		if err != nil {
			return err
		}
		if r.Phase == "pending" {
			if err = s.apply(ctx, r); err != nil {
				return err
			}
		}
		if err = releaseLock(s.dir); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-s.worker.done:
			return err
		case <-tick.C:
		}
	}
}

func (s *supervisor) recover(ctx context.Context) error {
	r, err := readReceipt(s.dir)
	if err != nil {
		return err
	}
	if r.Phase == "trial" || r.Phase == "restoring" {
		if err = ownLock(s.dir); err != nil {
			return err
		}
		if err = s.restore(ctx, r); err != nil {
			return err
		}
	} else if err = s.start(false, ctx); err != nil {
		return err
	}
	return nil
}
