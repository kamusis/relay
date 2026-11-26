package adapter

import (
	"path/filepath"
	"strings"
)

// resolveSymlinksBestEffort resolves symlinks in cleanPath, falling back to resolving
// the deepest existing ancestor directory when the leaf path does not exist on disk
// (for example, a deleted subdirectory under macOS /tmp -> /private/tmp).
func resolveSymlinksBestEffort(cleanPath string) string {
	if cleanPath == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cleanPath); err == nil {
		return filepath.Clean(resolved)
	}

	var tail []string
	curr := cleanPath
	for {
		parent := filepath.Dir(curr)
		if parent == curr {
			return cleanPath
		}
		tail = append(tail, filepath.Base(curr))
		curr = parent
		if resolved, err := filepath.EvalSymlinks(curr); err == nil {
			parts := make([]string, 0, len(tail)+1)
			parts = append(parts, resolved)
			for i := len(tail) - 1; i >= 0; i-- {
				parts = append(parts, tail[i])
			}
			return filepath.Clean(filepath.Join(parts...))
		}
	}
}

// WorkspacePathVariants returns the cleaned workspace path and, if symlink resolution
// produces a distinct canonical path, the canonical path as well.
func WorkspacePathVariants(p string) []string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return nil
	}
	clean := filepath.Clean(trimmed)
	resolved := resolveSymlinksBestEffort(clean)
	if resolved != "" && resolved != clean {
		return []string{clean, resolved}
	}
	return []string{clean}
}

// WorkspaceMatcher matches session workspace paths against a filter directory,
// handling exact matches, nested subdirectories, and symlinked path aliases
// (such as macOS /tmp -> /private/tmp) on both sides.
type WorkspaceMatcher struct {
	variants []string
	prefixes []string
}

// NewWorkspaceMatcher constructs a reusable WorkspaceMatcher for filterWorkspace.
func NewWorkspaceMatcher(filterWorkspace string) WorkspaceMatcher {
	variants := WorkspacePathVariants(filterWorkspace)
	prefixes := make([]string, len(variants))
	sep := string(filepath.Separator)
	for i, v := range variants {
		if strings.HasSuffix(v, sep) {
			prefixes[i] = v
		} else {
			prefixes[i] = v + sep
		}
	}
	return WorkspaceMatcher{
		variants: variants,
		prefixes: prefixes,
	}
}

// Matches reports whether sessionWS equals or resides within the matcher's filter directory,
// comparing both literal and symlink-resolved variants.
func (m WorkspaceMatcher) Matches(sessionWS string) bool {
	if len(m.variants) == 0 {
		return false
	}
	sessVariants := WorkspacePathVariants(sessionWS)
	if len(sessVariants) == 0 {
		return false
	}
	for _, sv := range sessVariants {
		for i, fv := range m.variants {
			if strings.EqualFold(sv, fv) || strings.HasPrefix(sv, m.prefixes[i]) {
				return true
			}
		}
	}
	return false
}
