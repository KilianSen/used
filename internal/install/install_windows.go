package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName = "used"

	detachedProcess = 0x00000008
	newProcessGroup = 0x00000200
)

func logPath() string {
	dir, _ := os.UserCacheDir() // %LOCALAPPDATA%
	return filepath.Join(dir, "used", "used.log")
}

func installed() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(valueName)
	return err == nil
}

func install(exe string) (string, error) {
	args := []string{"daemon", "--background", "--log", logPath()}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	cmdline := fmt.Sprintf(`"%s" daemon --background --log "%s"`, exe, logPath())
	if err := k.SetStringValue(valueName, cmdline); err != nil {
		return "", err
	}

	// Start now, detached, so the user doesn't have to log out and in.
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | newProcessGroup, HideWindow: true}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = cmd.Process.Release()
	return fmt.Sprintf("autostart HKCU\\%s\\%s\n  runs  %s\n  logs  %s", runKey, valueName, cmdline, logPath()), nil
}

func uninstall() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	if err := k.DeleteValue(valueName); err != nil && err != registry.ErrNotExist {
		return "", err
	}
	return `removed autostart HKCU\` + runKey + `\` + valueName, nil
}
