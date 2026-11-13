package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/vioira/viorka-agent/internal/config"
	"log/slog"
	"net"
	"testing"
	"time"
)

func probeForTest() *TCPProbes {
	p := NewTCPProbes(&config.TCPProbe{Targets: []string{"example.com:443"}, TimeoutMS: 1000}, nil)
	p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	return p
}
func TestTCPProbeConnectionAndWindow(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		c, err := listener.Accept()
		if err == nil {
			c.Close()
		}
	}()
	p := probeForTest()
	at := time.Now()
	clock := at
	p.now = func() time.Time { return clock }
	p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		clock = clock.Add(500 * time.Millisecond)
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	p.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "1.1.1.1:443" {
			t.Errorf("not pinned: %s", address)
		}
		c, e := (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
		clock = clock.Add(12 * time.Millisecond)
		return c, e
	}
	batch, err := p.Sample(context.Background(), at)
	if err != nil || batch.Targets[0].Ping != 12 || batch.Targets[0].Loss != 0 || !batch.SampledAt.Equal(clock) {
		t.Fatalf("DNS timer/batch time: %+v %v", batch, err)
	}
	<-accepted
	p.dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("refused") }
	clock = at.Add(20 * time.Second)
	batch, _ = p.Sample(context.Background(), clock)
	if batch.Targets[0].Loss != 50 || batch.Targets[0].Ping != 12 {
		t.Fatal(batch.Targets[0])
	}
	original := batch
	clock = at.Add(21 * time.Second)
	batch, _ = p.Sample(context.Background(), clock)
	if batch != original || len(p.histories["example.com:443"].observations) != 2 {
		t.Fatal("extra attempt before 20s")
	}
	p.lookup = func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("dns") }
	clock = at.Add(141 * time.Second)
	batch, _ = p.Sample(context.Background(), clock)
	if batch.Targets[0].Loss != 100 || batch.Targets[0].Ping != -1 || len(p.histories["example.com:443"].observations) != 1 {
		t.Fatal("expiry/DNS failure", batch)
	}
	if p.Snapshot(clock.Add(120*time.Second)) != nil {
		t.Fatal("expired snapshot reported")
	}
}
func TestTCPProbeMedianCapacityAndTargetChange(t *testing.T) {
	p := probeForTest()
	at := time.Now()
	clock := at
	p.now = func() time.Time { return clock }
	durations := []time.Duration{10, 20, 30, 40, 1000, 0}
	calls := 0
	p.dial = func(context.Context, string, string) (net.Conn, error) {
		ms := durations[calls%len(durations)]
		calls++
		if ms == 0 {
			return nil, errors.New("failed")
		}
		clock = clock.Add(ms * time.Millisecond)
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	for i := 0; i < 6; i++ {
		clock = at.Add(time.Duration(i) * 20 * time.Second)
		batch, err := p.Sample(context.Background(), clock)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && batch.Targets[0].Ping != 15 {
			t.Fatal("even median")
		}
	}
	batch := p.snapshot.Load()
	if batch.Targets[0].Ping != 30 || batch.Targets[0].Loss != 16 || calls != 6 {
		t.Fatal("spike/integer loss", batch.Targets[0], calls)
	}
	// Fixed timing ignores old local overrides, and count never exceeds six.
	p.settings.WindowSeconds = 3600
	for i := 6; i < 200; i++ {
		clock = at.Add(time.Duration(i) * 20 * time.Second)
		p.Sample(context.Background(), clock)
	}
	if len(p.histories["example.com:443"].observations) != 6 {
		t.Fatal("unbounded window")
	}
	cfg := p.settings
	cfg.Targets = []string{"example.net:443", "example.com:443"}
	p.Configure(&cfg)
	if len(p.histories["example.com:443"].observations) != 6 || p.histories["example.net:443"] != nil || p.snapshot.Load() != nil {
		t.Fatal("target histories not isolated")
	}
	cfg.Targets = []string{"example.com:443", "example.net:443"}
	p.Configure(&cfg)
	if len(p.histories["example.com:443"].observations) != 6 {
		t.Fatal("reorder reset history")
	}
	p.Configure(nil)
	if b, e := p.Sample(context.Background(), clock); e != nil || b != nil {
		t.Fatal("disabled", b, e)
	}
}
func TestTCPProbeCancellationAndAddressBoundary(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "64:ff9b::a00:1", "::ffff:127.0.0.1"} {
		t.Run(address, func(t *testing.T) {
			p := probeForTest()
			p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}, {IP: net.ParseIP(address)}}, nil
			}
			p.dial = func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("dialed blocked address")
				return nil, nil
			}
			batch, err := p.Sample(context.Background(), time.Now())
			if err != nil || batch.Targets[0].Loss != 100 || batch.Targets[0].Ping != -1 {
				t.Fatal(batch, err)
			}
		})
	}
	p := probeForTest()
	p.settings.TimeoutMS = 100
	p.dial = func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
	b, err := p.Sample(context.Background(), time.Now())
	if err != nil || b.Targets[0].Loss != 100 || b.Targets[0].Ping != -1 {
		t.Fatal("timeout", b, err)
	}
}
func TestTCPProbeCanceledTickRetriesAllTargets(t *testing.T) {
	p := probeForTest()
	p.settings.Targets = []string{"example.com:443", "example.net:443"}
	at := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	p.dial = func(context.Context, string, string) (net.Conn, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return nil, errors.New("refused")
	}
	if _, err := p.Sample(ctx, at); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(p.histories) != 0 || !p.next.IsZero() || p.snapshot.Load() != nil {
		t.Fatal("partial tick committed")
	}
	batch, err := p.Sample(context.Background(), at.Add(time.Second))
	if err != nil || calls != 4 || len(batch.Targets) != 2 {
		t.Fatal(batch, err, calls)
	}
	for _, row := range batch.Targets {
		if row.Loss != 100 || row.Ping != -1 {
			t.Fatal(row)
		}
	}
}

func TestTCPProbeDiagnostics(t *testing.T) {
	for _, scenario := range []string{"success", "dns", "blocked", "connect", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			var output bytes.Buffer
			level := new(slog.LevelVar)
			p := probeForTest()
			p.logger = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.dial = func(context.Context, string, string) (net.Conn, error) {
				if scenario == "connect" {
					return nil, errors.New("connection refused")
				}
				a, b := net.Pipe()
				b.Close()
				return a, nil
			}
			if scenario == "dns" || scenario == "cancel" {
				p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
					if scenario == "cancel" {
						cancel()
						return nil, context.Canceled
					}
					return nil, errors.New("DNS timeout")
				}
			}
			if scenario == "blocked" {
				p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
					return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
				}
			}
			p.measure(ctx, "example.com:443")
			if output.Len() != 0 {
				t.Fatal("probe logged at info level")
			}
			level.Set(slog.LevelDebug)
			p.measure(ctx, "example.com:443")
			var record map[string]any
			if err := json.Unmarshal(output.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			stage, outcome := "connect", "failed"
			switch scenario {
			case "success":
				outcome = "succeeded"
			case "dns":
				stage = "dns"
			case "blocked":
				stage = "address_validation"
			case "cancel":
				stage = "dns"
				outcome = "canceled"
			}
			if record["stage"] != stage || record["outcome"] != outcome || record["target"] != "example.com:443" {
				t.Fatal(record)
			}
			if scenario != "success" && record["error"] == nil {
				t.Fatal("missing original error", record)
			}
			for _, key := range []string{"dns_ms", "connect_ms", "total_ms", "timeout_ms", "resolved_ips", "selected_ip"} {
				if _, ok := record[key]; !ok {
					t.Fatal("missing", key, record)
				}
			}
			if stage == "connect" && record["selected_ip"] != "1.1.1.1" {
				t.Fatal(record)
			}
		})
	}
}
