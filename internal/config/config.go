package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultConfigDir      = ".folder-to-gphotos-album"
	DefaultConfigFile     = "config.json"
	DefaultBatchSize      = 25
	DefaultDebounceDurMs  = 3000
)

// Config holds all application configuration.
type Config struct {
	WatchedFolder     string `json:"watched_folder"`
	AlbumName         string `json:"album_name"`
	BatchSize         int    `json:"batch_size"`
	DebounceDurationMs int   `json:"debounce_duration_ms"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		BatchSize:          DefaultBatchSize,
		DebounceDurationMs: DefaultDebounceDurMs,
	}
}

// ConfigDir returns the path to the application config directory.
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, DefaultConfigDir), nil
}

// ConfigPath returns the full path to the config JSON file.
func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DefaultConfigFile), nil
}

// Load reads the config from disk, returning defaults if the file doesn't exist.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// Apply defaults for zero values.
	if cfg.BatchSize <= 0 || cfg.BatchSize > 50 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.DebounceDurationMs <= 0 {
		cfg.DebounceDurationMs = DefaultDebounceDurMs
	}

	return cfg, nil
}

// Save writes the config to disk, creating the directory if needed.
func (c *Config) Save() error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	path, err := ConfigPath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}
	return nil
}

// Validate returns an error if required config fields are missing or invalid.
func (c *Config) Validate() error {
	if c.WatchedFolder == "" {
		return fmt.Errorf("watched_folder is not configured; run 'folder-to-gphotos-album config'")
	}
	if c.AlbumName == "" {
		return fmt.Errorf("album_name is not configured; run 'folder-to-gphotos-album config'")
	}
	info, err := os.Stat(c.WatchedFolder)
	if err != nil {
		return fmt.Errorf("watched_folder %q is not accessible: %w", c.WatchedFolder, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("watched_folder %q is not a directory", c.WatchedFolder)
	}
	return nil
}
