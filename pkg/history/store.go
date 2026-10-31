package history

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

var (
	// ErrTransmissionNotFound is returned when a requested transmission UUID does not exist.
	ErrTransmissionNotFound = errors.New("transmission not found")
)

// Store manages appending and reading transmission audit logs in ~/.config/relay/history.jsonl.
type Store struct {
	path string
	mu   sync.Mutex
}

// DefaultPath returns the default absolute path to ~/.config/relay/history.jsonl.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to determine user home directory: %w", err)
	}
	return filepath.Join(home, ".config", "relay", "history.jsonl"), nil
}

// NewStore creates a Store with a specific file path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// NewDefaultStore creates a Store pointing to the default history location.
func NewDefaultStore() (*Store, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return NewStore(path), nil
}

// Append persists a new Transmission record into the JSONL audit file.
func (s *Store) Append(t *adapter.Transmission) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.Timestamp == 0 {
		t.Timestamp = time.Now().Unix()
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open history file: %w", err)
	}
	defer f.Close()

	line, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("failed to marshal transmission: %w", err)
	}

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("failed to write transmission to file: %w", err)
	}

	return nil
}

// List returns historical transmissions, optionally filtered by linkName, ordered latest first.
// If limit <= 0, all matching records are returned.
func (s *Store) List(linkName string, limit int) ([]adapter.Transmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		return nil, nil
	}

	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("failed to open history file: %w", err)
	}
	defer f.Close()

	var records []adapter.Transmission
	scanner := bufio.NewScanner(f)
	// Allow large tokens for lengthy assistant bodies (up to 16MB)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var t adapter.Transmission
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			continue // skip corrupted line
		}
		if linkName != "" && t.LinkName != linkName {
			continue
		}
		records = append(records, t)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan history file: %w", err)
	}

	// Reverse to get latest first
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}

	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}

	return records, nil
}

// Get finds a transmission by full ID or prefix.
func (s *Store) Get(id string) (*adapter.Transmission, error) {
	list, err := s.List("", 0)
	if err != nil {
		return nil, err
	}

	id = strings.TrimSpace(id)
	for _, t := range list {
		if t.ID == id || strings.HasPrefix(t.ID, id) {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrTransmissionNotFound, id)
}
