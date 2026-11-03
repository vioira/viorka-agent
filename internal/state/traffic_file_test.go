package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTrafficFileRestartAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	boot := "11111111-1111-4111-8111-111111111111"
	filter := strings.Repeat("a", 64)
	l, err := OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Observe(at, boot, filter, trafficTestInterface(1000, 2000)); err != nil {
		t.Fatal(err)
	}
	if err = l.Save(context.Background(), at, false); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Observe(at.Add(time.Minute), boot, filter, trafficTestInterface(1100, 2200)); err != nil {
		t.Fatal(err)
	}
	if err = l.Save(context.Background(), at.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, trafficFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	store.Close()
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	v, err := restored.Observe(at.Add(2*time.Minute), boot, filter, trafficTestInterface(1150, 2300))
	if err != nil || *v.RXBytes != 150 || *v.TXBytes != 300 {
		t.Fatal(v, err)
	}
	if err = restored.Save(context.Background(), at.Add(2*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := store.readTrafficFile()
	if err != nil || snapshot.RX != 150 || snapshot.Interfaces[0].RX != 1150 {
		t.Fatal(snapshot, err)
	}
}
func TestTrafficCorruptAndUnsafeFilesPreserved(t *testing.T) {
	for _, kind := range []string{"corrupt", "permission", "symlink", "binding", "checksum"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			store, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			path := filepath.Join(dir, trafficFile)
			at := time.Now().UTC()
			boot := "11111111-1111-4111-8111-111111111111"
			filter := strings.Repeat("a", 64)
			l, _ := OpenTrafficLedger(store, "https://panel.test", "node")
			l.Observe(at, boot, filter, trafficTestInterface(1, 2))
			if err = l.Save(context.Background(), at, false); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "corrupt":
				err = os.WriteFile(path, []byte("not json"), 0600)
			case "permission":
				err = os.Chmod(path, 0644)
			case "symlink":
				target := filepath.Join(dir, "other")
				err = os.Rename(path, target)
				if err == nil {
					err = os.Symlink(target, path)
				}
			case "binding":
				snap, _ := l.Snapshot()
				snap.NodeID = "other"
				envelope, _ := encodeTraffic(snap)
				err = store.Write(trafficFile, envelope)
			case "checksum":
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(path, []byte(strings.Replace(string(raw), `"rx_bytes":0`, `"rx_bytes":9`, 1)), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := OpenTrafficLedger(store, "https://panel.test", "node")
			if err == nil || loaded == nil {
				t.Fatal("unsafe file accepted")
			}
			v, err := loaded.Observe(at.Add(time.Minute), boot, filter, trafficTestInterface(2, 3))
			if err == nil || v.RXBytes != nil || !slices.Contains(v.Quality, "unknown") {
				t.Fatal(v, err)
			}
			if err = loaded.Save(context.Background(), at.Add(time.Minute), true); err == nil {
				t.Fatal("unsafe file overwritten")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("original state modified")
			}
		})
	}
}
func TestTrafficSaveRetryAndPostRenameFailure(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	l, _ := OpenTrafficLedger(store, "https://panel.test", "node")
	at := time.Now().UTC()
	boot := "11111111-1111-4111-8111-111111111111"
	filter := strings.Repeat("a", 64)
	l.Observe(at, boot, filter, trafficTestInterface(100, 200))
	calls := 0
	l.write = func(name string, v any) error {
		calls++
		if calls == 1 {
			return errors.New("disk unavailable")
		}
		if err := store.Write(name, v); err != nil {
			return err
		}
		if calls == 2 {
			return errors.New("directory sync failed")
		}
		return nil
	}
	if err = l.Save(context.Background(), at, false); err == nil {
		t.Fatal("failure hidden")
	}
	v, err := l.Observe(at.Add(time.Second), boot, filter, trafficTestInterface(110, 220))
	if err != nil || !slices.Contains(v.Quality, "persist_failed") {
		t.Fatal(v, err)
	}
	if err = l.Save(context.Background(), at.Add(59*time.Second), false); err != nil || calls != 1 {
		t.Fatal("retry not throttled", calls, err)
	}
	if err = l.Save(context.Background(), at.Add(time.Minute), false); err == nil || calls != 2 {
		t.Fatal(err, calls)
	}
	v, err = l.Observe(at.Add(61*time.Second), boot, filter, trafficTestInterface(120, 240))
	if err != nil || *v.RXBytes != 20 {
		t.Fatal(v, err)
	}
	if err = l.Save(context.Background(), at.Add(2*time.Minute), false); err != nil || calls != 3 {
		t.Fatal("post-rename retry failed", calls, err)
	}
	v, err = l.Observe(at.Add(121*time.Second), boot, filter, trafficTestInterface(130, 260))
	if err != nil || slices.Contains(v.Quality, "persist_failed") || *v.RXBytes != 30 {
		t.Fatal(v, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = l.Save(ctx, at.Add(122*time.Second), true); !errors.Is(err, context.Canceled) || calls != 3 {
		t.Fatal(err, calls)
	}
	if err = l.Save(context.Background(), at.Add(122*time.Second), true); err != nil || calls != 4 {
		t.Fatal("final flush", err, calls)
	}
}

func TestTrafficExternalReplacementPreserved(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	l, _ := OpenTrafficLedger(store, "https://panel.test", "node")
	at := time.Now().UTC()
	boot := "11111111-1111-4111-8111-111111111111"
	filter := strings.Repeat("a", 64)
	if _, err = l.Observe(at, boot, filter, trafficTestInterface(100, 200)); err != nil {
		t.Fatal(err)
	}
	if err = l.Save(context.Background(), at, false); err != nil {
		t.Fatal(err)
	}
	// Even a structurally valid external replacement is not silently overwritten.
	replacement, _ := l.Snapshot()
	replacement.RX = 99
	envelope, _ := encodeTraffic(replacement)
	if err = store.Write(trafficFile, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Observe(at.Add(time.Minute), boot, filter, trafficTestInterface(200, 400)); err != nil {
		t.Fatal(err)
	}
	if err = l.Save(context.Background(), at.Add(time.Minute), false); !errors.Is(err, ErrTrafficFileChanged) {
		t.Fatal(err)
	}
	actual, _, err := store.readTrafficFile()
	if err != nil || actual.RX != 99 {
		t.Fatal("replacement overwritten", actual, err)
	}
}

func TestTrafficSaveDoesNotBlockObserveOrClearNewSample(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	l, _ := OpenTrafficLedger(store, "https://panel.test", "node")
	at := time.Now().UTC()
	boot := "11111111-1111-4111-8111-111111111111"
	filter := strings.Repeat("a", 64)
	if _, err = l.Observe(at, boot, filter, trafficTestInterface(100, 200)); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	l.write = func(name string, value any) error { close(entered); <-release; return store.Write(name, value) }
	done := make(chan error, 1)
	go func() { done <- l.Save(context.Background(), at, false) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("save did not start")
	}
	if err = l.Save(context.Background(), at, true); !errors.Is(err, ErrTrafficSaveInProgress) {
		t.Fatal(err)
	}
	observed := make(chan error, 1)
	go func() {
		_, err := l.Observe(at.Add(time.Second), boot, filter, trafficTestInterface(150, 300))
		observed <- err
	}()
	select {
	case err = <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("disk I/O blocked sampling")
	}
	close(release)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("save did not finish")
	}
	persisted, _, err := store.readTrafficFile()
	if err != nil || persisted.RX != 0 || persisted.Interfaces[0].RX != 100 {
		t.Fatal("mixed snapshot", persisted, err)
	}
	current, ok := l.Snapshot()
	if !ok || current.RX != 50 || current.Interfaces[0].RX != 150 {
		t.Fatal("new sample lost", current)
	}
	l.write = store.Write
	if err = l.Save(context.Background(), at.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	persisted, _, err = store.readTrafficFile()
	if err != nil || persisted.RX != 50 {
		t.Fatal("new sample marked clean by old save", persisted, err)
	}
}
