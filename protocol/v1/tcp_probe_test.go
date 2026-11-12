package v1

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestTCPProbeContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/node.metrics.v3.tcp_probe.json")
	if err != nil {
		t.Fatal(err)
	}
	var e Envelope
	if err = json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	var b TelemetryBatch
	if err = json.Unmarshal(e.Payload, &b); err != nil {
		t.Fatal(err)
	}
	now := b.Full[0].SampledAt
	if _, err = DecodeTelemetryBatch(e.Payload, b.NodeID, now, 60, true); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*TelemetryTCPProbe){
		func(p *TelemetryTCPProbe) { p.Failures = p.Samples + 1 },
		func(p *TelemetryTCPProbe) { p.MedianMs = nil },
		func(p *TelemetryTCPProbe) { p.FailurePercent = nil },
		func(p *TelemetryTCPProbe) { v := 99.0; p.FailurePercent = &v },
		func(p *TelemetryTCPProbe) { p.SampledAt = now.Add(time.Second) },
		func(p *TelemetryTCPProbe) { p.Target = "https://example.com" },
	} {
		copy := *b.Full[0].TcpProbes[0]
		change(&copy)
		full := *b.Full[0]
		full.TcpProbes = []*TelemetryTCPProbe{&copy}
		bad := b
		bad.Full = []*TelemetryFull{&full}
		if bad.ValidateIdentity(b.NodeID, now, 60) == nil {
			t.Error("invalid probe accepted")
		}
	}
	b.Full[0].TcpProbes = nil
	old, _ := json.Marshal(b)
	if _, err = DecodeTelemetryBatch(old, b.NodeID, now, 60, true); err != nil {
		t.Fatal("unextended v3 rejected", err)
	}
}
