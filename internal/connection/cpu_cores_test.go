package connection

import (
	"errors"
	"testing"
	"time"
)

func TestCPUCoreCadenceAndReconciliation(t *testing.T) {
	var sample cpuCoreSample
	start := time.Now()
	reads := 0
	for _, tc := range []struct {
		minute             int
		known, count, want uint64
		read, fail         bool
	}{
		{0, 0, 2, 2, true, false}, {1, 2, 0, 0, false, false}, {2, 0, 0, 2, false, false},
		{60, 2, 2, 0, true, false}, {120, 2, 0, 0, true, true}, {129, 2, 4, 0, false, false},
		{130, 2, 4, 4, true, false}, {131, 4, 0, 0, false, false},
	} {
		before := reads
		got, err := sample.observe(start.Add(time.Duration(tc.minute)*time.Minute), tc.known, func() (uint64, error) {
			reads++
			if tc.fail {
				return 0, errors.New("no affinity")
			}
			return tc.count, nil
		})
		if (err != nil) != tc.fail || (reads != before) != tc.read || (got == nil) != (tc.want == 0) || (got != nil && *got != tc.want) {
			t.Fatalf("minute %d: count %v, reads %d, err %v", tc.minute, got, reads, err)
		}
	}
}
