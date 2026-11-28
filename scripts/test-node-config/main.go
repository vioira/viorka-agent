// Command test-node-config handles local JSON and directory keys for run-test-node.sh.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	arity := map[string]int{"panel": 2, "panel-key": 2, "identity": 2, "new-config": 4}
	if len(args) == 0 || len(args) != arity[args[0]] {
		return errors.New("invalid test-node-config arguments")
	}
	if args[0] == "panel-key" {
		fmt.Printf("%x\n", sha256.Sum256([]byte(args[1])))
		return nil
	}
	cfg, err := config.LoadFile(args[1])
	if err != nil {
		return err
	}
	switch args[0] {
	case "panel":
		fmt.Println(cfg.PanelURL)
	case "identity":
		store, err := state.Open(cfg.StateDir)
		if err != nil {
			return err
		}
		defer store.Close()
		id, err := store.Identity(cfg.PanelURL, false)
		if err != nil {
			return err
		}
		origin := cfg.PanelURL
		if id.ActiveOrigin != "" {
			origin = id.ActiveOrigin
		}
		for _, value := range []string{origin, id.NodeID} {
			if value == "" || strings.IndexFunc(value, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
				return errors.New("本地身份字段无效。")
			}
		}
		fmt.Println(origin)
		fmt.Println(id.NodeID)
	case "new-config":
		cfg.PanelURL, cfg.StateDir = args[2], args[3]
		if err := cfg.Validate(); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(cfg)
	}
	return nil
}
