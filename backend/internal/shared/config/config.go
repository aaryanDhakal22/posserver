package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
)

type AppConfig struct {
	AppEnv      string
	Domain      string
	AdminAPIKey string
	// SessionSecret signs the store-manager web session cookie. It MUST be
	// stable + identical across the blue-green slots (Doppler-managed) — a
	// per-boot secret would log every manager out on a deploy swap. Required in
	// stg/prd; a fixed insecure default is used in dev so local runs don't need
	// it set until it's added to the dev Doppler config.
	SessionSecret string
}

// devSessionSecret is the dev-only fallback for SESSION_SECRET (never used when
// APP_ENV != "dev", where the var is required and boot fails fast if unset).
const devSessionSecret = "dev-insecure-session-secret-change-me-please-0123456789"

type ServerConfig struct {
	Port string
	Host string
}
type DatabaseConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
}

type LogConfig struct {
	Level  string
	Output io.Writer
	Style  string
}

type SQSConfig struct {
	QueueURL string
	Region   string
	Endpoint string
}

// OTELConfig holds the OpenTelemetry exporter/resource settings. Endpoint may
// be empty — in that case observability.Setup installs no-op providers and
// the service runs with zero telemetry backend reachable.
type OTELConfig struct {
	Endpoint    string
	ServiceName string
}

type Config struct {
	AppConfig      AppConfig
	ServerConfig   ServerConfig
	DatabaseConfig DatabaseConfig
	LogConfig      LogConfig
	SQSConfig      SQSConfig
	OTELConfig     OTELConfig
	logger         *zerolog.Logger
}

func NewConfig(logger *zerolog.Logger) *Config {
	logger.Info().Msg("Creating config")
	cfg := Config{
		logger: logger,
	}
	appEnv := cfg.GetEnv("APP_ENV", true, "dev")
	appConfig := AppConfig{
		AppEnv:      appEnv,
		Domain:      cfg.GetEnv("DOMAIN", false, "localhost"),
		AdminAPIKey: cfg.GetEnv("ADMIN_API_KEY", true, ""),
		// Required outside dev (fail-fast); dev falls back to a fixed insecure secret.
		SessionSecret: cfg.GetEnv("SESSION_SECRET", appEnv != "dev", devSessionSecret),
	}
	serverConfig := ServerConfig{
		Port: cfg.GetEnv("SERVER_PORT", false, "1323"),
		Host: cfg.GetEnv("HOST", false, "0.0.0.0"),
	}
	databaseConfig := DatabaseConfig{
		Host:     cfg.GetEnv("DB_HOST", true, "localhost"),
		Port:     cfg.GetEnv("DB_PORT", true, "5432"),
		User:     cfg.GetEnv("DB_USER", true, "pguser"),
		Password: cfg.GetEnv("DB_PASSWORD", true, "secretP@ssword"),
		Name:     cfg.GetEnv("DB_NAME", true, "postgres"),
	}
	logConfig := LogConfig{
		Level:  cfg.GetEnv("LOG_LEVEL", false, "info"),
		Output: getLogOutput(cfg.GetEnv("LOG_OUTPUT", false, "standard")),
		Style:  cfg.GetEnv("LOG_STYLE", false, "console"),
	}
	sqsConfig := SQSConfig{
		QueueURL: cfg.GetEnv("SQS_QUEUE_URL", true, ""),
		Region:   cfg.GetEnv("AWS_REGION", false, "us-east-1"),
		Endpoint: cfg.GetEnv("SQS_ENDPOINT", false, ""),
	}
	otelConfig := OTELConfig{
		Endpoint:    cfg.GetEnv("OTEL_EXPORTER_OTLP_ENDPOINT", false, ""),
		ServiceName: cfg.GetEnv("OTEL_SERVICE_NAME", false, "quicc-main"),
	}

	cfg.AppConfig = appConfig
	cfg.ServerConfig = serverConfig
	cfg.DatabaseConfig = databaseConfig
	cfg.LogConfig = logConfig
	cfg.SQSConfig = sqsConfig
	cfg.OTELConfig = otelConfig

	return &cfg
}

// GetEnv reads one env var. It logs only the name and whether it was set —
// never a value or a default: ADMIN_API_KEY, SESSION_SECRET and DB_PASSWORD
// come through here, and the startup logger is fixed at debug level.
func (c *Config) GetEnv(envVar string, req bool, defaultVal string) string {
	val, found := os.LookupEnv(envVar)
	if !found {
		if req {
			c.logger.Error().Msgf("Required Env var %s is not set", envVar)
			c.logger.Fatal().Msg("Exiting...")
			panic("Environment variable " + envVar + " is not set")
		}
		c.logger.Debug().Msgf("Env var %s not set, using the default", envVar)
		return defaultVal
	}
	c.logger.Debug().Msgf("Env var %s set", envVar)
	return val
}

func generateLogFilePath() string {
	filePath := fmt.Sprintf("logs/app_%s.log", time.Now().Format("2006-01-02_15-04-05"))
	return filepath.Join(os.TempDir(), filePath)
}

func getLogOutput(text string) io.Writer {
	if text == "file" {
		filePath := generateLogFilePath()
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Println("Error opening log file, defaulting to stderr")
			return os.Stderr
		}
		return file
	}
	if text == "standard" {
		return os.Stderr
	}
	fmt.Println("Invalid log output, defaulting to stderr")
	return os.Stderr
}
