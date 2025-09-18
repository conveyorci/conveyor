package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the main configuration struct for the Conveyor server.
type Config struct {
	Server       ServerConfig   `yaml:"server"`
	Security     SecurityConfig `yaml:"security"`
	ResolvedPath string         `yaml:"-"`
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

// findConfigFile implements the search logic for the config file.
func findConfigFile() (string, error) {
	searchPaths := []string{}
	searchPaths = append(searchPaths, "config.yml")

	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		searchPaths = append(searchPaths, filepath.Join(exeDir, "config.yml"))
	}

	if homeDir, err := os.UserHomeDir(); err == nil {
		searchPaths = append(searchPaths, filepath.Join(homeDir, ".config", "conveyor", "config.yml"))
	}

	searchPaths = append(searchPaths, "/etc/conveyor/config.yml")

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil // Found it!
		}
	}

	return "", nil // Not found anywhere
}

// LoadConfig reads the configuration file from a given path or searches for it.
func LoadConfig(path string) (*Config, error) {
	var configPath string
	var err error

	if path != "" {
		configPath = path
	} else {
		configPath, err = findConfigFile()
		if err != nil {
			return nil, err
		}
		if configPath == "" {
			return nil, fmt.Errorf("config file not found. Please provide one with -config flag, or place config.yml in one of the standard locations (e.g., next to the executable)")
		}
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("could not read config file '%s': %w", configPath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("could not parse config file '%s': %w", configPath, err)
	}

	absPath, err := filepath.Abs(configPath)
	if err != nil {
		cfg.ResolvedPath = configPath // Fallback to relative path on error
	} else {
		cfg.ResolvedPath = absPath
	}

	if !filepath.IsAbs(cfg.Server.Database) {
		configDir := filepath.Dir(cfg.ResolvedPath)
		cfg.Server.Database = filepath.Join(configDir, cfg.Server.Database)
	}

	if cfg.Security.SessionKey == "" || cfg.Security.GitHubWebhookSecret == "" {
		return nil, fmt.Errorf("session_key and github_webhook_secret must be set in the config file")
	}

	return &cfg, nil
}
