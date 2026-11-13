package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

const trafficFile = "traffic.dat"
const trafficFileLimit = 256 << 10

var ErrTrafficFileChanged = errors.New("traffic state changed outside its owner")

type trafficFileEnvelope struct {
	Snapshot TrafficSnapshot `json:"snapshot"`
	SHA256   string          `json:"sha256"`
}

func trafficDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func encodeTraffic(s TrafficSnapshot) (trafficFileEnvelope, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return trafficFileEnvelope{}, err
	}
	return trafficFileEnvelope{Snapshot: s, SHA256: trafficDigest(raw)}, nil
}
func (s *Store) readTrafficFile() (TrafficSnapshot, string, error) {
	var empty TrafficSnapshot
	info, err := os.Lstat(s.dir)
	if err != nil {
		return empty, "", err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return empty, "", fmt.Errorf("unsafe traffic state directory")
	}
	fd, err := syscall.Open(filepath.Join(s.dir, trafficFile), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return empty, "", err
	}
	f := os.NewFile(uintptr(fd), trafficFile)
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return empty, "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > trafficFileLimit {
		return empty, "", fmt.Errorf("unsafe traffic state file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
		return empty, "", fmt.Errorf("unsafe traffic state ownership")
	}
	raw, err := io.ReadAll(io.LimitReader(f, trafficFileLimit+1))
	if err != nil {
		return empty, "", err
	}
	if len(raw) > trafficFileLimit {
		return empty, "", fmt.Errorf("traffic state capacity exceeded")
	}
	var envelope trafficFileEnvelope
	if err = v1.DecodeTelemetry(raw, &envelope, false); err != nil {
		return empty, "", fmt.Errorf("decode traffic state: %w", err)
	}
	canonical, err := encodeTraffic(envelope.Snapshot)
	if err != nil {
		return empty, "", err
	}
	if envelope.SHA256 != canonical.SHA256 {
		return empty, "", fmt.Errorf("traffic state integrity mismatch")
	}
	// The checksum detects corruption, not malicious tampering by the owner.
	return envelope.Snapshot, trafficDigest(raw), nil
}

// OpenTrafficLedger returns a blocked ledger plus an error when an existing file
// is unsafe or corrupt. Callers keep other monitoring alive; never replace the
// blocked ledger with a fresh zero ledger or overwrite the original file.
func OpenTrafficLedger(store *Store, panel, node string) (*TrafficLedger, error) {
	ledger, err := NewTrafficLedger(panel, node)
	if err != nil {
		return nil, err
	}
	ledger.store = store
	ledger.write = store.Write
	snapshot, etag, err := store.readTrafficFile()
	if errors.Is(err, os.ErrNotExist) {
		return ledger, nil
	}
	if err == nil {
		err = ledger.Restore(snapshot)
	}
	if err != nil {
		ledger.blocked = err
		return ledger, err
	}
	ledger.etag = etag
	return ledger, nil
}

// Save permits concurrent Observe without holding a lock during filesystem I/O.
// force is reserved for the final shutdown
// attempt. A failed write retains totals in memory and limits retries to 60s.
// Context bounds scheduling and checkpoints; fsync itself cannot be interrupted.
var ErrTrafficSaveInProgress = errors.New("traffic save already in progress")

func (l *TrafficLedger) Save(ctx context.Context, at time.Time, force bool) error {
	if !l.saveMu.TryLock() {
		return ErrTrafficSaveInProgress
	}
	defer l.saveMu.Unlock()
	l.mu.Lock()
	original := l.snapshot
	work := &TrafficLedger{store: l.store, write: l.write, etag: l.etag, nextSave: l.nextSave, dirty: l.dirty, panel: l.panel, node: l.node, blocked: l.blocked, persistFailed: l.persistFailed}
	if original != nil {
		snapshot := cloneTraffic(*original)
		work.snapshot = &snapshot
	}
	l.mu.Unlock()
	err := work.saveSnapshot(ctx, at, force)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil && work.snapshot != nil && !work.dirty {
		l.durableDaily = cloneDaily(work.snapshot.Daily)
	}
	l.etag = work.etag
	l.nextSave = work.nextSave
	l.persistFailed = work.persistFailed
	if l.blocked == nil {
		l.blocked = work.blocked
	}
	// A sample received during I/O remains dirty; the old commit cannot mark it
	// durable or replace its newer counter baseline/month.
	if l.snapshot == original {
		l.snapshot = work.snapshot
		l.dirty = work.dirty
	}
	return err
}
func (l *TrafficLedger) saveSnapshot(ctx context.Context, at time.Time, force bool) error {
	if l.blocked != nil {
		return l.blocked
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !l.dirty || l.snapshot == nil {
		return nil
	}
	if !force && !l.nextSave.IsZero() && at.Before(l.nextSave) {
		return nil
	}
	if l.store == nil || l.write == nil {
		return fmt.Errorf("traffic persistence is not configured")
	}
	l.nextSave = at.Add(time.Minute)
	fail := func(err error) error { l.persistFailed = true; return err }
	current, etag, err := l.store.readTrafficFile()
	if errors.Is(err, os.ErrNotExist) {
		if l.etag != "" {
			l.blocked = ErrTrafficFileChanged
			return fail(l.blocked)
		}
	} else if err != nil {
		l.blocked = err
		return fail(err)
	} else {
		if err = validateTrafficSnapshot(current, l.panel, l.node); err != nil {
			l.blocked = err
			return fail(err)
		}
		if etag != l.etag {
			l.blocked = ErrTrafficFileChanged
			return fail(l.blocked)
		}
	}
	snapshot := cloneTraffic(*l.snapshot)
	snapshot.SavedAt = at.UTC()
	if err = validateTrafficSnapshot(snapshot, l.panel, l.node); err != nil {
		return fail(err)
	}
	envelope, err := encodeTraffic(snapshot)
	if err != nil {
		return fail(err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil || len(raw) > trafficFileLimit {
		return fail(fmt.Errorf("traffic state capacity exceeded"))
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	err = l.write(trafficFile, envelope)
	// Rename can succeed before directory sync fails. Recognize our exact bytes
	// on disk, but retain dirty/persist_failed until a later durable write succeeds.
	expected := trafficDigest(raw)
	if _, actual, readErr := l.store.readTrafficFile(); readErr == nil && actual == expected {
		l.etag = actual
	}
	if err != nil {
		return fail(err)
	}
	if l.etag != expected {
		return fail(fmt.Errorf("traffic write verification failed"))
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	l.snapshot.SavedAt = snapshot.SavedAt
	l.dirty = false
	l.persistFailed = false
	return nil
}
