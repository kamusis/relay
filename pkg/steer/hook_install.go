package steer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kamusis/relay/pkg/adapter"
)

// HookDrainCommand is the canonical command installed into PostToolUse JSON hooks.
const HookDrainCommand = "relay steer drain --format hook-json --read-hook-stdin"

// PiExtensionMarker uniquely identifies the relay-managed Pi extension file.
const PiExtensionMarker = "relay-steer-extension"

// PiExtensionSource is the TypeScript extension installed into ~/.pi/agent/extensions/relay-steer.ts
// or <project>/.pi/extensions/relay-steer.ts for Pi Coding Agent.
const PiExtensionSource = `import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";

// Managed by relay steer hook-install (relay-steer-extension).
// Runs: relay steer drain --format text after mutating tool calls and injects
// pending directives into the active Pi agent loop via deliverAs: "steer".
const MONITORED_TOOLS = new Set(["edit", "write", "bash"]);

export default function relaySteerExtension(pi: ExtensionAPI) {
  pi.on("tool_result", async (event, ctx) => {
    if (!MONITORED_TOOLS.has(event.toolName)) {
      return;
    }
    const args = ["steer", "drain", "--format", "text"];
    const cwd = ctx.cwd || ctx.sessionManager.getCwd();
    if (cwd) {
      args.push("--workspace", cwd);
    }
    const sessionId = ctx.sessionManager.getSessionId?.();
    if (sessionId) {
      args.push("--target", ` + "`pi:${sessionId}`" + `);
    }
    try {
      const res = await pi.exec("relay", args, { timeout: 5000 });
      if (res.code !== 0) {
        return;
      }
      const text = (res.stdout || "").trim();
      if (!text) {
        return;
      }
      pi.sendUserMessage(text, { deliverAs: "steer" });
    } catch {
      // Fail open so tool execution is never interrupted if relay is unavailable.
    }
  });
}
`

// HookInstallKind identifies how a hook-capable agent loads its steering integration.
type HookInstallKind string

const (
	// HookKindJSONSettings installs a PostToolUse command hook in settings.json.
	HookKindJSONSettings HookInstallKind = "json-settings"
	// HookKindPiExtension installs a TypeScript extension file discovered by Pi.
	HookKindPiExtension HookInstallKind = "pi-extension"
)

// HookAgentSpec defines file locations and installation strategy for a hook-capable agent.
type HookAgentSpec struct {
	Agent            adapter.AgentType
	Kind             HookInstallKind
	UserPathRel      string
	ProjectPathRel   string
	ProjectCheckRels []string // Extra project-level paths inspected during detection (e.g. .claude/settings.local.json).
	Matcher          string
}

var supportedHookSpecs = map[adapter.AgentType]HookAgentSpec{
	adapter.AgentClaude: {
		Agent:            adapter.AgentClaude,
		Kind:             HookKindJSONSettings,
		UserPathRel:      filepath.Join(".claude", "settings.json"),
		ProjectPathRel:   filepath.Join(".claude", "settings.json"),
		ProjectCheckRels: []string{filepath.Join(".claude", "settings.local.json")},
		Matcher:          "Edit|Write|MultiEdit|Bash",
	},
	adapter.AgentQoder: {
		Agent:          adapter.AgentQoder,
		Kind:           HookKindJSONSettings,
		UserPathRel:    filepath.Join(".qoder", "settings.json"),
		ProjectPathRel: filepath.Join(".qoder", "settings.json"),
		Matcher:        "Edit|Write|MultiEdit|Bash",
	},
	adapter.AgentPi: {
		Agent:          adapter.AgentPi,
		Kind:           HookKindPiExtension,
		UserPathRel:    filepath.Join(".pi", "agent", "extensions", "relay-steer.ts"),
		ProjectPathRel: filepath.Join(".pi", "extensions", "relay-steer.ts"),
	},
}

// ResolveHookAgents expands an agent selector ("claude", "qoder", "pi", or "all") into ordered HookAgentSpecs.
func ResolveHookAgents(selector string) ([]HookAgentSpec, error) {
	norm := strings.ToLower(strings.TrimSpace(selector))
	switch norm {
	case "", "all":
		return []HookAgentSpec{
			supportedHookSpecs[adapter.AgentClaude],
			supportedHookSpecs[adapter.AgentQoder],
			supportedHookSpecs[adapter.AgentPi],
		}, nil
	case "claude", "claudecode":
		return []HookAgentSpec{supportedHookSpecs[adapter.AgentClaude]}, nil
	case "qoder", "qodercli":
		return []HookAgentSpec{supportedHookSpecs[adapter.AgentQoder]}, nil
	case "pi", "pi-agent":
		return []HookAgentSpec{supportedHookSpecs[adapter.AgentPi]}, nil
	default:
		return nil, fmt.Errorf("unsupported hook-install agent '%s' (supported: claude, qoder, pi, all)", selector)
	}
}

// HookTargetPath returns the settings or extension file path for spec under "user" or "project" scope.
func HookTargetPath(spec HookAgentSpec, scope, homeDir, projectDir string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", "user":
		if strings.TrimSpace(homeDir) == "" {
			h, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("unable to determine user home directory: %w", err)
			}
			homeDir = h
		}
		return filepath.Join(homeDir, spec.UserPathRel), nil
	case "project":
		if strings.TrimSpace(projectDir) == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return "", fmt.Errorf("unable to determine working directory: %w", err)
			}
			projectDir = cwd
		}
		absDir, err := filepath.Abs(projectDir)
		if err != nil {
			return "", err
		}
		return filepath.Join(absDir, spec.ProjectPathRel), nil
	default:
		return "", fmt.Errorf("invalid --scope '%s': expected 'user' or 'project'", scope)
	}
}

// isRelaySteerCommand reports whether a hook command string invokes relay steer drain.
func isRelaySteerCommand(cmd string) bool {
	return strings.Contains(cmd, "relay steer drain") || strings.Contains(cmd, "relay-steer-hook.sh")
}

// hasRelayHookInPostToolUse checks whether any PostToolUse entry already runs relay steer drain.
func hasRelayHookInPostToolUse(entries []any) bool {
	for _, item := range entries {
		entryMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hookList, ok := entryMap["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hookList {
			hMap, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, ok := hMap["command"].(string); ok && isRelaySteerCommand(cmd) {
				return true
			}
		}
	}
	return false
}

// IsHookInstalled reports whether settingsPath exists, parses as valid JSON,
// and contains a relay steer PostToolUse hook entry.
func IsHookInstalled(settingsPath string) bool {
	data, err := os.ReadFile(settingsPath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return false
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return false
	}
	hooksMap, ok := root["hooks"].(map[string]any)
	if !ok {
		return false
	}
	postToolUse, ok := hooksMap["PostToolUse"].([]any)
	if !ok {
		return false
	}
	return hasRelayHookInPostToolUse(postToolUse)
}

// IsPiExtensionInstalled reports whether extPath exists and contains the relay-managed Pi extension marker.
func IsPiExtensionInstalled(extPath string) bool {
	data, err := os.ReadFile(extPath)
	if err != nil {
		return false
	}
	content := string(data)
	return strings.Contains(content, PiExtensionMarker) || strings.Contains(content, "relay steer drain")
}

// IsAgentHookInstalledAt dispatches installation status detection based on spec.Kind.
func IsAgentHookInstalledAt(spec HookAgentSpec, targetPath string) bool {
	if spec.Kind == HookKindPiExtension {
		return IsPiExtensionInstalled(targetPath)
	}
	return IsHookInstalled(targetPath)
}

// InstallAgentHook dispatches installation based on spec.Kind.
func InstallAgentHook(spec HookAgentSpec, targetPath string) (bool, error) {
	if spec.Kind == HookKindPiExtension {
		return InstallPiExtension(targetPath)
	}
	return InstallHook(targetPath, spec.Matcher, HookDrainCommand)
}

// UninstallAgentHook dispatches removal based on spec.Kind.
func UninstallAgentHook(spec HookAgentSpec, targetPath string) (bool, error) {
	if spec.Kind == HookKindPiExtension {
		return UninstallPiExtension(targetPath)
	}
	return UninstallHook(targetPath)
}

// CheckAgentHookInstalled checks user and optional project paths (including extra project check paths
// such as .claude/settings.local.json) and returns all paths where the relay steer integration is present.
func CheckAgentHookInstalled(spec HookAgentSpec, homeDir, projectDir string) []string {
	var found []string
	seen := make(map[string]struct{})
	addIfInstalled := func(p string) {
		clean := filepath.Clean(p)
		if _, exists := seen[clean]; exists {
			return
		}
		if IsAgentHookInstalledAt(spec, clean) {
			seen[clean] = struct{}{}
			found = append(found, clean)
		}
	}

	if userPath, err := HookTargetPath(spec, "user", homeDir, ""); err == nil {
		addIfInstalled(userPath)
	}
	if strings.TrimSpace(projectDir) != "" {
		if projPath, err := HookTargetPath(spec, "project", "", projectDir); err == nil {
			addIfInstalled(projPath)
		}
		if absDir, err := filepath.Abs(projectDir); err == nil {
			for _, extraRel := range spec.ProjectCheckRels {
				addIfInstalled(filepath.Join(absDir, extraRel))
			}
		}
	}
	return found
}

// InstallPiExtension writes the relay-managed TypeScript extension to extPath atomically.
// It is idempotent when identical content is already installed and refuses to overwrite
// an unrelated user-authored file lacking the relay marker.
func InstallPiExtension(extPath string) (bool, error) {
	var fileMode os.FileMode = 0o644
	if info, err := os.Stat(extPath); err == nil {
		fileMode = info.Mode().Perm()
		existing, readErr := os.ReadFile(extPath)
		if readErr != nil {
			return false, fmt.Errorf("failed to read %s: %w", extPath, readErr)
		}
		if string(existing) == PiExtensionSource {
			return false, nil
		}
		if !strings.Contains(string(existing), PiExtensionMarker) && !strings.Contains(string(existing), "relay steer drain") {
			return false, fmt.Errorf("%s already exists and is not managed by relay (refusing to overwrite)", extPath)
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to stat %s: %w", extPath, err)
	}

	if err := writeFileAtomic(extPath, []byte(PiExtensionSource), fileMode); err != nil {
		return false, err
	}
	return true, nil
}

// UninstallPiExtension removes the relay-managed Pi extension at extPath.
// Returns removed=false if the file does not exist, and refuses to delete files without the relay marker.
func UninstallPiExtension(extPath string) (bool, error) {
	data, err := os.ReadFile(extPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to read %s: %w", extPath, err)
	}
	content := string(data)
	if !strings.Contains(content, PiExtensionMarker) && !strings.Contains(content, "relay steer drain") {
		return false, fmt.Errorf("%s is not managed by relay (refusing to delete)", extPath)
	}
	if err := os.Remove(extPath); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to remove %s: %w", extPath, err)
	}
	return true, nil
}

// InstallHook idempotently merges the relay steer PostToolUse hook into settingsPath
// while preserving all existing top-level keys and hooks. Returns changed=false if already installed.
func InstallHook(settingsPath, matcher, command string) (bool, error) {
	if strings.TrimSpace(command) == "" {
		command = HookDrainCommand
	}

	root, fileMode, err := readSettingsJSON(settingsPath)
	if err != nil {
		return false, err
	}

	var hooksMap map[string]any
	if rawHooks, exists := root["hooks"]; exists && rawHooks != nil {
		m, ok := rawHooks.(map[string]any)
		if !ok {
			return false, fmt.Errorf("%s: 'hooks' field is not a JSON object", settingsPath)
		}
		hooksMap = m
	} else {
		hooksMap = make(map[string]any)
	}

	var postList []any
	if rawPost, exists := hooksMap["PostToolUse"]; exists && rawPost != nil {
		list, ok := rawPost.([]any)
		if !ok {
			return false, fmt.Errorf("%s: 'hooks.PostToolUse' field is not a JSON array", settingsPath)
		}
		postList = list
	}

	if hasRelayHookInPostToolUse(postList) {
		return false, nil
	}

	entry := map[string]any{
		"matcher": matcher,
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": command,
			},
		},
	}
	postList = append(postList, entry)
	hooksMap["PostToolUse"] = postList
	root["hooks"] = hooksMap

	if err := writeSettingsJSONAtomic(settingsPath, root, fileMode); err != nil {
		return false, err
	}
	return true, nil
}

// UninstallHook removes any relay steer PostToolUse hook entries from settingsPath
// while preserving all other user hooks and settings. Returns removed=false if none were found.
func UninstallHook(settingsPath string) (bool, error) {
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		return false, nil
	}

	root, fileMode, err := readSettingsJSON(settingsPath)
	if err != nil {
		return false, err
	}

	hooksMap, ok := root["hooks"].(map[string]any)
	if !ok {
		return false, nil
	}
	postList, ok := hooksMap["PostToolUse"].([]any)
	if !ok {
		return false, nil
	}

	removed := false
	filteredEntries := make([]any, 0, len(postList))
	for _, item := range postList {
		entryMap, ok := item.(map[string]any)
		if !ok {
			filteredEntries = append(filteredEntries, item)
			continue
		}
		hookList, ok := entryMap["hooks"].([]any)
		if !ok {
			filteredEntries = append(filteredEntries, item)
			continue
		}
		keptHooks := make([]any, 0, len(hookList))
		for _, h := range hookList {
			hMap, ok := h.(map[string]any)
			if ok {
				if cmd, ok := hMap["command"].(string); ok && isRelaySteerCommand(cmd) {
					removed = true
					continue
				}
			}
			keptHooks = append(keptHooks, h)
		}
		if len(keptHooks) > 0 {
			entryMap["hooks"] = keptHooks
			filteredEntries = append(filteredEntries, entryMap)
		}
	}

	if !removed {
		return false, nil
	}

	if len(filteredEntries) == 0 {
		delete(hooksMap, "PostToolUse")
	} else {
		hooksMap["PostToolUse"] = filteredEntries
	}
	if len(hooksMap) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = hooksMap
	}

	if err := writeSettingsJSONAtomic(settingsPath, root, fileMode); err != nil {
		return false, err
	}
	return true, nil
}

// MissingHookWarning inspects local settings and extension files for a normalized steer target and
// returns an actionable warning message if no matching steering hook/extension is installed.
func MissingHookWarning(normalizedTarget, homeDir string) string {
	if strings.HasPrefix(normalizedTarget, WorkspacePrefix) {
		wsDir := strings.TrimPrefix(normalizedTarget, WorkspacePrefix)
		claudePaths := CheckAgentHookInstalled(supportedHookSpecs[adapter.AgentClaude], homeDir, wsDir)
		qoderPaths := CheckAgentHookInstalled(supportedHookSpecs[adapter.AgentQoder], homeDir, wsDir)
		piPaths := CheckAgentHookInstalled(supportedHookSpecs[adapter.AgentPi], homeDir, wsDir)
		if len(claudePaths) == 0 && len(qoderPaths) == 0 && len(piPaths) == 0 {
			return "Warning: no steering hook/extension detected for claude, qoder, or pi (user or project scope). Run 'relay steer hook-install --agent all' so worker hooks can consume queued steers (takes effect on the next session)."
		}
		return ""
	}

	parts := strings.SplitN(normalizedTarget, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	agType := adapter.AgentType(parts[0])
	spec, ok := supportedHookSpecs[agType]
	if !ok {
		return ""
	}
	if len(CheckAgentHookInstalled(spec, homeDir, "")) == 0 {
		if spec.Kind == HookKindPiExtension {
			return fmt.Sprintf("Warning: Pi steering extension is not installed for %s. Run 'relay steer hook-install --agent %s' (takes effect on the next session).", agType, agType)
		}
		return fmt.Sprintf("Warning: PostToolUse hook is not installed for %s. Run 'relay steer hook-install --agent %s' (takes effect on the next session).", agType, agType)
	}
	return ""
}

func readSettingsJSON(path string) (map[string]any, os.FileMode, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return make(map[string]any), 0o600, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("failed to stat %s: %w", path, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read %s: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return make(map[string]any), info.Mode().Perm(), nil
	}

	// Use json.Decoder with UseNumber so integers larger than 2^53 preserve exact precision.
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, 0, fmt.Errorf("failed to parse %s as JSON (refusing to overwrite): %w", path, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, 0, fmt.Errorf("failed to parse %s as JSON (trailing tokens; refusing to overwrite)", path)
	}
	if root == nil {
		root = make(map[string]any)
	}
	return root, info.Mode().Perm(), nil
}

func writeSettingsJSONAtomic(path string, root map[string]any, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings JSON: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, mode)
}

// resolveWriteTarget resolves an existing symlink to its underlying target path so atomic
// rename updates the backing file rather than replacing a dotfile manager's symlink.
func resolveWriteTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", fmt.Errorf("failed to lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	if resolved, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
		return resolved, nil
	}
	// Handle dangling symlinks by resolving the link target relative to the symlink's directory.
	linkTarget, readErr := os.Readlink(path)
	if readErr != nil {
		return "", fmt.Errorf("failed to read symlink %s: %w", path, readErr)
	}
	if !filepath.IsAbs(linkTarget) {
		linkTarget = filepath.Join(filepath.Dir(path), linkTarget)
	}
	return filepath.Clean(linkTarget), nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	targetPath, err := resolveWriteTarget(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	tmpFile := fmt.Sprintf("%s.%d.%d.tmp", targetPath, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, data, mode); err != nil {
		return fmt.Errorf("failed to write temporary file: %w", err)
	}
	if err := os.Rename(tmpFile, targetPath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to atomically replace %s: %w", targetPath, err)
	}
	return nil
}
