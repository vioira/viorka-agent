package v1

import (
	"bytes"

	"os"
	"testing"
	"time"
)

func TestTelemetrySettingsIndependentCadence(t *testing.T) {
	s := DefaultTelemetrySettings()
	if s.Validate() != nil || s.CollectionSeconds != 30 {
		t.Fatal(s)
	}
	s.CollectionSeconds = 60
	s.HttpReportSeconds = 30
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []uint64{0, 61} {
		s.CollectionSeconds = c
		if s.Validate() == nil {
			t.Fatal("accepted invalid C", c)
		}
	}

}

func TestTelemetryContextAdmission(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	batch := TelemetryBatch{NodeID: "node-1", RunID: "11111111-1111-4111-8111-111111111111", BatchID: "batch-1", Fast: []*TelemetryFast{}, Full: []*TelemetryFull{}, PublicIp: []*TelemetryPublicIP{}}
	fast := &TelemetryFast{Sequence: 1, SampledAt: now.Add(-495 * time.Second), FilterRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	batch.Fast = append(batch.Fast, fast)
	if err := batch.ValidateIdentity("node-1", now, 180); err != nil {
		t.Fatal(err)
	}
	if batch.ValidateIdentity("other-node", now, 180) == nil {
		t.Fatal("cross node accepted")
	}
	if batch.ValidateIdentity("node-1", now.Add(time.Nanosecond), 180) == nil {
		t.Fatal("expired sample accepted")
	}
	fast.SampledAt = now.Add(300 * time.Second)
	if err := batch.ValidateIdentity("node-1", now, 180); err != nil {
		t.Fatal(err)
	}
	fast.SampledAt = now.Add(300*time.Second + time.Nanosecond)
	if batch.ValidateIdentity("node-1", now, 180) == nil {
		t.Fatal("future sample accepted")
	}
	fast.SampledAt = now
	batch.Fast = append(batch.Fast, fast)
	if batch.ValidateIdentity("node-1", now, 180) == nil {
		t.Fatal("duplicate in batch accepted")
	}
	batch.Fast = []*TelemetryFast{}
	batch.Full = []*TelemetryFull{{Sequence: 1, SampledAt: now}}
	batch.PublicIp = []*TelemetryPublicIP{{Sequence: 1, SampledAt: now.Add(-600 * time.Second), Ipv4: "203.0.113.1"}}
	if err := batch.ValidateIdentity("node-1", now, 60); err != nil {
		t.Fatal("slow-only / cached IP", err)
	}
	batch.PublicIp[0].SampledAt = now.Add(-916 * time.Second)
	if batch.ValidateIdentity("node-1", now, 60) == nil {
		t.Fatal("expired IP accepted")
	}
}

func TestTelemetryLeaseAndCapability(t *testing.T) {
	d := TelemetryDemand{SessionGeneration: "generation-2", DemandRevision: 1, LeaseID: "lease-1", Enabled: true, TtlSeconds: 90}
	if d.Validate("generation-2") != nil {
		t.Fatal(d)
	}
	if d.Validate("generation-1") == nil {
		t.Fatal("stale generation accepted")
	}
	d.TtlSeconds = 91
	if d.Validate("generation-2") == nil {
		t.Fatal("unbounded lease")
	}
	d.Enabled = false
	d.TtlSeconds = 0
	if d.Validate("generation-2") != nil {
		t.Fatal("disable rejected")
	}
	for _, kind := range []string{"node.telemetry.ready", "node.settings.v3", "node.settings.v3_result", "node.metrics.v3", "node.metrics.receipt", "node.metrics.demand", "node.metrics.demand_result"} {
		if TelemetryMessageAllowed(kind, false) || !TelemetryMessageAllowed(kind, true) {
			t.Fatal("capability gate", kind)
		}
	}
	if TelemetryMessageAllowed("shell.exec", true) {
		t.Fatal("unknown message")
	}
}

func TestTelemetryReceiptCountConservation(t *testing.T) {
	r := TelemetryReceipt{BatchID: "batch-1", State: "accepted", AcceptedCount: 2, DuplicateCount: 3}
	if r.Validate(5) != nil {
		t.Fatal(r)
	}
	if r.Validate(4) == nil || r.Validate(6) == nil {
		t.Fatal("inconsistent count accepted")
	}
	r.State = "rejected"
	r.ErrorCode = "overloaded"
	if r.Validate(5) == nil {
		t.Fatal("partial acceptance")
	}
	r.AcceptedCount = 0
	r.DuplicateCount = 0
	if r.Validate(5) != nil {
		t.Fatal("overload rejected")
	}
}

func TestTelemetryCanonicalPayloads(t *testing.T) {
	fixtures := map[string]any{"node.telemetry.ready": &TelemetryReady{}, "node.settings.v3": &TelemetrySettings{}, "node.settings.v3_result": &TelemetrySettingsResult{}, "node.metrics.v3": &TelemetryBatch{}, "node.metrics.receipt": &TelemetryReceipt{}, "node.metrics.demand": &TelemetryDemand{}, "node.metrics.demand_result": &TelemetryDemandResult{}}
	for kind, payload := range fixtures {
		t.Run(kind, func(t *testing.T) {
			path := "testdata/" + kind + ".json"
			raw, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				raw, err = os.ReadFile("../../api/agent/v1/examples/" + kind + ".json")
			}
			if err != nil {
				t.Fatal(err)
			}
			e, err := Parse(raw, "node-demo")
			if err != nil {
				t.Fatal(err)
			}
			if err = Decode(e.Payload, payload); err != nil {
				t.Fatal(err)
			}
			if b, ok := payload.(*TelemetryBatch); ok {
				if b.Fast[0].Interfaces[0].RXBytes != 9007199254740993 {
					t.Fatal("integer precision lost")
				}
			}
		})
	}
}

func TestTelemetryEnvelopeBoundsRemainStrict(t *testing.T) {
	var value TelemetryBatch
	if Decode(bytes.Repeat([]byte(" "), TelemetryMaxFrameBytes+1), &value) == nil {
		t.Fatal("oversized WSS frame")
	}
	raw := append(bytes.Repeat([]byte("["), 17), bytes.Repeat([]byte("]"), 17)...)
	if Decode(raw, &value) == nil {
		t.Fatal("unbounded nesting")
	}
	if Decode([]byte(`{"node_id":"n","run_id":"r","batch_id":"b","fast":[],"full":[],"public_ip":[],"unknown":true}`), &value) == nil {
		t.Fatal("unknown field")
	}
}
