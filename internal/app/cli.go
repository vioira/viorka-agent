package app

import (
	"context"
	"flag"
	"fmt"
	"github.com/vioira/viorka-agent/internal/connection"
	"github.com/vioira/viorka-agent/internal/state"
	"github.com/vioira/viorka-agent/internal/version"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/vioira/viorka-agent/internal/config"
)

const Version = version.Version

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
	case "update-check":
		return updateCheck(args[1:], out, errOut)
	case "doctor":
		return doctor(args[1:], out, errOut)
	case "config-check":
		flags := flag.NewFlagSet("config-check", flag.ContinueOnError)
		flags.SetOutput(errOut)
		path := flags.String("config", "", "local JSON config (default ~/.viorka-agent/agent.json)")
		if err := flags.Parse(args[1:]); err != nil {
			return 2
		}
		if _, err := loadConfig(*path); err != nil {
			fmt.Fprintf(errOut, "invalid configuration: %v\n", err)
			return 2
		}
		fmt.Fprintln(out, "configuration format OK (connection not attempted)")
		return 0
	case "run", "enroll", "register":
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
	fmt.Fprintln(w, "Usage: viorka-agent [version|update-check [--prerelease] [--config PATH]|doctor [--public-ip] [--panel --config PATH]|config-check --config PATH|enroll --config PATH [--token-stdin] [--non-interactive] [--replace-node ID]|register --config PATH [--token-stdin] [--non-interactive] [--name NAME]|run --config PATH [--non-interactive]|help]")
	fmt.Fprintln(w, "M0 supports enrollment, WSS and read-only node.inspect tasks.")
}

func connect(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	path := flags.String("config", "", "local JSON config (default ~/.viorka-agent/agent.json)")
	name := flags.String("name", "", "node display name for batch registration; defaults to hostname")
	replaceNode := flags.String("replace-node", "", "explicitly replace identity for this node; preserves local state")
	nonInteractive := flags.Bool("non-interactive", false, "disable terminal prompts")
	tokenStdin := flags.Bool("token-stdin", false, "read enrollment token from standard input")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 || (args[0] != "enroll" && *replaceNode != "") || (args[0] != "register" && *name != "") || (args[0] == "run" && *tokenStdin) {
		return 2
	}
	cfg, err := loadConfig(*path)
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
	client.SetLogOutput(errOut)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var prompt *terminalPrompt
	runFailureShown := false
	if !*nonInteractive {
		var closeTerminal func()
		prompt, closeTerminal = openTerminal(ctx)
		defer closeTerminal()
	}
	if args[0] == "enroll" || args[0] == "register" {
		var token string
		if *tokenStdin {
			var b []byte
			b, err = io.ReadAll(io.LimitReader(os.Stdin, 130))
			token = strings.TrimSpace(string(b))
		} else if prompt != nil {
			token, err = prompt.Token()
		} else {
			fmt.Fprintln(errOut, "没有交互终端，请使用 --token-stdin 提供注册令牌")
			return 2
		}
		if err == nil {
			var input enrollmentPrompt
			if prompt != nil {
				input = prompt
			}
			err = enrollWithRetry(ctx, token, func(ctx context.Context, token string) error {
				if args[0] == "register" {
					return client.Register(ctx, token, *name)
				}
				if *replaceNode != "" {
					return client.Reenroll(ctx, token, *replaceNode)
				}
				return client.Enroll(ctx, token)
			}, input, errOut)
			if err != nil {
				return 1 // enrollWithRetry already printed the failure.
			}
		}
	} else if prompt != nil {
		err = client.RunWithRetry(ctx, func(err error) bool {
			fmt.Fprintln(errOut, err)
			retry := prompt.Retry(false)
			runFailureShown = !retry
			return retry
		})
	} else {
		err = client.Run(ctx)
	}
	if err != nil {
		if !runFailureShown {
			fmt.Fprintln(errOut, err)
		}
		return 1
	}
	if args[0] == "enroll" || args[0] == "register" {
		fmt.Fprintln(out, "identity enrolled and saved")
	}
	return 0
}

// loadConfig resolves the default only when no explicit config path was supplied.
func loadConfig(path string) (config.Local, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return config.Local{}, fmt.Errorf("resolve config home: %w", err)
		}
		path = filepath.Join(home, ".viorka-agent", "agent.json")
	}
	return config.LoadFile(path)
}
