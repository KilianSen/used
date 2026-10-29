// Package install registers `used daemon` to start at login for the current
// user: a LaunchAgent on macOS, a systemd user unit on Linux, and the
// per-user Run key on Windows. No admin rights needed anywhere.
package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Executable is the resolved path of the running binary, which is what gets
// registered. Installing from a temporary build directory is refused.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	if strings.Contains(exe, "go-build") {
		return "", fmt.Errorf("refusing to register a `go run` binary (%s); build or download used first", exe)
	}
	return exe, nil
}

// Install registers and starts the daemon. Returns a human summary.
func Install() (string, error) {
	exe, err := Executable()
	if err != nil {
		return "", err
	}
	return install(exe)
}

// Uninstall stops and unregisters the daemon.
func Uninstall() (string, error) { return uninstall() }

// Installed reports whether autostart is registered for this user.
func Installed() bool { return installed() }

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func write(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
