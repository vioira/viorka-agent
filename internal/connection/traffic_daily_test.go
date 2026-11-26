package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
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
	flight := window.take()
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

func TestTransportsSendLatestDailyWithoutPersistence(t *testing.T) {
	b := uploadFixture(t)
	ledger, err := state.NewTrafficLedger("https://panel.test", b.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(24 * time.Hour).Add(-time.Hour)
	observe := func(second int) {
		t.Helper()
		_, err := ledger.Observe(at.Add(time.Duration(second)*time.Second), "11111111-1111-4111-8111-111111111111", strings.Repeat("a", 64), []state.TrafficInterface{{Name: "eth0", Ifindex: 1, RX: uint64(second)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	observe(0)
	observe(1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch v1.TelemetryBatch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch.DailyTraffic) != 1 || batch.DailyTraffic[0].RXBytes != "1" {
			t.Errorf("unexpected HTTP daily traffic: %v, %v", batch.DailyTraffic, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		d := batch.DailyTraffic[0]
		w.Header().Set("Content-Type", "application/json")
		receipt := v1.TelemetryReceipt{
			BatchID: batch.BatchID, State: "accepted", AcceptedCount: 1,
			DailyTrafficAcks: []*v1.TelemetryDailyTrafficAck{{DayStart: d.DayStart, Revision: d.Revision}},
		}
		if err := json.NewEncoder(w).Encode(receipt); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	c := &Client{cfg: config.Local{PanelURL: server.URL}, http: server.Client(), dailyPending: ledger.PendingDaily}
	c.dailyTrafficSupported.Store(true)
	var window telemetryWindow
	if err := window.add(b.Fast[0]); err != nil {
		t.Fatal(err)
	}
	req := telemetryTransmission{
		send:       &telemetrySend{attempt: telemetryAttempt{HTTP: true}, flight: window.take()},
		report:     60,
		credential: sessionCredential{Token: strings.Repeat("a", 64), Generation: 1, ExpiresAt: time.Now().Add(time.Hour)},
	}
	if err := c.telemetryTransport(b.NodeID, b.RunID, nil)(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	observe(2)
	req.send.attempt.HTTP = false
	var received bool
	deliver := func(_ context.Context, raw []byte, _ string, _ uint64) error {
		var e v1.Envelope
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		batch, err := v1.DecodeTelemetryBatch(e.Payload, b.NodeID, time.Now(), 60, false)
		if err != nil {
			return err
		}
		received = len(batch.DailyTraffic) == 1 && batch.DailyTraffic[0].RXBytes == "2"
		return nil
	}
	if err := c.telemetryTransport(b.NodeID, b.RunID, func() telemetryFrameDelivery { return deliver })(t.Context(), req); err != nil || !received {
		t.Fatalf("latest WSS daily traffic: received=%v err=%v", received, err)
	}
}
