package collector

import "testing"

func TestLinuxCPUCoreObservation(t *testing.T) {
	count, err := LogicalCPUs()
	if err != nil || count == 0 {
		t.Fatal(count, err)
	}
	t.Logf("observed %d logical CPUs", count)
}
