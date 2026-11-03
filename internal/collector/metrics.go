package collector

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Metrics reads bounded kernel counters and proc directory entries; it never shells out.
type Metrics struct{ root string }

func NewMetrics() *Metrics { return &Metrics{root: "/proc"} }
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
func parseNetwork(s string) (map[string][2]uint64, error) {
	return parseNetworkCounters(s, false)
}

func parseNetworkCounters(s string, includeLoopback bool) (map[string][2]uint64, error) {
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
		if name == "lo" && !includeLoopback {
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
		if (!includeLoopback && len(result) > 64) || len(result) > 4096 {
			return nil, fmt.Errorf("too many interfaces")
		}
	}
	return result, nil
}
