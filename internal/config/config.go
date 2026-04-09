// Package config loads and persists the tiny set of tunables the vig
// daemon exposes. A missing config file is not an error — the defaults
// are what you almost always want.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Config struct {
	ScanIntervalSecs     int  `json:"scan_interval_secs"`
	BaselineRefreshHours int  `json:"baseline_refresh_hours"`
	NotifyOnAnomaly      bool `json:"notify_on_anomaly"`
}

func Default() Config {
	return Config{
		ScanIntervalSecs:     30,
		BaselineRefreshHours: 6,
		NotifyOnAnomaly:      true,
	}
}

func (c Config) ScanInterval() time.Duration {
	if c.ScanIntervalSecs <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.ScanIntervalSecs) * time.Second
}

func (c Config) BaselineRefresh() time.Duration {
	if c.BaselineRefreshHours <= 0 {
		return 6 * time.Hour
	}
	return time.Duration(c.BaselineRefreshHours) * time.Hour
}

func dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vig")
}

// Load reads ~/.config/vig/config.json, layering any present values on top
// of Default(). Missing file → defaults, no error.
func Load() Config {
	data, err := os.ReadFile(filepath.Join(dir(), "config.json"))
	if err != nil {
		return Default()
	}
	cfg := Default()
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

func Save(cfg Config) error {
	d := dir()
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(filepath.Join(d, "config.json"), data, 0o644)
}
