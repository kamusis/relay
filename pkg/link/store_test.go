package link

import (
	"path/filepath"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
)

func TestStore_CRUD(t *testing.T) {
	tempDir := t.TempDir()
	storePath := filepath.Join(tempDir, "links.json")
	store := NewStore(storePath)

	link1 := adapter.Link{
		Name: "moc-qoder",
		SessionA: adapter.Session{
			ID:        "session-agy-1",
			Agent:     adapter.AgentAntigravity,
			Title:     "Old Title",
			Workspace: "/path/to/project",
		},
		SessionB: adapter.Session{
			ID:        "session-qoder-1",
			Agent:     adapter.AgentQoder,
			Title:     "Review Session",
			Workspace: "/path/to/project",
		},
	}

	// 1. Create
	if err := store.Create(link1); err != nil {
		t.Fatalf("failed to create link: %v", err)
	}

	// 2. Duplicate create fails
	if err := store.Create(link1); err == nil {
		t.Fatal("expected duplicate create to fail, got nil")
	}

	// 3. Get
	got, err := store.Get("moc-qoder")
	if err != nil {
		t.Fatalf("failed to get link: %v", err)
	}
	if got.Name != "moc-qoder" || got.SessionA.Title != "Old Title" {
		t.Errorf("unexpected got: %+v", got)
	}

	// 4. Update (self-healing metadata)
	got.SessionA.Title = "New Renamed Title"
	if err := store.Update(*got); err != nil {
		t.Fatalf("failed to update link: %v", err)
	}

	updated, err := store.Get("moc-qoder")
	if err != nil {
		t.Fatalf("failed to get updated link: %v", err)
	}
	if updated.SessionA.Title != "New Renamed Title" {
		t.Errorf("expected title 'New Renamed Title', got '%s'", updated.SessionA.Title)
	}

	// 5. List
	list, err := store.List()
	if err != nil {
		t.Fatalf("failed to list links: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 link, got %d", len(list))
	}

	// 6. Delete
	if err := store.Delete("moc-qoder"); err != nil {
		t.Fatalf("failed to delete link: %v", err)
	}

	// 7. Get after delete fails
	if _, err := store.Get("moc-qoder"); err == nil {
		t.Fatal("expected get after delete to fail, got nil")
	}
}
