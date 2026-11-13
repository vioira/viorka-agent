package v1

import (
	"errors"
	"math"
	"net/netip"
	"regexp"
	"time"
)

// TelemetryCapability is a contract identifier, not an advertisement of support.
// Neither peer advertises it until storage, transport and scheduling are ready.
const TelemetryCapability = "node.telemetry.v3"
const TCPProbeCapability = "node.tcp_probe.v2"
const TCPProbeSettingsCapability = "node.tcp_probe.settings.v2"
const (
	TelemetryMaxBodyBytes   = 1 << 20
	TelemetryMaxFrameBytes  = 64 << 10
	TelemetryMaxFastSamples = 300
)

var telemetryID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var telemetryRunID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-4[a-fA-F0-9]{3}-[89abAB][a-fA-F0-9]{3}-[a-fA-F0-9]{12}$`)
var telemetryFilter = regexp.MustCompile(`^[a-f0-9]{64}$`)

func DefaultTelemetrySettings() TelemetrySettings {
	return TelemetrySettings{LogLevel: "warn", Revision: 1, CollectionSeconds: 30, HttpReportSeconds: 60, WssReportSeconds: 2, ConnectionMode: "auto_http_wss"}
}
func (s TelemetrySettings) Validate() error {
	if s.TcpProbe != nil {
		if err := ValidateTCPProbeNames(s.TcpProbe.Targets, s.TcpProbe.Names); err != nil {
			return err
		}
		if err := ValidateTCPProbeTargets(s.TcpProbe.Targets); err != nil {
			return err
		}
	}
	if err := s.TrafficSettings().Validate(); err != nil {
		return err
	}
	if s.LogLevel != "" && s.LogLevel != "debug" && s.LogLevel != "info" && s.LogLevel != "warn" && s.LogLevel != "error" {
		return errors.New("invalid log level")
	}
	if s.Revision == 0 || s.Revision > math.MaxInt64 || s.CollectionSeconds < 1 || s.CollectionSeconds > 60 || s.HttpReportSeconds < 30 || s.HttpReportSeconds > 180 || s.WssReportSeconds < 1 || s.WssReportSeconds > 5 || s.ConnectionMode != "auto_http_wss" {
		return errors.New("invalid telemetry settings")
	}
	return nil
}
func (d TelemetryDemand) Validate(generation string) error {
	if !telemetryID.MatchString(d.SessionGeneration) || d.SessionGeneration != generation || !telemetryID.MatchString(d.LeaseID) || d.DemandRevision == 0 || d.DemandRevision > math.MaxInt64 {
		return errors.New("invalid telemetry demand identity")
	}
	if (d.Enabled && d.TtlSeconds != 90) || (!d.Enabled && d.TtlSeconds != 0) {
		return errors.New("invalid telemetry lease duration")
	}
	return nil
}
func (r TelemetryReceipt) Validate(total uint64) error {
	if !telemetryID.MatchString(r.BatchID) || total > 310 || r.AcceptedCount > total || r.DuplicateCount > total-r.AcceptedCount {
		return errors.New("invalid telemetry receipt counts")
	}
	if r.State == "accepted" && r.ErrorCode == "" && r.AcceptedCount+r.DuplicateCount == total {
		return nil
	}
	if r.State == "rejected" && r.AcceptedCount == 0 && r.DuplicateCount == 0 {
		switch r.ErrorCode {
		case "invalid_batch", "overloaded", "rate_limited", "session_expired", "session_invalid", "identity_revoked", "node_disabled", "unsupported_protocol", "stale_session", "stale_demand", "lease_expired":
			return nil
		}
	}
	return errors.New("invalid telemetry receipt")
}

// ValidateIdentity checks contextual admission after structural schema validation.
// It does not mutate deduplication state. Full and IP retain their own timestamps:
// accepting cached IP does not create a new history observation.
func (b TelemetryBatch) ValidateIdentity(node string, now time.Time, reportSeconds uint64) error {
	if err := ValidateDailyTraffic(b.DailyTraffic, now); err != nil {
		return err
	}
	if !telemetryID.MatchString(b.NodeID) || b.NodeID != node || !telemetryRunID.MatchString(b.RunID) || !telemetryID.MatchString(b.BatchID) || reportSeconds < 30 || reportSeconds > 180 || now.IsZero() {
		return errors.New("invalid telemetry batch identity")
	}
	if b.Fast == nil || b.Full == nil || b.PublicIp == nil || len(b.Fast) > 300 || len(b.Full) > 8 || len(b.PublicIp) > 2 || len(b.Fast)+len(b.Full)+len(b.PublicIp) == 0 {
		return errors.New("invalid telemetry batch size")
	}
	check := func(seq, previous uint64, at time.Time, past time.Duration) bool {
		return seq > previous && seq <= math.MaxInt64 && !at.IsZero() && !at.Before(now.Add(-past)) && !at.After(now.Add(300*time.Second))
	}
	var previous uint64
	for _, f := range b.Fast {
		if f == nil || !check(f.Sequence, previous, f.SampledAt, time.Duration(reportSeconds+315)*time.Second) || !telemetryFilter.MatchString(f.FilterRevision) {
			return errors.New("invalid fast observation identity")
		}
		previous = f.Sequence
	}
	previous = 0
	for _, f := range b.Full {
		if f == nil || !check(f.Sequence, previous, f.SampledAt, time.Duration(reportSeconds+315)*time.Second) {
			return errors.New("invalid full observation identity")
		}

		if batch := f.TcpProbes; batch != nil {
			if batch.SampledAt.IsZero() || batch.SampledAt.After(f.SampledAt) || f.SampledAt.Sub(batch.SampledAt) >= 120*time.Second {
				return errors.New("invalid TCP probe batch time")
			}
			seen := map[string]bool{}
			for _, p := range batch.Targets {
				if p == nil {
					return errors.New("nil TCP probe")
				}
				if err := ValidateTCPProbeTargets([]string{p.Target}); err != nil {
					return err
				}
				if seen[p.Target] || (p.Ping < 0 && p.Ping != -1) || (p.Ping == -1) != (p.Loss == 100) {
					return errors.New("invalid TCP probe observation")
				}
				seen[p.Target] = true
			}
		}
		previous = f.Sequence
	}
	previous = 0
	for _, ip := range b.PublicIp {
		if ip == nil || !check(ip.Sequence, previous, ip.SampledAt, 915*time.Second) {
			return errors.New("invalid public IP observation identity")
		}
		if ip.Ipv4 != "" {
			a, e := netip.ParseAddr(ip.Ipv4)
			if e != nil || !a.Is4() {
				return errors.New("invalid public IPv4")
			}
		}
		if ip.Ipv6 != "" {
			a, e := netip.ParseAddr(ip.Ipv6)
			if e != nil || !a.Is6() || a.Is4In6() || a.Zone() != "" {
				return errors.New("invalid public IPv6")
			}
		}
		previous = ip.Sequence
	}
	return nil
}

// TelemetryMessageAllowed is deliberately opt-in. The old 1.0 envelope stays
// unchanged; a new type is legal only after the complete v3 capability exchange.
func TelemetryMessageAllowed(kind string, negotiated bool) bool {
	if !negotiated {
		return false
	}
	switch kind {
	case "node.telemetry.ready", "node.settings.v3", "node.settings.v3_result", "node.metrics.v3", "node.metrics.receipt", "node.metrics.demand", "node.metrics.demand_result":
		return true
	}
	return false
}
