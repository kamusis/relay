package qoder

import (
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

	return base
}

func TestQoderAdapter(t *testing.T) {
	baseDir := setupTestQoderDir(t)
	ad := New(Config{BaseDir: baseDir})

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
	if len(list) != 1 {
		t.Errorf("expected 1 session, got %d", len(list))
	}

	// 6. ListSessions filtered by workspace
	listWS, err := ad.ListSessions("/tmp/test-project", 10)
	if err != nil {
		t.Fatalf("failed to list sessions by ws: %v", err)
	}
	if len(listWS) != 1 {
		t.Errorf("expected 1 session, got %d", len(listWS))
	}
}
