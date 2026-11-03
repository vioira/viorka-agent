package collector

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// TelemetryFast owns CPU/network baselines for one Agent run. A single sampling
// owner retains it across mode/settings changes. It performs no full-group I/O.
type TelemetryFast struct {
	traffic      *state.TrafficLedger
	trafficError error
	source       *Metrics
	network      *TelemetryNetwork
	previous     time.Time
	cpu          []uint64
	sequence     uint64
	boot         string
}

func NewTelemetryFast(filter config.NetworkFilter) (*TelemetryFast, error) {
	network, err := NewTelemetryNetwork(filter)
	if err != nil {
		return nil, err
	}
	return &TelemetryFast{source: network.source, network: network}, nil
}
func (t *TelemetryFast) Sample(ctx context.Context, at time.Time) (*v1.TelemetryFast, NetworkObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, NetworkObservation{}, err
	}
	if at.IsZero() || t.sequence >= math.MaxInt64 {
		return nil, NetworkObservation{}, fmt.Errorf("invalid fast sequence or sample time")
	}
	t.sequence++
	f := &v1.TelemetryFast{Sequence: t.sequence, SampledAt: at.UTC(), Interfaces: []*v1.TelemetryInterface{}, Errors: []string{}, FilterRevision: t.network.revision}
	elapsed := at.Sub(t.previous).Seconds()
	valid := !t.previous.IsZero() && elapsed > 0 && elapsed <= 600
	if valid {
		f.ElapsedSeconds = elapsed
	}
	t.previous = at
	if runtime.GOOS != "linux" && t.source.root == "/proc" {
		f.Errors = []string{"unsupported_platform"}
		return f, NetworkObservation{}, nil
	}
	network, networkErr := t.network.Sample(ctx, at)
	cpuInterval := valid && t.boot != "" && t.boot == network.BootID
	t.boot = network.BootID
	data, err := t.source.read(ctx, "stat")
	cpu, _, parseErr := parseCPU(data)
	if err == nil && parseErr == nil {
		if cpuInterval && len(cpu) == len(t.cpu) {
			var total, idle float64
			monotonic := true
			for i, v := range cpu {
				if v < t.cpu[i] {
					monotonic = false
					break
				}
				delta := float64(v - t.cpu[i])
				total += delta
				if i == 3 || i == 4 {
					idle += delta
				}
			}
			if monotonic && total > 0 {
				percent := 100 * (total - idle) / total
				f.CPUPercent = &percent
			}
		}
		t.cpu = cpu
	} else {
		t.cpu = nil
	}
	if f.CPUPercent == nil {
		f.Errors = append(f.Errors, "cpu_unavailable")
	}
	data, err = t.source.read(ctx, "meminfo")
	total, used, swap, swapUsed, parseErr := parseTelemetryMemory(data)
	if err != nil || parseErr != nil {
		f.Errors = append(f.Errors, "memory_unavailable")
	} else {
		f.RamTotal = &total
		f.RamUsed = &used
		f.SwapTotal = &swap
		f.SwapUsed = &swapUsed
	}
	data, err = t.source.read(ctx, "uptime")
	fields := strings.Fields(data)
	if err == nil && len(fields) > 0 {
		v, e := number(fields[0])
		if e == nil && v <= 1e20 {
			f.UptimeSeconds = &v
		}
	}
	if f.UptimeSeconds == nil {
		f.Errors = append(f.Errors, "uptime_unavailable")
	}
	tcp, udp, err := t.source.sampleSockets(ctx)
	if err != nil {
		f.Errors = append(f.Errors, "sockets_unavailable")
	} else {
		f.TcpInuse = &tcp
		f.UdpInuse = &udp
	}
	if networkErr != nil {
		f.Errors = append(f.Errors, "network_unavailable")
	} else {
		f.Interfaces = network.Interfaces
	}
	t.trafficError = nil
	if t.traffic != nil && networkErr == nil {
		counters := make([]state.TrafficInterface, 0, len(network.Interfaces))
		for _, i := range network.Interfaces {
			counters = append(counters, state.TrafficInterface{Name: i.Name, Ifindex: i.Ifindex, RX: i.RXBytes, TX: i.TXBytes})
		}
		f.Traffic, t.trafficError = t.traffic.Observe(at, network.BootID, network.FilterRevision, counters)
	} else {
		f.Errors = append(f.Errors, "traffic_unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, NetworkObservation{}, err
	}
	return f, network, nil
}
func parseTelemetryMemory(data string) (total, used, swap, swapUsed uint64, err error) {
	values := map[string]uint64{}
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		if key != "MemTotal" && key != "MemAvailable" && key != "SwapTotal" && key != "SwapFree" {
			continue
		}
		if _, exists := values[key]; exists || len(fields) != 3 || fields[2] != "kB" {
			err = fmt.Errorf("invalid memory field")
			return
		}
		n, e := strconv.ParseUint(fields[1], 10, 64)
		if e != nil || n > math.MaxInt64/1024 {
			err = fmt.Errorf("invalid memory counter")
			return
		}
		values[key] = n * 1024
	}
	if len(values) != 4 || values["MemTotal"] == 0 || values["MemAvailable"] > values["MemTotal"] || values["SwapFree"] > values["SwapTotal"] {
		err = fmt.Errorf("incomplete or contradictory memory counters")
		return
	}
	return values["MemTotal"], values["MemTotal"] - values["MemAvailable"], values["SwapTotal"], values["SwapTotal"] - values["SwapFree"], nil
}

// SetTrafficLedger is called during startup, before the sampling owner starts.
// Persistence may run concurrently; it never executes on the fast path.
func (t *TelemetryFast) SetTrafficLedger(ledger *state.TrafficLedger) { t.traffic = ledger }

// TrafficError exposes diagnostics to the sampling owner without failing other
// metrics. The traffic observation already carries unknown when accounting fails.
func (t *TelemetryFast) TrafficError() error { return t.trafficError }
