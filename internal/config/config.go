// Package config loads hrd's user configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// HerdrConfigSync controls when hrd copies local Herdr config to a sandbox.
type HerdrConfigSync uint8

const (
	HerdrConfigSyncNever HerdrConfigSync = iota + 1
	HerdrConfigSyncInstall
	HerdrConfigSyncOpen
)

// Settings contains validated user configuration.
type Settings struct {
	HerdrConfigSync HerdrConfigSync
}

type document struct {
	HerdrConfigSync *string `toml:"herdr_config_sync"`
}

// Load reads hrd's standard user config file. A missing file uses defaults.
func Load() (Settings, error) {
	path, err := defaultPath()
	if err != nil {
		return Settings{}, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultSettings(), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("open hrd config %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var raw document
	metadata, err := toml.NewDecoder(file).Decode(&raw)
	if err != nil {
		return Settings{}, fmt.Errorf("decode hrd config %q: %w", path, err)
	}
	if unknown := metadata.Undecoded(); len(unknown) > 0 {
		return Settings{}, fmt.Errorf("decode hrd config %q: unknown setting %q", path, unknown[0])
	}
	settings := defaultSettings()
	if raw.HerdrConfigSync == nil {
		return settings, nil
	}
	switch *raw.HerdrConfigSync {
	case "never":
		settings.HerdrConfigSync = HerdrConfigSyncNever
	case "install":
		settings.HerdrConfigSync = HerdrConfigSyncInstall
	case "open":
		settings.HerdrConfigSync = HerdrConfigSyncOpen
	default:
		return Settings{}, fmt.Errorf("decode hrd config %q: herdr_config_sync must be one of never, install, or open", path)
	}
	return settings, nil
}

func defaultSettings() Settings {
	return Settings{HerdrConfigSync: HerdrConfigSyncInstall}
}

func defaultPath() (string, error) {
	directory := os.Getenv("XDG_CONFIG_HOME")
	if directory == "" {
		var err error
		directory, err = os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("find hrd config directory: %w", err)
		}
	}
	if !filepath.IsAbs(directory) {
		return "", errors.New("find hrd config directory: config path is not absolute")
	}
	return filepath.Join(directory, "hrd", "config.toml"), nil
}
