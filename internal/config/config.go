// Package config manages the per-user offscreen configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	defaultBase    = "ghcr.io/cirruslabs/macos-tahoe-xcode:latest"
	defaultDisplay = "1440x900" // Points; the 2x framebuffer is 2880x1800 pixels.
)

// Image contains settings for building and connecting to the golden image.
type Image struct {
	Base        string   `toml:"base"`
	Display     string   `toml:"display"`
	SSHUser     string   `toml:"ssh_user"`
	SSHPassword string   `toml:"ssh_password"`
	SSHKey      string   `toml:"ssh_key"`
	Setup       []string `toml:"setup"`
}

// Config contains daemon settings and the golden-image recipe.
type Config struct {
	CurrentImage         string        `toml:"current_image"`
	IdleThreshold        time.Duration `toml:"-"`
	Grace                time.Duration `toml:"-"`
	VMSlots              int           `toml:"vm_slots"`
	LowMemoryThresholdGB int           `toml:"low_memory_threshold_gb"`
	Image                Image         `toml:"image"`
}

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{
		IdleThreshold:        5 * time.Minute,
		Grace:                time.Minute,
		VMSlots:              2,
		LowMemoryThresholdGB: 8,
		Image: Image{
			Base:        defaultBase,
			Display:     defaultDisplay,
			SSHUser:     "admin",
			SSHPassword: "admin",
			Setup:       []string{},
		},
	}
}

// Path returns the location of the per-user config file.
func Path() (string, error) {
	if path := os.Getenv("OFFSCREEN_CONFIG"); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "offscreen", "config.toml"), nil
}

// Load reads the per-user config, using defaults if the file does not exist.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	return LoadFile(path)
}

// LoadFile reads a config from path. Omitted fields retain their defaults.
func LoadFile(path string) (Config, error) {
	cfg := Default()
	contents, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	raw := fileConfig{Config: cfg, IdleThreshold: cfg.IdleThreshold.String(), Grace: cfg.Grace.String()}
	if err := toml.Unmarshal(contents, &raw); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if raw.Config.IdleThreshold, err = time.ParseDuration(raw.IdleThreshold); err != nil {
		return Config{}, fmt.Errorf("invalid idle_threshold: %w", err)
	}
	if raw.Config.Grace, err = time.ParseDuration(raw.Grace); err != nil {
		return Config{}, fmt.Errorf("invalid grace: %w", err)
	}
	return raw.Config, nil
}

// Save writes the per-user config with owner-only permissions.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	return SaveFile(path, cfg)
}

// SaveFile writes a config atomically, creating the parent directory if necessary.
func SaveFile(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary config: %w", err)
	}
	if err := toml.NewEncoder(file).Encode(fileConfig{
		Config:        cfg,
		IdleThreshold: cfg.IdleThreshold.String(),
		Grace:         cfg.Grace.String(),
	}); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// fileConfig stores durations as human-readable TOML strings.
type fileConfig struct {
	Config
	IdleThreshold string `toml:"idle_threshold"`
	Grace         string `toml:"grace"`
}
