package qoder

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
)

func TestQoderAdapterAskNewResumeErrorAndFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake CLI test is Unix-only")
	}

	tmp := t.TempDir()
	ws := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	// Seed an existing Qoder session JSONL under a fake ~/.qoder directory.
	qoderDir := filepath.Join(tmp, ".qoder")
	existingID := "2a037e6d-9a69-4484-afd6-57a579b8e67b"
	slugDir := filepath.Join(qoderDir, "projects", PathToSlug(ws))
	if err := os.MkdirAll(slugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existingJSONL := strings.Join([]string{
		`{"type":"workspace-directories","directories":["` + ws + `"]}`,
		`{"type":"user","uuid":"u1","message":{"role":"user","content":"Initial review"}}`,
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Fallback transcript verdict"}]}}`,
		`{"type":"active-leaf","leafUuid":"a1"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(slugDir, existingID+".jsonl"), []byte(existingJSONL), 0o600); err != nil {
		t.Fatal(err)
	}

	argsFile := filepath.Join(tmp, "args.txt")
	modeFile := filepath.Join(tmp, "mode.txt")
	fakeCLI := filepath.Join(tmp, "qodercli")
	script := `#!/bin/sh
printf '%s\n' "$@" > "` + argsFile + `"
MODE=$(cat "` + modeFile + `" 2>/dev/null || echo ok)
case "$MODE" in
  ok)
    printf '{"type":"result","is_error":false,"result":"Qoder review OK","session_id":"9e0bb726-8f81-47af-b2f5-de32477bb258"}\n'
    ;;
  error)
    printf '{"type":"result","is_error":true,"result":"model quota exceeded","session_id":"9e0bb726-8f81-47af-b2f5-de32477bb258"}\n'
    ;;
  empty)
    printf '{"type":"result","is_error":false,"result":"","session_id":"` + existingID + `"}\n'
    ;;
esac
`
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("QODER_CLI_PATH", fakeCLI)
	ad := New(Config{
		BaseDir: qoderDir,
		DBPath:  ":none:",
	})

	// 1. Fresh Ask (new session)
	if err := os.WriteFile(modeFile, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := ad.Ask(context.Background(), adapter.AskRequest{
		Workspace: ws,
		Prompt:    "Review diff",
	})
	if err != nil {
		t.Fatalf("Ask(new) error = %v", err)
	}
	if resp.SessionID != "9e0bb726-8f81-47af-b2f5-de32477bb258" || resp.Output != "Qoder review OK" {
		t.Fatalf("Ask(new) = %+v", resp)
	}
	argv1, _ := os.ReadFile(argsFile)
	argv1Str := string(argv1)
	if strings.Contains(argv1Str, "\n-r\n") {
		t.Fatalf("fresh Ask should not pass -r:\n%s", argv1Str)
	}
	for _, want := range []string{"-w\n" + ws, "--dangerously-skip-permissions", "-f\njson", "-p\nReview diff"} {
		if !strings.Contains(argv1Str, want) {
			t.Fatalf("fresh Ask argv missing %q:\n%s", want, argv1Str)
		}
	}

	// 2. Resume Ask by short prefix (omitting Workspace falls back to session's workspace)
	resp2, err := ad.Ask(context.Background(), adapter.AskRequest{
		SessionID: "2a037e6d",
		Prompt:    "Followup check",
	})
	if err != nil {
		t.Fatalf("Ask(resume) error = %v", err)
	}
	if resp2.Output != "Qoder review OK" {
		t.Fatalf("Ask(resume) = %+v", resp2)
	}
	argv2, _ := os.ReadFile(argsFile)
	argv2Str := string(argv2)
	for _, want := range []string{"-w\n" + ws, "-r\n" + existingID, "-f\njson", "-p\nFollowup check"} {
		if !strings.Contains(argv2Str, want) {
			t.Fatalf("resume Ask argv missing %q:\n%s", want, argv2Str)
		}
	}

	// 3. JSON is_error=true returns error
	if err := os.WriteFile(modeFile, []byte("error"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.Ask(context.Background(), adapter.AskRequest{Workspace: ws, Prompt: "boom"}); err == nil || !strings.Contains(err.Error(), "model quota exceeded") {
		t.Fatalf("expected error from is_error=true JSON, got %v", err)
	}

	// 4. Empty result in JSON falls back to GetLatestAssistantOutput
	if err := os.WriteFile(modeFile, []byte("empty"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp4, err := ad.Ask(context.Background(), adapter.AskRequest{SessionID: "2a037e6d", Prompt: "empty stdout"})
	if err != nil {
		t.Fatalf("Ask(empty fallback) error = %v", err)
	}
	if resp4.Output != "Fallback transcript verdict" {
		t.Fatalf("expected transcript fallback output, got %+v", resp4)
	}
}
