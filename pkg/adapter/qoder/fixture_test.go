package qoder

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
)

func TestQoderFixture(t *testing.T) {
	testdataDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatalf("failed to get testdata abs path: %v", err)
	}

	sessionID := "e959378f-57da-496f-93e2-ff7092aa1e02"

	// Create adapter pointing to testdata (with no external sqlite DB)
	ad := New(Config{
		BaseDir: testdataDir,
		DBPath:  ":none:",
	})

	// 1. GetSession verification via short ID
	sess, err := ad.GetSession("e959378f")
	if err != nil {
		t.Fatalf("failed to get session by short ID: %v", err)
	}
	if sess.ID != sessionID {
		t.Errorf("expected session ID %s, got %s", sessionID, sess.ID)
	}
	if sess.Workspace != "/home/kamus/CascadeProjects/relay" {
		t.Errorf("expected workspace '/home/kamus/CascadeProjects/relay', got '%s'", sess.Workspace)
	}
	if sess.Title != "你觉得这个repo的design如何" {
		t.Errorf("expected title '你觉得这个repo的design如何', got '%s'", sess.Title)
	}

	// 2. GetLatestAssistantOutput extracts active-leaf without thinking tokens
	output, err := ad.GetLatestAssistantOutput("e959378f")
	if err != nil {
		t.Fatalf("failed to get latest assistant output: %v", err)
	}
	expectedOutput := "看完了设计文档和全部实现，方向是对的，但格式漂移和短ID歧义是核心隐患。"
	if output != expectedOutput {
		t.Errorf("expected '%s', got '%s'", expectedOutput, output)
	}

	// 3. Lease lock detection returns ErrTurnInProgress
	lockDir := filepath.Join(testdataDir, "tasks", sessionID, ".task-locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatalf("failed to create lock dir: %v", err)
	}
	leaseFile := filepath.Join(lockDir, "test.lease")
	if err := os.WriteFile(leaseFile, []byte("busy"), 0o600); err != nil {
		t.Fatalf("failed to write lease file: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(filepath.Join(testdataDir, "tasks"))
	}()

	_, err = ad.GetLatestAssistantOutput("e959378f")
	if !errors.Is(err, adapter.ErrTurnInProgress) {
		t.Errorf("expected ErrTurnInProgress when lease lock present, got %v", err)
	}

	// Remove lease lock and verify it succeeds again
	_ = os.Remove(leaseFile)
	outputAfterLock, err := ad.GetLatestAssistantOutput("e959378f")
	if err != nil {
		t.Fatalf("expected success after lock removal, got %v", err)
	}
	if outputAfterLock != expectedOutput {
		t.Errorf("unexpected output after lock removal: %s", outputAfterLock)
	}

	// 4. ListSessions for workspace
	sessions, err := ad.ListSessions("/home/kamus/CascadeProjects/relay", 10)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != sessionID {
		t.Errorf("expected 1 session matching %s, got %+v", sessionID, sessions)
	}
}
