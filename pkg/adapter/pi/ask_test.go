package pi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
)

func TestPiAdapterAskNewResumeAndEmptyFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake CLI test is Unix-only")
	}

	tmp := t.TempDir()
	ws := filepath.Join(tmp, "pi-repo")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	agentDir := filepath.Join(tmp, ".pi", "agent")
	sessDir := filepath.Join(agentDir, "sessions", "proj")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existingID := "33333333-4444-5555-6666-777777777777"
	existingPath := filepath.Join(sessDir, "2026-11-28_"+existingID+".jsonl")
	existingJSONL := strings.Join([]string{
		`{"type":"session","version":3,"id":"` + existingID + `","cwd":"` + ws + `"}`,
		`{"type":"message","id":"m1","parentId":null,"message":{"role":"user","content":"Check diff"}}`,
		`{"type":"message","id":"m2","parentId":"m1","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"Pi fallback transcript verdict"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(existingPath, []byte(existingJSONL), 0o600); err != nil {
		t.Fatal(err)
	}

	argsFile := filepath.Join(tmp, "args.txt")
	modeFile := filepath.Join(tmp, "mode.txt")
	fakeCLI := filepath.Join(tmp, "pi")
	script := `#!/bin/sh
printf '%s\n' "$@" > "` + argsFile + `"
MODE=$(cat "` + modeFile + `" 2>/dev/null || echo ok)
if [ "$MODE" = "ok" ]; then
  printf 'Pi direct reply\n'
fi
`
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ad := New(Config{
		AgentDir: agentDir,
		CLIPath:  fakeCLI,
	})

	// 1. Fresh Ask generates UUID and passes --session-id <uuid> -- <prompt>
	if err := os.WriteFile(modeFile, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := ad.Ask(context.Background(), adapter.AskRequest{
		Workspace: ws,
		Prompt:    "--- leading dash prompt",
	})
	if err != nil {
		t.Fatalf("Ask(new) error = %v", err)
	}
	if resp.SessionID == "" || resp.Output != "Pi direct reply" {
		t.Fatalf("Ask(new) = %+v", resp)
	}
	argv1, _ := os.ReadFile(argsFile)
	wantNew := "-p\n--session-id\n" + resp.SessionID + "\n--\n--- leading dash prompt\n"
	if string(argv1) != wantNew {
		t.Fatalf("fresh pi Ask argv =\n%q\nwant:\n%q", string(argv1), wantNew)
	}

	// 2. Resume Ask by prefix passes --session <path> -- <prompt>
	resp2, err := ad.Ask(context.Background(), adapter.AskRequest{
		SessionID: "33333333",
		Prompt:    "Followup",
	})
	if err != nil {
		t.Fatalf("Ask(resume) error = %v", err)
	}
	if resp2.SessionID != existingID || resp2.Output != "Pi direct reply" {
		t.Fatalf("Ask(resume) = %+v", resp2)
	}
	argv2, _ := os.ReadFile(argsFile)
	wantResume := "-p\n--session\n" + existingPath + "\n--\nFollowup\n"
	if string(argv2) != wantResume {
		t.Fatalf("resume pi Ask argv =\n%q\nwant:\n%q", string(argv2), wantResume)
	}

	// 3. Empty stdout falls back to GetLatestAssistantOutput
	if err := os.WriteFile(modeFile, []byte("empty"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp3, err := ad.Ask(context.Background(), adapter.AskRequest{
		SessionID: "33333333",
		Prompt:    "Silent stdout",
	})
	if err != nil {
		t.Fatalf("Ask(empty fallback) error = %v", err)
	}
	if resp3.Output != "Pi fallback transcript verdict" {
		t.Fatalf("expected Pi transcript fallback, got %+v", resp3)
	}
}
