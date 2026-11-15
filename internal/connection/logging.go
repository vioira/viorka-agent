package connection

import (
	"io"
	"log/slog"
)

func parseLogLevel(value string) slog.Level {
	switch value {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}

// SetLogOutput configures the destination before Run or Enroll starts.
func (c *Client) SetLogOutput(w io.Writer) {
	c.lifecycleLogger = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
	c.logger = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: c.logLevel}))
}
func (c *Client) applyLogLevel(value string) {
	if c.logLevel != nil {
		c.logLevel.Set(parseLogLevel(value))
	}
}

func (c *Client) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// Lifecycle messages remain visible even when routine diagnostics are filtered.
// Configure the output before starting the client, just like the regular logger.
func (c *Client) lifecycle() *slog.Logger {
	if c.lifecycleLogger != nil {
		return c.lifecycleLogger
	}
	return c.log()
}
