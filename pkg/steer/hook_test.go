package steer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseHookInputAndResolveDrainTargets(t *testing.T) {
	in, err := ParseHookInput(strings.NewReader(`{"session_id":"abc-123","cwd":"/tmp/demo","tool_name":"Edit","hook_event_name":"PostToolUse"}`))
	if err != nil {
		t.Fatalf("ParseHookInput: %v", err)
	}
	if in.SessionID != "abc-123" || in.Cwd != "/tmp/demo" {
		t.Fatalf("unexpected HookInput: %+v", in)
	}

	// Empty stdin should not error
	empty, err := ParseHookInput(strings.NewReader("   "))
	if err != nil || empty.SessionID != "" {
		t.Fatalf("empty ParseHookInput = %+v, %v", empty, err)
	}

	env := map[string]string{
		"CLAUDE_SESSION_ID": "claude-99",
		"PWD":               "/tmp/fallback",
	}
	targets := ResolveDrainTargets("", "", in, func(k string) string { return env[k] })

	has := func(want string) bool {
		for _, v := range targets {
			if v == want {
				return true
			}
		}
		return false
	}
	if !has("claude:claude-99") {
		t.Fatalf("expected claude:claude-99 in targets, got %v", targets)
	}
	if !has("claude:abc-123") || !has("qoder:abc-123") {
		t.Fatalf("expected prefixed hook session_id targets, got %v", targets)
	}
	foundCwd := false
	for _, v := range targets {
		if strings.HasPrefix(v, "ws:") && strings.HasSuffix(v, "/tmp/demo") {
			foundCwd = true
		}
	}
	if !foundCwd {
		t.Fatalf("expected hook cwd workspace target, got %v", targets)
	}
}

func TestFormatHookJSON(t *testing.T) {
	// Empty returns nil (silent exit 0)
	if out, err := FormatHookJSON(nil); err != nil || out != nil {
		t.Fatalf("FormatHookJSON(nil) = %q, %v; want nil, nil", string(out), err)
	}

	// Advisory only -> additionalContext without decision=block
	advBytes, err := FormatHookJSON([]Message{
		{ID: "11111111-aaaa", Severity: SeverityAdvisory, From: "agy", Body: "Prefer absolute path"},
	})
	if err != nil {
		t.Fatalf("FormatHookJSON advisory: %v", err)
	}
	var adv HookOutput
	if err := json.Unmarshal(advBytes, &adv); err != nil {
		t.Fatalf("unmarshal advisory: %v", err)
	}
	if adv.Decision != "" {
		t.Fatalf("expected empty decision for advisory, got %q", adv.Decision)
	}
	if adv.HookSpecificOutput == nil || adv.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Fatalf("missing PostToolUse hookSpecificOutput: %+v", adv)
	}
	if !strings.Contains(adv.HookSpecificOutput.AdditionalContext, "Prefer absolute path") {
		t.Fatalf("missing body in additionalContext: %q", adv.HookSpecificOutput.AdditionalContext)
	}

	// Blocking -> sets decision="block" and reason
	blkBytes, err := FormatHookJSON([]Message{
		{ID: "22222222-bbbb", Severity: SeverityBlocking, Body: "Missing workspace_id filter"},
	})
	if err != nil {
		t.Fatalf("FormatHookJSON blocking: %v", err)
	}
	var blk HookOutput
	if err := json.Unmarshal(blkBytes, &blk); err != nil {
		t.Fatalf("unmarshal blocking: %v", err)
	}
	if blk.Decision != "block" || !strings.Contains(blk.Reason, "Missing workspace_id filter") {
		t.Fatalf("expected blocking decision + reason, got %+v", blk)
	}
}

func TestHookSnippet(t *testing.T) {
	for _, ag := range []string{"claude", "qoder", "pi"} {
		s, err := HookSnippet(ag)
		if err != nil || !strings.Contains(s, "relay steer drain") {
			t.Fatalf("HookSnippet(%q) = %q, %v", ag, s, err)
		}
	}
	if _, err := HookSnippet("unknown-agent"); err == nil {
		t.Fatal("expected error for unsupported hook agent")
	}
}
