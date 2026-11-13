package connection

import (
	"encoding/json"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"testing"
	"time"
)

func TestDailyTrafficReceiptAndFrame(t *testing.T) {
	b := uploadFixture(t)
	at := b.Fast[0].SampledAt
	d := &v1.TelemetryDailyTraffic{DayStart: at.Truncate(24 * time.Hour), DayEnd: at.Truncate(24 * time.Hour).Add(24 * time.Hour), RXBytes: "12", TXBytes: "34", SampledAt: at, Revision: 7}
	if validDailyAcks(nil, []*v1.TelemetryDailyTraffic{d}) {
		t.Fatal("missing durable acknowledgement accepted")
	}
	ack := &v1.TelemetryDailyTrafficAck{DayStart: d.DayStart, Revision: 6}
	if validDailyAcks([]*v1.TelemetryDailyTrafficAck{ack}, []*v1.TelemetryDailyTraffic{d}) {
		t.Fatal("wrong revision acknowledged")
	}
	ack.Revision = 7
	if !validDailyAcks([]*v1.TelemetryDailyTrafficAck{ack}, []*v1.TelemetryDailyTraffic{d}) {
		t.Fatal("matching acknowledgement rejected")
	}
	var window telemetryWindow
	if err := window.add(b.Fast[0]); err != nil {
		t.Fatal(err)
	}
	flight := window.take(false)
	flight.daily = []*v1.TelemetryDailyTraffic{d}
	raw, _, err := flight.frame(0, b.NodeID, b.RunID, b.BatchID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var envelope v1.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	decoded, err := v1.DecodeTelemetryBatch(envelope.Payload, b.NodeID, time.Now(), 60, false)
	if err != nil || len(decoded.DailyTraffic) != 1 || decoded.DailyTraffic[0].RXBytes != "12" {
		t.Fatal(decoded, err)
	}
}
