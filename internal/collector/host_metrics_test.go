package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostMetrics(t *testing.T) {
	m, write := fixtureMetrics(t)
	write("diskstats", "8 0 sda 1 0 20 0 2 0 40 0 0 0 0\n")
	write("net/sockstat", "sockets: used 20\nTCP: inuse 4 orphan 0 tw 10\nUDP: inuse 2\n")
	write("net/sockstat6", "TCP6: inuse 3\nUDP6: inuse 1\n")
	for _, name := range []string{"1", "22"} {
		if e := os.Mkdir(filepath.Join(m.root, name), 0700); e != nil {
			t.Fatal(e)
		}
	}
	first := m.Sample(context.Background()).Host
	if len(first.Errors) != 0 || *first.Processes != 2 || *first.TcpSockets != 7 || *first.UdpSockets != 3 || first.DiskIo[0].ReadBytes != 10240 || first.DiskIo[0].ReadBytesPerSecond != nil {
		t.Fatalf("first: %+v", first)
	}
	now := m.now().Add(10 * time.Second)
	m.now = func() time.Time { return now }
	write("diskstats", "8 0 sda 1 0 30 0 2 0 60 0 0 0 0\n")
	second := m.Sample(context.Background()).Host
	if *second.DiskIo[0].ReadBytesPerSecond != 512 || *second.DiskIo[0].WriteBytesPerSecond != 1024 {
		t.Fatal(second.DiskIo[0])
	}
	now = now.Add(10 * time.Second)
	write("diskstats", "8 0 sda 1 0 1 0 2 0 2 0 0 0 0\n")
	if m.Sample(context.Background()).Host.DiskIo[0].ReadBytesPerSecond != nil {
		t.Fatal("reset produced a rate")
	}
	write("diskstats", "broken")
	write("net/sockstat6", "broken")
	bad := m.Sample(context.Background()).Host
	if len(bad.Errors) != 2 || len(bad.DiskIo) != 0 || bad.TcpSockets != nil || bad.UdpSockets != nil {
		t.Fatal(bad)
	}
	write("diskstats", "8 0 sda 1 0 30 0 2 0 60 0 0 0 0\n")
	now = now.Add(time.Second)
	if m.Sample(context.Background()).Host.DiskIo[0].ReadBytesPerSecond != nil {
		t.Fatal("recovery reused baseline")
	}
	now = now.Add(time.Hour)
	if m.Sample(context.Background()).Host.DiskIo[0].ReadBytesPerSecond != nil {
		t.Fatal("long gap produced rate")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if h := m.Sample(ctx).Host; len(h.Errors) != 3 || h.Processes != nil {
		t.Fatal(h)
	}
}

func TestHostCounterValidation(t *testing.T) {
	for _, s := range []string{"TCP: inuse -1\nUDP: inuse 0", "TCP: inuse 1", "TCP: inuse 1\nTCP: inuse 2\nUDP: inuse 0", "TCP: inuse 1048577\nUDP: inuse 0"} {
		if _, _, e := parseSockets(s, ""); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"8 0 sda", "8 0 sda 0 0 -1 0 0 0 0 0 0 0 0", "8 0 sda 0 0 1 0 0 0 1 0 0 0 0\n8 0 sda 0 0 1 0 0 0 1 0 0 0 0"} {
		if _, _, e := parseDiskIO(s); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

func TestHostMissingIPv6AndBounds(t *testing.T) {
	m, write := fixtureMetrics(t)
	write("net/sockstat", "TCP: inuse 0\nUDP: inuse 0\n")
	write("diskstats", "") // Empty is valid in a namespace without block devices.
	h := m.Sample(context.Background()).Host
	if h.TcpSockets == nil || *h.TcpSockets != 0 || h.UdpSockets == nil || len(h.Errors) != 0 {
		t.Fatal(h)
	}
	var disks strings.Builder
	for i := 0; i < 65; i++ {
		fmt.Fprintf(&disks, "8 %d disk%d 0 0 0 0 0 0 0 0 0 0 0\n", i, i)
	}
	write("diskstats", disks.String())
	h = m.Sample(context.Background()).Host
	if len(h.DiskIo) != 0 || len(h.Errors) != 1 || h.Errors[0] != "disk_io_unavailable" {
		t.Fatal(h)
	}
	write("diskstats", strings.Repeat(" ", 262145))
	h = m.Sample(context.Background()).Host
	if len(h.Errors) != 1 {
		t.Fatal("oversized diskstats accepted")
	}
}
