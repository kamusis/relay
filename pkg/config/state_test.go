package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateDirDefaultsToUserConfigRelay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(EnvRelayHome, "")

	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir() error = %v", err)
	}
	want := filepath.Join(home, ".config", "relay")
	if got != want {
		t.Fatalf("StateDir() = %q, want %q", got, want)
	}
}

func TestStateDirHonorsRelayHomeAndExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// Explicit absolute RELAY_HOME
	custom := filepath.Join(t.TempDir(), "custom-relay")
	t.Setenv(EnvRelayHome, "  "+custom+"  ")
	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir(abs) error = %v", err)
	}
	if got != custom {
		t.Fatalf("StateDir(abs) = %q, want %q", got, custom)
	}

	// Tilde expansion
	t.Setenv(EnvRelayHome, "~/sandbox-relay")
	gotTilde, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir(tilde) error = %v", err)
	}
	wantTilde := filepath.Join(home, "sandbox-relay")
	if gotTilde != wantTilde {
		t.Fatalf("StateDir(tilde) = %q, want %q", gotTilde, wantTilde)
	}

	// Relative path resolves to absolute
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvRelayHome, "rel-state")
	gotRel, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir(rel) error = %v", err)
	}
	wantRel := filepath.Join(wd, "rel-state")
	if gotRel != wantRel {
		t.Fatalf("StateDir(rel) = %q, want %q", gotRel, wantRel)
	}

	// StateFile appends subpaths cleanly
	filePath, err := StateFile("steer.jsonl")
	if err != nil {
		t.Fatalf("StateFile error = %v", err)
	}
	if filePath != filepath.Join(wantRel, "steer.jsonl") {
		t.Fatalf("StateFile = %q, want %q", filePath, filepath.Join(wantRel, "steer.jsonl"))
	}
}
