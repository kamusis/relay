package antigravity

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kamusis/relay/pkg/adapter"
	_ "modernc.org/sqlite"
)

// Config configures the Antigravity adapter paths.
type Config struct {
	BaseDir string // Defaults to ~/.gemini/antigravity
}

// Adapter implements the adapter.Adapter interface for Google Antigravity.
type Adapter struct {
	baseDir string
}

// New creates a new Antigravity Adapter with default or custom config.
func New(cfg ...Config) *Adapter {
	base := ""
	if len(cfg) > 0 && cfg[0].BaseDir != "" {
		base = cfg[0].BaseDir
	} else {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".gemini", "antigravity")
	}
	return &Adapter{baseDir: base}
}

// Type returns the unique identifier for Antigravity.
func (a *Adapter) Type() adapter.AgentType {
	return adapter.AgentAntigravity
}

func (a *Adapter) dbPath() string {
	return filepath.Join(a.baseDir, "conversation_summaries.db")
}

func (a *Adapter) agentAPIPath() string {
	return filepath.Join(a.baseDir, "bin", "agentapi")
}

func (a *Adapter) transcriptPath(sessionID string) string {
	return filepath.Join(a.baseDir, "brain", sessionID, ".system_generated", "logs", "transcript.jsonl")
}

func (a *Adapter) transcriptFullPath(sessionID string) string {
	return filepath.Join(a.baseDir, "brain", sessionID, ".system_generated", "logs", "transcript_full.jsonl")
}

// openDB opens the SQLite conversation summaries database in read-only mode.
func (a *Adapter) openDB() (*sql.DB, error) {
	path := a.dbPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("antigravity database not found at %s: %w", path, adapter.ErrSessionNotFound)
	}

	dsn := fmt.Sprintf("file:%s?mode=ro", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open antigravity database: %w", err)
	}
	return db, nil
}

// parseWorkspaceURI converts a JSON workspace_uris array into a clean local directory path.
func parseWorkspaceURI(raw string) string {
	if raw == "" {
		return ""
	}
	var uris []string
	if err := json.Unmarshal([]byte(raw), &uris); err == nil && len(uris) > 0 {
		clean := strings.TrimPrefix(uris[0], "file://")
		return filepath.Clean(clean)
	}
	// Fallback if not JSON array
	clean := strings.TrimPrefix(raw, "file://")
	return filepath.Clean(clean)
}

// parseTime parses SQLite datetime strings into unix timestamps.
func parseTime(raw string) int64 {
	layouts := []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, raw); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// GetSession queries the SQLite summaries table to confirm existence and fetch fresh metadata.
func (a *Adapter) GetSession(sessionID string) (*adapter.Session, error) {
	db, err := a.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	query := `
		SELECT conversation_id, title, workspace_uris, last_modified_time, not_fully_idle, killed
		FROM conversation_summaries
		WHERE conversation_id = ? OR conversation_id LIKE ?;
	`
	rows, err := db.Query(query, sessionID, sessionID+"%")
	if err != nil {
		return nil, fmt.Errorf("failed to query antigravity session: %w", err)
	}
	defer rows.Close()

	type sessionRow struct {
		id           string
		title        string
		wsRaw        string
		lastModRaw   string
		notFullyIdle int
		killed       int
	}

	var allMatches []sessionRow
	for rows.Next() {
		var r sessionRow
		if err := rows.Scan(&r.id, &r.title, &r.wsRaw, &r.lastModRaw, &r.notFullyIdle, &r.killed); err != nil {
			return nil, fmt.Errorf("failed to scan session row: %w", err)
		}
		allMatches = append(allMatches, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading session rows: %w", err)
	}

	if len(allMatches) == 0 {
		return nil, fmt.Errorf("%w: %s", adapter.ErrSessionNotFound, sessionID)
	}

	// 1. Exact match check
	for _, r := range allMatches {
		if r.id == sessionID {
			if r.killed != 0 {
				return nil, fmt.Errorf("%w: session %s was terminated/killed", adapter.ErrSessionNotFound, r.id)
			}
			return &adapter.Session{
				ID:        r.id,
				Agent:     adapter.AgentAntigravity,
				Title:     r.title,
				Workspace: parseWorkspaceURI(r.wsRaw),
				UpdatedAt: parseTime(r.lastModRaw),
			}, nil
		}
	}

	// 2. Prefix match: filter to active (killed == 0)
	var activeMatches []sessionRow
	for _, r := range allMatches {
		if r.killed == 0 {
			activeMatches = append(activeMatches, r)
		}
	}

	if len(activeMatches) == 0 {
		return nil, fmt.Errorf("%w: session matching prefix '%s' was terminated/killed", adapter.ErrSessionNotFound, sessionID)
	}

	if len(activeMatches) > 1 {
		var matchedIDs []string
		for _, m := range activeMatches {
			matchedIDs = append(matchedIDs, adapter.ShortID(m.id))
		}
		return nil, fmt.Errorf("%w: short ID prefix '%s' matches %d antigravity sessions: %s",
			adapter.ErrAmbiguousSession, sessionID, len(activeMatches), strings.Join(matchedIDs, ", "))
	}

	match := activeMatches[0]
	return &adapter.Session{
		ID:        match.id,
		Agent:     adapter.AgentAntigravity,
		Title:     match.title,
		Workspace: parseWorkspaceURI(match.wsRaw),
		UpdatedAt: parseTime(match.lastModRaw),
	}, nil
}

// transcriptRecord mirrors relevant fields from Antigravity's transcript.jsonl.
type transcriptRecord struct {
	StepIndex       int      `json:"step_index"`
	Source          string   `json:"source"`
	Type            string   `json:"type"`
	Status          string   `json:"status"`
	Content         string   `json:"content"`
	TruncatedFields []string `json:"truncated_fields"`
}

// GetLatestAssistantOutput extracts the latest assistant turn from transcript.jsonl.
func (a *Adapter) GetLatestAssistantOutput(sessionID string) (string, error) {
	// First check if turn is currently in progress via database
	sess, err := a.GetSession(sessionID)
	if err != nil {
		return "", err
	}

	// Verify idle status in DB
	db, err := a.openDB()
	if err == nil {
		var notFullyIdle int
		_ = db.QueryRow("SELECT not_fully_idle FROM conversation_summaries WHERE conversation_id = ?", sess.ID).Scan(&notFullyIdle)
		db.Close()
		if notFullyIdle == 1 {
			return "", adapter.ErrTurnInProgress
		}
	}

	path := a.transcriptPath(sess.ID)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("%w: transcript log not found at %s", adapter.ErrNoOutputFound, path)
	}
	if err != nil {
		return "", fmt.Errorf("failed to open transcript file: %w", err)
	}
	defer f.Close()

	var records []transcriptRecord
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r transcriptRecord
		if err := json.Unmarshal([]byte(line), &r); err == nil {
			records = append(records, r)
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("failed to read transcript file: %w", err)
	}

	// Scan backwards for the latest assistant planner response with content
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if r.Source == "MODEL" && r.Type == "PLANNER_RESPONSE" {
			// Check if content was truncated to transcript_full.jsonl
			isTruncated := false
			for _, tf := range r.TruncatedFields {
				if tf == "content" {
					isTruncated = true
					break
				}
			}

			if isTruncated {
				fullContent, err := a.readFullContent(sess.ID, r.StepIndex)
				if err == nil && fullContent != "" {
					return fullContent, nil
				}
			}

			if strings.TrimSpace(r.Content) != "" {
				return strings.TrimSpace(r.Content), nil
			}
		}
	}

	return "", fmt.Errorf("%w for session %s", adapter.ErrNoOutputFound, sess.ID)
}

// readFullContent reads the complete untruncated content from transcript_full.jsonl for a step.
func (a *Adapter) readFullContent(sessionID string, stepIndex int) (string, error) {
	fullPath := a.transcriptFullPath(sessionID)
	f, err := os.Open(fullPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 32*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r transcriptRecord
		if err := json.Unmarshal([]byte(line), &r); err == nil {
			if r.StepIndex == stepIndex && strings.TrimSpace(r.Content) != "" {
				return strings.TrimSpace(r.Content), nil
			}
		}
	}
	return "", fmt.Errorf("step %d not found in full transcript", stepIndex)
}

// Deliver formats and injects the transmission into Antigravity via agentapi send-message.
func (a *Adapter) Deliver(ctx context.Context, t adapter.Transmission) error {
	bin := a.agentAPIPath()
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("antigravity agentapi binary not found at %s: %w", bin, err)
	}

	addr, token, err := a.resolveLSConnection()
	if err != nil {
		return fmt.Errorf("cannot connect to antigravity: %w", err)
	}

	payload := t.FormatPayload()
	cmd := exec.CommandContext(ctx, bin, "send-message", t.ToSession, payload)

	// Ensure ANTIGRAVITY_LS_ADDRESS and ANTIGRAVITY_CSRF_TOKEN are present in child environment
	env := os.Environ()
	env = append(env, "ANTIGRAVITY_LS_ADDRESS="+addr)
	env = append(env, "ANTIGRAVITY_CSRF_TOKEN="+token)
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("agentapi send-message failed: %s: %w", string(out), err)
	}
	return nil
}

// ListSessions discovers recent Antigravity sessions from SQLite summaries.
func (a *Adapter) ListSessions(workspace string, limit int) ([]adapter.Session, error) {
	db, err := a.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if limit <= 0 {
		limit = 30
	}

	var query string
	if workspace == adapter.WorkspaceNone {
		query = `
			SELECT conversation_id, title, workspace_uris, last_modified_time
			FROM conversation_summaries
			WHERE killed = 0 AND (workspace_uris = '' OR workspace_uris = '[]')
			ORDER BY last_modified_time DESC
			LIMIT ?;
		`
	} else {
		query = `
			SELECT conversation_id, title, workspace_uris, last_modified_time
			FROM conversation_summaries
			WHERE killed = 0
			ORDER BY last_modified_time DESC
			LIMIT ?;
		`
	}

	rows, err := db.Query(query, limit*2) // fetch a bit more for workspace filtering
	if err != nil {
		return nil, fmt.Errorf("failed to query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []adapter.Session
	workspaceClean := filepath.Clean(workspace)

	for rows.Next() {
		var (
			id         string
			title      string
			wsRaw      string
			lastModRaw string
		)
		if err := rows.Scan(&id, &title, &wsRaw, &lastModRaw); err != nil {
			continue
		}

		ws := parseWorkspaceURI(wsRaw)
		if workspace == adapter.WorkspaceNone {
			if ws != "" {
				continue
			}
		} else if workspace != "" {
			if ws == "" {
				continue
			}
			isExact := strings.EqualFold(ws, workspaceClean)
			isSubdir := strings.HasPrefix(ws, workspaceClean+string(filepath.Separator))
			if !isExact && !isSubdir {
				continue
			}
		}

		sessions = append(sessions, adapter.Session{
			ID:        id,
			Agent:     adapter.AgentAntigravity,
			Title:     title,
			Workspace: ws,
			UpdatedAt: parseTime(lastModRaw),
		})

		if len(sessions) >= limit {
			break
		}
	}

	return sessions, nil
}
