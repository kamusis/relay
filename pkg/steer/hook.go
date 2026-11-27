package steer

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kamusis/relay/pkg/adapter"
)

// HookInput models the JSON payload passed on stdin to PostToolUse hooks
// by hook-capable agents such as Claude Code and Qoder CLI.
type HookInput struct {
	SessionID     string `json:"session_id"`
	Cwd           string `json:"cwd"`
	ToolName      string `json:"tool_name"`
	HookEventName string `json:"hook_event_name"`
}

// HookOutput models the structured JSON response understood by Claude Code and Qoder CLI
// PostToolUse hooks to inject mid-turn steering context into the active agent loop.
type HookOutput struct {
	Decision           string              `json:"decision,omitempty"`
	Reason             string              `json:"reason,omitempty"`
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// HookSpecificOutput carries the PostToolUse additionalContext payload.
type HookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// ParseHookInput decodes a hook JSON payload from r. Empty input returns a zero HookInput without error.
func ParseHookInput(r io.Reader) (HookInput, error) {
	if r == nil {
		return HookInput{}, nil
	}
	raw, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return HookInput{}, fmt.Errorf("failed to read hook stdin: %w", err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return HookInput{}, nil
	}
	var in HookInput
	if err := json.Unmarshal([]byte(trimmed), &in); err != nil {
		return HookInput{}, fmt.Errorf("failed to parse hook stdin JSON: %w", err)
	}
	return in, nil
}

// ResolveDrainTargets collects all candidate steering targets from explicit CLI flags,
// hook stdin metadata, and standard agent environment variables.
func ResolveDrainTargets(explicitTarget, explicitWorkspace string, hookIn HookInput, getenv func(string) string) []string {
	var raw []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" {
			raw = append(raw, v)
		}
	}

	add(explicitTarget)
	if ws := strings.TrimSpace(explicitWorkspace); ws != "" {
		add(workspacePrefix + ws)
	}

	if getenv != nil {
		add(getenv("RELAY_STEER_TARGET"))
		if id := strings.TrimSpace(getenv("CLAUDE_SESSION_ID")); id != "" {
			add("claude:" + id)
		}
		if id := strings.TrimSpace(getenv("QODER_SESSION_ID")); id != "" {
			add("qoder:" + id)
		}
		if id := strings.TrimSpace(getenv("PI_SESSION_ID")); id != "" {
			add("pi:" + id)
		}
		if id := strings.TrimSpace(getenv("CODEX_SESSION_ID")); id != "" {
			add("codex:" + id)
		}
	}

	// Hook stdin session_id may omit an agent prefix; match against hook-capable agent types
	// when no explicit agent prefix is present.
	if sid := strings.TrimSpace(hookIn.SessionID); sid != "" {
		if strings.Contains(sid, ":") {
			add(sid)
		} else {
			for _, ag := range []adapter.AgentType{adapter.AgentClaude, adapter.AgentQoder, adapter.AgentPi, adapter.AgentCodex} {
				add(fmt.Sprintf("%s:%s", ag, sid))
			}
		}
	}

	if cwd := strings.TrimSpace(hookIn.Cwd); cwd != "" {
		add(workspacePrefix + cwd)
	} else if explicitWorkspace == "" && getenv != nil {
		if pwd := strings.TrimSpace(getenv("PWD")); pwd != "" {
			add(workspacePrefix + pwd)
		}
	}

	// Deduplicate normalized targets while preserving order.
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		norm, err := NormalizeTarget(r)
		if err != nil {
			continue
		}
		if _, exists := seen[norm]; exists {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out
}

// FormatText renders drained steering messages as readable Markdown for agent consumption.
func FormatText(msgs []Message) string {
	if len(msgs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[Relay Steer — Mid-Turn Correction]\n")
	for i, m := range msgs {
		if i > 0 {
			sb.WriteString("\n---\n")
		}
		header := fmt.Sprintf("- #%d (%s)", i+1, adapter.ShortID(m.ID))
		if m.Severity == SeverityBlocking {
			header += " [BLOCKING]"
		}
		if m.From != "" {
			header += fmt.Sprintf(" from %s", m.From)
		}
		if m.CreatedAt > 0 {
			header += fmt.Sprintf(" at %s", time.Unix(m.CreatedAt, 0).Format("15:04:05"))
		}
		sb.WriteString(header + "\n")
		sb.WriteString(strings.TrimSpace(m.Body) + "\n")
	}
	return strings.TrimSpace(sb.String())
}

// FormatHookJSON renders drained steering messages as a PostToolUse hook JSON response.
// Returns (nil, nil) when msgs is empty so the hook stays silent with exit code 0.
func FormatHookJSON(msgs []Message) ([]byte, error) {
	if len(msgs) == 0 {
		return nil, nil
	}

	text := FormatText(msgs)
	out := HookOutput{
		HookSpecificOutput: &HookSpecificOutput{
			HookEventName:     "PostToolUse",
			AdditionalContext: text,
		},
	}

	var blockingReasons []string
	for _, m := range msgs {
		if m.Severity == SeverityBlocking {
			blockingReasons = append(blockingReasons, strings.TrimSpace(m.Body))
		}
	}
	if len(blockingReasons) > 0 {
		out.Decision = "block"
		out.Reason = "[Relay Steer BLOCKING] " + strings.Join(blockingReasons, " | ")
	}

	return json.Marshal(out)
}

// HookSnippet returns copy-pasteable PostToolUse hook configuration for a hook-capable agent.
func HookSnippet(agentName string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(agentName))
	if norm == "" {
		norm = "claude"
	}

	switch norm {
	case "claude", "claudecode":
		return strings.TrimSpace(`
// Add to .claude/settings.json or ~/.claude/settings.json:
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write|MultiEdit|Bash",
        "hooks": [
          {
            "type": "command",
            "command": "relay steer drain --format hook-json --read-hook-stdin"
          }
        ]
      }
    ]
  }
}`), nil
	case "qoder", "qodercli":
		return strings.TrimSpace(`
// Add to .qoder/settings.json or ~/.qoder/settings.json:
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write|Bash",
        "hooks": [
          {
            "type": "command",
            "command": "relay steer drain --format hook-json --read-hook-stdin"
          }
        ]
      }
    ]
  }
}`), nil
	case "pi", "pi-agent":
		return strings.TrimSpace(`
# Invoke from a Pi tool/turn hook or extension after file edits / bash commands:
relay steer drain --format text --workspace "$PWD"
# Or with JSON hook payload on stdin:
relay steer drain --format hook-json --read-hook-stdin`), nil
	default:
		return "", fmt.Errorf("unsupported hook agent '%s' (supported: claude, qoder, pi)", agentName)
	}
}
