package collector

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Metrics reads bounded kernel counters, never shells out or scans a filesystem.
// One owner calls Sample; failed/reset counters discard their rate baseline.
type Metrics struct {
	root     string
	disk     func() (*v1.DiskMetrics, error)
	now      func() time.Time
	previous time.Time
	cpu      []uint64
	network  map[string][2]uint64
}

func NewMetrics() *Metrics { return &Metrics{root: "/proc", disk: rootDisk, now: time.Now} }
func (m *Metrics) read(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(m.root, name))
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 262145))
	if err != nil {
		return "", err
	}
	if len(b) > 262144 {
		return "", fmt.Errorf("kernel counters exceed limit")
	}
	return string(b), ctx.Err()
}
func number(s string) (float64, error) {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0, fmt.Errorf("invalid counter")
	}
	return n, nil
}
func (m *Metrics) Sample(ctx context.Context) v1.NodeMetrics {
	now := m.now()
	result := v1.NodeMetrics{SampledAt: now.UTC(), Errors: []string{}, Interfaces: []*v1.InterfaceMetrics{}}
	elapsed := now.Sub(m.previous).Seconds()
	validInterval := !m.previous.IsZero() && elapsed > 0 && elapsed <= 600
	if validInterval {
		result.IntervalSeconds = elapsed
	}
	if runtime.GOOS != "linux" && m.root == "/proc" {
		m.previous = now
		result.Errors = append(result.Errors, "unsupported_platform")
		return result
	}
	fail := func(s string) { result.Errors = append(result.Errors, s+"_unavailable") }
	stat, err := m.read(ctx, "stat")
	cpu, cores, e := parseCPU(stat)
	if err != nil || e != nil {
		fail("cpu")
		m.cpu = nil
	} else {
		result.CPU = &v1.CPUMetrics{LogicalCPUs: cores}
		if validInterval && len(m.cpu) == len(cpu) {
			var total, idle float64
			ok := true
			for i, v := range cpu {
				if v < m.cpu[i] {
					ok = false
					break
				}
				delta := float64(v - m.cpu[i])
				total += delta
				if i == 3 || i == 4 {
					idle += delta
				}
			}
			if ok && total > 0 {
				usage := 100 * float64(total-idle) / float64(total)
				result.CPU.UsagePercent = &usage
			}
		}
		m.cpu = cpu
	}
	mem, err := m.read(ctx, "meminfo")
	result.Memory, e = parseMemory(mem)
	if err != nil || e != nil {
		result.Memory = nil
		fail("memory")
	}
	load, err := m.read(ctx, "loadavg")
	fields := strings.Fields(load)
	if err == nil && len(fields) >= 3 {
		a, e1 := number(fields[0])
		b, e2 := number(fields[1])
		c, e3 := number(fields[2])
		if e1 == nil && e2 == nil && e3 == nil {
			result.Load = &v1.LoadMetrics{One: a, Five: b, Fifteen: c}
		}
	}
	if result.Load == nil {
		fail("load")
	}
	up, err := m.read(ctx, "uptime")
	fields = strings.Fields(up)
	if err == nil && len(fields) >= 1 {
		n, e := number(fields[0])
		if e == nil {
			result.UptimeSeconds = &n
		}
	}
	if result.UptimeSeconds == nil {
		fail("uptime")
	}
	if ctx.Err() == nil {
		result.Disk, err = m.disk()
	} else {
		err = ctx.Err()
	}
	if err != nil {
		result.Disk = nil
		fail("disk")
	}
	net, err := m.read(ctx, "net/dev")
	counters, e := parseNetwork(net)
	if err != nil || e != nil {
		fail("network")
		m.network = nil
	} else {
		result.Interfaces = make([]*v1.InterfaceMetrics, 0, len(counters))
		names := make([]string, 0, len(counters))
		for name := range counters {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			v := counters[name]
			i := &v1.InterfaceMetrics{Name: name, RXBytes: float64(v[0]), TXBytes: float64(v[1])}
			if old, ok := m.network[name]; ok && validInterval && v[0] >= old[0] && v[1] >= old[1] {
				rx := float64(v[0]-old[0]) / elapsed
				tx := float64(v[1]-old[1]) / elapsed
				i.RXBytesPerSecond = &rx
				i.TXBytesPerSecond = &tx
			}
			result.Interfaces = append(result.Interfaces, i)
		}
		m.network = counters
	}
	m.previous = now
	return result
}
func parseCPU(s string) ([]uint64, uint64, error) {
	var values []uint64
	var cores uint64
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if f[0] == "cpu" {
			if len(f) < 9 {
				return nil, 0, fmt.Errorf("incomplete CPU counters")
			}
			values = make([]uint64, 8)
			for i := range values {
				v, e := strconv.ParseUint(f[i+1], 10, 64)
				if e != nil {
					return nil, 0, e
				}
				values[i] = v
			}
		} else if strings.HasPrefix(f[0], "cpu") {
			if _, e := strconv.ParseUint(strings.TrimPrefix(f[0], "cpu"), 10, 32); e == nil {
				cores++
			}
		}
	}
	if len(values) != 8 || cores == 0 {
		return nil, 0, fmt.Errorf("missing CPU counters")
	}
	return values, cores, nil
}
func parseMemory(s string) (*v1.MemoryMetrics, error) {
	values := map[string]float64{}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[2] != "kB" {
			continue
		}
		n, e := number(f[1])
		if e != nil {
			return nil, e
		}
		values[strings.TrimSuffix(f[0], ":")] = n * 1024
	}
	for _, key := range []string{"MemTotal", "MemAvailable", "SwapTotal", "SwapFree"} {
		if _, ok := values[key]; !ok {
			return nil, fmt.Errorf("missing memory counter")
		}
	}
	total, available, swap, free := values["MemTotal"], values["MemAvailable"], values["SwapTotal"], values["SwapFree"]
	if total <= 0 || available > total || free > swap {
		return nil, fmt.Errorf("invalid memory counters")
	}
	return &v1.MemoryMetrics{TotalBytes: total, AvailableBytes: available, UsedBytes: total - available, SwapTotalBytes: swap, SwapUsedBytes: swap - free}, nil
}
func parseNetwork(s string) (map[string][2]uint64, error) {
	result := map[string][2]uint64{}
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "Inter-") || !strings.Contains(lines[1], "bytes") {
		return nil, fmt.Errorf("missing network headers")
	}
	for _, line := range lines[2:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if !ok || len(f) != 16 || name == "" || len(name) > 64 {
			return nil, fmt.Errorf("invalid network counters")
		}
		if name == "lo" {
			continue
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("duplicate interface")
		}
		rx, e1 := strconv.ParseUint(f[0], 10, 64)
		tx, e2 := strconv.ParseUint(f[8], 10, 64)
		if e1 != nil || e2 != nil {
			return nil, fmt.Errorf("invalid network bytes")
		}
		result[name] = [2]uint64{rx, tx}
		if len(result) > 64 {
			return nil, fmt.Errorf("too many interfaces")
		}
	}
	return result, nil
}
