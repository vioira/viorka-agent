package v1

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTelemetrySemanticContradictions(t *testing.T) {
	fast := func(v map[string]any) map[string]any { return v["fast"].([]any)[0].(map[string]any) }
	full := func(v map[string]any) map[string]any { return v["full"].([]any)[0].(map[string]any) }
	iface := func(v map[string]any) map[string]any { return fast(v)["interfaces"].([]any)[0].(map[string]any) }
	tests := []struct {
		name   string
		change func(map[string]any)
	}{
		{"known CPU with error", func(v map[string]any) { f := fast(v); f["errors"] = append(f["errors"].([]any), "cpu_unavailable") }},
		{"CPU zero elapsed", func(v map[string]any) { fast(v)["elapsed_seconds"] = 0 }},
		{"missing swap", func(v map[string]any) { delete(fast(v), "swap_total") }},
		{"RAM exceeds total", func(v map[string]any) { fast(v)["ram_used"] = json.Number("1073741825") }},
		{"uptime with error", func(v map[string]any) { fast(v)["uptime_seconds"] = 1 }},
		{"sockets with error", func(v map[string]any) { fast(v)["tcp_inuse"] = 1 }},
		{"duplicate interface", func(v map[string]any) { f := fast(v); f["interfaces"] = append(f["interfaces"].([]any), iface(v)) }},
		{"baseline with rate", func(v map[string]any) { iface(v)["rx_bytes_per_second"] = 1 }},
		{"missing rates without quality", func(v map[string]any) { iface(v)["quality"] = []string{} }},
		{"aggregate network overflow", func(v map[string]any) {
			iface(v)["rx_bytes"] = json.Number("9223372036854775807")
			fast(v)["interfaces"] = append(fast(v)["interfaces"].([]any), map[string]any{"name": "eth1", "ifindex": 3, "rx_bytes": 1, "tx_bytes": 0, "quality": []string{"baseline"}})
		}},
		{"full known load with error", func(v map[string]any) { full(v)["load_1"] = 1 }},
		{"full known processes with error", func(v map[string]any) { full(v)["processes"] = 1 }},
		{"full known OS with error", func(v map[string]any) { full(v)["os_name"] = "Linux" }},
		{"disk with unavailable", func(v map[string]any) { full(v)["disk_total"] = 1 }},
	}
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := schemaFixture(t)
			tc.change(v)
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateTelemetryJSON(raw, "TelemetryBatch", true); err != nil {
				t.Fatalf("test must reach semantics: %v", err)
			}
			if _, err = DecodeTelemetryBatch(raw, "node-demo", now, 60, true); err == nil {
				t.Fatal("contradiction admitted")
			}
		})
	}
}

func TestTelemetrySettingsResultSemantics(t *testing.T) {
	for _, tc := range []struct {
		r     TelemetrySettingsResult
		valid bool
	}{
		{TelemetrySettingsResult{Revision: 2, AppliedRevision: 2, State: "applied"}, true},
		{TelemetrySettingsResult{Revision: 2, AppliedRevision: 1, State: "applied"}, false},
		{TelemetrySettingsResult{Revision: 2, AppliedRevision: 1, State: "failed", ErrorCode: "persist_failed"}, true},
		{TelemetrySettingsResult{Revision: 2, State: "failed", ErrorCode: "arbitrary"}, false},
	} {
		if (tc.r.Validate() == nil) != tc.valid {
			t.Fatalf("unexpected validation: %+v", tc)
		}
	}
}

func TestTelemetrySemanticCompleteGroups(t *testing.T) {
	u := func(n uint64) *uint64 { return &n }
	n := func(n float64) *float64 { return &n }
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	fixture := func() TelemetryBatch {
		raw, _ := json.Marshal(schemaFixture(t))
		var b TelemetryBatch
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		f := b.Fast[0]
		f.Errors = []string{}
		f.UptimeSeconds = n(60)
		f.TcpInuse = u(0)
		f.UdpInuse = u(0)
		f.Traffic = &TelemetryTraffic{MonthStart: now.Add(-24 * time.Hour), RXBytes: u(10), TXBytes: u(20), FilterRevision: f.FilterRevision, Quality: []string{}}
		g := b.Full[0]
		g.Errors = []string{}
		g.Load1 = n(0)
		g.Load5 = n(0)
		g.Load15 = n(0)
		g.Processes = u(1)
		g.OSName = "Linux"
		g.DiskTotal = u(100)
		g.DiskUsed = u(20)
		g.DiskAvailable = u(70)
		g.DiskIo = &TelemetryDiskIO{ReadBytesPerSecond: n(0), WriteBytesPerSecond: n(0), ReadIops: n(0), WriteIops: n(0), UtilPercent: n(0), Quality: []string{}}
		b.PublicIp = []*TelemetryPublicIP{{Sequence: 1, SampledAt: now, Ipv4: "8.8.8.8", Errors: []string{"ipv6_unavailable"}}}
		return b
	}
	b := fixture()
	raw, _ := json.Marshal(b)
	if _, err := DecodeTelemetryBatch(raw, "node-demo", now, 60, true); err != nil {
		t.Fatal("complete fixture", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*TelemetryBatch)
	}{
		{"wrong month", func(b *TelemetryBatch) { b.Fast[0].Traffic.MonthStart = now }},
		{"different filter", func(b *TelemetryBatch) {
			b.Fast[0].Traffic.FilterRevision = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}},
		{"unknown with counters", func(b *TelemetryBatch) { b.Fast[0].Traffic.Quality = []string{"unknown"} }},
		{"incomplete capacity", func(b *TelemetryBatch) { b.Full[0].DiskTotal = nil }},
		{"reserved blocks overflow", func(b *TelemetryBatch) { *b.Full[0].DiskAvailable = 81 }},
		{"used exceeds total", func(b *TelemetryBatch) { *b.Full[0].DiskUsed = 101 }},
		{"OS control text", func(b *TelemetryBatch) { b.Full[0].OSName = "Linux\n" }},
		{"await without operations", func(b *TelemetryBatch) { b.Full[0].DiskIo.ReadAwaitMs = n(1) }},
		{"disk baseline with rates", func(b *TelemetryBatch) { b.Full[0].DiskIo.Quality = []string{"baseline"} }},
		{"missing disk summary", func(b *TelemetryBatch) { b.Full[0].DiskIo = nil }},
		{"private public IP", func(b *TelemetryBatch) { b.PublicIp[0].Ipv4 = "10.1.2.3" }},
		{"IP error with value", func(b *TelemetryBatch) { b.PublicIp[0].Errors = append(b.PublicIp[0].Errors, "ipv4_unavailable") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := fixture()
			tc.change(&b)
			raw, _ := json.Marshal(b)
			if err := ValidateTelemetryJSON(raw, "TelemetryBatch", true); err != nil {
				t.Fatalf("test must reach semantics: %v", err)
			}
			if _, err := DecodeTelemetryBatch(raw, "node-demo", now, 60, true); err == nil {
				t.Fatal("contradiction admitted")
			}
		})
	}
}
