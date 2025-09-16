package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the main configuration struct for the Conveyor server.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Security SecurityConfig `yaml:"security"`
}

// ServerConfig holds server-specific settings.
type ServerConfig struct {
	Port     string `yaml:"port"`
	Database string `yaml:"database"`
}

// SecurityConfig holds all secrets and security-related settings.
type SecurityConfig struct {
	SessionKey          string `yaml:"session_key"`
	GitHubWebhookSecret string `yaml:"github_webhook_secret"`
}

// LoadConfig reads the configuration file from a given path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read config file '%s': %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("could not parse config file '%s': %w", path, err)
	}

	// You could add validation here, e.g., check if keys are empty
	if cfg.Security.SessionKey == "" || cfg.Security.GitHubWebhookSecret == "" {
		return nil, fmt.Errorf("session_key and github_webhook_secret must be set in the config file")
	}

	return &cfg, nil
}
