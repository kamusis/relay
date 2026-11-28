package steer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
)

func TestInstallAndUninstallHookPreservesExistingSettingsAndLargeInts(t *testing.T) {
	tmp := t.TempDir()
	settingsPath := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}

	// Includes an integer larger than 2^53 to verify json.Decoder UseNumber preserves precision.
	initial := `{
  "theme": "light",
  "largeId": 9007199254740993,
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

	// 1. First install adds relay entry without disturbing existing hooks, keys, or large integers.
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
	if !strings.Contains(string(rawAfterInstall), "9007199254740993") {
		t.Fatalf("large integer lost precision after install:\n%s", string(rawAfterInstall))
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

	// 3. Uninstall removes ONLY the relay entry and keeps existing user hooks & large int intact.
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
	if !strings.Contains(string(rawAfterUninstall), "cc-status") ||
		!strings.Contains(string(rawAfterUninstall), "guard.sh") ||
		!strings.Contains(string(rawAfterUninstall), "9007199254740993") {
		t.Fatalf("existing user hooks or large int lost after uninstall:\n%s", string(rawAfterUninstall))
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

func TestInstallHookPreservesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	tmp := t.TempDir()
	realFile := filepath.Join(tmp, "dotfiles", "claude-settings.json")
	if err := os.MkdirAll(filepath.Dir(realFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realFile, []byte(`{"theme":"light"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realFile, linkPath); err != nil {
		t.Fatal(err)
	}

	changed, err := InstallHook(linkPath, "Edit|Write|MultiEdit|Bash", HookDrainCommand)
	if err != nil || !changed {
		t.Fatalf("InstallHook via symlink = (%v, %v)", changed, err)
	}

	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to remain a symlink after InstallHook", linkPath)
	}
	if !IsHookInstalled(realFile) {
		t.Fatalf("expected backing file %s to contain installed hook", realFile)
	}
}

func TestInstallHookCreatesFreshFileAndRefusesMalformedJSON(t *testing.T) {
	tmp := t.TempDir()

	// Fresh file creation
	fresh := filepath.Join(tmp, ".qoder", "settings.json")
	changed, err := InstallHook(fresh, "Edit|Write|MultiEdit|Bash", HookDrainCommand)
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

	// Malformed JSON and trailing garbage must fail closed without overwriting
	for name, content := range map[string]string{
		"broken.json":   "{not valid json",
		"trailing.json": `{"theme":"light"} extra`,
	} {
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallHook(p, "Edit", HookDrainCommand); err == nil {
			t.Fatalf("expected error for %s", name)
		}
		kept, _ := os.ReadFile(p)
		if string(kept) != content {
			t.Fatalf("%s was unexpectedly modified: %q", name, string(kept))
		}
	}
}

func TestPiExtensionInstallUninstallAndProtection(t *testing.T) {
	tmp := t.TempDir()
	extPath := filepath.Join(tmp, ".pi", "agent", "extensions", "relay-steer.ts")

	if IsPiExtensionInstalled(extPath) {
		t.Fatal("expected IsPiExtensionInstalled=false before install")
	}

	// 1. Fresh install
	changed, err := InstallPiExtension(extPath)
	if err != nil || !changed {
		t.Fatalf("InstallPiExtension #1 = (%v, %v), want (true, nil)", changed, err)
	}
	if !IsPiExtensionInstalled(extPath) {
		t.Fatal("expected IsPiExtensionInstalled=true after install")
	}
	data, err := os.ReadFile(extPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `pi.on("tool_result"`) || !strings.Contains(string(data), `deliverAs: "steer"`) {
		t.Fatalf("unexpected Pi extension content:\n%s", string(data))
	}

	// 2. Second install is idempotent
	changedAgain, err := InstallPiExtension(extPath)
	if err != nil || changedAgain {
		t.Fatalf("InstallPiExtension #2 = (%v, %v), want (false, nil)", changedAgain, err)
	}

	// 3. Uninstall removes relay-managed file cleanly
	removed, err := UninstallPiExtension(extPath)
	if err != nil || !removed {
		t.Fatalf("UninstallPiExtension #1 = (%v, %v), want (true, nil)", removed, err)
	}
	if _, err := os.Stat(extPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be deleted after uninstall", extPath)
	}

	// 4. Uninstall on missing file is idempotent
	removedAgain, err := UninstallPiExtension(extPath)
	if err != nil || removedAgain {
		t.Fatalf("UninstallPiExtension #2 = (%v, %v), want (false, nil)", removedAgain, err)
	}

	// 5. Refuse to overwrite or delete unrelated user-authored file
	if err := os.MkdirAll(filepath.Dir(extPath), 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "// user custom pi extension\nexport default function() {}\n"
	if err := os.WriteFile(extPath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallPiExtension(extPath); err == nil {
		t.Fatal("expected error refusing to overwrite unmanaged extension file")
	}
	if _, err := UninstallPiExtension(extPath); err == nil {
		t.Fatal("expected error refusing to delete unmanaged extension file")
	}
	kept, _ := os.ReadFile(extPath)
	if string(kept) != custom {
		t.Fatalf("unmanaged extension file was modified: %q", string(kept))
	}
}

func TestMissingHookWarningSettingsLocalAndResolveHookAgents(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	// Neither user nor project hook installed -> warns for qoder, claude, pi, and ws target
	if w := MissingHookWarning("qoder:abc123", home); !strings.Contains(w, "hook-install --agent qoder") {
		t.Fatalf("expected qoder warning, got %q", w)
	}
	if w := MissingHookWarning("pi:sess1", home); !strings.Contains(w, "hook-install --agent pi") {
		t.Fatalf("expected pi warning, got %q", w)
	}
	if w := MissingHookWarning(WorkspaceTarget(ws), home); !strings.Contains(w, "hook-install --agent all") {
		t.Fatalf("expected workspace warning, got %q", w)
	}

	// ResolveHookAgents("all") returns claude, qoder, pi in order
	allSpecs, err := ResolveHookAgents("all")
	if err != nil || len(allSpecs) != 3 {
		t.Fatalf("ResolveHookAgents(all) = %+v, %v", allSpecs, err)
	}
	if allSpecs[0].Agent != adapter.AgentClaude || allSpecs[1].Agent != adapter.AgentQoder || allSpecs[2].Agent != adapter.AgentPi {
		t.Fatalf("unexpected agent order: %+v", allSpecs)
	}
	if allSpecs[1].Matcher != "Edit|Write|MultiEdit|Bash" {
		t.Fatalf("expected qoder matcher to include MultiEdit, got %q", allSpecs[1].Matcher)
	}

	// Claude .claude/settings.local.json suppresses workspace warning (N5)
	claudeLocal := filepath.Join(ws, ".claude", "settings.local.json")
	if _, err := InstallHook(claudeLocal, "Edit|Write|MultiEdit|Bash", HookDrainCommand); err != nil {
		t.Fatal(err)
	}
	if w := MissingHookWarning(WorkspaceTarget(ws), home); w != "" {
		t.Fatalf("expected .claude/settings.local.json to satisfy workspace hook check, got %q", w)
	}

	// User-scoped Pi extension suppresses pi:<session> warning
	piUserPath, err := HookTargetPath(allSpecs[2], "user", home, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallAgentHook(allSpecs[2], piUserPath); err != nil {
		t.Fatal(err)
	}
	if w := MissingHookWarning("pi:sess1", home); w != "" {
		t.Fatalf("expected no pi warning once user extension is installed, got %q", w)
	}

	// User-scoped Qoder hook suppresses qoder:<session> warning
	qoderUserPath, err := HookTargetPath(allSpecs[1], "user", home, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallAgentHook(allSpecs[1], qoderUserPath); err != nil {
		t.Fatal(err)
	}
	if w := MissingHookWarning("qoder:abc123", home); w != "" {
		t.Fatalf("expected no qoder warning once user hook is installed, got %q", w)
	}
}
