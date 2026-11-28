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
	"unicode/utf8"

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

	// 1. Enqueue blocking directive targeting worktree (emits missing-hook warning on stderr)
	var enqOut, enqErr bytes.Buffer
	if err := executeSteerEnqueue(&enqOut, &enqErr, store, tmp, worktree, "Use /usr/sbin/ioreg instead of bare ioreg", "agy:17f5151f", true); err != nil {
		t.Fatalf("executeSteerEnqueue: %v", err)
	}
	if !strings.Contains(enqOut.String(), "Severity: blocking") {
		t.Fatalf("unexpected enqueue output: %s", enqOut.String())
	}
	if !strings.Contains(enqErr.String(), "relay steer hook-install") {
		t.Fatalf("expected missing-hook warning on stderr when no hook installed, got: %q", enqErr.String())
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

func TestSteerDrainInvalidFormatDoesNotConsumeQueue(t *testing.T) {
	tmp := t.TempDir()
	store := steer.NewStore(filepath.Join(tmp, "steer.jsonl"))
	worktree := filepath.Join(tmp, "wt-safe")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. Invalid format must fail even on an empty queue.
	var emptyOut bytes.Buffer
	if err := executeSteerDrain(nil, &emptyOut, store, worktree, "", "bogus", false, func(string) string { return "" }); err == nil {
		t.Fatal("expected error for invalid --format on empty queue")
	}

	// 2. Enqueue directive, then drain with invalid format -> must error AND leave directive pending.
	if _, err := store.Enqueue(steer.Message{Target: worktree, Body: "must not be lost"}); err != nil {
		t.Fatal(err)
	}
	var badOut bytes.Buffer
	if err := executeSteerDrain(nil, &badOut, store, worktree, "", "bogus", false, func(string) string { return "" }); err == nil {
		t.Fatal("expected error for invalid --format on non-empty queue")
	}

	pending, err := store.List([]string{worktree}, false)
	if err != nil || len(pending) != 1 || pending[0].Body != "must not be lost" {
		t.Fatalf("directive was lost after invalid --format drain: %+v, err=%v", pending, err)
	}
}

func TestSteerListCJKTargetUTF8SafeAndResolveParity(t *testing.T) {
	tmp := t.TempDir()
	store := steer.NewStore(filepath.Join(tmp, "steer.jsonl"))
	cjkDir := filepath.Join(tmp, "评审-数据库迁移验证-很长的中文工作区目录名称测试")
	if err := os.MkdirAll(cjkDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Enqueue(steer.Message{Target: cjkDir, Body: "修复路径编码"}); err != nil {
		t.Fatal(err)
	}

	var listOut bytes.Buffer
	if err := executeSteerList(&listOut, store, "", false, false); err != nil {
		t.Fatalf("executeSteerList table: %v", err)
	}
	if !utf8.ValidString(listOut.String()) {
		t.Fatalf("steer list table output contains invalid UTF-8: %q", listOut.String())
	}

	// Parity between `relay steer -w dir "msg"` and `relay steer add -w dir "msg"`.
	tgt, body, err := resolveSteerTargetAndBody(cjkDir, "", []string{"use absolute path"})
	if err != nil || tgt != steer.WorkspaceTarget(cjkDir) || body != "use absolute path" {
		t.Fatalf("resolveSteerTargetAndBody(-w + pos msg) = (%q, %q, %v)", tgt, body, err)
	}
}

func TestSteerHookInstallLifecycleAndWarningSuppression(t *testing.T) {
	home := t.TempDir()
	store := steer.NewStore(filepath.Join(home, "steer.jsonl"))

	// 1. Status before install shows not installed
	var statusBefore bytes.Buffer
	if err := executeSteerHookInstall(&statusBefore, "all", "user", home, "", false, true); err != nil {
		t.Fatalf("status before: %v", err)
	}
	if !strings.Contains(statusBefore.String(), "not installed") {
		t.Fatalf("expected not installed status, got:\n%s", statusBefore.String())
	}

	// 2. Install for all (claude, qoder, pi)
	var installOut bytes.Buffer
	if err := executeSteerHookInstall(&installOut, "all", "user", home, "", false, false); err != nil {
		t.Fatalf("install all: %v", err)
	}
	for _, want := range []string{
		"Installed relay steer PostToolUse hook for claude",
		"Installed relay steer PostToolUse hook for qoder",
		"Installed relay steer steering extension for pi",
	} {
		if !strings.Contains(installOut.String(), want) {
			t.Fatalf("missing %q in install output:\n%s", want, installOut.String())
		}
	}

	// 3. Enqueue for qoder and pi now produce NO missing-hook warnings on stderr
	for _, target := range []string{"qoder:sess-1", "pi:sess-2"} {
		var enqOut, enqErr bytes.Buffer
		if err := executeSteerEnqueue(&enqOut, &enqErr, store, home, target, "check lock", "agy", false); err != nil {
			t.Fatalf("executeSteerEnqueue(%s): %v", target, err)
		}
		if enqErr.Len() != 0 {
			t.Fatalf("expected no warning for %s after hook install, got: %q", target, enqErr.String())
		}
	}

	// 4. Reject conflicting --uninstall + --status and --dir with --scope user
	if err := executeSteerHookInstall(&installOut, "qoder", "user", home, "", true, true); err == nil {
		t.Fatal("expected error when both --uninstall and --status are set")
	}
	if err := executeSteerHookInstall(&installOut, "qoder", "user", home, "/tmp/some-repo", false, false); err == nil {
		t.Fatal("expected error when --dir is passed with --scope user")
	}

	// 5. Reject unexpected positional arguments on hook-install and hook-snippet
	steerCmd := newSteerCmd()
	for _, sub := range []string{"hook-install", "hook-snippet"} {
		cmd, _, err := steerCmd.Find([]string{sub})
		if err != nil {
			t.Fatalf("find %s: %v", sub, err)
		}
		if err := cmd.Args(cmd, []string{"unexpected-arg"}); err == nil {
			t.Fatalf("expected %s to reject positional arguments", sub)
		}
	}

	// 6. Uninstall removes hooks and Pi extension cleanly
	var uninstallOut bytes.Buffer
	if err := executeSteerHookInstall(&uninstallOut, "all", "user", home, "", true, false); err != nil {
		t.Fatalf("uninstall all: %v", err)
	}
	for _, want := range []string{
		"Removed relay steer PostToolUse hook for claude",
		"Removed relay steer PostToolUse hook for qoder",
		"Removed relay steer steering extension for pi",
	} {
		if !strings.Contains(uninstallOut.String(), want) {
			t.Fatalf("missing %q in uninstall output:\n%s", want, uninstallOut.String())
		}
	}
}
