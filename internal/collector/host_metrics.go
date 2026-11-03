package collector

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func (m *Metrics) sampleSockets(ctx context.Context) (uint64, uint64, error) {
	data, err := m.read(ctx, "net/sockstat")
	tcp, udp, parseErr := parseSockets(data, "")
	v6, err6 := m.read(ctx, "net/sockstat6")
	if err6 == nil {
		t, u, e := parseSockets(v6, "6")
		tcp += t
		udp += u
		err6 = e
	} else if os.IsNotExist(err6) {
		err6 = nil
	}
	if err != nil || parseErr != nil || err6 != nil || tcp > 1048576 || udp > 1048576 {
		return 0, 0, fmt.Errorf("socket counters unavailable")
	}
	return tcp, udp, nil
}

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
