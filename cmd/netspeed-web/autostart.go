package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const systemdServiceName = "netspeed-web.service"

func autostartSupported() bool {
	return runtime.GOOS == "linux"
}

func systemdUnitPath() (string, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil || cfgDir == "" {
		home, herr := os.UserHomeDir()
		if herr != nil || home == "" {
			return "", fmt.Errorf("cannot determine home/config directory for systemd user unit")
		}
		cfgDir = filepath.Join(home, ".config")
	}
	return filepath.Join(cfgDir, "systemd", "user", systemdServiceName), nil
}

// buildSystemdUnit renders a systemd unit file for netspeed-web on Raspberry Pi OS.
func buildSystemdUnit(execPath, listenAddr, dataDir string, systemWide bool) string {
	args := ""
	if strings.TrimSpace(listenAddr) != "" {
		args += " -addr " + listenAddr
	}
	if strings.TrimSpace(dataDir) != "" {
		args += " -data-dir " + dataDir
	}
	wantedBy := "default.target"
	if systemWide {
		wantedBy = "multi-user.target"
	}
	return fmt.Sprintf(`[Unit]
Description=NetSpeed Web Dashboard & Scheduled Speed Test Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=%s
`, execPath, args, wantedBy)
}

func systemdUnitPreview(listenAddr string) string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "/usr/local/bin/netspeed-web"
	}
	return buildSystemdUnit(exe, listenAddr, "", true)
}

func isAutostartEnabled() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	unitPath, err := systemdUnitPath()
	if err != nil {
		return false
	}
	if _, err := os.Stat(unitPath); err != nil {
		return false
	}
	out, err := exec.Command("systemctl", "--user", "is-enabled", systemdServiceName).Output()
	if err == nil {
		state := strings.TrimSpace(string(out))
		return state == "enabled" || state == "static"
	}
	// If systemd user bus is unavailable (e.g. headless shell), treat existing unit file as enabled.
	return true
}

func setAutostartEnabled(enable bool, listenAddr, dataDir string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd autostart management is only supported on Linux / Raspberry Pi OS")
	}
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if enable {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
			return err
		}
		unitBody := buildSystemdUnit(exe, listenAddr, dataDir, false)
		if err := os.WriteFile(unitPath, []byte(unitBody), 0o644); err != nil {
			return err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		_ = exec.Command("systemctl", "--user", "enable", systemdServiceName).Run()
		return nil
	}

	_ = exec.Command("systemctl", "--user", "disable", "--now", systemdServiceName).Run()
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}
