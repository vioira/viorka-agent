package collector

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
)

func probeForTest() *TCPProbes {
	p := NewTCPProbes(&config.TCPProbe{Targets: []string{"example.com:443"}, IntervalSeconds: 30, TimeoutMS: 1000, WindowSeconds: 120}, nil)
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
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()
	p := probeForTest()
	clock := time.Now()
	p.now = func() time.Time { return clock }
	p.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		clock = clock.Add(500 * time.Millisecond)
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	p.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "1.1.1.1:443" {
			t.Errorf("not pinned to resolved IP: %s", address)
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
		clock = clock.Add(12 * time.Millisecond)
		return conn, err
	}
	at := time.Now()
	rows, err := p.Sample(context.Background(), at)
	if err != nil || len(rows) != 1 || rows[0].Samples != 1 || rows[0].MedianMs == nil || *rows[0].MedianMs != 12 || *rows[0].FailurePercent != 0 {
		t.Fatalf("success/DNS timing: %+v %v", rows, err)
	}
	<-accepted
	p.dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("refused") }
	rows, _ = p.Sample(context.Background(), at.Add(30*time.Second))
	if rows[0].Failures != 1 || *rows[0].FailurePercent != 50 || *rows[0].MedianMs != 12 {
		t.Fatalf("failed window: %+v", rows[0])
	}
	rows, _ = p.Sample(context.Background(), at.Add(31*time.Second))
	if rows[0].Samples != 2 {
		t.Fatal("probed before interval")
	}
	p.lookup = func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("dns") }
	rows, _ = p.Sample(context.Background(), at.Add(151*time.Second))
	if rows[0].Samples != 0 || rows[0].FailurePercent != nil || rows[0].MedianMs != nil || rows[0].LastError != "dns_failed" {
		t.Fatalf("expired or DNS treated as TCP loss: %+v", rows[0])
	}
}
func TestTCPProbeMedianCapacityAndTargetChange(t *testing.T) {
	p := probeForTest()
	p.settings.WindowSeconds = 3600
	at := time.Now()
	h := &tcpHistory{at: at, observations: []tcpObservation{{at, 40, false}, {at, 10, false}, {at, 20, false}, {at, 30, false}, {at, 0, true}}}
	p.histories["example.com:443"] = h
	p.next = at.Add(time.Minute)
	rows, _ := p.Sample(context.Background(), at)
	if *rows[0].MedianMs != 25 || *rows[0].FailurePercent != 20 {
		t.Fatalf("median: %+v", rows[0])
	}
	p.dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("refused") }
	for i := 1; i <= 200; i++ {
		p.Sample(context.Background(), at.Add(time.Duration(i)*30*time.Second))
	}
	if n := len(p.histories["example.com:443"].observations); n != 120 {
		t.Fatalf("unbounded window %d", n)
	}
	cfg := p.settings
	cfg.Targets = []string{"example.net:443"}
	p.Configure(&cfg)
	rows, _ = p.Sample(context.Background(), at.Add(201*30*time.Second))
	if rows[0].Target != "example.net:443" || rows[0].Samples != 1 || rows[0].MedianMs != nil {
		t.Fatal("target change retained old history")
	}
	p.Configure(nil)
	rows, err := p.Sample(context.Background(), at.Add(202*30*time.Second))
	if err != nil || len(rows) != 0 {
		t.Fatal("disabled probe")
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
			rows, err := p.Sample(context.Background(), time.Now())
			if err != nil || rows[0].LastError != "address_blocked" || rows[0].Samples != 0 {
				t.Fatalf("address boundary: %+v %v", rows, err)
			}
		})
	}
	p := probeForTest()
	ctx, cancel := context.WithCancel(context.Background())
	p.dial = func(ctx context.Context, _, _ string) (net.Conn, error) { cancel(); return nil, ctx.Err() }
	if _, err := p.Sample(ctx, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if len(p.histories) != 0 {
		t.Fatal("cancel counted as failure")
	}
	p = probeForTest()
	p.settings.TimeoutMS = 100
	p.dial = func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
	rows, err := p.Sample(context.Background(), time.Now())
	if err != nil || rows[0].Failures != 1 || *rows[0].FailurePercent != 100 || rows[0].MedianMs != nil {
		t.Fatalf("timeout: %+v %v", rows, err)
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
	if len(p.histories) != 0 || !p.next.IsZero() {
		t.Fatal("partial tick committed")
	}
	rows, err := p.Sample(context.Background(), at.Add(time.Second))
	if err != nil || calls != 4 {
		t.Fatal(err, calls)
	}
	for _, row := range rows {
		if row.SampledAt.IsZero() || row.Samples != 1 || row.Failures != 1 {
			t.Fatalf("invalid retry: %+v", row)
		}
	}
}
