package codex

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
)

func TestCodexAdapterSessionsAndOutput(t *testing.T) {
	codexDir := t.TempDir()
	ws := filepath.Join(t.TempDir(), "codex-repo")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	sessDir := filepath.Join(codexDir, "sessions", "2026", "11", "28")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sid := "01a22289-21a7-76c1-8b3c-52c816893701"
	rollout := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"` + sid + `","cwd":"` + ws + `"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"Inspect mutex usage"}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"No deadlock detected. Verdict: APPROVED"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(sessDir, "rollout-2026-"+sid+".jsonl"), []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}

	ad := New(Config{CodexDir: codexDir})

	sessions, err := ad.ListSessions(ws, 10)
	if err != nil || len(sessions) != 1 || sessions[0].ID != sid {
		t.Fatalf("ListSessions = (%+v, %v)", sessions, err)
	}

	sess, err := ad.GetSession("01a22289")
	if err != nil || sess.ID != sid || sess.Title != "Inspect mutex usage" {
		t.Fatalf("GetSession = (%+v, %v)", sess, err)
	}

	out, err := ad.GetLatestAssistantOutput("01a22289")
	if err != nil || !strings.Contains(out, "Verdict: APPROVED") {
		t.Fatalf("GetLatestAssistantOutput = (%q, %v)", out, err)
	}
}

func TestCodexAdapterAskNewAndResume(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake CLI test is Unix-only")
	}
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	argsFile := filepath.Join(tmp, "args.txt")
	fakeCLI := filepath.Join(tmp, "codex")
	// Fake codex CLI writes -o file and emits thread.started JSONL event.
	script := `#!/bin/sh
printf '%s\n' "$@" > "` + argsFile + `"
OUT=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    OUT="$2"
    shift 2
    continue
  fi
  shift
done
if [ -n "$OUT" ]; then
  printf 'Codex review: APPROVED\n' > "$OUT"
fi
printf '{"type":"thread.started","thread_id":"01a22289-21a7-76c1-8b3c-52c816893701"}\n'
`
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	codexDir := filepath.Join(tmp, ".codex")
	existingID := "01a00036-0064-7bb3-9b07-1b479662dede"
	sessDir := filepath.Join(codexDir, "sessions", "2026", "11", "28")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "rollout-"+existingID+".jsonl"), []byte(`{"type":"session_meta","payload":{"id":"`+existingID+`","cwd":"`+ws+`"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ad := New(Config{CodexDir: codexDir, CLIPath: fakeCLI})

	// 1. Fresh session Ask
	resp, err := ad.Ask(context.Background(), adapter.AskRequest{Workspace: ws, Prompt: "Check diff"})
	if err != nil {
		t.Fatalf("Ask(new) error = %v", err)
	}
	if resp.SessionID != "01a22289-21a7-76c1-8b3c-52c816893701" || resp.Output != "Codex review: APPROVED" {
		t.Fatalf("Ask(new) = %+v", resp)
	}
	argv1, _ := os.ReadFile(argsFile)
	if strings.Contains(string(argv1), "\nresume\n") || !strings.Contains(string(argv1), "-C\n"+ws) {
		t.Fatalf("unexpected fresh codex Ask argv:\n%s", string(argv1))
	}

	// 2. Resume existing session by prefix
	resp2, err := ad.Ask(context.Background(), adapter.AskRequest{SessionID: "01a00036", Workspace: ws, Prompt: "Followup"})
	if err != nil {
		t.Fatalf("Ask(resume) error = %v", err)
	}
	if resp2.Output != "Codex review: APPROVED" {
		t.Fatalf("Ask(resume) = %+v", resp2)
	}
	argv2, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(argv2), "exec\nresume\n"+existingID) {
		t.Fatalf("expected exec resume %s in argv:\n%s", existingID, string(argv2))
	}
}
