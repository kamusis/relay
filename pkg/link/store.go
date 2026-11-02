package link

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/kamusis/relay/pkg/adapter"
)

var (
	// ErrLinkNotFound is returned when a requested link name does not exist.
	ErrLinkNotFound = errors.New("link not found")
	// ErrLinkAlreadyExists is returned when attempting to create a link with an existing name.
	ErrLinkAlreadyExists = errors.New("link already exists")
)

// Store manages persistence of named links in ~/.config/relay/links.json.
type Store struct {
	path string
	mu   sync.Mutex
}

// DefaultPath returns the default absolute path to ~/.config/relay/links.json.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to determine user home directory: %w", err)
	}
	return filepath.Join(home, ".config", "relay", "links.json"), nil
}

// NewStore creates a Store with a specific file path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// NewDefaultStore creates a Store pointing to the default configuration location.
func NewDefaultStore() (*Store, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return NewStore(path), nil
}

// withFileLock executes fn while holding both in-process mutex and cross-process file lock on s.path + ".lock".
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
		return fmt.Errorf("failed to open lock file %s: %w", lockPath, err)
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return fmt.Errorf("failed to acquire file lock on %s: %w", lockPath, err)
	}
	defer unlockFile(f)

	return fn()
}

// readLinksLocked reads and unmarshals links from disk. Caller must hold file lock.
func (s *Store) readLinksLocked() (map[string]adapter.Link, error) {
	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		return make(map[string]adapter.Link), nil
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("failed to read links file %s: %w", s.path, err)
	}

	if len(data) == 0 {
		return make(map[string]adapter.Link), nil
	}

	var links map[string]adapter.Link
	if err := json.Unmarshal(data, &links); err != nil {
		return nil, fmt.Errorf("failed to parse links JSON from %s: %w", s.path, err)
	}
	return links, nil
}

// writeLinksLocked writes links to disk atomically via a uniquely named temporary file. Caller must hold file lock.
func (s *Store) writeLinksLocked(links map[string]adapter.Link) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(links, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal links: %w", err)
	}

	tmpFile := fmt.Sprintf("%s.%d.%d.tmp", s.path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write temporary links file: %w", err)
	}

	if err := os.Rename(tmpFile, s.path); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to replace links file atomically: %w", err)
	}
	return nil
}

// Create persists a new named link. Returns ErrLinkAlreadyExists if the name is taken.
func (s *Store) Create(l adapter.Link) error {
	return s.withFileLock(func() error {
		links, err := s.readLinksLocked()
		if err != nil {
			return err
		}

		if _, exists := links[l.Name]; exists {
			return fmt.Errorf("%w: %s", ErrLinkAlreadyExists, l.Name)
		}

		now := time.Now().Unix()
		if l.CreatedAt == 0 {
			l.CreatedAt = now
		}
		l.UpdatedAt = now

		links[l.Name] = l
		return s.writeLinksLocked(links)
	})
}

// Get retrieves a link by its name. Returns ErrLinkNotFound if absent.
func (s *Store) Get(name string) (*adapter.Link, error) {
	var targetLink *adapter.Link
	err := s.withFileLock(func() error {
		links, err := s.readLinksLocked()
		if err != nil {
			return err
		}

		l, ok := links[name]
		if !ok {
			return fmt.Errorf("%w: %s", ErrLinkNotFound, name)
		}
		targetLink = &l
		return nil
	})
	if err != nil {
		return nil, err
	}
	return targetLink, nil
}

// Update updates an existing link in place (e.g. for self-healing metadata).
func (s *Store) Update(l adapter.Link) error {
	return s.withFileLock(func() error {
		links, err := s.readLinksLocked()
		if err != nil {
			return err
		}

		if _, ok := links[l.Name]; !ok {
			return fmt.Errorf("%w: %s", ErrLinkNotFound, l.Name)
		}

		l.UpdatedAt = time.Now().Unix()
		links[l.Name] = l
		return s.writeLinksLocked(links)
	})
}

// Delete removes a link by name. Returns ErrLinkNotFound if absent.
func (s *Store) Delete(name string) error {
	return s.withFileLock(func() error {
		links, err := s.readLinksLocked()
		if err != nil {
			return err
		}

		if _, ok := links[name]; !ok {
			return fmt.Errorf("%w: %s", ErrLinkNotFound, name)
		}

		delete(links, name)
		return s.writeLinksLocked(links)
	})
}

// List returns all persisted links sorted by UpdatedAt descending.
func (s *Store) List() ([]adapter.Link, error) {
	var result []adapter.Link
	err := s.withFileLock(func() error {
		links, err := s.readLinksLocked()
		if err != nil {
			return err
		}

		result = make([]adapter.Link, 0, len(links))
		for _, l := range links {
			result = append(result, l)
		}

		sort.Slice(result, func(i, j int) bool {
			if result[i].UpdatedAt != result[j].UpdatedAt {
				return result[i].UpdatedAt > result[j].UpdatedAt
			}
			return result[i].Name < result[j].Name
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
