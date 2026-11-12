package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/vioira/viorka-agent/internal/updater"
)

func updateCheck(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet("update-check", flag.ContinueOnError)
	flags.SetOutput(errOut)
	preview := flags.Bool("prerelease", false, "include prereleases")
	path := flags.String("config", "", "read DNS fallback from this local config")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errOut, "update-check: positional arguments are unsupported")
		return 2
	}
	var dns []string
	if *path != "" {
		cfg, err := loadConfig(*path)
		if err != nil {
			fmt.Fprintln(errOut, "update-check: invalid or unreadable configuration")
			return 2
		}
		dns = cfg.DNSFallbackServers
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	result, err := updater.Check(ctx, Version, runtime.GOOS, runtime.GOARCH, *preview, dns)
	if err != nil {
		fmt.Fprintf(errOut, "update-check: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	switch result.Status {
	case "unsupported-platform", "asset-missing", "checksum-missing", "no-release":
		return 1
	}
	return 0
}
