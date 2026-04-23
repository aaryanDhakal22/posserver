package logger

import (
	"io"
	"os"
	"strings"
	"time"

	"quiccpos/main/internal/observability"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// NewLogger builds the application logger. If serviceName is non-empty, the
// returned logger is wired with the OTEL trace hook (adds trace_id/span_id
// from ctx) and the OTLP logs bridge (every event becomes an OTLP log
// record). Passing an empty serviceName returns a plain zerolog logger.
func NewLogger(level string, output io.Writer, style string, serviceName string) zerolog.Logger {
	level = strings.ToLower(level)
	logLevel, err := zerolog.ParseLevel(level)
	if err != nil {
		log.Error().Msgf("Invalid log level Detected: %s", level)
		log.Error().Msg("Defaulting to info")
		logLevel = zerolog.InfoLevel
	}
	if output == nil {
		output = os.Stdout
	}

	var writer io.Writer
	if style == "json" {
		writer = output
	} else {
		writer = zerolog.ConsoleWriter{Out: output, TimeFormat: time.RFC3339}
	}

	zerolog.SetGlobalLevel(logLevel)
	log.Logger = log.Output(writer)
	base := zerolog.New(writer).With().Timestamp().Logger()

	if serviceName == "" {
		return base
	}
	return observability.Wire(base, serviceName)
}

func TempLogger() zerolog.Logger {
	return NewLogger("debug", nil, "console", "")
}
