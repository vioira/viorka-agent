package runtime

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// Command output is bounded even when the service manager is unexpectedly noisy.
type commandOutput struct {
	data      []byte
	truncated bool
}

func (w *commandOutput) Write(p []byte) (int, error) {
	n := len(p)
	left := 8192 - len(w.data)
	if len(p) > left {
		p = p[:left]
		w.truncated = true
	}
	w.data = append(w.data, p...)
	return n, nil
}

func (m *Managed) runLogged(ctx context.Context, path string, args ...string) error {
	started := time.Now()
	command := filepath.Base(path)
	m.logger.Debug("Engine command started", "command", command)
	var output commandOutput
	var writer io.Writer = io.Discard
	// Engine validators can echo credentials from the configuration. Only fixed
	// service-manager commands may contribute raw diagnostic output.
	supervisor := path == "/sbin/rc-service" || path == "/sbin/rc-update" || path == "/usr/bin/systemctl" || path == "/bin/systemctl"
	if supervisor {
		writer = &output
	}
	err := runControlledOutput(ctx, writer, path, args...)
	attrs := []any{"command", command, "duration", time.Since(started)}
	if supervisor {
		attrs = append(attrs, "args", args, "output", strings.TrimSpace(string(output.data)), "output_truncated", output.truncated)
	}
	// A stopped service is an expected status probe result.
	probe := false
	for _, arg := range args {
		if arg == "status" || arg == "is-active" {
			probe = true
		}
	}
	if err != nil {
		attrs = append(attrs, "error", err)
		if ctx.Err() != nil {
			attrs = append(attrs, "context_error", ctx.Err())
		}
		if !probe {
			m.logger.Error("Engine command failed", attrs...)
			return err
		}
	}
	m.logger.Debug("Engine command completed", attrs...)
	return err
}
