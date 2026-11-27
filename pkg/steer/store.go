package steer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kamusis/relay/pkg/adapter"
)

// Status tracks the lifecycle of a steering directive in the queue.
type Status string

const (
	// StatusPending indicates the directive is awaiting consumption by a worker hook.
	StatusPending Status = "pending"
	// StatusConsumed indicates the directive has been drained and injected into the worker.
	StatusConsumed Status = "consumed"
	// StatusCleared indicates the directive was manually cancelled before consumption.
	StatusCleared Status = "cleared"
)

// Severity controls how aggressively a PostToolUse hook surfaces a steering directive.
type Severity string

const (
	// SeverityAdvisory injects additionalContext without blocking tool flow.
	SeverityAdvisory Severity = "advisory"
	// SeverityBlocking additionally sets decision="block" in hook JSON output.
	SeverityBlocking Severity = "blocking"
)

const workspacePrefix = "ws:"

var (
	// ErrEmptyBody is returned when attempting to enqueue a blank steering message.
	ErrEmptyBody = errors.New("steer message body cannot be empty")
	// ErrEmptyTarget is returned when no target session or workspace is provided.
	ErrEmptyTarget = errors.New("steer target cannot be empty")
)

// Message represents a single queued or historical mid-turn steering directive.
type Message struct {
	ID         string   `json:"id"`
	Target     string   `json:"target"`
	From       string   `json:"from,omitempty"`
	Severity   Severity `json:"severity,omitempty"`
	Body       string   `json:"body"`
	Status     Status   `json:"status"`
	CreatedAt  int64    `json:"created_at"`
	ConsumedAt int64    `json:"consumed_at,omitempty"`
	ClearedAt  int64    `json:"cleared_at,omitempty"`
}

// Store manages cross-process locked persistence of steering directives in JSONL format.
type Store struct {
	path string
	mu   sync.Mutex
}

// DefaultPath returns the default absolute path ~/.config/relay/steer.jsonl.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to determine user home directory: %w", err)
	}
	return filepath.Join(home, ".config", "relay", "steer.jsonl"), nil
}

// NewStore creates a Store backed by an explicit file path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// NewDefaultStore creates a Store backed by the default ~/.config/relay/steer.jsonl path.
func NewDefaultStore() (*Store, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return NewStore(path), nil
}

// NormalizeTarget canonicalizes a target string into either "<agent>:<session_id>"
// or "ws:<canonical_abs_path>".
func NormalizeTarget(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrEmptyTarget
	}

	if strings.HasPrefix(trimmed, workspacePrefix) {
		p := strings.TrimSpace(strings.TrimPrefix(trimmed, workspacePrefix))
		if p == "" {
			return "", ErrEmptyTarget
		}
		return workspacePrefix + canonicalPath(p), nil
	}

	// Check for "<agent>:<session_id>" form without misclassifying Windows drive letters (e.g. "C:\foo").
	if idx := strings.IndexByte(trimmed, ':'); idx > 1 {
		prefix := trimmed[:idx]
		rest := strings.TrimSpace(trimmed[idx+1:])
		if agType, err := adapter.ParseAgentType(prefix); err == nil {
			if rest == "" {
				return "", fmt.Errorf("invalid session target '%s': session ID is empty", raw)
			}
			return fmt.Sprintf("%s:%s", agType, rest), nil
		}
	}

	return workspacePrefix + canonicalPath(trimmed), nil
}

// NormalizeWorkspaceTarget canonicalizes a filesystem directory into a "ws:<path>" target key.
func NormalizeWorkspaceTarget(dir string) (string, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return "", ErrEmptyTarget
	}
	return workspacePrefix + canonicalPath(trimmed), nil
}

// canonicalPath resolves ~, converts to an absolute path, and evaluates symlinks when present.
func canonicalPath(p string) string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if trimmed == "~" {
				trimmed = home
			} else {
				trimmed = filepath.Join(home, trimmed[2:])
			}
		}
	}
	abs, err := filepath.Abs(trimmed)
	if err == nil {
		trimmed = abs
	}
	if resolved, err := filepath.EvalSymlinks(trimmed); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(trimmed)
}

// MatchesTarget reports whether a stored steer target matches a query target.
// Session targets support prefix matching within the same agent type; workspace targets
// match the exact workspace directory or any subdirectory beneath it.
func MatchesTarget(storedTarget, queryTarget string) bool {
	if storedTarget == "" || queryTarget == "" {
		return false
	}
	if storedTarget == queryTarget {
		return true
	}

	// Workspace matching: stored ws:<root> matches query ws:<root>/subdir.
	if strings.HasPrefix(storedTarget, workspacePrefix) && strings.HasPrefix(queryTarget, workspacePrefix) {
		storedDir := filepath.Clean(strings.TrimPrefix(storedTarget, workspacePrefix))
		queryDir := filepath.Clean(strings.TrimPrefix(queryTarget, workspacePrefix))
		if storedDir == queryDir {
			return true
		}
		sep := string(os.PathSeparator)
		prefix := storedDir
		if !strings.HasSuffix(prefix, sep) {
			prefix += sep
		}
		return strings.HasPrefix(queryDir, prefix)
	}

	// Session matching: allow prefix match in either direction within the same agent type
	// (e.g. steering "qoder:e09937bb" matches hook drain with full UUID "qoder:e09937bb-...").
	sParts := strings.SplitN(storedTarget, ":", 2)
	qParts := strings.SplitN(queryTarget, ":", 2)
	if len(sParts) == 2 && len(qParts) == 2 && sParts[0] == qParts[0] && sParts[0] != "ws" {
		sID, qID := sParts[1], qParts[1]
		if sID != "" && qID != "" {
			return strings.HasPrefix(qID, sID) || strings.HasPrefix(sID, qID)
		}
	}

	return false
}

// withFileLock acquires both the in-process mutex and an exclusive cross-process file lock.
func (s *Store) withFileLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	lockPath := s.path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open steer lock file %s: %w", lockPath, err)
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return fmt.Errorf("failed to acquire file lock on %s: %w", lockPath, err)
	}
	defer unlockFile(f)

	return fn()
}

// readLocked reads all messages from the JSONL file. Caller must hold the file lock.
func (s *Store) readLocked() ([]Message, error) {
	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		return nil, nil
	}

	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("failed to open steer queue %s: %w", s.path, err)
	}
	defer f.Close()

	var msgs []Message
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 4*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var m Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue // skip corrupted line
		}
		msgs = append(msgs, m)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan steer queue: %w", err)
	}
	return msgs, nil
}

// writeLocked atomically replaces the JSONL file with msgs. Caller must hold the file lock.
func (s *Store) writeLocked(msgs []Message) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	var sb strings.Builder
	for _, m := range msgs {
		data, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("failed to marshal steer message: %w", err)
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}

	tmpFile := fmt.Sprintf("%s.%d.%d.tmp", s.path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, []byte(sb.String()), 0o600); err != nil {
		return fmt.Errorf("failed to write temporary steer queue: %w", err)
	}
	if err := os.Rename(tmpFile, s.path); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to atomically replace steer queue: %w", err)
	}
	return nil
}

// Enqueue validates and persists a new pending steering directive.
func (s *Store) Enqueue(msg Message) (*Message, error) {
	body := strings.TrimSpace(msg.Body)
	if body == "" {
		return nil, ErrEmptyBody
	}
	normTarget, err := NormalizeTarget(msg.Target)
	if err != nil {
		return nil, err
	}
	sev := msg.Severity
	if sev == "" {
		sev = SeverityAdvisory
	}
	if sev != SeverityAdvisory && sev != SeverityBlocking {
		return nil, fmt.Errorf("invalid severity '%s': expected 'advisory' or 'blocking'", sev)
	}

	created := Message{
		ID:        uuid.NewString(),
		Target:    normTarget,
		From:      strings.TrimSpace(msg.From),
		Severity:  sev,
		Body:      body,
		Status:    StatusPending,
		CreatedAt: time.Now().Unix(),
	}
	if msg.ID != "" {
		created.ID = msg.ID
	}
	if msg.CreatedAt > 0 {
		created.CreatedAt = msg.CreatedAt
	}

	err = s.withFileLock(func() error {
		msgs, err := s.readLocked()
		if err != nil {
			return err
		}
		msgs = append(msgs, created)
		return s.writeLocked(msgs)
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// List returns steering messages matching any of queryTargets (or all targets when empty).
// By default only pending messages are returned; set includeAll to include consumed and cleared items.
func (s *Store) List(queryTargets []string, includeAll bool) ([]Message, error) {
	var normalized []string
	for _, q := range queryTargets {
		if strings.TrimSpace(q) == "" {
			continue
		}
		n, err := NormalizeTarget(q)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, n)
	}

	var out []Message
	err := s.withFileLock(func() error {
		msgs, err := s.readLocked()
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if !includeAll && m.Status != StatusPending {
				continue
			}
			if len(normalized) > 0 && !matchesAny(m.Target, normalized) {
				continue
			}
			out = append(out, m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Drain atomically marks all pending messages matching any of queryTargets as consumed
// and returns them in FIFO order. Returns nil when no pending messages match.
func (s *Store) Drain(queryTargets []string) ([]Message, error) {
	var normalized []string
	for _, q := range queryTargets {
		if strings.TrimSpace(q) == "" {
			continue
		}
		n, err := NormalizeTarget(q)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, n)
	}
	if len(normalized) == 0 {
		return nil, nil
	}

	var drained []Message
	err := s.withFileLock(func() error {
		msgs, err := s.readLocked()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return nil
		}

		now := time.Now().Unix()
		changed := false
		for i := range msgs {
			if msgs[i].Status != StatusPending {
				continue
			}
			if !matchesAny(msgs[i].Target, normalized) {
				continue
			}
			msgs[i].Status = StatusConsumed
			msgs[i].ConsumedAt = now
			drained = append(drained, msgs[i])
			changed = true
		}
		if !changed {
			return nil
		}
		return s.writeLocked(msgs)
	})
	if err != nil {
		return nil, err
	}
	return drained, nil
}

// Clear marks matching pending messages as cleared and returns the number of cleared entries.
// When idPrefix is non-empty, only pending messages matching that ID prefix are cleared.
func (s *Store) Clear(queryTargets []string, idPrefix string) (int, error) {
	var normalized []string
	for _, q := range queryTargets {
		if strings.TrimSpace(q) == "" {
			continue
		}
		n, err := NormalizeTarget(q)
		if err != nil {
			return 0, err
		}
		normalized = append(normalized, n)
	}
	idPrefix = strings.TrimSpace(idPrefix)

	cleared := 0
	err := s.withFileLock(func() error {
		msgs, err := s.readLocked()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return nil
		}

		now := time.Now().Unix()
		for i := range msgs {
			if msgs[i].Status != StatusPending {
				continue
			}
			if idPrefix != "" && !strings.HasPrefix(msgs[i].ID, idPrefix) {
				continue
			}
			if len(normalized) > 0 && !matchesAny(msgs[i].Target, normalized) {
				continue
			}
			msgs[i].Status = StatusCleared
			msgs[i].ClearedAt = now
			cleared++
		}
		if cleared == 0 {
			return nil
		}
		return s.writeLocked(msgs)
	})
	return cleared, err
}

func matchesAny(stored string, queries []string) bool {
	for _, q := range queries {
		if MatchesTarget(stored, q) {
			return true
		}
	}
	return false
}
