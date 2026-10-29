// Package secret resolves config values that may point elsewhere.
package secret

import (
	"fmt"
	"os"
	"strings"
)

// Resolve expands env:NAME and file:/path references; anything else is literal.
func Resolve(v string) (string, error) {
	switch {
	case strings.HasPrefix(v, "env:"):
		name := v[4:]
		val, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("environment variable %s is not set", name)
		}
		return val, nil
	case strings.HasPrefix(v, "file:"):
		b, err := os.ReadFile(v[5:])
		return strings.TrimSpace(string(b)), err
	}
	return v, nil
}
