package steer

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNormalizeTargetAndMatching(t *testing.T) {
	tmp := t.TempDir()
	sub := filepath.Join(tmp, "server", "internal")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Session normalization
	got, err := NormalizeTarget("agy:17f5151f")
	if err != nil || got != "antigravity:17f5151f" {
		t.Fatalf("NormalizeTarget(agy) = %q, %v", got, err)
	}
	gotQ, err := NormalizeTarget("qoder:e09937bb")
	if err != nil || gotQ != "qoder:e09937bb" {
		t.Fatalf("NormalizeTarget(qoder) = %q, %v", gotQ, err)
	}
	if _, err := NormalizeTarget("qoder:"); err == nil {
		t.Fatal("expected error for empty session ID")
	}

	// Session prefix matching within same agent
	if !MatchesTarget("qoder:e09937bb", "qoder:e09937bb-edb1-4621-91a3-03d9b17bb023") {
		t.Fatal("expected short session target to match full UUID query")
	}
	if MatchesTarget("qoder:e09937bb", "claude:e09937bb-edb1-4621-91a3-03d9b17bb023") {
		t.Fatal("must not match across different agent types")
	}

	// Workspace normalization & subdirectory matching
	wsRoot, err := NormalizeTarget(tmp)
	if err != nil {
		t.Fatalf("NormalizeTarget(tmp): %v", err)
	}
	wsSub, err := NormalizeWorkspaceTarget(sub)
	if err != nil {
		t.Fatalf("NormalizeWorkspaceTarget(sub): %v", err)
	}
	if !MatchesTarget(wsRoot, wsSub) {
		t.Fatalf("expected workspace root %q to match subdir %q", wsRoot, wsSub)
	}
	if MatchesTarget(wsSub, wsRoot) {
		t.Fatalf("expected subdir target %q not to match parent %q", wsSub, wsRoot)
	}
}

func TestStoreEnqueueListDrainClear(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(filepath.Join(tmp, "steer.jsonl"))
	worktree := filepath.Join(tmp, "wt-feat")
	if err := os.MkdirAll(filepath.Join(worktree, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	m1, err := store.Enqueue(Message{
		Target:   worktree,
		From:     "antigravity:17f5151f",
		Severity: SeverityBlocking,
		Body:     "Use /usr/sbin/ioreg instead of bare ioreg",
	})
	if err != nil {
		t.Fatalf("Enqueue m1: %v", err)
	}
	m2, err := store.Enqueue(Message{
		Target: "qoder:e09937bb",
		Body:   "Add unit test for empty PATH",
	})
	if err != nil {
		t.Fatalf("Enqueue m2: %v", err)
	}

	pending, err := store.List(nil, false)
	if err != nil || len(pending) != 2 {
		t.Fatalf("List pending = %d, %v; want 2", len(pending), err)
	}

	// Drain from subdirectory + full Qoder UUID matches both atomically.
	drained, err := store.Drain([]string{
		"ws:" + filepath.Join(worktree, "sub"),
		"qoder:e09937bb-edb1-4621-91a3-03d9b17bb023",
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(drained) != 2 || drained[0].ID != m1.ID || drained[1].ID != m2.ID {
		t.Fatalf("unexpected drained messages: %+v", drained)
	}

	// Second drain must return empty (no double delivery).
	drainedAgain, err := store.Drain([]string{"ws:" + worktree, "qoder:e09937bb"})
	if err != nil || len(drainedAgain) != 0 {
		t.Fatalf("second Drain = %d, %v; want 0", len(drainedAgain), err)
	}

	// Audit history still retains consumed messages when includeAll=true.
	all, err := store.List(nil, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("List includeAll = %d, %v; want 2", len(all), err)
	}
	if all[0].Status != StatusConsumed || all[0].ConsumedAt == 0 {
		t.Fatalf("expected consumed status + timestamp, got %+v", all[0])
	}

	// Enqueue one more and clear by ID prefix.
	m3, err := store.Enqueue(Message{Target: "claude:abc12345", Body: "Cancel me"})
	if err != nil {
		t.Fatalf("Enqueue m3: %v", err)
	}
	cleared, err := store.Clear(nil, m3.ID[:8])
	if err != nil || cleared != 1 {
		t.Fatalf("Clear = %d, %v; want 1", cleared, err)
	}
	pendingAfter, err := store.List(nil, false)
	if err != nil || len(pendingAfter) != 0 {
		t.Fatalf("pending after clear = %d, %v; want 0", len(pendingAfter), err)
	}
}

func TestStoreConcurrentDrainSingleDelivery(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(filepath.Join(tmp, "steer.jsonl"))

	for i := 0; i < 5; i++ {
		if _, err := store.Enqueue(Message{Target: "qoder:sess-1", Body: "fix issue"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs, err := store.Drain([]string{"qoder:sess-1"})
			if err != nil {
				t.Errorf("concurrent Drain error: %v", err)
				return
			}
			mu.Lock()
			total += len(msgs)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if total != 5 {
		t.Fatalf("total drained across concurrent hooks = %d, want exactly 5", total)
	}
}
