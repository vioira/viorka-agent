package config

import "testing"

func TestTCPProbeValidation(t *testing.T) {
	valid := TCPProbe{Targets: []string{"example.com:443", "1.1.1.1:443", "[2606:4700:4700::1111]:443"}, IntervalSeconds: 60, TimeoutMS: 1000, WindowSeconds: 600}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"https://example.com:443", "localhost:80", "127.0.0.1:80", "10.0.0.1:80", "169.254.169.254:80", "100.64.0.1:80", "example.com:0", "example.com:65536", "[::1]:80", "bad..example:80", "example.com:443/path"} {
		cfg := valid
		cfg.Targets = []string{target}
		if cfg.Validate() == nil {
			t.Errorf("accepted %s", target)
		}
	}
	for _, change := range []func(*TCPProbe){
		func(p *TCPProbe) { p.Targets = append(p.Targets, "example.net:443") },
		func(p *TCPProbe) { p.Targets = []string{"example.com:443", "EXAMPLE.COM.:443"} },
		func(p *TCPProbe) { p.IntervalSeconds = 29 }, func(p *TCPProbe) { p.TimeoutMS = 1001 },
		func(p *TCPProbe) { p.WindowSeconds = 3601 }, func(p *TCPProbe) { p.WindowSeconds = 59 },
		func(p *TCPProbe) { p.IntervalSeconds = 601 },
	} {
		cfg := valid
		change(&cfg)
		if cfg.Validate() == nil {
			t.Error("accepted invalid bounds")
		}
	}
}
