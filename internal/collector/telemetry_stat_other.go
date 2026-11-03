//go:build !linux

package collector

import "fmt"

func statTelemetryFilesystem(string) (filesystemCapacity, error) {
	return filesystemCapacity{}, fmt.Errorf("unsupported platform")
}
