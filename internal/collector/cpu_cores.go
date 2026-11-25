package collector

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// LogicalCPUs reads the effective Linux affinity mask, including cpuset restrictions.
func LogicalCPUs() (uint64, error) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return parseLogicalCPUs(f)
}

func parseLogicalCPUs(r io.Reader) (uint64, error) {
	scan := bufio.NewScanner(io.LimitReader(r, 1<<20))
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		raw, ok := strings.CutPrefix(scan.Text(), "Cpus_allowed_list:")
		if !ok {
			continue
		}
		var count uint64
		var previous uint64
		for _, part := range strings.Split(strings.TrimSpace(raw), ",") {
			left, right, ranged := strings.Cut(part, "-")
			lo, err := strconv.ParseUint(left, 10, 32)
			if err != nil || (count > 0 && lo <= previous) {
				return 0, fmt.Errorf("invalid CPU list")
			}
			hi := lo
			if ranged {
				hi, err = strconv.ParseUint(right, 10, 32)
				if err != nil || hi < lo {
					return 0, fmt.Errorf("invalid CPU range")
				}
			}
			count += hi - lo + 1
			if count > 65536 {
				return 0, fmt.Errorf("CPU count exceeds protocol maximum")
			}
			previous = hi
		}
		return count, nil
	}
	if err := scan.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("CPU affinity unavailable")
}
