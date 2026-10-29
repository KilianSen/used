//go:build !darwin && !windows && !linux

package tray

import (
	"context"
	"errors"
)

func run(context.Context, Menu, <-chan Menu, func(string)) error {
	return errors.New("tray: not supported on this OS")
}
