package link

import (
	"fmt"
	"path/filepath"
	"sync"
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

func TestStore_Concurrent(t *testing.T) {
	tempDir := t.TempDir()
	storePath := filepath.Join(tempDir, "links.json")

	const workers = 10
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			// Simulate separate CLI process with own Store instance
			s := NewStore(storePath)
			link := adapter.Link{
				Name: fmt.Sprintf("link-%d", workerID),
				SessionA: adapter.Session{
					ID:    fmt.Sprintf("sess-a-%d", workerID),
					Agent: adapter.AgentAntigravity,
				},
				SessionB: adapter.Session{
					ID:    fmt.Sprintf("sess-b-%d", workerID),
					Agent: adapter.AgentQoder,
				},
			}
			if err := s.Create(link); err != nil {
				errCh <- fmt.Errorf("worker %d create failed: %w", workerID, err)
				return
			}
			// Update it
			link.SessionA.Title = "Updated"
			if err := s.Update(link); err != nil {
				errCh <- fmt.Errorf("worker %d update failed: %w", workerID, err)
				return
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}

	// Verify all links exist and intact
	finalStore := NewStore(storePath)
	list, err := finalStore.List()
	if err != nil {
		t.Fatalf("failed to list links after concurrent writes: %v", err)
	}
	if len(list) != workers {
		t.Fatalf("expected %d links, got %d", workers, len(list))
	}
}
