package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/vioira/viorka-agent/internal/collector"
	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/network"
)

type panelDiagnostic struct {
	Stage     string `json:"stage"`
	Status    string `json:"status"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

type doctorChecks struct {
	publicIP func(context.Context, ...string) []collector.IPDiagnostic
	panel    func(context.Context, config.Local) ([]panelDiagnostic, error)
}

func doctor(args []string, out, errOut io.Writer) int {
	return doctorWithChecks(args, out, errOut, doctorChecks{collector.DiagnosePublicIP, diagnosePanel})
}
func doctorWithChecks(args []string, out, errOut io.Writer, checks doctorChecks) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(errOut)
	publicIP := flags.Bool("public-ip", false, "check all fixed public IP sources")
	panel := flags.Bool("panel", false, "check configured Panel DNS/TCP/TLS without authentication")
	path := flags.String("config", "", "Panel config (default ~/.viorka-agent/agent.json)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || (*path != "" && !*panel && !*publicIP) {
		fmt.Fprintln(errOut, "doctor: --config requires --panel or --public-ip; positional arguments are unsupported")
		return 2
	}
	var cfg config.Local
	if *panel || *path != "" {
		var err error
		cfg, err = loadConfig(*path)
		if err != nil {
			// Parsing errors can contain user-provided secrets. Do not echo them.
			fmt.Fprintln(errOut, "doctor: invalid or unreadable Panel configuration")
			return 2
		}
	}
	info := map[string]any{"product": "Viorka Agent", "version": Version, "os": runtime.GOOS, "arch": runtime.GOARCH, "runtime": runtime.Version(), "connection": "not-checked"}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	code := 0
	if *panel {
		results, err := checks.panel(ctx, cfg)
		if err != nil {
			fmt.Fprintln(errOut, "doctor: invalid or unreadable Panel CA")
			return 2
		}
		info["panel"] = results
		info["connection"] = "transport-checked"
		for _, d := range results {
			if d.Status != "ok" {
				code = 1
			}
		}
	}
	if *publicIP {
		results := checks.publicIP(ctx, cfg.DNSFallbackServers...)
		info["public_ip"] = results
		for _, d := range results {
			if d.Status != "ok" {
				code = 1
			}
		}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(info); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return code
}

// diagnosePanel performs only transport checks, with one shared ten-second budget.
// It never opens the identity store or sends an HTTP request.
func diagnosePanel(ctx context.Context, cfg config.Local) ([]panelDiagnostic, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	u, _ := url.Parse(cfg.PanelURL)
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	if cfg.CAFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		b, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("invalid CA certificate")
		}
		tlsConfig.RootCAs = pool
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	results := []panelDiagnostic{{Stage: "dns", Status: "skipped"}, {Stage: "tcp", Status: "skipped"}, {Stage: "tls", Status: "skipped"}}
	record := func(index int, start time.Time, err error) {
		results[index].ElapsedMS = time.Since(start).Milliseconds()
		results[index].Status = "ok"
		if err != nil {
			results[index].Status = "failed"
		}
	}
	start := time.Now()
	addresses, err := network.NewDNS(cfg.DNSFallbackServers).LookupIPAddr(ctx, u.Hostname())
	if err == nil && len(addresses) == 0 {
		err = fmt.Errorf("no addresses")
	}
	record(0, start, err)
	if err != nil {
		return results, nil
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	start = time.Now()
	var conn net.Conn
	// Use the resolved addresses directly, avoiding a second DNS lookup.
	for _, address := range addresses {
		host := address.IP.String()
		if address.Zone != "" {
			host += "%" + address.Zone
		}
		conn, err = (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
		if err == nil {
			break
		}
	}
	record(1, start, err)
	if err != nil {
		return results, nil
	}
	defer conn.Close()
	start = time.Now()
	secure := tls.Client(conn, tlsConfig)
	err = secure.HandshakeContext(ctx)
	record(2, start, err)
	return results, nil
}
