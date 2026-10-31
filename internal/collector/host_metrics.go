package collector

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Disk entries are reported separately: partitions and stacked devices must not
// be summed as host throughput. Kernel diskstats sectors are always 512 bytes.
func (m *Metrics) sampleHost(ctx context.Context, elapsed float64, valid bool) *v1.HostMetrics {
	h := &v1.HostMetrics{DiskIo: []*v1.DiskIOMetrics{}, Errors: []string{}}
	data, err := m.read(ctx, "diskstats")
	counters, devices, parseErr := parseDiskIO(data)
	if err != nil || parseErr != nil {
		h.Errors = append(h.Errors, "disk_io_unavailable")
		m.diskIO = nil
	} else {
		for _, device := range devices {
			v := counters[device]
			item := &v1.DiskIOMetrics{Device: device, ReadBytes: float64(v[0]) * 512, WriteBytes: float64(v[1]) * 512}
			if old, ok := m.diskIO[device]; ok && valid && v[0] >= old[0] && v[1] >= old[1] {
				r, w := float64(v[0]-old[0])*512/elapsed, float64(v[1]-old[1])*512/elapsed
				item.ReadBytesPerSecond, item.WriteBytesPerSecond = &r, &w
			}
			h.DiskIo = append(h.DiskIo, item)
		}
		m.diskIO = counters
	}
	data, err = m.read(ctx, "net/sockstat")
	tcp, udp, parseErr := parseSockets(data, "")
	v6, err6 := m.read(ctx, "net/sockstat6")
	if err6 == nil {
		t, u, e := parseSockets(v6, "6")
		tcp, udp = tcp+t, udp+u
		err6 = e
	} else if os.IsNotExist(err6) {
		// Kernels built without IPv6 have no sockstat6.
		err6 = nil
	}
	if err != nil || parseErr != nil || err6 != nil || tcp > 1048576 || udp > 1048576 {
		h.Errors = append(h.Errors, "sockets_unavailable")
	} else {
		h.TcpSockets, h.UdpSockets = &tcp, &udp
	}
	count, err := m.processCount(ctx)
	if err != nil {
		h.Errors = append(h.Errors, "processes_unavailable")
	} else {
		h.Processes = &count
	}
	return h
}

func parseDiskIO(data string) (map[string][2]uint64, []string, error) {
	counters := map[string][2]uint64{}
	var devices []string
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 14 || len(f[2]) > 64 {
			return nil, nil, fmt.Errorf("invalid disk counters")
		}
		// Include the kernel device identity so replacement under a different major/
		// minor does not reuse its baseline. Device names remain human readable.
		name := f[0] + ":" + f[1] + ":" + f[2]
		if len(name) > 64 {
			return nil, nil, fmt.Errorf("device identity too long")
		}
		if _, ok := counters[name]; ok {
			return nil, nil, fmt.Errorf("duplicate disk")
		}
		for _, x := range []string{f[0], f[1], f[5], f[9]} {
			if _, e := strconv.ParseUint(x, 10, 64); e != nil {
				return nil, nil, e
			}
		}
		r, _ := strconv.ParseUint(f[5], 10, 64)
		w, _ := strconv.ParseUint(f[9], 10, 64)
		counters[name] = [2]uint64{r, w}
		devices = append(devices, name)
		if len(devices) > 64 {
			return nil, nil, fmt.Errorf("too many disk devices")
		}
	}
	return counters, devices, nil
}

// inuse counts allocated TCP/UDP sockets (IPv4 plus IPv6), including listeners;
// TCP TIME_WAIT sockets are excluded. UDP has no TCP-style connection state.
func parseSockets(data, suffix string) (uint64, uint64, error) {
	values := map[string]uint64{}
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || (f[0] != "TCP"+suffix+":" && f[0] != "UDP"+suffix+":") {
			continue
		}
		if _, ok := values[f[0]]; ok {
			return 0, 0, fmt.Errorf("duplicate socket counters")
		}
		if len(f) < 3 || f[1] != "inuse" {
			return 0, 0, fmt.Errorf("missing socket counter")
		}
		n, e := strconv.ParseUint(f[2], 10, 64)
		if e != nil || n > 1048576 {
			return 0, 0, fmt.Errorf("invalid socket counter")
		}
		values[f[0]] = n
	}
	tcp, t := values["TCP"+suffix+":"]
	udp, u := values["UDP"+suffix+":"]
	if !t || !u {
		return 0, 0, fmt.Errorf("missing socket counters")
	}
	return tcp, udp, nil
}

// Enumerate only top-level proc entries in bounded batches, never process files
// or thread trees. This is a best-effort instantaneous visible process count.
func (m *Metrics) processCount(ctx context.Context) (uint64, error) {
	f, err := os.Open(m.root)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var count uint64
	for entries := 0; ; {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		names, err := f.Readdirnames(256)
		entries += len(names)
		if entries > 65536 {
			return 0, fmt.Errorf("proc entry limit exceeded")
		}
		for _, name := range names {
			n, e := strconv.ParseUint(name, 10, 64)
			if e == nil && n > 0 {
				count++
			}
		}
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return 0, err
		}
	}
}
