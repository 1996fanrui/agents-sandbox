package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestSetupLoggerValidLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			logger, err := SetupLogger(level)
			if err != nil {
				t.Fatalf("SetupLogger(%q) returned error: %v", level, err)
			}
			if logger == nil {
				t.Fatalf("SetupLogger(%q) returned nil logger", level)
			}
		})
	}
}

func TestSetupLoggerCaseInsensitive(t *testing.T) {
	for _, level := range []string{"INFO", "Debug", "WARN", "Error"} {
		t.Run(level, func(t *testing.T) {
			logger, err := SetupLogger(level)
			if err != nil {
				t.Fatalf("SetupLogger(%q) returned error: %v", level, err)
			}
			if logger == nil {
				t.Fatalf("SetupLogger(%q) returned nil logger", level)
			}
		})
	}
}

func TestSetupLoggerInvalidLevel(t *testing.T) {
	_, err := SetupLogger("invalid")
	if err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestSetupLoggerEmptyLevel(t *testing.T) {
	_, err := SetupLogger("")
	if err == nil {
		t.Fatal("expected error for empty level")
	}
}

func TestLoggerOutputsJSON(t *testing.T) {
	var buf bytes.Buffer
	logger, err := SetupLoggerWithWriter("info", &buf)
	if err != nil {
		t.Fatalf("SetupLoggerWithWriter returned error: %v", err)
	}

	logger.Info("test message")

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("log output is not valid JSON: %v\noutput: %s", err, buf.String())
	}
}

func TestLoggerJSONContainsRequiredFields(t *testing.T) {
	var buf bytes.Buffer
	logger, err := SetupLoggerWithWriter("info", &buf)
	if err != nil {
		t.Fatalf("SetupLoggerWithWriter returned error: %v", err)
	}

	logger.Info("hello")

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("log output is not valid JSON: %v", err)
	}
	for _, field := range []string{"time", "level", "msg"} {
		if _, ok := parsed[field]; !ok {
			t.Errorf("expected field %q in JSON output, got: %v", field, parsed)
		}
	}
}

func TestLevelFiltering(t *testing.T) {
	t.Run("info_level_suppresses_debug", func(t *testing.T) {
		var buf bytes.Buffer
		logger, err := SetupLoggerWithWriter("info", &buf)
		if err != nil {
			t.Fatalf("SetupLoggerWithWriter returned error: %v", err)
		}

		logger.Debug("should not appear")
		if buf.Len() != 0 {
			t.Fatalf("info-level logger emitted debug output: %s", buf.String())
		}
	})

	t.Run("debug_level_emits_debug", func(t *testing.T) {
		var buf bytes.Buffer
		logger, err := SetupLoggerWithWriter("debug", &buf)
		if err != nil {
			t.Fatalf("SetupLoggerWithWriter returned error: %v", err)
		}

		logger.Debug("should appear")
		if buf.Len() == 0 {
			t.Fatal("debug-level logger did not emit debug output")
		}
		if !strings.Contains(buf.String(), "should appear") {
			t.Fatalf("unexpected output: %s", buf.String())
		}
	})
}

func TestLoggerWithAttributes(t *testing.T) {
	var buf bytes.Buffer
	logger, err := SetupLoggerWithWriter("info", &buf)
	if err != nil {
		t.Fatalf("SetupLoggerWithWriter returned error: %v", err)
	}

	logger.Info("test", slog.String("key", "value"))

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("log output is not valid JSON: %v", err)
	}
	if parsed["key"] != "value" {
		t.Fatalf("expected key=value in JSON output, got: %v", parsed)
	}
}
