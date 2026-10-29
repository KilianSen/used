package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPlistIsValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "used.plist")
	if err := os.WriteFile(path, []byte(plist(`/Applications/Odd & "Name"/used`, "/tmp/used.log")), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v\n%s", err, out)
	}
}
