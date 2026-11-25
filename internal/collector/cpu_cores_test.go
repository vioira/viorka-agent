package collector

import (
	"strings"
	"testing"
)

func TestLogicalCPUs(t *testing.T) {
	for raw, want := range map[string]uint64{"0": 1, "0-2,8,10-12": 7, "12-10": 0, "2,2": 0, "": 0, "0-65536": 0, "0-65535": 65536, "1-4,4-6": 0} {
		got, err := parseLogicalCPUs(strings.NewReader("Name: test\nCpus_allowed_list:\t" + raw + "\n"))
		if got != want || (err != nil) != (want == 0) {
			t.Fatalf("%q: %d %v", raw, got, err)
		}
	}
}
