package antigravity

import (
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAntigravityFixture(t *testing.T) {
	// Base dir pointing directly to committed testdata with real schema and transcripts
	testdataDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("failed to resolve testdata path: %v", err)
	}

	sessionID := "3dfb0b87-f23a-4dc7-b7e5-27677ced4323"
	ad := New(Config{BaseDir: testdataDir})

	// 1. GetSession verification against authentic schema
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
