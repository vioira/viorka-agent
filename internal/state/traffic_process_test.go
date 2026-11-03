package state

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise durable baseline ownership across address spaces, rather than
// reconstructing another ledger object in the same process. Counters and UTC
// time are controlled inputs; this does not claim a real NIC reboot test.
func TestTrafficLedgerAcrossProcesses(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "state")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, stage := range []string{"seed", "recover", "duplicate"} {
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestTrafficLedgerProcessHelper$", "-test.timeout=10s")
		cmd.Env = append(os.Environ(), "VIORKA_TEST_TRAFFIC_PROCESS_STAGE="+stage, "VIORKA_TEST_TRAFFIC_PROCESS_DIR="+dir)
		cmd.WaitDelay = time.Second
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", stage, err, output)
		}
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, _, err := store.readTrafficFile()
	if err != nil || snapshot.RX != 150 || snapshot.TX != 300 || len(snapshot.Interfaces) != 1 || snapshot.Interfaces[0].RX != 1150 || snapshot.Interfaces[0].TX != 2300 {
		t.Fatalf("durable totals/baseline: %+v %v", snapshot, err)
	}
	info, err := os.Stat(filepath.Join(dir, trafficFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("ledger permissions", info, err)
	}
}

func TestTrafficLedgerProcessHelper(t *testing.T) {
	stage := os.Getenv("VIORKA_TEST_TRAFFIC_PROCESS_STAGE")
	if stage == "" {
		t.Skip("subprocess only")
	}
	dir := os.Getenv("VIORKA_TEST_TRAFFIC_PROCESS_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute test directory required")
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately no final close/save defer: each child exits after its explicit
	// durable Save, so the next process must rely exclusively on traffic.dat.
	ledger, err := OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	boot, filter := "11111111-1111-4111-8111-111111111111", strings.Repeat("a", 64)
	observe := func(minute int, rx, tx, wantRX, wantTX uint64) {
		t.Helper()
		value, err := ledger.Observe(at.Add(time.Duration(minute)*time.Minute), boot, filter, trafficTestInterface(rx, tx))
		if err != nil || value.RXBytes == nil || value.TXBytes == nil || *value.RXBytes != wantRX || *value.TXBytes != wantTX {
			t.Fatalf("stage=%s observation=%+v error=%v", stage, value, err)
		}
	}
	minute := 0
	switch stage {
	case "seed":
		observe(0, 1000, 2000, 0, 0)
		observe(1, 1100, 2200, 100, 200)
		minute = 1
	case "recover":
		observe(2, 1150, 2300, 150, 300)
		minute = 2
	case "duplicate":
		observe(3, 1150, 2300, 150, 300)
		minute = 3
	default:
		t.Fatal(fmt.Errorf("unknown traffic test stage"))
	}
	if err := ledger.Save(context.Background(), at.Add(time.Duration(minute)*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
