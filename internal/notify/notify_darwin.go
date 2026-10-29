package notify

import (
	"os/exec"
	"strconv"
)

// show uses AppleScript: no bundle or entitlements needed, works from a
// LaunchAgent in the user's GUI session.
func show(title, body string) error {
	script := "display notification " + strconv.Quote(body) + " with title " + strconv.Quote("used") +
		" subtitle " + strconv.Quote(title)
	return exec.Command("osascript", "-e", script).Run()
}
