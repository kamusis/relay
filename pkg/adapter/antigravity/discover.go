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

	if addr != "" && token != "" {
		return addr, token, nil
	}

	discoveredAddr, discoveredToken, err := discoverLS()
	if err != nil {
		if addr == "" && token == "" {
			return "", "", fmt.Errorf("antigravity language_server not discovered and environment variables not set: %w", err)
		}
		if addr == "" {
			return "", "", fmt.Errorf("ANTIGRAVITY_LS_ADDRESS is not set and auto-discovery failed: %w", err)
		}
		return "", "", fmt.Errorf("ANTIGRAVITY_CSRF_TOKEN is not set and auto-discovery failed: %w", err)
	}

	if addr == "" {
		addr = discoveredAddr
	}
	if token == "" {
		token = discoveredToken
	}

	return addr, token, nil
}
