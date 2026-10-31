package qoder

import (
	"os/exec"
	"strings"
	"testing"
)

func TestFindCLI(t *testing.T) {
	ad := New()
	cliPath, err := ad.FindCLI()
	if err != nil {
		t.Fatalf("FindCLI failed: %v", err)
	}
	t.Logf("Discovered Qoder CLI path: %s", cliPath)

	out, err := exec.Command(cliPath, "-v").Output()
	if err != nil {
		t.Fatalf("failed to run CLI (%s -v): %v", cliPath, err)
	}
	ver := strings.TrimSpace(string(out))
	t.Logf("Discovered Qoder CLI version: %s", ver)
	if ver == "" {
		t.Error("expected non-empty version string")
	}
}
