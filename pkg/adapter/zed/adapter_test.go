package zed

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"

	"github.com/kamusis/relay/pkg/adapter"
)

type testThreadSpec struct {
	ID          string
	Summary     string
	UpdatedAt   string
	ParentID    *string
	FolderPaths *string
	DataType    string // defaults to "zstd"
	PayloadJSON string
}

func strPtr(s string) *string { return &s }

func compressZstd(t *testing.T, raw []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	defer enc.Close()
	return enc.EncodeAll(raw, nil)
}

func createTestThreadsDB(t *testing.T, specs []testThreadSpec) string {
	t.Helper()
	// Include spaces in the parent directory name to mirror macOS "Application Support" URI encoding.
	dir := filepath.Join(t.TempDir(), "Application Support", "Zed", "threads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	dbPath := filepath.Join(dir, "threads.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	schema := `CREATE TABLE threads (
		id TEXT PRIMARY KEY,
		summary TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		data_type TEXT NOT NULL,
		data BLOB NOT NULL,
		parent_id TEXT,
		folder_paths TEXT,
		folder_paths_order TEXT,
		created_at TEXT
	);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	for _, s := range specs {
		dtype := s.DataType
		if dtype == "" {
			dtype = "zstd"
		}
		var blob []byte
		if dtype == "zstd" {
			blob = compressZstd(t, []byte(s.PayloadJSON))
		} else {
			blob = []byte(s.PayloadJSON)
		}
		if _, err := db.Exec(
			`INSERT INTO threads (id, summary, updated_at, data_type, data, parent_id, folder_paths)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			s.ID, s.Summary, s.UpdatedAt, dtype, blob, s.ParentID, s.FolderPaths,
		); err != nil {
			t.Fatalf("insert thread %s: %v", s.ID, err)
		}
	}

	return dbPath
}

func TestListSessions_FilterSortAndFallbacks(t *testing.T) {
	dbPath := createTestThreadsDB(t, []testThreadSpec{
		{
			ID:          "8878e5d3-2ef9-42b2-8180-da81d19bd299",
			Summary:     "Checking mopheus repo",
			UpdatedAt:   "2026-11-26T16:30:10.200365+00:00",
			FolderPaths: strPtr("/workspace/mopheus"),
			PayloadJSON: `{"version":"0.3.0","title":"Checking mopheus repo","messages":[]}`,
		},
		{
			ID:          "1aa793e7-8091-4e94-86e8-1e12a55eb968",
			Summary:     "Subdir worktree thread",
			UpdatedAt:   "2026-11-25T10:00:00Z",
			FolderPaths: strPtr("/workspace/other\n/workspace/mopheus/.worktrees/feat-a"),
			PayloadJSON: `{"version":"0.3.0","title":"Subdir worktree thread","messages":[]}`,
		},
		{
			ID:          "subagent-0000-0000-0000-000000000001",
			Summary:     "Hidden subagent child",
			UpdatedAt:   "2026-11-26T17:00:00Z",
			ParentID:    strPtr("8878e5d3-2ef9-42b2-8180-da81d19bd299"),
			FolderPaths: strPtr("/workspace/mopheus"),
			PayloadJSON: `{"version":"0.3.0","title":"Hidden subagent child","messages":[]}`,
		},
		{
			ID:          "6d6d628a-0a1b-41a0-8298-b94ff23e91ec",
			Summary:     "",
			UpdatedAt:   "2026-01-03T06:24:52.100580+00:00",
			FolderPaths: nil, // Legacy thread without folder_paths column; must fall back to snapshot
			PayloadJSON: `{"version":"0.3.0","title":"Legacy Snapshot Title","initial_project_snapshot":{"worktree_snapshots":[{"worktree_path":"/workspace/legacy"}]},"messages":[]}`,
		},
		{
			ID:          "00000000-1111-2222-3333-444444444444",
			Summary:     "Global Scratch Thread",
			UpdatedAt:   "2026-01-01T00:00:00Z",
			FolderPaths: nil,
			PayloadJSON: `{"version":"0.3.0","title":"Global Scratch Thread","messages":[]}`,
		},
	})

	ad := New(Config{DBPath: dbPath})

	// 1. WorkspaceAll excludes subagent child and preserves updated_at DESC order
	all, err := ad.ListSessions(adapter.WorkspaceAll, 10)
	if err != nil {
		t.Fatalf("ListSessions(all) error: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 top-level threads, got %d: %+v", len(all), all)
	}
	if all[0].ID != "8878e5d3-2ef9-42b2-8180-da81d19bd299" || all[0].Agent != adapter.AgentZed {
		t.Errorf("unexpected first session: %+v", all[0])
	}
	if all[0].UpdatedAt == 0 {
		t.Errorf("expected non-zero UpdatedAt timestamp")
	}

	// 2. Workspace filter matches exact path and nested worktree in multi-folder thread
	moph, err := ad.ListSessions("/workspace/mopheus", 10)
	if err != nil {
		t.Fatalf("ListSessions(/workspace/mopheus) error: %v", err)
	}
	if len(moph) != 2 {
		t.Fatalf("expected 2 mopheus threads, got %d: %+v", len(moph), moph)
	}
	if moph[1].Workspace != "/workspace/mopheus/.worktrees/feat-a" {
		t.Errorf("expected matched subdir workspace, got %q", moph[1].Workspace)
	}

	// 3. Legacy thread with NULL folder_paths matches via initial_project_snapshot and falls back to JSON title
	legacy, err := ad.ListSessions("/workspace/legacy", 10)
	if err != nil {
		t.Fatalf("ListSessions(/workspace/legacy) error: %v", err)
	}
	if len(legacy) != 1 || legacy[0].Title != "Legacy Snapshot Title" || legacy[0].Workspace != "/workspace/legacy" {
		t.Fatalf("unexpected legacy session result: %+v", legacy)
	}

	// 4. WorkspaceNone returns only scratch threads without workspace
	scratch, err := ad.ListSessions(adapter.WorkspaceNone, 10)
	if err != nil {
		t.Fatalf("ListSessions(WorkspaceNone) error: %v", err)
	}
	if len(scratch) != 1 || scratch[0].ID != "00000000-1111-2222-3333-444444444444" {
		t.Fatalf("unexpected scratch sessions: %+v", scratch)
	}

	// 5. Missing database gracefully returns empty slice without error
	missingAd := New(Config{DBPath: ":none:"})
	empty, err := missingAd.ListSessions(adapter.WorkspaceAll, 10)
	if err != nil || len(empty) != 0 {
		t.Fatalf("expected nil, nil for missing db; got %+v, err=%v", empty, err)
	}
}

func TestGetSession_ExactPrefixAndAmbiguous(t *testing.T) {
	dbPath := createTestThreadsDB(t, []testThreadSpec{
		{
			ID:          "aaaa1111-0000-0000-0000-000000000001",
			Summary:     "First",
			UpdatedAt:   "2026-11-26T16:00:00Z",
			FolderPaths: strPtr("/workspace/app"),
			PayloadJSON: `{"version":"0.3.0","messages":[]}`,
		},
		{
			ID:          "aaaa2222-0000-0000-0000-000000000002",
			Summary:     "Second",
			UpdatedAt:   "2026-11-26T15:00:00Z",
			FolderPaths: strPtr("/workspace/app"),
			PayloadJSON: `{"version":"0.3.0","messages":[]}`,
		},
	})

	ad := New(Config{DBPath: dbPath})

	// Exact match
	s, err := ad.GetSession("aaaa1111-0000-0000-0000-000000000001")
	if err != nil || s.Title != "First" {
		t.Fatalf("exact GetSession failed: s=%+v, err=%v", s, err)
	}

	// Unique short prefix
	s, err = ad.GetSession("aaaa2222")
	if err != nil || s.ID != "aaaa2222-0000-0000-0000-000000000002" {
		t.Fatalf("prefix GetSession failed: s=%+v, err=%v", s, err)
	}

	// Ambiguous prefix
	_, err = ad.GetSession("aaaa")
	if !errors.Is(err, adapter.ErrAmbiguousSession) {
		t.Fatalf("expected ErrAmbiguousSession, got %v", err)
	}

	// Wildcard characters in session ID must not match arbitrarily
	_, err = ad.GetSession("%")
	if !errors.Is(err, adapter.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound for wildcard input, got %v", err)
	}

	// Missing session
	_, err = ad.GetSession("deadbeef")
	if !errors.Is(err, adapter.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestGetLatestAssistantOutput_TurnStatesAndFormats(t *testing.T) {
	dbPath := createTestThreadsDB(t, []testThreadSpec{
		{
			ID:        "completed-zstd-0001",
			Summary:   "Completed with Thinking and Resume",
			UpdatedAt: "2026-11-26T16:30:00Z",
			PayloadJSON: `{
				"version": "0.3.0",
				"messages": [
					{"User": {"id": "u1", "content": [{"Text": "Review this"}]}},
					{"Agent": {"content": [{"ToolUse": {"id": "call_1", "name": "terminal", "is_input_complete": true}}], "tool_results": {"call_1": {"tool_use_id": "call_1"}}}},
					"Resume",
					{"Agent": {"content": [{"Thinking": {"text": "internal thought"}}, {"Text": "Part 1.\n"}, {"Text": "Part 2."}], "tool_results": {}}}
				]
			}`,
		},
		{
			ID:        "completed-plain-json",
			Summary:   "Plain JSON payload",
			UpdatedAt: "2026-11-26T16:20:00Z",
			DataType:  "json",
			PayloadJSON: `{
				"version": "0.3.0",
				"messages": [
					{"Agent": {"content": [{"Text": "Plain JSON response"}], "tool_results": {}}}
				]
			}`,
		},
		{
			ID:        "busy-user-last",
			Summary:   "Waiting for agent",
			UpdatedAt: "2026-11-26T16:10:00Z",
			PayloadJSON: `{
				"version": "0.3.0",
				"messages": [
					{"Agent": {"content": [{"Text": "Previous turn"}], "tool_results": {}}},
					{"User": {"id": "u2", "content": [{"Text": "Next question"}]}}
				]
			}`,
		},
		{
			ID:        "busy-tool-pending",
			Summary:   "Pending tool result",
			UpdatedAt: "2026-11-26T16:05:00Z",
			PayloadJSON: `{
				"version": "0.3.0",
				"messages": [
					{"Agent": {"content": [{"Text": "Running command..."}, {"ToolUse": {"id": "call_99", "name": "terminal", "is_input_complete": true}}], "tool_results": {}}}
				]
			}`,
		},
		{
			ID:        "busy-tool-only",
			Summary:   "Tool resolved without final text",
			UpdatedAt: "2026-11-26T16:00:00Z",
			PayloadJSON: `{
				"version": "0.3.0",
				"messages": [
					{"Agent": {"content": [{"ToolUse": {"id": "call_77", "name": "read_file", "is_input_complete": true}}], "tool_results": {"call_77": {"tool_use_id": "call_77"}}}}
				]
			}`,
		},
		{
			ID:        "busy-preamble-with-resolved-tool",
			Summary:   "Preamble text paired with resolved tool call",
			UpdatedAt: "2026-11-26T15:55:00Z",
			PayloadJSON: `{
				"version": "0.3.0",
				"messages": [
					{"Agent": {"content": [{"Text": "Let me inspect the repository first."}, {"ToolUse": {"id": "call_88", "name": "terminal", "is_input_complete": true}}], "tool_results": {"call_88": {"tool_use_id": "call_88"}}}}
				]
			}`,
		},
		{
			ID:          "empty-thread",
			Summary:     "Empty",
			UpdatedAt:   "2026-11-26T15:00:00Z",
			PayloadJSON: `{"version": "0.3.0", "messages": []}`,
		},
	})

	ad := New(Config{DBPath: dbPath})

	// 1. Completed zstd turn strips Thinking, skips Resume, and joins Text blocks
	out, err := ad.GetLatestAssistantOutput("completed-zstd-0001")
	if err != nil {
		t.Fatalf("completed-zstd-0001 error: %v", err)
	}
	if out != "Part 1.\nPart 2." {
		t.Errorf("unexpected output: %q", out)
	}

	// 2. Plain JSON data_type compatibility
	out, err = ad.GetLatestAssistantOutput("completed-plain-json")
	if err != nil || out != "Plain JSON response" {
		t.Fatalf("completed-plain-json failed: out=%q, err=%v", out, err)
	}

	// 3. Trailing User turn -> ErrTurnInProgress
	if _, err := ad.GetLatestAssistantOutput("busy-user-last"); !errors.Is(err, adapter.ErrTurnInProgress) {
		t.Errorf("expected ErrTurnInProgress for trailing user message, got %v", err)
	}

	// 4. Missing tool_results entry -> ErrTurnInProgress
	if _, err := ad.GetLatestAssistantOutput("busy-tool-pending"); !errors.Is(err, adapter.ErrTurnInProgress) {
		t.Errorf("expected ErrTurnInProgress for pending tool call, got %v", err)
	}

	// 5. Trailing Agent with ToolUse only (awaiting final assistant text) -> ErrTurnInProgress
	if _, err := ad.GetLatestAssistantOutput("busy-tool-only"); !errors.Is(err, adapter.ErrTurnInProgress) {
		t.Errorf("expected ErrTurnInProgress for tool-only trailing agent turn, got %v", err)
	}

	// 6. Trailing Agent with preamble text + resolved ToolUse -> ErrTurnInProgress (must not leak preamble)
	if out, err := ad.GetLatestAssistantOutput("busy-preamble-with-resolved-tool"); !errors.Is(err, adapter.ErrTurnInProgress) {
		t.Errorf("expected ErrTurnInProgress for preamble+tool trailing agent turn, got out=%q err=%v", out, err)
	}

	// 7. Empty thread -> ErrNoOutputFound
	if _, err := ad.GetLatestAssistantOutput("empty-thread"); !errors.Is(err, adapter.ErrNoOutputFound) {
		t.Errorf("expected ErrNoOutputFound for empty thread, got %v", err)
	}
}

func TestDeliver_GuidesToRelayPull(t *testing.T) {
	ad := New(Config{DBPath: ":none:"})
	err := ad.Deliver(context.Background(), adapter.Transmission{
		LinkName:  "review-mopheus",
		FromAgent: adapter.AgentQoder,
		ToAgent:   adapter.AgentZed,
	})
	if err == nil {
		t.Fatalf("expected error from zed Deliver, got nil")
	}
	if !strings.Contains(err.Error(), "relay pull review-mopheus --from qoder") {
		t.Errorf("expected actionable relay pull guidance, got: %v", err)
	}
}
