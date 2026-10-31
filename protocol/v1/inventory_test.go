package v1

import (
	"strings"
	"testing"
	"time"
)

func TestInventoryValidation(t *testing.T) {
	now := time.Now().UTC()
	good := NodeInventory{SampledAt: now, OSName: "Ubuntu 24.04", PublicIpv4: "8.8.8.8", Errors: []string{"public_ipv6_unavailable"}}
	if err := good.Validate(now); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*NodeInventory){
		func(v *NodeInventory) { v.SampledAt = now.Add(-time.Hour) }, func(v *NodeInventory) { v.SampledAt = now.Add(time.Hour) },
		func(v *NodeInventory) { v.Errors = nil }, func(v *NodeInventory) { v.Errors = []string{"unknown"} },
		func(v *NodeInventory) { v.Errors = []string{"public_ipv6_unavailable", "public_ipv6_unavailable"} },
		func(v *NodeInventory) { v.PublicIpv6 = "2606:4700:4700::1111" }, func(v *NodeInventory) { v.PublicIpv4 = "::ffff:8.8.8.8" },
		func(v *NodeInventory) { v.OSName = strings.Repeat("x", 257) }, func(v *NodeInventory) { v.OSName = "bad\nname" },
	} {
		v := good
		mutate(&v)
		if v.Validate(now) == nil {
			t.Fatalf("accepted %+v", v)
		}
	}
}
