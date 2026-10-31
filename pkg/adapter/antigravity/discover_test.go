package antigravity

import (
	"os"
	"testing"
)

func TestDiscoverLS(t *testing.T) {
	// Clear environment variables to force auto-discovery
	origAddr := os.Getenv("ANTIGRAVITY_LS_ADDRESS")
	origToken := os.Getenv("ANTIGRAVITY_CSRF_TOKEN")
	defer func() {
		if origAddr != "" {
			os.Setenv("ANTIGRAVITY_LS_ADDRESS", origAddr)
		}
		if origToken != "" {
			os.Setenv("ANTIGRAVITY_CSRF_TOKEN", origToken)
		}
	}()

	os.Unsetenv("ANTIGRAVITY_LS_ADDRESS")
	os.Unsetenv("ANTIGRAVITY_CSRF_TOKEN")

	ad := New()
	addr, token, err := ad.resolveLSConnection()
	if err != nil {
		t.Logf("resolveLSConnection returned error (expected if language_server is not running): %v", err)
		return
	}

	if addr == "" {
		t.Errorf("expected non-empty discovered address")
	}
	if token == "" {
		t.Errorf("expected non-empty discovered csrf token")
	}
	t.Logf("Successfully discovered Antigravity Language Server: addr=%s token=%s", addr, token)
}
