package zed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kamusis/relay/pkg/adapter"
)

// Config configures the Zed native Agent adapter paths.
type Config struct {
	// DBPath optionally overrides Zed's threads.db location.
	// Use ":none:" to disable database discovery in tests.
	DBPath string
}

// Adapter implements adapter.Adapter for Zed's native AI Agent Panel.
type Adapter struct {
	dbPath string
}

// New creates a new Zed native Agent Adapter.
func New(cfg ...Config) *Adapter {
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}
	return &Adapter{
		dbPath: c.DBPath,
	}
}

// Type returns the unique identifier for Zed's native Agent.
func (a *Adapter) Type() adapter.AgentType {
	return adapter.AgentZed
}

type threadRow struct {
	ID          string
	Summary     string
	UpdatedAt   string
	ParentID    sql.NullString
	FolderPaths sql.NullString
	DataType    string
	Data        []byte
}

// threadPayload models the relevant fields of Zed's SerializedThread JSON (v0.3.0+).
type threadPayload struct {
	Version                string            `json:"version"`
	Title                  string            `json:"title"`
	Messages               []json.RawMessage `json:"messages"`
	InitialProjectSnapshot *struct {
		WorktreeSnapshots []struct {
			WorktreePath string `json:"worktree_path"`
		} `json:"worktree_snapshots"`
	} `json:"initial_project_snapshot"`
}

// agentMessage models the "Agent" variant of Zed's SerializedMessage enum.
type agentMessage struct {
	Content     []json.RawMessage          `json:"content"`
	ToolResults map[string]json.RawMessage `json:"tool_results"`
}

// parseUpdatedAt parses Zed's RFC3339Nano updated_at timestamps into Unix seconds.
func parseUpdatedAt(raw string) int64 {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// parseFolderPaths splits Zed's serialized folder_paths column into cleaned workspace paths.
func parseFolderPaths(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var paths []string
	seen := make(map[string]struct{})
	for _, line := range strings.Split(raw, "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		clean := filepath.Clean(p)
		if _, exists := seen[clean]; !exists {
			seen[clean] = struct{}{}
			paths = append(paths, clean)
		}
	}
	return paths
}

// decodePayload decompresses and unmarshals a threadRow's JSON payload.
func decodePayload(row *threadRow) (*threadPayload, error) {
	if len(row.Data) == 0 {
		return &threadPayload{}, nil
	}
	raw, err := decompressPayload(row.Data, row.DataType)
	if err != nil {
		return nil, err
	}
	var payload threadPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse zed thread payload JSON: %w", err)
	}
	return &payload, nil
}

// resolveRowMetadata extracts workspace paths and display title from a threadRow,
// decoding the compressed payload only when SQL columns lack folder_paths or summary.
func resolveRowMetadata(row *threadRow) (title string, workspaces []string) {
	title = strings.TrimSpace(row.Summary)
	workspaces = parseFolderPaths(row.FolderPaths.String)

	if (len(workspaces) == 0 || title == "") && len(row.Data) > 0 {
		if payload, err := decodePayload(row); err == nil {
			if title == "" {
				title = strings.TrimSpace(payload.Title)
			}
			if len(workspaces) == 0 && payload.InitialProjectSnapshot != nil {
				seen := make(map[string]struct{})
				for _, ws := range payload.InitialProjectSnapshot.WorktreeSnapshots {
					p := strings.TrimSpace(ws.WorktreePath)
					if p == "" {
						continue
					}
					clean := filepath.Clean(p)
					if _, exists := seen[clean]; !exists {
						seen[clean] = struct{}{}
						workspaces = append(workspaces, clean)
					}
				}
			}
		}
	}

	if title == "" {
		title = "Untitled Session"
	}
	return title, workspaces
}

// matchWorkspace checks whether a thread's workspaces satisfy the requested workspace filter
// and returns the representative workspace path to display.
func matchWorkspace(workspaces []string, filter string, wsMatcher adapter.WorkspaceMatcher) (string, bool) {
	primary := ""
	if len(workspaces) > 0 {
		primary = workspaces[0]
	}

	if filter == adapter.WorkspaceAll {
		return primary, true
	}
	if filter == adapter.WorkspaceNone {
		return "", len(workspaces) == 0
	}
	if len(workspaces) == 0 {
		return "", false
	}

	for _, ws := range workspaces {
		if wsMatcher.Matches(ws) {
			return ws, true
		}
	}
	return "", false
}

// escapeLikePattern escapes SQLite LIKE wildcard characters using backslash.
func escapeLikePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func scanThreadRow(scanner interface{ Scan(dest ...any) error }) (*threadRow, error) {
	var row threadRow
	if err := scanner.Scan(
		&row.ID,
		&row.Summary,
		&row.UpdatedAt,
		&row.ParentID,
		&row.FolderPaths,
		&row.DataType,
		&row.Data,
	); err != nil {
		return nil, err
	}
	return &row, nil
}

// locateThread resolves a full UUID or unambiguous prefix against Zed's threads table.
func locateThread(db *sql.DB, sessionID string) (*threadRow, error) {
	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		return nil, fmt.Errorf("%w: empty zed session ID", adapter.ErrSessionNotFound)
	}

	// 1. Exact ID lookup
	row, err := scanThreadRow(db.QueryRow(
		`SELECT id, summary, updated_at, parent_id, folder_paths, data_type, data
		 FROM threads
		 WHERE id = ?`,
		cleanID,
	))
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to query zed thread %s: %w", cleanID, err)
	}

	// 2. Prefix lookup
	rows, err := db.Query(
		`SELECT id, summary, updated_at, parent_id, folder_paths, data_type, data
		 FROM threads
		 WHERE id LIKE ? ESCAPE '\'
		 ORDER BY updated_at DESC`,
		escapeLikePattern(cleanID)+"%",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to search zed thread prefix %s: %w", cleanID, err)
	}
	defer rows.Close()

	var matches []*threadRow
	for rows.Next() {
		r, scanErr := scanThreadRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("failed to scan zed thread row: %w", scanErr)
		}
		matches = append(matches, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate zed thread rows: %w", err)
	}

	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, adapter.ShortID(m.ID))
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("%w: short ID prefix '%s' matches %d zed sessions: %s",
			adapter.ErrAmbiguousSession, cleanID, len(matches), strings.Join(ids, ", "))
	}

	return nil, fmt.Errorf("%w: zed session %s not found", adapter.ErrSessionNotFound, cleanID)
}

// GetSession verifies session existence in Zed's threads.db and returns fresh metadata.
func (a *Adapter) GetSession(sessionID string) (*adapter.Session, error) {
	var row *threadRow
	err := a.withReadOnlyDB(func(db *sql.DB) error {
		var locateErr error
		row, locateErr = locateThread(db, sessionID)
		return locateErr
	})
	if err != nil {
		if errors.Is(err, errDBNotFound) {
			return nil, fmt.Errorf("%w: zed threads.db not found", adapter.ErrSessionNotFound)
		}
		return nil, err
	}

	title, workspaces := resolveRowMetadata(row)
	primaryWS := ""
	if len(workspaces) > 0 {
		primaryWS = workspaces[0]
	}

	return &adapter.Session{
		ID:        row.ID,
		Agent:     adapter.AgentZed,
		Title:     title,
		Workspace: primaryWS,
		UpdatedAt: parseUpdatedAt(row.UpdatedAt),
	}, nil
}

// GetLatestAssistantOutput extracts the latest completed Agent response text from Zed's thread payload.
func (a *Adapter) GetLatestAssistantOutput(sessionID string) (string, error) {
	var row *threadRow
	err := a.withReadOnlyDB(func(db *sql.DB) error {
		var locateErr error
		row, locateErr = locateThread(db, sessionID)
		return locateErr
	})
	if err != nil {
		if errors.Is(err, errDBNotFound) {
			return "", fmt.Errorf("%w: zed threads.db not found", adapter.ErrSessionNotFound)
		}
		return "", err
	}

	payload, err := decodePayload(row)
	if err != nil {
		return "", err
	}

	// Inspect messages from newest to oldest, ignoring non-object markers such as "Resume".
	for i := len(payload.Messages) - 1; i >= 0; i-- {
		raw := bytes.TrimSpace(payload.Messages[i])
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}

		var envelope struct {
			User  json.RawMessage `json:"User"`
			Agent *agentMessage   `json:"Agent"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			continue
		}

		if len(envelope.User) > 0 && string(bytes.TrimSpace(envelope.User)) != "null" {
			return "", fmt.Errorf("%w: latest zed message is from user (awaiting agent completion)", adapter.ErrTurnInProgress)
		}

		if envelope.Agent != nil {
			return extractAgentTurnText(envelope.Agent, row.ID)
		}
	}

	return "", fmt.Errorf("%w for zed session %s", adapter.ErrNoOutputFound, row.ID)
}

// extractAgentTurnText inspects the latest Agent message, enforcing turn-in-progress guards
// for unfinished tool calls and returning concatenated Text blocks (excluding Thinking).
func extractAgentTurnText(msg *agentMessage, sessionID string) (string, error) {
	var textParts []string
	var hasToolUse bool
	var hasPendingToolUse bool

	for _, rawItem := range msg.Content {
		trimmed := bytes.TrimSpace(rawItem)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}

		var item struct {
			Text    *string `json:"Text"`
			ToolUse *struct {
				ID              string `json:"id"`
				IsInputComplete *bool  `json:"is_input_complete"`
			} `json:"ToolUse"`
		}
		if err := json.Unmarshal(trimmed, &item); err != nil {
			continue
		}

		if item.Text != nil && *item.Text != "" {
			textParts = append(textParts, *item.Text)
		}
		if item.ToolUse != nil {
			hasToolUse = true
			if item.ToolUse.IsInputComplete != nil && !*item.ToolUse.IsInputComplete {
				hasPendingToolUse = true
			}
			if item.ToolUse.ID != "" {
				if _, ok := msg.ToolResults[item.ToolUse.ID]; !ok {
					hasPendingToolUse = true
				}
			}
		}
	}

	if hasPendingToolUse {
		return "", fmt.Errorf("%w: latest zed agent turn in session %s has unfinished tool call",
			adapter.ErrTurnInProgress, adapter.ShortID(sessionID))
	}

	// Any trailing Agent message containing a ToolUse is an intermediate tool-calling step
	// (even when tool_results are populated and preamble Text is present); final responses
	// arrive in a subsequent Agent message with no ToolUse blocks.
	if hasToolUse {
		return "", fmt.Errorf("%w: latest zed agent turn in session %s ended with tool calls (awaiting final assistant text)",
			adapter.ErrTurnInProgress, adapter.ShortID(sessionID))
	}

	joined := strings.TrimSpace(strings.Join(textParts, ""))
	if joined != "" {
		return joined, nil
	}

	return "", fmt.Errorf("%w: latest zed agent turn in session %s has no text output",
		adapter.ErrNoOutputFound, adapter.ShortID(sessionID))
}

// Deliver refuses direct writes to live Zed native threads and directs callers to synchronous relay pull.
func (a *Adapter) Deliver(_ context.Context, t adapter.Transmission) error {
	linkName := strings.TrimSpace(t.LinkName)
	if linkName == "" {
		linkName = "<link>"
	}
	fromAgent := strings.TrimSpace(string(t.FromAgent))
	if fromAgent == "" {
		fromAgent = "<source-agent>"
	}
	return fmt.Errorf("zed native agent sessions do not support direct push delivery; run 'relay pull %s --from %s' from inside Zed instead", linkName, fromAgent)
}

// ListSessions discovers top-level Zed native Agent threads from threads.db,
// optionally filtered by workspace directory.
func (a *Adapter) ListSessions(workspace string, limit int) ([]adapter.Session, error) {
	if limit <= 0 {
		limit = 30
	}

	var wsMatcher adapter.WorkspaceMatcher
	if workspace != adapter.WorkspaceAll && workspace != adapter.WorkspaceNone {
		wsMatcher = adapter.NewWorkspaceMatcher(workspace)
	}

	var sessions []adapter.Session
	err := a.withReadOnlyDB(func(db *sql.DB) error {
		// Exclude subagent child threads (parent_id IS NOT NULL) from top-level listings.
		rows, err := db.Query(
			`SELECT id, summary, updated_at, parent_id, folder_paths, data_type, data
			 FROM threads
			 WHERE parent_id IS NULL
			 ORDER BY updated_at DESC`,
		)
		if err != nil {
			return fmt.Errorf("failed to query zed threads: %w", err)
		}
		defer rows.Close()

		// Accumulate into a local slice so lock-fallback retries remain strictly idempotent.
		var local []adapter.Session
		for rows.Next() {
			row, scanErr := scanThreadRow(rows)
			if scanErr != nil {
				continue
			}

			title, workspaces := resolveRowMetadata(row)
			ws, ok := matchWorkspace(workspaces, workspace, wsMatcher)
			if !ok {
				continue
			}

			local = append(local, adapter.Session{
				ID:        row.ID,
				Agent:     adapter.AgentZed,
				Title:     title,
				Workspace: ws,
				UpdatedAt: parseUpdatedAt(row.UpdatedAt),
			})
			if len(local) >= limit {
				break
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		sessions = local
		return nil
	})
	if err != nil {
		if errors.Is(err, errDBNotFound) {
			return nil, nil
		}
		return nil, err
	}

	return sessions, nil
}
