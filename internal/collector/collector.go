// Package collector implements safe local node inventory and bounded Linux host metrics.
package collector

import "runtime"

type SystemInfo struct {
	GOOS      string `json:"os"`
	GOARCH    string `json:"arch"`
	GoVersion string `json:"go_version"`
}

func Inspect() SystemInfo {
	return SystemInfo{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version()}
}
