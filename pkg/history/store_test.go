package history

import (
	"path/filepath"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
)

func TestHistoryStore(t *testing.T) {
	tempDir := t.TempDir()
	historyPath := filepath.Join(tempDir, "history.jsonl")
	store := NewStore(historyPath)

	t1 := &adapter.Transmission{
		LinkName:    "moc-qoder",
		FromAgent:   adapter.AgentAntigravity,
		FromSession: "session-1",
		ToAgent:     adapter.AgentQoder,
		ToSession:   "session-2",
		UserPrompt:  "review this",
		Body:        "body 1",
		Status:      adapter.TransmissionDelivered,
	}

	t2 := &adapter.Transmission{
		LinkName:    "moc-qoder",
		FromAgent:   adapter.AgentQoder,
		FromSession: "session-2",
		ToAgent:     adapter.AgentAntigravity,
		ToSession:   "session-1",
		UserPrompt:  "fix this",
		Body:        "body 2",
		Status:      adapter.TransmissionFailed,
		Error:       "agent process terminated unexpectedly",
	}

	t3 := &adapter.Transmission{
		LinkName:    "other-link",
		FromAgent:   adapter.AgentAntigravity,
		FromSession: "session-3",
		ToAgent:     adapter.AgentCodex,
		ToSession:   "session-4",
		Body:        "body 3",
	}

	if err := store.Append(t1); err != nil {
		t.Fatalf("failed to append t1: %v", err)
	}
	if err := store.Append(t2); err != nil {
		t.Fatalf("failed to append t2: %v", err)
	}
	if err := store.Append(t3); err != nil {
		t.Fatalf("failed to append t3: %v", err)
	}

	// List all
	all, err := store.List("", 0)
	if err != nil {
		t.Fatalf("failed to list all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 records, got %d", len(all))
	}
	// Verify latest first order
	if all[0].Body != "body 3" || all[1].Body != "body 2" || all[2].Body != "body 1" {
		t.Errorf("expected latest first order, got bodies: %s, %s, %s", all[0].Body, all[1].Body, all[2].Body)
	}

	// Filter by linkName
	moc, err := store.List("moc-qoder", 0)
	if err != nil {
		t.Fatalf("failed to list moc-qoder: %v", err)
	}
	if len(moc) != 2 {
		t.Errorf("expected 2 records for moc-qoder, got %d", len(moc))
	}

	// Limit
	limited, err := store.List("moc-qoder", 1)
	if err != nil {
		t.Fatalf("failed to list with limit: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("expected 1 record, got %d", len(limited))
	}
	if limited[0].Body != "body 2" {
		t.Errorf("expected latest record body 2, got %s", limited[0].Body)
	}

	// Get by prefix
	got, err := store.Get(t1.ID[:8])
	if err != nil {
		t.Fatalf("failed to get by prefix: %v", err)
	}
	if got.ID != t1.ID {
		t.Errorf("expected id %s, got %s", t1.ID, got.ID)
	}
	if got.Status != adapter.TransmissionDelivered {
		t.Errorf("expected status delivered, got %s", got.Status)
	}

	gotFailed, err := store.Get(t2.ID[:8])
	if err != nil {
		t.Fatalf("failed to get t2 by prefix: %v", err)
	}
	if gotFailed.Status != adapter.TransmissionFailed || gotFailed.Error != "agent process terminated unexpectedly" {
		t.Errorf("expected failed status with error, got status=%s err=%s", gotFailed.Status, gotFailed.Error)
	}
}
