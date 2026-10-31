package antigravity

import (
	"fmt"
	"os"
)

// resolveLSConnection resolves the Antigravity Language Server address and CSRF token.
// It prioritizes explicit environment variables (ANTIGRAVITY_LS_ADDRESS and ANTIGRAVITY_CSRF_TOKEN).
// If either is missing, it dynamically discovers them from the running language_server process.
func (a *Adapter) resolveLSConnection() (string, string, error) {
	addr := os.Getenv("ANTIGRAVITY_LS_ADDRESS")
	token := os.Getenv("ANTIGRAVITY_CSRF_TOKEN")

	// If both explicit environment variables are provided, use them directly
	if addr != "" && token != "" {
		return addr, token, nil
	}
	// Prevent mismatched pairing between partial env vars and auto-discovery
	if addr != "" && token == "" {
		return "", "", fmt.Errorf("ANTIGRAVITY_LS_ADDRESS is set but ANTIGRAVITY_CSRF_TOKEN is missing")
	}
	if addr == "" && token != "" {
		return "", "", fmt.Errorf("ANTIGRAVITY_CSRF_TOKEN is set but ANTIGRAVITY_LS_ADDRESS is missing")
	}

	// Auto-discovery
	return discoverLS()
}
