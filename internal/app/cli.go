package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/vioira/viorka-agent/internal/connection"
	"github.com/vioira/viorka-agent/internal/state"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/vioira/viorka-agent/internal/config"
)

const Version = "0.1.0-m0"

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
			"runtime": runtime.Version(), "connection": "not-checked",
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
	case "run", "enroll":
		return connect(args, out, errOut)
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
	fmt.Fprintln(w, "Usage: viorka-agent [version|doctor|config-check --config PATH|enroll --config PATH --token-stdin|run --config PATH|help]")
	fmt.Fprintln(w, "M0 supports enrollment, WSS and read-only node.inspect tasks.")
}

func connect(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	path := flags.String("config", "examples/agent.example.json", "local config file")
	tokenStdin := flags.Bool("token-stdin", false, "read enrollment token from standard input")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		return 2
	}
	cfg, err := config.LoadFile(*path)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	store, err := state.Open(cfg.StateDir)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	defer store.Close()
	client, err := connection.New(cfg, store)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	defer client.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if args[0] == "enroll" {
		if !*tokenStdin {
			fmt.Fprintln(errOut, "enroll requires --token-stdin")
			return 2
		}
		var b []byte
		b, err = io.ReadAll(io.LimitReader(os.Stdin, 130))
		if err == nil {
			token := strings.TrimSpace(string(b))
			if len(token) != 64 {
				err = fmt.Errorf("invalid enrollment token length")
			} else {
				err = client.Enroll(ctx, token)
			}
		}
	} else {
		err = client.Run(ctx)
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if args[0] == "enroll" {
		fmt.Fprintln(out, "identity enrolled and saved")
	}
	return 0
}
