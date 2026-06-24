package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Security SecurityConfig `yaml:"security"`
	Admin    AdminConfig    `yaml:"admin"`
	Database DatabaseConfig `yaml:"database"`
	Poller   PollerConfig   `yaml:"poller"`
}

type ServerConfig struct {
	Addr string `yaml:"addr"`
}

type SecurityConfig struct {
	SessionSecret string `yaml:"session_secret"`
	EncryptionKey string `yaml:"encryption_key"`
}

type AdminConfig struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
}

type DatabaseConfig struct {
	DSN string `yaml:"dsn"`
}

type PollerConfig struct {
	IntervalSeconds int `yaml:"interval_seconds"`
}

func Load(path string) (Config, error) {
	cfg := Config{}
	cfg.Server.Addr = ":8080"
	cfg.Poller.IntervalSeconds = 1800

	if path == "" {
		path = "config.yaml"
	}

	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	applyEnv(&cfg)

	if cfg.Server.Addr == "" {
		cfg.Server.Addr = ":8080"
	}
	if cfg.Poller.IntervalSeconds <= 0 {
		cfg.Poller.IntervalSeconds = 1800
	}
	if cfg.Admin.Username == "" {
		return Config{}, errors.New("admin.username is required")
	}
	if cfg.Admin.PasswordHash == "" {
		return Config{}, errors.New("admin.password_hash is required")
	}
	if cfg.Security.SessionSecret == "" {
		return Config{}, errors.New("security.session_secret is required")
	}
	if cfg.Security.EncryptionKey == "" {
		return Config{}, errors.New("security.encryption_key is required")
	}
	if cfg.Database.DSN == "" {
		return Config{}, errors.New("database.dsn is required")
	}

	return cfg, nil
}

func applyEnv(cfg *Config) {
	setString(&cfg.Server.Addr, "MID_SERVER_ADDR")
	setString(&cfg.Security.SessionSecret, "MID_SESSION_SECRET")
	setString(&cfg.Security.EncryptionKey, "MID_ENCRYPTION_KEY")
	setString(&cfg.Admin.Username, "MID_ADMIN_USERNAME")
	setString(&cfg.Admin.PasswordHash, "MID_ADMIN_PASSWORD_HASH")
	setString(&cfg.Database.DSN, "MID_DATABASE_DSN")

	if v := strings.TrimSpace(os.Getenv("MID_POLLER_INTERVAL_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Poller.IntervalSeconds = n
		}
	}
}

func setString(target *string, key string) {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		*target = v
	}
}
