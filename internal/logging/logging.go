package logging

import (
	"io"
	"log/slog"
)

func New(output io.Writer, level string) *slog.Logger {
	var parsed slog.Level
	// Config validation happens before constructing the logger.
	_ = parsed.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: parsed}))
}
