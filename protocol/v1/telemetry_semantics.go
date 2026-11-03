package v1

import (
	"math"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
)

func telemetryText(s string, limit int) bool {
	return len(s) <= limit && strings.IndexFunc(s, unicode.IsControl) < 0
}
func allCounters(v ...*uint64) bool {
	for _, n := range v {
		if n == nil {
			return false
		}
	}
	return true
}
func anyCounters(v ...*uint64) bool {
	for _, n := range v {
		if n != nil {
			return true
		}
	}
	return false
}
func anyNumbers(v ...*float64) bool {
	for _, n := range v {
		if n != nil {
			return true
		}
	}
	return false
}
func capacity(total, used, available uint64) bool {
	return total <= math.MaxInt64 && used <= total && available <= total-used
}
func baselineQuality(q []string) bool {
	return slices.Contains(q, "baseline") || slices.Contains(q, "counter_reset") || slices.Contains(q, "identity_changed") || slices.Contains(q, "unknown")
}

// ValidateSemantics runs after Schema validation. It rejects contradictory
// known/unknown values and ambiguous device identities before any state changes.
func (b TelemetryBatch) ValidateSemantics() error {
	for _, f := range b.Fast {
		if f == nil || !validFast(f) {
			return ErrTelemetryPayload
		}
	}
	for _, f := range b.Full {
		if f == nil || !validFull(f) {
			return ErrTelemetryPayload
		}
	}
	for _, ip := range b.PublicIp {
		if ip == nil || (ip.Ipv4 == "") != slices.Contains(ip.Errors, "ipv4_unavailable") || (ip.Ipv6 == "") != slices.Contains(ip.Errors, "ipv6_unavailable") {
			return ErrTelemetryPayload
		}
		if ip.Ipv4 != "" && !PublicAddress(ip.Ipv4, true) {
			return ErrTelemetryPayload
		}
		if ip.Ipv6 != "" && !PublicAddress(ip.Ipv6, false) {
			return ErrTelemetryPayload
		}
	}
	return nil
}
func validFast(f *TelemetryFast) bool {
	unsupported := slices.Contains(f.Errors, "unsupported_platform")
	failed := func(code string) bool { return unsupported || slices.Contains(f.Errors, code) }
	if failed("cpu_unavailable") {
		if f.CPUPercent != nil {
			return false
		}
	} else if f.CPUPercent == nil || f.ElapsedSeconds <= 0 {
		return false
	}
	if failed("memory_unavailable") {
		if anyCounters(f.RamTotal, f.RamUsed, f.SwapTotal, f.SwapUsed) {
			return false
		}
	} else if !allCounters(f.RamTotal, f.RamUsed, f.SwapTotal, f.SwapUsed) || *f.RamTotal == 0 || *f.RamUsed > *f.RamTotal || *f.SwapUsed > *f.SwapTotal {
		return false
	}
	if failed("uptime_unavailable") {
		if f.UptimeSeconds != nil {
			return false
		}
	} else if f.UptimeSeconds == nil {
		return false
	}
	if failed("sockets_unavailable") {
		if anyCounters(f.TcpInuse, f.UdpInuse) {
			return false
		}
	} else if !allCounters(f.TcpInuse, f.UdpInuse) {
		return false
	}
	if failed("network_unavailable") && len(f.Interfaces) != 0 {
		return false
	}
	names := map[string]bool{}
	indices := map[uint64]bool{}
	var rx, tx uint64
	for _, i := range f.Interfaces {
		if i == nil || names[i.Name] || indices[i.Ifindex] || i.RXBytes > math.MaxInt64-rx || i.TXBytes > math.MaxInt64-tx {
			return false
		}
		names[i.Name] = true
		indices[i.Ifindex] = true
		rx += i.RXBytes
		tx += i.TXBytes
		rates := anyNumbers(i.RXBytesPerSecond, i.TXBytesPerSecond)
		if baselineQuality(i.Quality) && rates {
			return false
		}
		if rates && (f.ElapsedSeconds <= 0 || ((i.RXBytesPerSecond == nil || i.TXBytesPerSecond == nil) && !slices.Contains(i.Quality, "partial"))) {
			return false
		}
		if !rates && len(i.Quality) == 0 {
			return false
		}
	}
	if failed("traffic_unavailable") {
		return f.Traffic == nil
	}
	t := f.Traffic
	if t == nil {
		return false
	}
	sample := f.SampledAt.UTC()
	month := time.Date(sample.Year(), sample.Month(), 1, 0, 0, 0, 0, time.UTC)
	if !t.MonthStart.Equal(month) || t.FilterRevision != f.FilterRevision {
		return false
	}
	unknown := slices.Contains(t.Quality, "unknown")
	if unknown {
		return !anyCounters(t.RXBytes, t.TXBytes)
	}
	return allCounters(t.RXBytes, t.TXBytes)
}
func validFull(f *TelemetryFull) bool {
	unsupported := slices.Contains(f.Errors, "unsupported_platform")
	failed := func(code string) bool { return unsupported || slices.Contains(f.Errors, code) }
	if failed("load_unavailable") {
		if anyNumbers(f.Load1, f.Load5, f.Load15) {
			return false
		}
	} else if f.Load1 == nil || f.Load5 == nil || f.Load15 == nil {
		return false
	}
	if failed("processes_unavailable") {
		if f.Processes != nil {
			return false
		}
	} else if f.Processes == nil {
		return false
	}
	if failed("os_unavailable") {
		if f.OSID != "" || f.OSName != "" || f.OSVersion != "" {
			return false
		}
	} else if f.OSName == "" || !telemetryText(f.OSID, 128) || !telemetryText(f.OSName, 256) || !telemetryText(f.OSVersion, 256) {
		return false
	}
	if failed("disk_unavailable") {
		if anyCounters(f.DiskTotal, f.DiskUsed, f.DiskAvailable) || len(f.Filesystems) != 0 {
			return false
		}
	} else {
		if !allCounters(f.DiskTotal, f.DiskUsed, f.DiskAvailable) || !capacity(*f.DiskTotal, *f.DiskUsed, *f.DiskAvailable) {
			return false
		}
		ids := map[string]bool{}
		mounts := map[string]bool{}
		var total, used, available uint64
		for _, fs := range f.Filesystems {
			if fs == nil || ids[fs.Identity] || mounts[fs.MountPoint] || !telemetryText(fs.Identity, 256) || !telemetryText(fs.MountPoint, 4096) || !telemetryText(fs.FilesystemType, 64) || path.Clean(fs.MountPoint) != fs.MountPoint || !strings.HasPrefix(fs.MountPoint, "/") || !capacity(fs.TotalBytes, fs.UsedBytes, fs.AvailableBytes) || fs.TotalBytes > math.MaxInt64-total {
				return false
			}
			ids[fs.Identity] = true
			mounts[fs.MountPoint] = true
			total += fs.TotalBytes
			used += fs.UsedBytes
			available += fs.AvailableBytes
		}
		if total != *f.DiskTotal || used != *f.DiskUsed || available != *f.DiskAvailable {
			return false
		}
	}
	if failed("disk_io_unavailable") {
		return f.DiskIo == nil
	}
	d := f.DiskIo
	if d == nil {
		return false
	}
	rates := anyNumbers(d.ReadBytesPerSecond, d.WriteBytesPerSecond, d.ReadIops, d.WriteIops, d.ReadAwaitMs, d.WriteAwaitMs, d.AwaitMs, d.UtilPercent)
	if baselineQuality(d.Quality) && rates {
		return false
	}
	if !rates {
		if len(d.Quality) == 0 {
			return false
		}
		return true
	}
	if f.ElapsedSeconds <= 0 {
		return false
	}
	if !slices.Contains(d.Quality, "partial") && (d.ReadBytesPerSecond == nil || d.WriteBytesPerSecond == nil || d.ReadIops == nil || d.WriteIops == nil || d.UtilPercent == nil) {
		return false
	}
	if d.ReadAwaitMs != nil && (d.ReadIops == nil || *d.ReadIops <= 0) {
		return false
	}
	if d.WriteAwaitMs != nil && (d.WriteIops == nil || *d.WriteIops <= 0) {
		return false
	}
	if d.AwaitMs != nil && ((d.ReadIops == nil || *d.ReadIops == 0) && (d.WriteIops == nil || *d.WriteIops == 0)) {
		return false
	}
	return true
}
func (r TelemetrySettingsResult) Validate() error {
	if r.Revision == 0 || r.Revision > math.MaxInt64 || r.AppliedRevision > math.MaxInt64 {
		return ErrTelemetryPayload
	}
	if r.State == "applied" && r.AppliedRevision == r.Revision && r.ErrorCode == "" {
		return nil
	}
	if r.State == "failed" {
		switch r.ErrorCode {
		case "invalid_settings", "stale_revision", "revision_conflict", "persist_failed":
			return nil
		}
	}
	return ErrTelemetryPayload
}
