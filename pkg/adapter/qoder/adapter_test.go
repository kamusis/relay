package qoder

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
)

func setupTestQoderDir(t *testing.T) string {
	base := t.TempDir()

	projectSlug := "-tmp-test-project"
	projDir := filepath.Join(base, "projects", projectSlug)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("failed to create proj dir: %v", err)
	}

	sessionID := "3c58333e-7024-49d8-a9c4-b2a89629f705"
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")

	lines := `{"type":"workspace-directories","sessionId":"3c58333e-7024-49d8-a9c4-b2a89629f705","directories":["/tmp/test-project"]}
{"type":"user","uuid":"user-1","message":{"role":"user","content":[{"type":"text","text":"Please review"}]}}
{"type":"last-prompt","lastPrompt":"Code Review Round 1"}
{"type":"assistant","uuid":"leaf-uuid-1","stop_reason":"end_turn","message":{"role":"assistant","content":[{"type":"text","text":"LGTM. No blocking items found."}]}}
{"type":"active-leaf","leafUuid":"leaf-uuid-1"}
`
	if err := os.WriteFile(jsonlPath, []byte(lines), 0o600); err != nil {
		t.Fatalf("failed to write test jsonl: %v", err)
	}

	// Create two ambiguous sessions
	ambig1 := filepath.Join(projDir, "bbbb1111-7024-49d8-a9c4-b2a89629f705.jsonl")
	ambig2 := filepath.Join(projDir, "bbbb2222-7024-49d8-a9c4-b2a89629f705.jsonl")
	ambigContent := `{"type":"workspace-directories","directories":["/tmp/test-project"]}
{"type":"last-prompt","lastPrompt":"Ambiguous"}
`
	_ = os.WriteFile(ambig1, []byte(ambigContent), 0o600)
	_ = os.WriteFile(ambig2, []byte(ambigContent), 0o600)

	return base
}

func TestQoderAdapter(t *testing.T) {
	baseDir := setupTestQoderDir(t)
	ad := New(Config{BaseDir: baseDir, DBPath: ":none:"})

	if ad.Type() != adapter.AgentQoder {
		t.Errorf("expected type qoder, got %s", ad.Type())
	}

	// 1. PathToSlug
	slug := PathToSlug("/home/kamus/CascadeProjects/relay")
	if slug != "-home-kamus-CascadeProjects-relay" {
		t.Errorf("unexpected slug: %s", slug)
	}

	// 2. GetSession exact match
	sess, err := ad.GetSession("3c58333e-7024-49d8-a9c4-b2a89629f705")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	if sess.Title != "Code Review Round 1" {
		t.Errorf("expected title 'Code Review Round 1', got '%s'", sess.Title)
	}
	if sess.Workspace != "/tmp/test-project" {
		t.Errorf("expected workspace '/tmp/test-project', got '%s'", sess.Workspace)
	}

	// 3. GetSession prefix match
	sessPrefix, err := ad.GetSession("3c58333e")
	if err != nil {
		t.Fatalf("failed to get session by prefix: %v", err)
	}
	if sessPrefix.ID != sess.ID {
		t.Errorf("expected ID %s, got %s", sess.ID, sessPrefix.ID)
	}

	// 4. GetLatestAssistantOutput extracts active-leaf assistant text
	output, err := ad.GetLatestAssistantOutput("3c58333e")
	if err != nil {
		t.Fatalf("failed to get latest assistant output: %v", err)
	}
	if output != "LGTM. No blocking items found." {
		t.Errorf("expected 'LGTM. No blocking items found.', got '%s'", output)
	}

	// 5. ListSessions all
	list, err := ad.ListSessions("", 10)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(list))
	}

	// 6. ListSessions filtered by workspace
	listWS, err := ad.ListSessions("/tmp/test-project", 10)
	if err != nil {
		t.Fatalf("failed to list sessions by ws: %v", err)
	}
	if len(listWS) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(listWS))
	}

	// 7. Ambiguous short ID returns ErrAmbiguousSession
	_, err = ad.GetSession("bbbb")
	if !errors.Is(err, adapter.ErrAmbiguousSession) {
		t.Fatalf("expected ErrAmbiguousSession for ambiguous prefix, got %v", err)
	}
}

func TestQoderLeaseLock(t *testing.T) {
	baseDir := setupTestQoderDir(t)
	ad := New(Config{BaseDir: baseDir, DBPath: ":none:"})
	sessID := "3c58333e-7024-49d8-a9c4-b2a89629f705"

	// 1. Unlocked lingering lease file should NOT trigger ErrTurnInProgress
	lockDir := filepath.Join(baseDir, "tasks", sessID, ".task-locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatalf("failed to create lock dir: %v", err)
	}
	leasePath := filepath.Join(lockDir, "1.lease")
	if err := os.WriteFile(leasePath, []byte(""), 0o600); err != nil {
		t.Fatalf("failed to write lease file: %v", err)
	}

	// Output extraction must succeed because no active process holds the flock
	output, err := ad.GetLatestAssistantOutput(sessID)
	if err != nil {
		t.Fatalf("expected success with unlocked lingering lease, got: %v", err)
	}
	if output != "LGTM. No blocking items found." {
		t.Errorf("unexpected output: %s", output)
	}
}

func TestQoderTrailingMetadataParsing(t *testing.T) {
	base := t.TempDir()
	projDir := filepath.Join(base, "projects", "-test-trailing")
	_ = os.MkdirAll(projDir, 0o755)

	sessID := "aaaa1111-2222-3333-4444-555566667777"
	jsonlPath := filepath.Join(projDir, sessID+".jsonl")

	// stop_reason inside message, and active-leaf pointing to a trailing non-assistant event
	lines := `{"type":"workspace-directories","directories":["/test"]}
{"type":"assistant","uuid":"turn-1","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Verdict: APPROVED"}]}}
{"type":"active-leaf","leafUuid":"non-assistant-trailing-event"}
{"type":"runtime-config","timestamp":1793560324100}
`
	_ = os.WriteFile(jsonlPath, []byte(lines), 0o600)

	ad := New(Config{BaseDir: base, DBPath: ":none:"})
	output, err := ad.GetLatestAssistantOutput(sessID)
	if err != nil {
		t.Fatalf("expected fallback to latest assistant text when active-leaf is non-assistant, got: %v", err)
	}
	if output != "Verdict: APPROVED" {
		t.Errorf("expected 'Verdict: APPROVED', got '%s'", output)
	}
}

func TestQoderListSessions_SymlinkWorkspace(t *testing.T) {
	tmp := t.TempDir()
	realWS := filepath.Join(tmp, "real", "project")
	if err := os.MkdirAll(realWS, 0o755); err != nil {
		t.Fatalf("mkdir realWS: %v", err)
	}
	// Canonicalize realWS itself in case t.TempDir() resides under a symlinked root (e.g. macOS /var -> /private/var).
	canonicalWS, err := filepath.EvalSymlinks(realWS)
	if err != nil {
		t.Fatalf("EvalSymlinks(realWS): %v", err)
	}
	symWS := filepath.Join(tmp, "link-project")
	if err := os.Symlink(canonicalWS, symWS); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	base := filepath.Join(tmp, "qoder")
	projDir := filepath.Join(base, "projects", PathToSlug(canonicalWS))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir projDir: %v", err)
	}
	jsonlPath := filepath.Join(projDir, "11112222-3333-4444-5555-666677778888.jsonl")
	content := `{"type":"workspace-directories","directories":["` + canonicalWS + `"]}
{"type":"last-prompt","lastPrompt":"Symlink Workspace Session"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	ad := New(Config{BaseDir: base, DBPath: ":none:"})

	// Filtering by symlink path must discover sessions stored under the canonical path slug.
	sessions, err := ad.ListSessions(symWS, 10)
	if err != nil {
		t.Fatalf("ListSessions(symWS) error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "11112222-3333-4444-5555-666677778888" {
		t.Fatalf("expected 1 session via symlink path %q -> %q, got %+v", symWS, canonicalWS, sessions)
	}

	// Filtering by a non-existent child under a symlinked parent must also resolve the parent symlink.
	missingChildReal := filepath.Join(canonicalWS, "deleted-sub")
	missingChildSym := filepath.Join(symWS, "deleted-sub")
	projDir2 := filepath.Join(base, "projects", PathToSlug(missingChildReal))
	if err := os.MkdirAll(projDir2, 0o755); err != nil {
		t.Fatalf("mkdir projDir2: %v", err)
	}
	jsonlPath2 := filepath.Join(projDir2, "22223333-4444-5555-6666-777788889999.jsonl")
	content2 := `{"type":"workspace-directories","directories":["` + missingChildReal + `"]}
{"type":"last-prompt","lastPrompt":"Deleted Subdir Session"}
`
	if err := os.WriteFile(jsonlPath2, []byte(content2), 0o600); err != nil {
		t.Fatalf("write jsonl2: %v", err)
	}

	sessions2, err := ad.ListSessions(missingChildSym, 10)
	if err != nil {
		t.Fatalf("ListSessions(missingChildSym) error: %v", err)
	}
	if len(sessions2) != 1 || sessions2[0].ID != "22223333-4444-5555-6666-777788889999" {
		t.Fatalf("expected 1 session for deleted child under symlink parent, got %+v", sessions2)
	}
}
