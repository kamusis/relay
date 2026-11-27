package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kamusis/relay/pkg/peek"
	"github.com/kamusis/relay/pkg/registry"
	"github.com/kamusis/relay/pkg/steer"
)

func TestPeekCommandFlagsAndJSON(t *testing.T) {
	peekCmd := newPeekCmd()
	for _, flagName := range []string{"diff", "file", "base", "commits", "max-diff", "endpoint", "json"} {
		if peekCmd.Flags().Lookup(flagName) == nil {
			t.Fatalf("expected --%s flag on peek command", flagName)
		}
	}

	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	runGit("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "f.txt")
	runGit("commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	steerStore := steer.NewStore(filepath.Join(t.TempDir(), "steer.jsonl"))
	if _, err := steerStore.Enqueue(steer.Message{Target: repo, Body: "check edge case"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := executePeek(context.Background(), &out, registry.New(), nil, steerStore, peek.Options{
		Target:      repo,
		IncludeDiff: true,
	}, true); err != nil {
		t.Fatalf("executePeek json: %v", err)
	}

	var report peek.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("invalid peek JSON output: %v\n%s", err, out.String())
	}
	if report.Git == nil || !strings.Contains(report.Git.Diff, "+v2") {
		t.Fatalf("expected diff with +v2 in JSON report, got %+v", report.Git)
	}
	if len(report.PendingSteers) != 1 {
		t.Fatalf("expected 1 pending steer in report, got %d", len(report.PendingSteers))
	}
}

func TestSteerEndToEndEnqueueListDrainHookJSON(t *testing.T) {
	tmp := t.TempDir()
	store := steer.NewStore(filepath.Join(tmp, "steer.jsonl"))
	worktree := filepath.Join(tmp, "wt-demo")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. Enqueue blocking directive targeting worktree
	var enqOut bytes.Buffer
	if err := executeSteerEnqueue(&enqOut, store, worktree, "Use /usr/sbin/ioreg instead of bare ioreg", "agy:17f5151f", true); err != nil {
		t.Fatalf("executeSteerEnqueue: %v", err)
	}
	if !strings.Contains(enqOut.String(), "Severity: blocking") {
		t.Fatalf("unexpected enqueue output: %s", enqOut.String())
	}

	// 2. List as JSON
	var listOut bytes.Buffer
	if err := executeSteerList(&listOut, store, worktree, false, true); err != nil {
		t.Fatalf("executeSteerList: %v", err)
	}
	var listed []steer.Message
	if err := json.Unmarshal(listOut.Bytes(), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("unexpected steer list JSON: %v / %s", err, listOut.String())
	}

	// 3. Simulate PostToolUse hook draining via stdin JSON
	hookPayload := `{"session_id":"sess-99","cwd":"` + worktree + `","tool_name":"Edit","hook_event_name":"PostToolUse"}`
	var drainOut bytes.Buffer
	if err := executeSteerDrain(
		strings.NewReader(hookPayload),
		&drainOut,
		store,
		"", "", "hook-json", true,
		func(string) string { return "" },
	); err != nil {
		t.Fatalf("executeSteerDrain: %v", err)
	}

	var hookResp steer.HookOutput
	if err := json.Unmarshal(drainOut.Bytes(), &hookResp); err != nil {
		t.Fatalf("invalid hook JSON output: %v\n%s", err, drainOut.String())
	}
	if hookResp.Decision != "block" || hookResp.HookSpecificOutput == nil ||
		!strings.Contains(hookResp.HookSpecificOutput.AdditionalContext, "/usr/sbin/ioreg") {
		t.Fatalf("unexpected hook JSON response: %+v", hookResp)
	}

	// 4. Subsequent hook drain must produce empty output (exit 0 silence)
	var secondDrain bytes.Buffer
	if err := executeSteerDrain(
		strings.NewReader(hookPayload),
		&secondDrain,
		store,
		"", "", "hook-json", true,
		func(string) string { return "" },
	); err != nil {
		t.Fatalf("second executeSteerDrain: %v", err)
	}
	if secondDrain.Len() != 0 {
		t.Fatalf("expected silent empty output on second drain, got %q", secondDrain.String())
	}
}
