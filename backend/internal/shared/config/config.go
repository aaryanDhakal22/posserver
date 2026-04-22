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
	AppEnv        string
	Domain        string
	AdminPasscode string
}
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

type CacheConfig struct {
	Host string
	Port string
}

type SQSConfig struct {
	QueueURL string
	Region   string
	Endpoint string
}

type Config struct {
	AppConfig      AppConfig
	ServerConfig   ServerConfig
	DatabaseConfig DatabaseConfig
	LogConfig      LogConfig
	CacheConfig    CacheConfig
	SQSConfig      SQSConfig
	logger         *zerolog.Logger
}

func NewConfig(logger *zerolog.Logger) *Config {
	logger.Info().Msg("Creating config")
	cfg := Config{
		logger: logger,
	}
	appConfig := AppConfig{
		AppEnv:        cfg.GetEnv("APP_ENV", true, "dev"),
		Domain:        cfg.GetEnv("DOMAIN", false, "localhost"),
		AdminPasscode: cfg.GetEnv("ADMIN_PASSCODE", true, ""),
	}
	serverConfig := ServerConfig{
		Port: cfg.GetEnv("PORT", false, "1323"),
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
		Output: getLogOutput(cfg.GetEnv("LOG_OUTPUT", false, "stdout")),
		Style:  cfg.GetEnv("LOG_STYLE", false, "console"),
	}
	cacheConfig := CacheConfig{
		Host: cfg.GetEnv("CACHE_HOST", false, "localhost"),
		Port: cfg.GetEnv("CACHE_PORT", false, "6379"),
	}
	sqsConfig := SQSConfig{
		QueueURL: cfg.GetEnv("SQS_QUEUE_URL", true, ""),
		Region:   cfg.GetEnv("AWS_REGION", false, "us-east-1"),
		Endpoint: cfg.GetEnv("SQS_ENDPOINT", false, ""),
	}

	cfg.AppConfig = appConfig
	cfg.ServerConfig = serverConfig
	cfg.DatabaseConfig = databaseConfig
	cfg.LogConfig = logConfig
	cfg.CacheConfig = cacheConfig
	cfg.SQSConfig = sqsConfig

	return &cfg
}

func (c *Config) GetEnv(envVar string, req bool, defaultVal string) string {
	c.logger.Debug().Msgf("Getting env var %s", envVar)
	val, found := os.LookupEnv(envVar)
	if !found {
		c.logger.Debug().Msgf("Env var %s not found", envVar)
		if req {
			c.logger.Error().Msgf("Required Env var %s is not set", envVar)
			c.logger.Fatal().Msg("Exiting...")
			panic("Environment variable " + envVar + " is not set")
		}
		c.logger.Debug().Msgf("Returning default value %s", defaultVal)
		return defaultVal
	}
	c.logger.Debug().Msgf("Env var found, returning %s", val)
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

func getLogLevel() zerolog.Level {
	text := os.Getenv("LOG_LEVEL")
	logLevel, err := zerolog.ParseLevel(text)
	if err != nil {
		fmt.Println("Error parsing log level, defaulting to info")
		logLevel = zerolog.InfoLevel
	}
	return logLevel
}
