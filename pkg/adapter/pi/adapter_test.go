package pi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kamusis/relay/pkg/adapter"
)

func writeSessionFile(t *testing.T, dir, filename, content string, mtime time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, filename)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}
	return p
}

func TestPiAdapter_DiscoveryAndMetadata(t *testing.T) {
	agentDir := t.TempDir()
	now := time.Now().Truncate(time.Second)

	// Session 1: with explicit session_info name and thinking + text blocks
	s1 := `{"type":"session","version":3,"id":"01a212b0-ccd1-714a-bba7-599bb4b4b497","timestamp":"2026-11-25T12:31:46.386Z","cwd":"/home/dev/repo-a"}
{"type":"model_change","id":"m1","parentId":null,"timestamp":"2026-11-25T12:31:46.618Z","provider":"google-vertex","modelId":"gemini-4"}
{"type":"message","id":"u1","parentId":"m1","timestamp":"2026-11-25T12:33:05.461Z","message":{"role":"user","content":[{"type":"text","text":"First question\nsecond line"}]}}
{"type":"session_info","id":"si1","parentId":"u1","timestamp":"2026-11-25T12:33:05.635Z","name":"Named Pi Session"}
{"type":"message","id":"a1","parentId":"si1","timestamp":"2026-11-25T12:34:00.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"internal thought"},{"type":"text","text":"Final answer from Pi."}],"stopReason":"stop"}}
`
	writeSessionFile(t, filepath.Join(agentDir, "sessions", "--home-dev-repo-a--"),
		"2026-11-25T12-31-46-386Z_01a212b0-ccd1-714a-bba7-599bb4b4b497.jsonl", s1, now)

	// Session 2: subdir of repo-a, plain string user content (no session_info -> falls back to user message)
	s2 := `{"type":"session","version":3,"id":"01a29999-1111-2222-3333-444455556666","timestamp":"2026-11-25T11:00:00.000Z","cwd":"/home/dev/repo-a/.worktrees/feat-x"}
{"type":"message","id":"u1","parentId":null,"timestamp":"2026-11-25T11:00:01.000Z","message":{"role":"user","content":"Implement feature X\nwith details"}}
{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-11-25T11:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Feature X done."}],"stopReason":"stop"}}
`
	writeSessionFile(t, filepath.Join(agentDir, "sessions", "--home-dev-repo-a-.worktrees-feat-x--"),
		"2026-11-25T11-00-00-000Z_01a29999-1111-2222-3333-444455556666.jsonl", s2, now.Add(-time.Hour))

	// Session 3: different workspace
	s3 := `{"type":"session","version":3,"id":"bbbb1111-1111-2222-3333-444455556666","timestamp":"2026-11-25T10:00:00.000Z","cwd":"/home/dev/repo-b"}
{"type":"message","id":"u1","parentId":null,"timestamp":"2026-11-25T10:00:01.000Z","message":{"role":"user","content":"Repo B task"}}
`
	writeSessionFile(t, filepath.Join(agentDir, "sessions", "--home-dev-repo-b--"),
		"2026-11-25T10-00-00-000Z_bbbb1111-1111-2222-3333-444455556666.jsonl", s3, now.Add(-2*time.Hour))

	// Session 4: shares prefix "bbbb" with Session 3 for ambiguity test
	s4 := `{"type":"session","version":3,"id":"bbbb2222-1111-2222-3333-444455556666","timestamp":"2026-11-25T09:00:00.000Z","cwd":""}
`
	writeSessionFile(t, filepath.Join(agentDir, "sessions", "--scratch--"),
		"2026-11-25T09-00-00-000Z_bbbb2222-1111-2222-3333-444455556666.jsonl", s4, now.Add(-3*time.Hour))

	ad := New(Config{AgentDir: agentDir})
	if ad.Type() != adapter.AgentPi {
		t.Fatalf("expected type %q, got %q", adapter.AgentPi, ad.Type())
	}

	// Exact GetSession
	sess, err := ad.GetSession("01a212b0-ccd1-714a-bba7-599bb4b4b497")
	if err != nil {
		t.Fatalf("GetSession exact failed: %v", err)
	}
	if sess.Title != "Named Pi Session" || sess.Workspace != "/home/dev/repo-a" || sess.Agent != adapter.AgentPi {
		t.Errorf("unexpected session metadata: %+v", sess)
	}

	// Short prefix GetSession
	sess2, err := ad.GetSession("01a29999")
	if err != nil {
		t.Fatalf("GetSession prefix failed: %v", err)
	}
	if sess2.Title != "Implement feature X" {
		t.Errorf("expected fallback title 'Implement feature X', got %q", sess2.Title)
	}

	// Ambiguous prefix returns ErrAmbiguousSession
	_, err = ad.GetSession("bbbb")
	if !errors.Is(err, adapter.ErrAmbiguousSession) {
		t.Fatalf("expected ErrAmbiguousSession for prefix 'bbbb', got %v", err)
	}

	// Missing session returns ErrSessionNotFound
	_, err = ad.GetSession("deadbeef")
	if !errors.Is(err, adapter.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}

	// ListSessions all (sorted by mtime descending)
	all, err := ad.ListSessions(adapter.WorkspaceAll, 10)
	if err != nil {
		t.Fatalf("ListSessions all failed: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 sessions, got %d", len(all))
	}
	if all[0].ID != "01a212b0-ccd1-714a-bba7-599bb4b4b497" || all[1].ID != "01a29999-1111-2222-3333-444455556666" {
		t.Errorf("unexpected ordering: %+v", all)
	}

	// ListSessions filtered by repo-a includes exact + subdir worktree
	repoA, err := ad.ListSessions("/home/dev/repo-a", 10)
	if err != nil {
		t.Fatalf("ListSessions repo-a failed: %v", err)
	}
	if len(repoA) != 2 {
		t.Fatalf("expected 2 sessions for repo-a (root + worktree), got %d", len(repoA))
	}

	// ListSessions WorkspaceNone
	scratch, err := ad.ListSessions(adapter.WorkspaceNone, 10)
	if err != nil {
		t.Fatalf("ListSessions WorkspaceNone failed: %v", err)
	}
	if len(scratch) != 1 || scratch[0].ID != "bbbb2222-1111-2222-3333-444455556666" {
		t.Fatalf("expected 1 scratch session, got %+v", scratch)
	}
	if scratch[0].Title != "Untitled Session" {
		t.Errorf("expected 'Untitled Session', got %q", scratch[0].Title)
	}

	// Output extraction strips thinking blocks
	out, err := ad.GetLatestAssistantOutput("01a212b0")
	if err != nil {
		t.Fatalf("GetLatestAssistantOutput failed: %v", err)
	}
	if out != "Final answer from Pi." {
		t.Errorf("expected 'Final answer from Pi.', got %q", out)
	}
}

func TestPiAdapter_TreeBranchingAndTrailingMetadata(t *testing.T) {
	agentDir := t.TempDir()

	// Tree structure:
	// u1 -> a1 (abandoned branch A with later timestamp in file, wait: branch B is appended last so leaf is si2)
	// u1 -> u2 (branched from u1) -> a2 -> model_change -> session_info (trailing metadata)
	content := `{"type":"session","version":3,"id":"tree-sess-1111","timestamp":"2026-11-25T12:00:00.000Z","cwd":"/repo"}
{"type":"message","id":"u1","parentId":null,"timestamp":"2026-11-25T12:00:01.000Z","message":{"role":"user","content":"Start"}}
{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-11-25T12:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Abandoned branch response"}],"stopReason":"stop"}}
{"type":"message","id":"u2","parentId":"u1","timestamp":"2026-11-25T12:01:00.000Z","message":{"role":"user","content":"Retry on branch B"}}
{"type":"message","id":"a2","parentId":"u2","timestamp":"2026-11-25T12:01:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Part 1. "},{"type":"text","text":"Part 2."}],"stopReason":"stop"}}
{"type":"model_change","id":"mc1","parentId":"a2","timestamp":"2026-11-25T12:01:06.000Z","provider":"openai","modelId":"gpt-5"}
{"type":"session_info","id":"si2","parentId":"mc1","timestamp":"2026-11-25T12:01:07.000Z","name":"Branch B"}
`
	writeSessionFile(t, filepath.Join(agentDir, "sessions", "--repo--"), "tree.jsonl", content, time.Now())

	ad := New(Config{AgentDir: agentDir})
	out, err := ad.GetLatestAssistantOutput("tree-sess-1111")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "Part 1. Part 2." {
		t.Errorf("expected active branch output 'Part 1. Part 2.', got %q", out)
	}
}

func TestPiAdapter_TurnInProgressGuards(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr error
	}{
		{
			name: "pending user prompt at leaf",
			content: `{"type":"session","version":3,"id":"busy-user","cwd":"/repo"}
{"type":"message","id":"a1","parentId":null,"message":{"role":"assistant","content":[{"type":"text","text":"Old answer"}],"stopReason":"stop"}}
{"type":"message","id":"u2","parentId":"a1","message":{"role":"user","content":"New question"}}
`,
			wantErr: adapter.ErrTurnInProgress,
		},
		{
			name: "toolUse stopReason at leaf",
			content: `{"type":"session","version":3,"id":"busy-tool","cwd":"/repo"}
{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":"Check tests"}}
{"type":"message","id":"a1","parentId":"u1","message":{"role":"assistant","content":[{"type":"text","text":"Running tests..."},{"type":"toolCall","id":"tc1","name":"bash"}],"stopReason":"toolUse"}}
`,
			wantErr: adapter.ErrTurnInProgress,
		},
		{
			name: "toolResult at leaf waiting for assistant synthesis",
			content: `{"type":"session","version":3,"id":"busy-tool-res","cwd":"/repo"}
{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":"Check tests"}}
{"type":"message","id":"a1","parentId":"u1","message":{"role":"assistant","content":[{"type":"toolCall","id":"tc1","name":"bash"}],"stopReason":"toolUse"}}
{"type":"message","id":"tr1","parentId":"a1","message":{"role":"toolResult","toolCallId":"tc1","toolName":"bash","content":[{"type":"text","text":"PASS"}]}}
`,
			wantErr: adapter.ErrTurnInProgress,
		},
		{
			name: "aborted assistant turn does not silently return stale text",
			content: `{"type":"session","version":3,"id":"aborted-turn","cwd":"/repo"}
{"type":"message","id":"a1","parentId":null,"message":{"role":"assistant","content":[{"type":"text","text":"Old completed answer"}],"stopReason":"stop"}}
{"type":"message","id":"u2","parentId":"a1","message":{"role":"user","content":"Second question"}}
{"type":"message","id":"a2","parentId":"u2","message":{"role":"assistant","content":[{"type":"text","text":"Partial"}],"stopReason":"aborted"}}
`,
			wantErr: adapter.ErrNoOutputFound,
		},
		{
			name: "empty session with no messages",
			content: `{"type":"session","version":3,"id":"empty-sess","cwd":"/repo"}
{"type":"model_change","id":"m1","parentId":null,"provider":"google-vertex","modelId":"gemini-4"}
`,
			wantErr: adapter.ErrNoOutputFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentDir := t.TempDir()
			writeSessionFile(t, filepath.Join(agentDir, "sessions", "--repo--"), "s.jsonl", tt.content, time.Now())
			ad := New(Config{AgentDir: agentDir})

			// Extract session ID from first line
			entries, err := ad.ListSessions(adapter.WorkspaceAll, 10)
			if err != nil || len(entries) != 1 {
				t.Fatalf("expected 1 session, got %d (err=%v)", len(entries), err)
			}

			_, err = ad.GetLatestAssistantOutput(entries[0].ID)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected error wrapping %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestPiAdapter_DeliverInvokesHeadlessCLI(t *testing.T) {
	agentDir := t.TempDir()
	workspace := t.TempDir()
	argsCapture := filepath.Join(t.TempDir(), "captured.txt")

	// Create a fake pi CLI script that records cwd and argv
	fakePi := filepath.Join(t.TempDir(), "pi")
	script := "#!/bin/sh\n" +
		"pwd > \"" + argsCapture + "\"\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"" + argsCapture + "\"; done\n"
	if err := os.WriteFile(fakePi, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}

	sessionContent := `{"type":"session","version":3,"id":"deliver-sess-1234","cwd":"` + workspace + `"}` + "\n"
	sessionPath := writeSessionFile(t, filepath.Join(agentDir, "sessions", "--ws--"), "deliver.jsonl", sessionContent, time.Now())

	ad := New(Config{AgentDir: agentDir, CLIPath: fakePi})
	err := ad.Deliver(context.Background(), adapter.Transmission{
		ID:          "tx-1",
		LinkName:    "review-relay",
		FromAgent:   adapter.AgentQoder,
		FromSession: "3c58333e-7024-49d8-a9c4-b2a89629f705",
		ToAgent:     adapter.AgentPi,
		ToSession:   "deliver-sess-1234",
		Body:        "Verdict: APPROVED",
	})
	if err != nil {
		t.Fatalf("Deliver failed: %v", err)
	}

	capturedBytes, err := os.ReadFile(argsCapture)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(capturedBytes), "\n"), "\n")
	if len(lines) < 6 {
		t.Fatalf("unexpected captured output:\n%s", string(capturedBytes))
	}
	if lines[0] != workspace {
		t.Errorf("expected cmd.Dir=%q, got %q", workspace, lines[0])
	}
	if lines[1] != "-p" || lines[2] != "--session" || lines[3] != sessionPath || lines[4] != "--" {
		t.Errorf("unexpected argv prefix: %v", lines[1:5])
	}
	rest := strings.Join(lines[5:], "\n")
	if !strings.Contains(rest, "[Relay from qoder (session: 3c58333e) via link 'review-relay']") || !strings.Contains(rest, "Verdict: APPROVED") {
		t.Errorf("unexpected formatted payload:\n%s", rest)
	}
}

func TestPiAdapter_ListSessions_SymlinkWorkspace(t *testing.T) {
	tmp := t.TempDir()
	realWS := filepath.Join(tmp, "real", "repo")
	if err := os.MkdirAll(realWS, 0o755); err != nil {
		t.Fatalf("mkdir realWS: %v", err)
	}
	canonicalWS, err := filepath.EvalSymlinks(realWS)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	symWS := filepath.Join(tmp, "sym-repo")
	if err := os.Symlink(canonicalWS, symWS); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	agentDir := filepath.Join(tmp, "pi-agent")
	content := `{"type":"session","version":3,"id":"pi-sym-1111","cwd":"` + canonicalWS + `"}` + "\n"
	writeSessionFile(t, filepath.Join(agentDir, "sessions", "--real--"), "s.jsonl", content, time.Now())

	ad := New(Config{AgentDir: agentDir})
	sessions, err := ad.ListSessions(symWS, 10)
	if err != nil {
		t.Fatalf("ListSessions(symWS) error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "pi-sym-1111" {
		t.Fatalf("expected 1 pi session via symlink %q -> %q, got %+v", symWS, canonicalWS, sessions)
	}
}
