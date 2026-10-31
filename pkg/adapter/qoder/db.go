package qoder

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type dbSessionInfo struct {
	SessionID string
	Title     string
	Cwd       string
	UpdatedAt int64
}

// findQoderDBPath searches common locations across Linux, macOS, and Windows/WSL for Qoder's main.sqlite database.
func (a *Adapter) findQoderDBPath() string {
	if a.dbPath != "" {
		if a.dbPath == ":none:" {
			return ""
		}
		if _, err := os.Stat(a.dbPath); err == nil {
			return a.dbPath
		}
	}
	if custom := os.Getenv("QODER_DB_PATH"); custom != "" {
		if _, err := os.Stat(custom); err == nil {
			return custom
		}
	}

	home, _ := os.UserHomeDir()

	// 1. Linux / macOS desktop config paths
	linuxCandidates := []string{
		filepath.Join(home, ".config", "com.qoder.app.stable", "main.sqlite"),
		filepath.Join(home, ".config", "Qoder", "main.sqlite"),
		filepath.Join(home, ".config", "QoderWork", "main.sqlite"),
		filepath.Join(home, ".qoder", "main.sqlite"),
		filepath.Join(home, "Library", "Application Support", "com.qoder.app.stable", "main.sqlite"),
		filepath.Join(home, "Library", "Application Support", "Qoder", "main.sqlite"),
		filepath.Join(home, "Library", "Application Support", "QoderWork", "main.sqlite"),
	}
	for _, p := range linuxCandidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// 2. Windows native AppData paths
	if appData := os.Getenv("APPDATA"); appData != "" {
		winCandidates := []string{
			filepath.Join(appData, "com.qoder.app.stable", "main.sqlite"),
			filepath.Join(appData, "Qoder", "main.sqlite"),
			filepath.Join(appData, "QoderWork", "main.sqlite"),
		}
		for _, p := range winCandidates {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}

	// 3. WSL mount paths (/mnt/c/Users/*/AppData/Roaming/...)
	wslPatterns := []string{
		"/mnt/c/Users/*/AppData/Roaming/com.qoder.app.stable/main.sqlite",
		"/mnt/c/Users/*/AppData/Roaming/Qoder/main.sqlite",
		"/mnt/c/Users/*/AppData/Roaming/QoderWork/main.sqlite",
		"/mnt/*/Users/*/AppData/Roaming/com.qoder.app.stable/main.sqlite",
		"/mnt/*/Users/*/AppData/Roaming/Qoder/main.sqlite",
		"/mnt/*/Users/*/AppData/Roaming/QoderWork/main.sqlite",
	}
	for _, pattern := range wslPatterns {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) > 0 {
			for _, m := range matches {
				if _, err := os.Stat(m); err == nil {
					return m
				}
			}
		}
	}

	return ""
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

// loadDBSessions reads session titles and cwd from Qoder desktop's main.sqlite database.
// To avoid lock contention with running Qoder desktop instances and drvfs filesystem limitations in WSL,
// it copies the database and wal files to a temporary directory before querying.
// Results are cached in the Adapter instance for the lifetime of the CLI process.
func (a *Adapter) loadDBSessions() map[string]dbSessionInfo {
	a.dbCacheMu.Lock()
	defer a.dbCacheMu.Unlock()

	if a.dbCache != nil {
		return a.dbCache
	}

	results := make(map[string]dbSessionInfo)

	dbPath := a.findQoderDBPath()
	if dbPath == "" {
		a.dbCache = results
		return results
	}

	tempDir, err := os.MkdirTemp("", "relay-qoder-db-*")
	if err != nil {
		a.dbCache = results
		return results
	}
	defer os.RemoveAll(tempDir)

	targetDB := filepath.Join(tempDir, "main.sqlite")
	if err := copyFile(dbPath, targetDB); err != nil {
		a.dbCache = results
		return results
	}
	// Copy WAL and SHM files if they exist to include latest unflushed writes
	_ = copyFile(dbPath+"-wal", targetDB+"-wal")
	_ = copyFile(dbPath+"-shm", targetDB+"-shm")

	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", targetDB))
	if err != nil {
		a.dbCache = results
		return results
	}
	defer db.Close()

	rows, err := db.Query("SELECT session_id, title, cwd, updated_at FROM chat_sessions WHERE title != ''")
	if err != nil {
		a.dbCache = results
		return results
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id      string
			title   string
			cwd     sql.NullString
			updated sql.NullInt64
		)
		if err := rows.Scan(&id, &title, &cwd, &updated); err == nil {
			var upTs int64
			if updated.Valid && updated.Int64 > 0 {
				// Convert millisecond timestamp to seconds if needed
				if updated.Int64 > 1e11 {
					upTs = updated.Int64 / 1000
				} else {
					upTs = updated.Int64
				}
			}
			results[id] = dbSessionInfo{
				SessionID: id,
				Title:     title,
				Cwd:       cwd.String,
				UpdatedAt: upTs,
			}
		}
	}

	a.dbCache = results
	return results
}
