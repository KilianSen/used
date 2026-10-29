package install

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
)

const label = "io.github.kiliansen.used"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func logPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "used.log")
}

func plist(exe, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>daemon</string></array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardErrorPath</key><string>%s</string>
	<key>StandardOutPath</key><string>%s</string>
</dict>
</plist>
`, label, html.EscapeString(exe), html.EscapeString(logPath), html.EscapeString(logPath))
}

func install(exe string) (string, error) {
	_ = run("launchctl", "bootout", domain()+"/"+label) // replace a previous install
	if err := write(plistPath(), plist(exe, logPath())); err != nil {
		return "", err
	}
	if err := run("launchctl", "bootstrap", domain(), plistPath()); err != nil {
		return "", err
	}
	return fmt.Sprintf("LaunchAgent %s\n  runs  %s daemon\n  logs  %s", plistPath(), exe, logPath()), nil
}

func uninstall() (string, error) {
	_ = run("launchctl", "bootout", domain()+"/"+label)
	if err := os.Remove(plistPath()); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return "removed LaunchAgent " + plistPath(), nil
}
