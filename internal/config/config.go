package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Config holds persistent application preferences shared by netspeed and netspeed-cli.
type Config struct {
	Theme                   string   `json:"theme"` // "dark" or "light"
	Unit                    string   `json:"unit"`  // "Mbps", "MB/s", "Kbps"
	Provider                string   `json:"provider"`
	PreferredServerID       string   `json:"preferred_server_id,omitempty"`
	CustomURL               string   `json:"custom_url,omitempty"`
	SavedCustomURLs         []string `json:"saved_custom_urls,omitempty"`
	Threads                 int      `json:"threads"`
	DurationSeconds         int      `json:"duration_seconds"`
	HideIP                  bool     `json:"hide_ip"`
	CloseToTray             bool     `json:"close_to_tray"`
	StartMinimized          bool     `json:"start_minimized"`
	ScheduleEnabled         bool     `json:"schedule_enabled"`
	ScheduleIntervalMinutes int      `json:"schedule_interval_minutes"`
	TrayNotifications       bool     `json:"tray_notifications"`
	AlertMinDownloadMbps    float64  `json:"alert_min_download_mbps,omitempty"`
}

var ioMu sync.Mutex

// Default returns the default configuration.
func Default() Config {
	return Config{
		Theme:                   "dark",
		Unit:                    "Mbps",
		Provider:                "auto",
		Threads:                 8,
		DurationSeconds:         10,
		HideIP:                  false,
		CloseToTray:             true,
		StartMinimized:          false,
		ScheduleEnabled:         false,
		ScheduleIntervalMinutes: 60,
		TrayNotifications:       true,
		AlertMinDownloadMbps:    0,
	}
}

// AppDir returns the persistent data directory (%AppData%\NetSpeed on Windows).
func AppDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return "."
	}
	dir := filepath.Join(base, "NetSpeed")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// Path returns the full path to config.json.
func Path() string {
	return filepath.Join(AppDir(), "config.json")
}

// Load reads config from disk or returns Default() if not present.
func Load() Config {
	return LoadFrom(Path())
}

// LoadFrom reads config from an explicit file path.
func LoadFrom(path string) Config {
	ioMu.Lock()
	defer ioMu.Unlock()

	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	normalize(&cfg)
	return cfg
}

// Save writes config atomically to disk.
func Save(cfg Config) error {
	return SaveTo(Path(), cfg)
}

// SaveTo writes config atomically to an explicit path.
func SaveTo(path string, cfg Config) error {
	ioMu.Lock()
	defer ioMu.Unlock()

	normalize(&cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data)
}

func normalize(cfg *Config) {
	if cfg.Theme != "light" && cfg.Theme != "dark" {
		cfg.Theme = "dark"
	}
	switch strings.ToLower(cfg.Unit) {
	case "mb/s":
		cfg.Unit = "MB/s"
	case "kbps":
		cfg.Unit = "Kbps"
	default:
		cfg.Unit = "Mbps"
	}
	switch cfg.Provider {
	case "auto", "ookla", "cloudflare", "custom":
	default:
		cfg.Provider = "auto"
	}
	if cfg.Threads < 1 {
		cfg.Threads = 8
	}
	if cfg.Threads > 32 {
		cfg.Threads = 32
	}
	if cfg.DurationSeconds < 1 {
		cfg.DurationSeconds = 10
	}
	if cfg.ScheduleIntervalMinutes < 1 {
		cfg.ScheduleIntervalMinutes = 60
	}
	if cfg.AlertMinDownloadMbps < 0 {
		cfg.AlertMinDownloadMbps = 0
	}
}

// AddSavedCustomURL prepends a custom URL to the recent list, keeping at most 10 unique items.
func (c *Config) AddSavedCustomURL(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	c.CustomURL = raw
	out := []string{raw}
	for _, item := range c.SavedCustomURLs {
		if item != raw && item != "" {
			out = append(out, item)
		}
		if len(out) >= 10 {
			break
		}
	}
	c.SavedCustomURLs = out
}

func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		// Windows os.Rename replaces existing files since Go 1.5, but fall back cleanly if needed.
		_ = os.Remove(path)
		if err2 := os.Rename(tmpName, path); err2 != nil {
			_ = os.Remove(tmpName)
			return err2
		}
	}
	return nil
}
