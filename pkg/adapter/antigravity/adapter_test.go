package antigravity

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
	_ "modernc.org/sqlite"
)

func setupTestAntigravityDir(t *testing.T) string {
	base := t.TempDir()

	// 1. Create SQLite database
	dbPath := filepath.Join(base, "conversation_summaries.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	defer db.Close()

	schema := `
	CREATE TABLE conversation_summaries (
		conversation_id TEXT PRIMARY KEY,
		title TEXT,
		workspace_uris TEXT,
		last_modified_time DATETIME,
		not_fully_idle NUMERIC DEFAULT 0,
		killed NUMERIC DEFAULT 0
	);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO conversation_summaries VALUES 
		('11112222-3333-4444-5555-666677778888', 'Session One', '["file:///tmp/workspace-a"]', '2026-10-31 10:00:00', 0, 0),
		('99998888-7777-6666-5555-444433332222', 'Session Two', '["file:///tmp/workspace-b"]', '2026-10-31 11:00:00', 0, 0),
		('44445555-6666-7777-8888-999900001111', 'Scratch Session', '', '2026-10-31 08:00:00', 0, 0),
		('aaaa1111-0000-0000-0000-000000000001', 'Ambiguous A', '["file:///tmp/workspace-a"]', '2026-10-31 12:00:00', 0, 0),
		('aaaa2222-0000-0000-0000-000000000002', 'Ambiguous B', '["file:///tmp/workspace-a"]', '2026-10-31 12:01:00', 0, 0),
		('deadbeef-0000-0000-0000-000000000000', 'Dead Session', '["file:///tmp/workspace-c"]', '2026-10-31 09:00:00', 0, 1);
	`)
	if err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	// 2. Create transcript for session 1
	logDir := filepath.Join(base, "brain", "11112222-3333-4444-5555-666677778888", ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("failed to create log dir: %v", err)
	}

	transcriptContent := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"hello"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"first model reply"}
{"step_index":2,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"second message"}
{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"latest model response to relay"}
`
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(transcriptContent), 0o600); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	return base
}

func TestAntigravityAdapter(t *testing.T) {
	baseDir := setupTestAntigravityDir(t)
	ad := New(Config{BaseDir: baseDir})

	if ad.Type() != adapter.AgentAntigravity {
		t.Errorf("expected type antigravity, got %s", ad.Type())
	}

	// 1. GetSession exact match
	sess, err := ad.GetSession("11112222-3333-4444-5555-666677778888")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	if sess.Title != "Session One" {
		t.Errorf("expected title 'Session One', got '%s'", sess.Title)
	}
	if sess.Workspace != "/tmp/workspace-a" {
		t.Errorf("expected workspace '/tmp/workspace-a', got '%s'", sess.Workspace)
	}

	// 2. GetSession prefix match
	sessShort, err := ad.GetSession("11112222")
	if err != nil {
		t.Fatalf("failed to get session by prefix: %v", err)
	}
	if sessShort.ID != sess.ID {
		t.Errorf("expected session id %s, got %s", sess.ID, sessShort.ID)
	}

	// 3. GetSession killed session returns not found
	_, err = ad.GetSession("deadbeef")
	if err == nil {
		t.Errorf("expected error for killed session, got nil")
	}

	// 4. GetLatestAssistantOutput extracts the last model response
	output, err := ad.GetLatestAssistantOutput("11112222-3333-4444-5555-666677778888")
	if err != nil {
		t.Fatalf("failed to get latest assistant output: %v", err)
	}
	if output != "latest model response to relay" {
		t.Errorf("expected 'latest model response to relay', got '%s'", output)
	}

	// 5. ListSessions without workspace filter includes scratch session
	list, err := ad.ListSessions("", 10)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	if len(list) != 5 {
		t.Errorf("expected 5 active sessions, got %d", len(list))
	}

	// 6. ListSessions with workspace filter
	listFiltered, err := ad.ListSessions("/tmp/workspace-b", 10)
	if err != nil {
		t.Fatalf("failed to list filtered sessions: %v", err)
	}
	if len(listFiltered) != 1 || listFiltered[0].Title != "Session Two" {
		t.Errorf("expected 1 session 'Session Two', got %+v", listFiltered)
	}

	// 7. ListSessions with WorkspaceNone strictly returns scratch sessions
	listNone, err := ad.ListSessions(adapter.WorkspaceNone, 10)
	if err != nil {
		t.Fatalf("failed to list WorkspaceNone sessions: %v", err)
	}
	if len(listNone) != 1 || listNone[0].Title != "Scratch Session" {
		t.Errorf("expected 1 scratch session, got %+v", listNone)
	}

	// 8. Ambiguous short ID returns ErrAmbiguousSession
	_, err = ad.GetSession("aaaa")
	if !errors.Is(err, adapter.ErrAmbiguousSession) {
		t.Fatalf("expected ErrAmbiguousSession for ambiguous prefix, got %v", err)
	}
}
