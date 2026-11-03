package config

import "testing"

func TestNetworkFilterPrecedenceAndRevision(t *testing.T) {
	f := NetworkFilter{Include: []string{"wg*", "lo"}, Exclude: []string{"wg-private", "eth9"}}
	for name, want := range map[string]bool{"eth0": true, "ens3": true, "lo": true, "wg0": true, "wg-private": false, "eth9": false, "docker0": false, "veth123": false, "br0": false, "tun0": false, "tap0": false, "tailscale0": false, "zerotier0": false} {
		if f.Includes(name) != want {
			t.Fatal(name)
		}
	}
	a, err := f.Revision()
	if err != nil || len(a) != 64 {
		t.Fatal(a, err)
	}
	f.Include = []string{"lo", "wg*"}
	b, _ := f.Revision()
	if a != b {
		t.Fatal("order changed revision")
	}
	f.Exclude = []string{"eth9"}
	b, _ = f.Revision()
	if a == b {
		t.Fatal("scope did not change revision")
	}
	a, _ = (NetworkFilter{}).Revision()
	b, _ = (NetworkFilter{Include: []string{}, Exclude: []string{}}).Revision()
	if a != b {
		t.Fatal("nil/empty changed revision")
	}
}
func TestNetworkFilterRejectsUnsafePatterns(t *testing.T) {
	for _, p := range []string{"", "eth?", "eth[0]", "*eth", "eth**", "eth/0", "eth 0", "eth\n", "$(id)", "网卡"} {
		if (NetworkFilter{Include: []string{p}}).Validate() == nil {
			t.Fatal(p)
		}
	}
	if (NetworkFilter{Exclude: []string{"eth0", "eth0"}}).Validate() == nil {
		t.Fatal("duplicates")
	}
	if (NetworkFilter{Include: make([]string, 65)}).Validate() == nil {
		t.Fatal("oversized list")
	}
}
