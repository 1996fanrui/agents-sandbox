package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// SetupLogger creates a structured JSON logger writing to os.Stderr at the
// given level. The level string is case-insensitive and must be one of
// debug, info, warn, or error.
func SetupLogger(level string) (*slog.Logger, error) {
	return SetupLoggerWithWriter(level, os.Stderr)
}

// SetupLoggerWithWriter creates a structured JSON logger writing to w at the
// given level. Exposed so tests can capture log output into a buffer.
func SetupLoggerWithWriter(level string, w io.Writer) (*slog.Logger, error) {
	if level == "" {
		return nil, fmt.Errorf("log level must not be empty")
	}

	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("parse log level %q: %w", level, err)
	}

	levelVar := &slog.LevelVar{}
	levelVar.Set(lv)

	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: levelVar,
	})
	return slog.New(handler), nil
}
