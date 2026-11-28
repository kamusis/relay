package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvRelayHome overrides the base directory where Relay stores its state files
// (links.json, history.jsonl, steer.jsonl, and templates/).
// When unset or empty, Relay defaults to ~/.config/relay.
const EnvRelayHome = "RELAY_HOME"

// StateDir returns the resolved absolute directory for Relay state files.
// It honors RELAY_HOME when set (expanding a leading "~" and normalizing relative paths),
// and otherwise falls back to ~/.config/relay.
func StateDir() (string, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvRelayHome)); raw != "" {
		expanded, err := expandHomePrefix(raw)
		if err != nil {
			return "", err
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			return "", fmt.Errorf("failed to resolve %s path %q: %w", EnvRelayHome, raw, err)
		}
		return filepath.Clean(abs), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to determine user home directory: %w", err)
	}
	return filepath.Join(home, ".config", "relay"), nil
}

// StateFile returns the full path of a file or subdirectory inside StateDir.
func StateFile(elem ...string) (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	parts := append([]string{dir}, elem...)
	return filepath.Join(parts...), nil
}

func expandHomePrefix(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, "~\\") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to determine user home directory: %w", err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}
