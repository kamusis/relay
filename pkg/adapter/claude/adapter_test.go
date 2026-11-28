package claude

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
)

func TestClaudeAdapterSessionsAndOutput(t *testing.T) {
	claudeDir := t.TempDir()
	ws := filepath.Join(t.TempDir(), "my-repo")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	slugDir := filepath.Join(claudeDir, "projects", PathToSlug(ws))
	if err := os.MkdirAll(slugDir, 0o755); err != nil {
		t.Fatal(err)
	}

	doneID := "11111111-2222-3333-4444-555555555555"
	doneJSONL := strings.Join([]string{
		`{"type":"user","sessionId":"` + doneID + `","cwd":"` + ws + `","message":{"role":"user","content":"Review auth.go"}}`,
		`{"type":"assistant","sessionId":"` + doneID + `","cwd":"` + ws + `","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Verdict: APPROVED"}]}}`,
		`{"type":"last-prompt","lastPrompt":"Review auth.go","sessionId":"` + doneID + `"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(slugDir, doneID+".jsonl"), []byte(doneJSONL), 0o600); err != nil {
		t.Fatal(err)
	}

	busyID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	busyJSONL := strings.Join([]string{
		`{"type":"user","sessionId":"` + busyID + `","cwd":"` + ws + `","message":{"role":"user","content":"Run tests"}}`,
		`{"type":"assistant","sessionId":"` + busyID + `","cwd":"` + ws + `","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"Running tests..."}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(slugDir, busyID+".jsonl"), []byte(busyJSONL), 0o600); err != nil {
		t.Fatal(err)
	}

	ad := New(Config{ClaudeDir: claudeDir})

	sessions, err := ad.ListSessions(ws, 10)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("ListSessions = (%+v, %v), want 2 sessions", sessions, err)
	}

	sess, err := ad.GetSession("11111111")
	if err != nil || sess.ID != doneID || sess.Title != "Review auth.go" {
		t.Fatalf("GetSession(prefix) = (%+v, %v)", sess, err)
	}

	out, err := ad.GetLatestAssistantOutput(doneID)
	if err != nil || out != "Verdict: APPROVED" {
		t.Fatalf("GetLatestAssistantOutput(done) = (%q, %v)", out, err)
	}

	if _, err := ad.GetLatestAssistantOutput(busyID); err == nil || !strings.Contains(err.Error(), "tool_use") {
		t.Fatalf("expected busy tool_use error, got %v", err)
	}
}

func TestClaudeAdapterAskNewAndResume(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake CLI test is Unix-only")
	}
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	// Fake claude CLI records argv and returns JSON result matching real Claude Code schema.
	argsFile := filepath.Join(tmp, "args.txt")
	fakeCLI := filepath.Join(tmp, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsFile + "\"\nprintf '{\"type\":\"result\",\"is_error\":false,\"result\":\"Looks good\",\"session_id\":\"99999999-aaaa-bbbb-cccc-dddddddddddd\"}\\n'\n"
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	claudeDir := filepath.Join(tmp, ".claude")
	existingID := "652e7f08-14b5-4a11-9da7-6454dbe57285"
	slugDir := filepath.Join(claudeDir, "projects", PathToSlug(ws))
	if err := os.MkdirAll(slugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slugDir, existingID+".jsonl"), []byte(`{"type":"user","sessionId":"`+existingID+`","cwd":"`+ws+`","message":{"role":"user","content":"init"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ad := New(Config{ClaudeDir: claudeDir, CLIPath: fakeCLI})

	// 1. Fresh session Ask
	resp, err := ad.Ask(context.Background(), adapter.AskRequest{Workspace: ws, Prompt: "Review diff"})
	if err != nil {
		t.Fatalf("Ask(new) error = %v", err)
	}
	if resp.SessionID != "99999999-aaaa-bbbb-cccc-dddddddddddd" || resp.Output != "Looks good" {
		t.Fatalf("Ask(new) = %+v", resp)
	}
	argv1, _ := os.ReadFile(argsFile)
	if strings.Contains(string(argv1), "--resume") || !strings.Contains(string(argv1), "--output-format") {
		t.Fatalf("unexpected fresh Ask argv:\n%s", string(argv1))
	}

	// 2. Resume existing session by short prefix
	resp2, err := ad.Ask(context.Background(), adapter.AskRequest{SessionID: "652e7f08", Workspace: ws, Prompt: "Followup"})
	if err != nil {
		t.Fatalf("Ask(resume) error = %v", err)
	}
	if resp2.Output != "Looks good" {
		t.Fatalf("Ask(resume) = %+v", resp2)
	}
	argv2, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(argv2), "--resume\n"+existingID) {
		t.Fatalf("expected --resume %s in argv:\n%s", existingID, string(argv2))
	}
}
