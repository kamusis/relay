package steer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kamusis/relay/pkg/adapter"
)

// HookDrainCommand is the canonical command installed into PostToolUse hooks.
const HookDrainCommand = "relay steer drain --format hook-json --read-hook-stdin"

// HookAgentSpec defines settings file location and default tool matcher for a hook-capable JSON-config agent.
type HookAgentSpec struct {
	Agent       adapter.AgentType
	DirName     string
	SettingsRel string
	Matcher     string
}

var supportedHookSpecs = map[adapter.AgentType]HookAgentSpec{
	adapter.AgentClaude: {
		Agent:       adapter.AgentClaude,
		DirName:     ".claude",
		SettingsRel: filepath.Join(".claude", "settings.json"),
		Matcher:     "Edit|Write|MultiEdit|Bash",
	},
	adapter.AgentQoder: {
		Agent:       adapter.AgentQoder,
		DirName:     ".qoder",
		SettingsRel: filepath.Join(".qoder", "settings.json"),
		Matcher:     "Edit|Write|Bash",
	},
}

// ResolveHookAgents expands an agent selector ("claude", "qoder", or "all") into ordered HookAgentSpecs.
func ResolveHookAgents(selector string) ([]HookAgentSpec, error) {
	norm := strings.ToLower(strings.TrimSpace(selector))
	switch norm {
	case "", "all":
		return []HookAgentSpec{
			supportedHookSpecs[adapter.AgentClaude],
			supportedHookSpecs[adapter.AgentQoder],
		}, nil
	case "claude", "claudecode":
		return []HookAgentSpec{supportedHookSpecs[adapter.AgentClaude]}, nil
	case "qoder", "qodercli":
		return []HookAgentSpec{supportedHookSpecs[adapter.AgentQoder]}, nil
	case "pi", "pi-agent":
		return nil, fmt.Errorf("automatic hook-install is not supported for 'pi' (Pi uses custom extensions/hooks); run 'relay steer hook-snippet --agent pi' for manual setup")
	default:
		return nil, fmt.Errorf("unsupported hook-install agent '%s' (supported: claude, qoder, all)", selector)
	}
}

// HookSettingsPath returns the settings.json path for spec under "user" or "project" scope.
func HookSettingsPath(spec HookAgentSpec, scope, homeDir, projectDir string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", "user":
		if strings.TrimSpace(homeDir) == "" {
			h, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("unable to determine user home directory: %w", err)
			}
			homeDir = h
		}
		return filepath.Join(homeDir, spec.SettingsRel), nil
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
		return filepath.Join(absDir, spec.SettingsRel), nil
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

// CheckAgentHookInstalled checks user and optional project settings.json files for spec
// and returns all paths where the relay steer PostToolUse hook is present.
func CheckAgentHookInstalled(spec HookAgentSpec, homeDir, projectDir string) []string {
	var found []string
	if userPath, err := HookSettingsPath(spec, "user", homeDir, ""); err == nil && IsHookInstalled(userPath) {
		found = append(found, userPath)
	}
	if strings.TrimSpace(projectDir) != "" {
		if projPath, err := HookSettingsPath(spec, "project", "", projectDir); err == nil && IsHookInstalled(projPath) {
			if len(found) == 0 || filepath.Clean(projPath) != filepath.Clean(found[0]) {
				found = append(found, projPath)
			}
		}
	}
	return found
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

// MissingHookWarning inspects local settings.json files for a normalized steer target and
// returns an actionable warning message if no matching PostToolUse hook is installed.
func MissingHookWarning(normalizedTarget, homeDir string) string {
	if strings.HasPrefix(normalizedTarget, WorkspacePrefix) {
		wsDir := strings.TrimPrefix(normalizedTarget, WorkspacePrefix)
		claudePaths := CheckAgentHookInstalled(supportedHookSpecs[adapter.AgentClaude], homeDir, wsDir)
		qoderPaths := CheckAgentHookInstalled(supportedHookSpecs[adapter.AgentQoder], homeDir, wsDir)
		if len(claudePaths) == 0 && len(qoderPaths) == 0 {
			return "Warning: no PostToolUse hook detected for claude or qoder (user or project scope). Run 'relay steer hook-install --agent all' so worker hooks can consume queued steers (takes effect on the next session)."
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

	var root map[string]any
	if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
		return nil, 0, fmt.Errorf("failed to parse %s as JSON (refusing to overwrite): %w", path, err)
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings JSON: %w", err)
	}
	data = append(data, '\n')

	tmpFile := fmt.Sprintf("%s.%d.%d.tmp", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, data, mode); err != nil {
		return fmt.Errorf("failed to write temporary settings file: %w", err)
	}
	if err := os.Rename(tmpFile, path); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to atomically replace %s: %w", path, err)
	}
	return nil
}
