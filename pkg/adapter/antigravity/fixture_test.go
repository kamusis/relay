package antigravity

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAntigravityFixture(t *testing.T) {
	// Base dir pointing to testdata
	testdataDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("failed to resolve testdata path: %v", err)
	}

	// Create test DB in temp directory but link brain to testdata
	tmpBase := t.TempDir()
	dbPath := filepath.Join(tmpBase, "conversation_summaries.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
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
		t.Fatalf("failed to exec schema: %v", err)
	}

	// Insert record matching testdata session
	sessionID := "3dfb0b87-f23a-4dc7-b7e5-27677ced4323"
	_, err = db.Exec(`
		INSERT INTO conversation_summaries (conversation_id, title, workspace_uris, last_modified_time, not_fully_idle, killed)
		VALUES (?, ?, ?, ?, 0, 0);
	`, sessionID, "准备开始测试relay", "[\"file:///home/kamus/CascadeProjects/relay\"]", "2026-10-31 14:23:05")
	if err != nil {
		t.Fatalf("failed to insert fixture summary: %v", err)
	}

	// Symlink or copy brain from testdata to tmpBase
	if err := os.Symlink(filepath.Join(testdataDir, "brain"), filepath.Join(tmpBase, "brain")); err != nil {
		t.Fatalf("failed to symlink brain fixture: %v", err)
	}

	ad := New(Config{BaseDir: tmpBase})

	// 1. GetSession verification
	sess, err := ad.GetSession(sessionID)
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	if sess.Title != "准备开始测试relay" {
		t.Errorf("unexpected title: %s", sess.Title)
	}
	if sess.Workspace != "/home/kamus/CascadeProjects/relay" {
		t.Errorf("unexpected workspace: %s", sess.Workspace)
	}

	// 2. GetSession short ID prefix
	sessShort, err := ad.GetSession("3dfb0b87")
	if err != nil {
		t.Fatalf("failed to get session by short prefix: %v", err)
	}
	if sessShort.ID != sessionID {
		t.Errorf("expected session id %s, got %s", sessionID, sessShort.ID)
	}

	// 3. GetLatestAssistantOutput with truncated field extraction from transcript_full.jsonl
	output, err := ad.GetLatestAssistantOutput(sessionID)
	if err != nil {
		t.Fatalf("failed to get latest assistant output: %v", err)
	}

	expectedPrefix := "收到，会话已建立"
	if len(output) < len(expectedPrefix) || output[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("expected output to be extracted from transcript_full.jsonl starting with '%s', got '%s'",
			expectedPrefix, output)
	}
}
