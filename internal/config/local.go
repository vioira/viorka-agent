// Package config provides strictly local non-secret bootstrap configuration.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

type Local struct {
	PanelURL string `json:"panel_url"`
	NodeName string `json:"node_name"`
	StateDir string `json:"state_dir"`
	LogLevel string `json:"log_level"`
}

func LoadFile(path string) (Local, error) {
	f, err := os.Open(path)
	if err != nil {
		return Local{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var cfg Local
	if err := dec.Decode(&cfg); err != nil {
		return Local{}, err
	}
	var rest any
	if err := dec.Decode(&rest); err != io.EOF {
		return Local{}, fmt.Errorf("unexpected trailing JSON content")
	}
	return cfg, cfg.Validate()
}

func (c Local) Validate() error {
	u, err := url.Parse(c.PanelURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return fmt.Errorf("panel_url must be an HTTPS base origin without credentials, query, path or fragment")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("panel_url must use HTTPS")
	}
	if strings.TrimSpace(c.NodeName) == "" || strings.TrimSpace(c.StateDir) == "" {
		return fmt.Errorf("node_name and state_dir are required")
	}
	if c.LogLevel != "" && c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return fmt.Errorf("unsupported log_level")
	}
	return nil
}
