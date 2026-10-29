package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"runtime"

	"github.com/vioira/viorka-agent/internal/config"
)

const Version = "0.1.0-scaffold"

// Main executes the small set of commands that are actually implemented.
func Main(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		usage(out)
		return 0
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(out, "viorka-agent %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return 0
	case "doctor":
		info := map[string]string{
			"product": "Viorka Agent", "version": Version,
			"os": runtime.GOOS, "arch": runtime.GOARCH,
			"runtime": runtime.Version(), "connection": "not-implemented",
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	case "config-check":
		flags := flag.NewFlagSet("config-check", flag.ContinueOnError)
		flags.SetOutput(errOut)
		path := flags.String("config", "examples/agent.example.json", "path to local JSON config")
		if err := flags.Parse(args[1:]); err != nil {
			return 2
		}
		if _, err := config.LoadFile(*path); err != nil {
			fmt.Fprintf(errOut, "invalid configuration: %v\n", err)
			return 2
		}
		fmt.Fprintln(out, "configuration format OK (connection not attempted)")
		return 0
	case "run":
		fmt.Fprintln(errOut, "Agent remote-control runtime not implemented in this architecture scaffold (planned M0).")
		return 2
	case "help", "-h", "--help":
		usage(out)
		return 0
	default:
		fmt.Fprintf(errOut, "unknown command: %s\n", args[0])
		usage(errOut)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: viorka-agent [version|doctor|config-check --config PATH|run|help]")
	fmt.Fprintln(w, "This is an architecture scaffold, not an operational agent.")
}
