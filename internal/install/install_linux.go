package install

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func unitPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "systemd", "user", "used.service")
}

func install(exe string) (string, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "", errors.New("systemd not found: start `used daemon` from your session's autostart instead")
	}
	unit := fmt.Sprintf(`[Unit]
Description=used: presence detection and reporting
After=graphical-session.target

[Service]
ExecStart="%s" daemon
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, strings.ReplaceAll(exe, `"`, `\"`))
	if err := write(unitPath(), unit); err != nil {
		return "", err
	}
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "used.service"},
		{"--user", "restart", "used.service"},
	} {
		if err := run("systemctl", args...); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("systemd user unit %s\n  runs  %s daemon\n  logs  journalctl --user -u used", unitPath(), exe), nil
}

func uninstall() (string, error) {
	_ = run("systemctl", "--user", "disable", "--now", "used.service")
	if err := os.Remove(unitPath()); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	_ = run("systemctl", "--user", "daemon-reload")
	return "removed systemd user unit " + unitPath(), nil
}
