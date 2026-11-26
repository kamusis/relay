package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspacePathVariantsAndMatcher(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real", "project")
	subDir := filepath.Join(realDir, ".worktrees", "feat-1")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subDir: %v", err)
	}
	canonicalDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(realDir): %v", err)
	}
	canonicalSub := filepath.Join(canonicalDir, ".worktrees", "feat-1")

	symDir := filepath.Join(tmp, "sym-project")
	if err := os.Symlink(canonicalDir, symDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Variants for symlink include both literal cleaned path and canonical target.
	variants := WorkspacePathVariants(symDir)
	if len(variants) != 2 || variants[0] != filepath.Clean(symDir) || variants[1] != canonicalDir {
		t.Fatalf("unexpected variants for symlink: %+v", variants)
	}

	// Non-existent child under a symlinked parent still resolves the ancestor symlink.
	missingSym := filepath.Join(symDir, "missing-child")
	missingReal := filepath.Join(canonicalDir, "missing-child")
	missingVariants := WorkspacePathVariants(missingSym)
	if len(missingVariants) != 2 || missingVariants[1] != missingReal {
		t.Fatalf("expected ancestor symlink resolution for missing child, got %+v", missingVariants)
	}

	// Empty input yields nil and never matches.
	if got := WorkspacePathVariants("   "); got != nil {
		t.Fatalf("expected nil for empty path, got %+v", got)
	}

	// Symlink filter matches canonical root, canonical subdir, and symlink itself,
	// while rejecting sibling directories that share a string prefix (e.g. project-2).
	symMatcher := NewWorkspaceMatcher(symDir)
	if !symMatcher.Matches(canonicalDir) {
		t.Errorf("symlink matcher should match canonical root")
	}
	if !symMatcher.Matches(canonicalSub) {
		t.Errorf("symlink matcher should match canonical subdir")
	}
	if !symMatcher.Matches(symDir) {
		t.Errorf("symlink matcher should match symlink itself")
	}
	if symMatcher.Matches(canonicalDir + "-other") {
		t.Errorf("symlink matcher must not match sibling directory sharing string prefix")
	}

	// Canonical filter also matches session stored with symlink path.
	realMatcher := NewWorkspaceMatcher(canonicalDir)
	if !realMatcher.Matches(symDir) {
		t.Errorf("canonical matcher should match symlink-stored workspace")
	}
	if !realMatcher.Matches(filepath.Join(symDir, "sub")) {
		t.Errorf("canonical matcher should match subdir under symlink-stored workspace")
	}
}
