package steer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
)

func TestInstallAndUninstallHookPreservesExistingSettings(t *testing.T) {
	tmp := t.TempDir()
	settingsPath := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}

	initial := `{
  "theme": "light",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "guard.sh"}]}
    ],
    "PostToolUse": [
      {"hooks": [{"type": "command", "command": "/Users/test/.config/iterm2/cc-status"}]}
    ]
  }
}
`
	if err := os.WriteFile(settingsPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	if IsHookInstalled(settingsPath) {
		t.Fatal("expected IsHookInstalled=false before install")
	}

	// 1. First install adds relay entry without disturbing existing hooks or keys.
	changed, err := InstallHook(settingsPath, "Edit|Write|MultiEdit|Bash", HookDrainCommand)
	if err != nil || !changed {
		t.Fatalf("InstallHook #1 = (%v, %v), want (true, nil)", changed, err)
	}
	if !IsHookInstalled(settingsPath) {
		t.Fatal("expected IsHookInstalled=true after install")
	}

	// 2. Second install is idempotent.
	changedAgain, err := InstallHook(settingsPath, "Edit|Write|MultiEdit|Bash", HookDrainCommand)
	if err != nil || changedAgain {
		t.Fatalf("InstallHook #2 = (%v, %v), want (false, nil)", changedAgain, err)
	}

	rawAfterInstall, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(rawAfterInstall, &parsed); err != nil {
		t.Fatalf("invalid JSON after install: %v", err)
	}
	if parsed["theme"] != "light" {
		t.Fatalf("existing top-level key lost: %+v", parsed)
	}
	hooksMap := parsed["hooks"].(map[string]any)
	if _, ok := hooksMap["PreToolUse"]; !ok {
		t.Fatal("existing PreToolUse hook lost")
	}
	postList := hooksMap["PostToolUse"].([]any)
	if len(postList) != 2 {
		t.Fatalf("expected 2 PostToolUse entries (existing + relay), got %d", len(postList))
	}

	// 3. Uninstall removes ONLY the relay entry and keeps existing user hook intact.
	removed, err := UninstallHook(settingsPath)
	if err != nil || !removed {
		t.Fatalf("UninstallHook #1 = (%v, %v), want (true, nil)", removed, err)
	}
	if IsHookInstalled(settingsPath) {
		t.Fatal("expected IsHookInstalled=false after uninstall")
	}

	rawAfterUninstall, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawAfterUninstall), "cc-status") || !strings.Contains(string(rawAfterUninstall), "guard.sh") {
		t.Fatalf("existing user hooks lost after uninstall:\n%s", string(rawAfterUninstall))
	}
	if strings.Contains(string(rawAfterUninstall), "relay steer drain") {
		t.Fatalf("relay hook still present after uninstall:\n%s", string(rawAfterUninstall))
	}

	// 4. Second uninstall is idempotent.
	removedAgain, err := UninstallHook(settingsPath)
	if err != nil || removedAgain {
		t.Fatalf("UninstallHook #2 = (%v, %v), want (false, nil)", removedAgain, err)
	}
}

func TestInstallHookCreatesFreshFileAndRefusesMalformedJSON(t *testing.T) {
	tmp := t.TempDir()

	// Fresh file creation
	fresh := filepath.Join(tmp, ".qoder", "settings.json")
	changed, err := InstallHook(fresh, "Edit|Write|Bash", HookDrainCommand)
	if err != nil || !changed {
		t.Fatalf("InstallHook fresh = (%v, %v)", changed, err)
	}
	if !IsHookInstalled(fresh) {
		t.Fatal("expected fresh qoder hook installed")
	}

	// Uninstall cleans up empty PostToolUse and empty hooks object
	removed, err := UninstallHook(fresh)
	if err != nil || !removed {
		t.Fatalf("UninstallHook fresh = (%v, %v)", removed, err)
	}
	raw, _ := os.ReadFile(fresh)
	if strings.Contains(string(raw), "PostToolUse") || strings.Contains(string(raw), "hooks") {
		t.Fatalf("expected empty hooks object pruned on clean uninstall, got:\n%s", string(raw))
	}

	// Malformed JSON must fail closed without overwriting
	broken := filepath.Join(tmp, "broken.json")
	if err := os.WriteFile(broken, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(broken, "Edit", HookDrainCommand); err == nil {
		t.Fatal("expected error for malformed JSON settings file")
	}
	kept, _ := os.ReadFile(broken)
	if string(kept) != "{not valid json" {
		t.Fatalf("malformed file was unexpectedly modified: %q", string(kept))
	}
}

func TestMissingHookWarningAndResolveHookAgents(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	// Neither user nor project hook installed -> warns for qoder, claude, and ws target; silent for pi
	if w := MissingHookWarning("qoder:abc123", home); !strings.Contains(w, "hook-install --agent qoder") {
		t.Fatalf("expected qoder warning, got %q", w)
	}
	if w := MissingHookWarning(WorkspaceTarget(ws), home); !strings.Contains(w, "hook-install --agent all") {
		t.Fatalf("expected workspace warning, got %q", w)
	}
	if w := MissingHookWarning("pi:sess1", home); w != "" {
		t.Fatalf("expected no warning for pi target, got %q", w)
	}

	// Install project-scoped hook for qoder -> workspace warning disappears, qoder user check still warns
	specs, err := ResolveHookAgents("qoder")
	if err != nil || len(specs) != 1 || specs[0].Agent != adapter.AgentQoder {
		t.Fatalf("ResolveHookAgents(qoder) = %+v, %v", specs, err)
	}
	projPath, err := HookSettingsPath(specs[0], "project", home, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(projPath, specs[0].Matcher, HookDrainCommand); err != nil {
		t.Fatal(err)
	}
	if w := MissingHookWarning(WorkspaceTarget(ws), home); w != "" {
		t.Fatalf("expected no workspace warning once project hook is installed, got %q", w)
	}

	// Install user-scoped hook for qoder -> qoder session warning disappears
	userPath, err := HookSettingsPath(specs[0], "user", home, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(userPath, specs[0].Matcher, HookDrainCommand); err != nil {
		t.Fatal(err)
	}
	if w := MissingHookWarning("qoder:abc123", home); w != "" {
		t.Fatalf("expected no qoder warning once user hook is installed, got %q", w)
	}

	// Pi returns actionable error for hook-install
	if _, err := ResolveHookAgents("pi"); err == nil {
		t.Fatal("expected error for hook-install --agent pi")
	}
}
