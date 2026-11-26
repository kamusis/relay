package zed

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"
)

var errDBNotFound = errors.New("zed threads database not found")

// maxDecodedPayloadBytes caps decompressed thread JSON size to guard against corrupted blobs.
const maxDecodedPayloadBytes = 64 << 20

// findThreadsDBPath locates Zed's threads.db across macOS, Linux, Windows, and WSL.
func (a *Adapter) findThreadsDBPath() string {
	if a.dbPath != "" {
		if a.dbPath == ":none:" {
			return ""
		}
		if _, err := os.Stat(a.dbPath); err == nil {
			return a.dbPath
		}
		return ""
	}

	if custom := strings.TrimSpace(os.Getenv("ZED_THREADS_DB_PATH")); custom != "" {
		if _, err := os.Stat(custom); err == nil {
			return custom
		}
		return ""
	}

	home, _ := os.UserHomeDir()
	var candidates []string

	switch runtime.GOOS {
	case "darwin":
		if home != "" {
			candidates = append(candidates, filepath.Join(home, "Library", "Application Support", "Zed", "threads", "threads.db"))
		}
	case "windows":
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			candidates = append(candidates, filepath.Join(localAppData, "Zed", "threads", "threads.db"))
		}
		if home != "" {
			candidates = append(candidates, filepath.Join(home, "AppData", "Local", "Zed", "threads", "threads.db"))
		}
	default:
		if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
			candidates = append(candidates, filepath.Join(xdg, "zed", "threads", "threads.db"))
		}
		if home != "" {
			candidates = append(candidates, filepath.Join(home, ".local", "share", "zed", "threads", "threads.db"))
		}
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// WSL fallback: search mounted Windows user profiles for native Zed threads.db.
	if runtime.GOOS == "linux" {
		wslPatterns := []string{
			"/mnt/c/Users/*/AppData/Local/Zed/threads/threads.db",
			"/mnt/*/Users/*/AppData/Local/Zed/threads/threads.db",
		}
		for _, pattern := range wslPatterns {
			matches, err := filepath.Glob(pattern)
			if err != nil {
				continue
			}
			for _, m := range matches {
				if _, err := os.Stat(m); err == nil {
					return m
				}
			}
		}
	}

	return ""
}

// sqliteReadOnlyDSN builds a properly percent-encoded SQLite read-only URI so paths
// containing spaces (e.g. "Application Support") open reliably across platforms.
func sqliteReadOnlyDSN(dbPath string) string {
	slashPath := filepath.ToSlash(dbPath)
	if len(slashPath) >= 2 && slashPath[1] == ':' {
		// Windows drive-letter URIs require a leading slash: file:///C:/...
		slashPath = "/" + slashPath
	}
	u := &url.URL{
		Scheme:   "file",
		Path:     slashPath,
		RawQuery: "mode=ro&_pragma=busy_timeout(1000)",
	}
	return u.String()
}

// isWSLDrvFS reports whether a path resides on a WSL Windows drive mount where
// direct SQLite file locking is unreliable.
func isWSLDrvFS(path string) bool {
	return runtime.GOOS == "linux" && strings.HasPrefix(filepath.ToSlash(path), "/mnt/")
}

// withReadOnlyDB opens Zed's threads.db in read-only mode and executes fn.
// On local filesystems it prefers direct read-only SQLite access with a busy timeout
// for transactional atomicity (avoiding torn raw file copies on rollback-journal DBs),
// falling back to a temporary snapshot copy on WSL drvfs or lock errors.
func (a *Adapter) withReadOnlyDB(fn func(db *sql.DB) error) error {
	dbPath := a.findThreadsDBPath()
	if dbPath == "" {
		return errDBNotFound
	}

	if !isWSLDrvFS(dbPath) {
		err := openAndRunReadOnly(dbPath, fn)
		if err == nil || !isSQLiteLockOrIOErr(err) {
			return err
		}
	}

	return openSnapshotAndRun(dbPath, fn)
}

func openAndRunReadOnly(dbPath string, fn func(db *sql.DB) error) error {
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(dbPath))
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(db)
}

func openSnapshotAndRun(dbPath string, fn func(db *sql.DB) error) error {
	tempDir, err := os.MkdirTemp("", "relay-zed-db-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	targetDB := filepath.Join(tempDir, "threads.db")
	if err := copyFile(dbPath, targetDB); err != nil {
		return err
	}
	// Copy optional journal/WAL sidecars when present so uncommitted pages remain consistent.
	_ = copyFile(dbPath+"-wal", targetDB+"-wal")
	_ = copyFile(dbPath+"-shm", targetDB+"-shm")
	_ = copyFile(dbPath+"-journal", targetDB+"-journal")

	return openAndRunReadOnly(targetDB, fn)
}

func isSQLiteLockOrIOErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "locked") ||
		strings.Contains(msg, "busy") ||
		strings.Contains(msg, "disk i/o") ||
		strings.Contains(msg, "unable to open")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// decompressPayload decodes a Zed threads.data blob according to its data_type column.
func decompressPayload(data []byte, dataType string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(dataType)) {
	case "zstd":
		dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecodedPayloadBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize zstd decoder: %w", err)
		}
		defer dec.Close()
		out, err := dec.DecodeAll(data, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to decompress zstd thread payload: %w", err)
		}
		return out, nil
	case "json", "":
		return data, nil
	default:
		return nil, fmt.Errorf("unsupported zed thread data_type %q", dataType)
	}
}
