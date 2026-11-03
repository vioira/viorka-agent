package collector

import (
	"fmt"

	"os"
	"path/filepath"

	"testing"
)

func fixtureMetrics(t *testing.T) (*Metrics, func(string, string)) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", "cpu 100 0 100 800 0 0 0 0 999 0\ncpu0 1\ncpu1 1\n")
	write("meminfo", "MemTotal: 1000 kB\nMemAvailable: 600 kB\nSwapTotal: 200 kB\nSwapFree: 100 kB\n")
	write("loadavg", "1.2 2.3 3.4 1/100 2\n")
	write("uptime", "1234.5 1000\n")
	write("net/dev", netFixture(1000, 2000))
	return &Metrics{root: root}, write
}
func netFixture(rx, tx uint64) string {
	return fmt.Sprintf("Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n lo: 99 0 0 0 0 0 0 0 99 0 0 0 0 0 0 0\n eth0: %d 0 0 0 0 0 0 0 %d 0 0 0 0 0 0 0\n", rx, tx)
}

func mountLine(id int, device, point, kind string) string {
	return fmt.Sprintf("%d 1 %s / %s rw - %s /dev/disk rw\n", id, device, point, kind)
}
