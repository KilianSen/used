//go:build !darwin && !windows && !linux

package notify

import "errors"

func show(string, string) error { return errors.New("notifications are not supported on this OS") }
