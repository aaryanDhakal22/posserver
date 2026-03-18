package logger

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func NewLogger(level string, output io.Writer, style string) zerolog.Logger {
	level = strings.ToLower(level)
	logLevel, err := zerolog.ParseLevel(level)
	if err != nil {
		log.Error().Msgf("Invalid log level Detected: %s", level)
		log.Error().Msg("Defaulting to info")
		logLevel = zerolog.InfoLevel
	}
	var zer zerolog.ConsoleWriter
	if output == nil {
		output = os.Stdout
	}
	zer = zerolog.ConsoleWriter{Out: output, TimeFormat: time.RFC3339}
	zerolog.SetGlobalLevel(logLevel)
	log.Logger = log.Output(zer)
	return zerolog.New(zer).With().Timestamp().Logger()
}

func TempLogger() zerolog.Logger {
	return NewLogger("debug", nil, "console")
}
