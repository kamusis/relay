package qoder

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/kamusis/relay/pkg/adapter"
	"github.com/mattn/go-runewidth"
)

// Config configures the Qoder adapter paths.
type Config struct {
	BaseDir string // Defaults to ~/.qoder
	DBPath  string // Optional path to main.sqlite; use ":none:" to disable DB lookup
}

// Adapter implements the adapter.Adapter interface for Qoder (QoderWork).
type Adapter struct {
	baseDir   string
	dbPath    string
	dbCacheMu sync.Mutex
	dbCache   map[string]dbSessionInfo
}

// New creates a new Qoder Adapter.
func New(cfg ...Config) *Adapter {
	base := ""
	db := ""
	if len(cfg) > 0 {
		if cfg[0].BaseDir != "" {
			base = cfg[0].BaseDir
		}
		if cfg[0].DBPath != "" {
			db = cfg[0].DBPath
		}
	}
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".qoder")
	}
	return &Adapter{
		baseDir: base,
		dbPath:  db,
	}
}

// Type returns the unique identifier for Qoder.
func (a *Adapter) Type() adapter.AgentType {
	return adapter.AgentQoder
}

func (a *Adapter) projectsDir() string {
	return filepath.Join(a.baseDir, "projects")
}

func (a *Adapter) tasksDir() string {
	return filepath.Join(a.baseDir, "tasks")
}

// PathToSlug converts an absolute workspace path to Qoder's directory slug (e.g. /a/b -> -a-b).
func PathToSlug(p string) string {
	clean := filepath.Clean(p)
	return strings.ReplaceAll(clean, string(os.PathSeparator), "-")
}

// FindCLI locates the qoder-worker-runtime or qodercli binary on the system.
func (a *Adapter) FindCLI() (string, error) {
	// 0. Explicit env override
	if custom := os.Getenv("QODER_CLI_PATH"); custom != "" {
		if _, err := os.Stat(custom); err == nil {
			return custom, nil
		}
	}

	// 1. Check ~/.local/bin/qodercli or ~/.local/bin/qoder-worker-runtime
	home, _ := os.UserHomeDir()
	localCandidates := []string{
		filepath.Join(home, ".local", "bin", "qodercli"),
		filepath.Join(home, ".local", "bin", "qoder-worker-runtime"),
	}
	for _, lc := range localCandidates {
		if _, err := os.Stat(lc); err == nil {
			return lc, nil
		}
	}

	// 2. Look in PATH for qodercli
	if p, err := exec.LookPath("qodercli"); err == nil {
		return p, nil
	}

	// 3. Look in PATH for qoder, but ignore ~/.qoder/entry/qoder which only launches IDE
	if p, err := exec.LookPath("qoder"); err == nil {
		if !strings.Contains(p, filepath.Join(".qoder", "entry", "qoder")) {
			return p, nil
		}
	}

	// 4. Built-in worker runtime fallback in ~/.qoder/remote/protocol-v1/installs/*/*/bin/qoder-worker-runtime
	patterns := []string{
		filepath.Join(a.baseDir, "remote", "protocol-v1", "installs", "*", "*", "bin", "qoder-worker-runtime"),
		filepath.Join(a.baseDir, "remote", "protocol-v1", "installs", "*", "bin", "qoder-worker-runtime"),
	}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		if len(matches) > 0 {
			sort.Strings(matches)
			for i := len(matches) - 1; i >= 0; i-- {
				candidate := matches[i]
				if _, err := os.Stat(candidate); err == nil {
					return candidate, nil
				}
			}
		}
	}

	return "", fmt.Errorf("qoder CLI binary not found in PATH or ~/.qoder/remote/protocol-v1/installs/...")
}

// locateSessionFile finds the .jsonl file for a session ID across all projects or inside a specific workspace.
func (a *Adapter) locateSessionFile(sessionID string) (filePath string, slug string, err error) {
	projDir := a.projectsDir()
	entries, err := os.ReadDir(projDir)
	if err != nil {
		return "", "", fmt.Errorf("%w: projects dir %s unreadable: %v", adapter.ErrSessionNotFound, projDir, err)
	}

	sessionIDClean := strings.TrimSpace(sessionID)

	type match struct {
		filePath  string
		slug      string
		sessionID string
	}

	var exactMatches []match
	prefixMatches := make(map[string]match) // sessionID -> match

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projPath := filepath.Join(projDir, entry.Name())
		files, err := os.ReadDir(projPath)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			baseName := strings.TrimSuffix(f.Name(), ".jsonl")
			m := match{
				filePath:  filepath.Join(projPath, f.Name()),
				slug:      entry.Name(),
				sessionID: baseName,
			}
			if baseName == sessionIDClean {
				exactMatches = append(exactMatches, m)
			} else if strings.HasPrefix(baseName, sessionIDClean) {
				prefixMatches[baseName] = m
			}
		}
	}

	// 1. Exact matches
	if len(exactMatches) == 1 {
		return exactMatches[0].filePath, exactMatches[0].slug, nil
	}
	if len(exactMatches) > 1 {
		// Check if they point to different workspaces
		wsMap := make(map[string]match)
		for _, em := range exactMatches {
			meta, err := readSessionMetadata(em.filePath)
			if err != nil {
				return "", "", fmt.Errorf("failed to read metadata for session at %s: %w", em.filePath, err)
			}
			ws := em.slug
			if len(meta.Workspaces) > 0 {
				ws = PathToSlug(meta.Workspaces[0])
			}
			wsMap[ws] = em
		}
		if len(wsMap) > 1 {
			var slugs []string
			for _, em := range exactMatches {
				slugs = append(slugs, em.slug)
			}
			return "", "", fmt.Errorf("%w: exact session ID '%s' exists across multiple distinct workspaces: %s",
				adapter.ErrAmbiguousSession, sessionIDClean, strings.Join(slugs, ", "))
		}
		// Same workspace across different slugs, pick most recent
		bestMatch := exactMatches[0]
		var bestMtime int64
		for _, em := range exactMatches {
			if fi, err := os.Stat(em.filePath); err == nil {
				if fi.ModTime().Unix() > bestMtime {
					bestMtime = fi.ModTime().Unix()
					bestMatch = em
				}
			}
		}
		return bestMatch.filePath, bestMatch.slug, nil
	}

	// 2. Prefix match
	if len(prefixMatches) == 1 {
		for _, m := range prefixMatches {
			return m.filePath, m.slug, nil
		}
	}

	if len(prefixMatches) > 1 {
		var matchedIDs []string
		for id := range prefixMatches {
			matchedIDs = append(matchedIDs, adapter.ShortID(id))
		}
		return "", "", fmt.Errorf("%w: short ID prefix '%s' matches %d qoder sessions: %s",
			adapter.ErrAmbiguousSession, sessionID, len(prefixMatches), strings.Join(matchedIDs, ", "))
	}

	return "", "", fmt.Errorf("%w: qoder session %s not found", adapter.ErrSessionNotFound, sessionID)
}

// parsedSessionMeta holds extracted metadata from reading a session's jsonl.
type parsedSessionMeta struct {
	ID         string
	Title      string
	Workspaces []string
	ActiveLeaf string
	UpdatedAt  int64
}

// readSessionMetadata reads a .jsonl file to extract session title, directories, and latest leaf UUID.
func readSessionMetadata(jsonlPath string) (*parsedSessionMeta, error) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, _ := f.Stat()
	mtime := int64(0)
	if stat != nil {
		mtime = stat.ModTime().Unix()
	}

	meta := &parsedSessionMeta{
		ID:        strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl"),
		UpdatedAt: mtime,
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)

	var firstUserPrompt string
	var lastPrompt string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var generic struct {
			Type        string   `json:"type"`
			LastPrompt  string   `json:"lastPrompt"`
			LeafUUID    string   `json:"leafUuid"`
			Directories []string `json:"directories"`
			Message     *struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}

		if err := json.Unmarshal(line, &generic); err != nil {
			continue
		}

		switch generic.Type {
		case "last-prompt":
			if generic.LastPrompt != "" {
				lastPrompt = generic.LastPrompt
			}
		case "active-leaf":
			if generic.LeafUUID != "" {
				meta.ActiveLeaf = generic.LeafUUID
			}
		case "workspace-directories":
			if len(generic.Directories) > 0 {
				meta.Workspaces = generic.Directories
			}
		case "user":
			if firstUserPrompt == "" && generic.Message != nil {
				for _, c := range generic.Message.Content {
					if c.Text != "" {
						firstUserPrompt = c.Text
						break
					}
				}
			}
		}
	}

	if lastPrompt != "" {
		meta.Title = lastPrompt
	} else if firstUserPrompt != "" {
		lines := strings.Split(firstUserPrompt, "\n")
		title := strings.TrimSpace(lines[0])
		meta.Title = runewidth.Truncate(title, 60, "...")
	} else {
		meta.Title = "Untitled Session"
	}

	return meta, nil
}

// GetSession retrieves fresh metadata for a Qoder session.
func (a *Adapter) GetSession(sessionID string) (*adapter.Session, error) {
	jsonlPath, _, err := a.locateSessionFile(sessionID)
	if err != nil {
		return nil, err
	}

	meta, err := readSessionMetadata(jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read qoder session metadata: %w", err)
	}

	primaryWS := ""
	if len(meta.Workspaces) > 0 {
		primaryWS = meta.Workspaces[0]
	}

	title := meta.Title
	dbSessions := a.loadDBSessions()
	if dbSess, ok := dbSessions[meta.ID]; ok && dbSess.Title != "" {
		title = dbSess.Title
		if primaryWS == "" && dbSess.Cwd != "" {
			primaryWS = dbSess.Cwd
		}
	}

	return &adapter.Session{
		ID:        meta.ID,
		Agent:     adapter.AgentQoder,
		Title:     title,
		Workspace: primaryWS,
		UpdatedAt: meta.UpdatedAt,
	}, nil
}

// checkLeaseLock checks if an active task lease lock is currently held by a running process.
func (a *Adapter) checkLeaseLock(sessionID string) bool {
	lockDir := filepath.Join(a.tasksDir(), sessionID, ".task-locks")
	matches, err := filepath.Glob(filepath.Join(lockDir, "*.lease"))
	if err != nil || len(matches) == 0 {
		return false
	}

	for _, m := range matches {
		if probeLeaseLocked(m) {
			return true
		}
	}
	return false
}

// GetLatestAssistantOutput extracts the latest completed assistant turn for a session.
func (a *Adapter) GetLatestAssistantOutput(sessionID string) (string, error) {
	jsonlPath, _, err := a.locateSessionFile(sessionID)
	if err != nil {
		return "", err
	}

	cleanID := strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl")
	if a.checkLeaseLock(cleanID) {
		return "", adapter.ErrTurnInProgress
	}

	f, err := os.Open(jsonlPath)
	if err != nil {
		return "", fmt.Errorf("failed to open session jsonl: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 32*1024*1024)

	var activeLeaf string
	var lastAssistantWithText string
	assistantTurns := make(map[string]struct {
		Text       string
		StopReason string
	})

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var evt struct {
			Type       string `json:"type"`
			UUID       string `json:"uuid"`
			LeafUUID   string `json:"leafUuid"`
			StopReason string `json:"stop_reason"`
			Message    *struct {
				Role       string `json:"role"`
				StopReason string `json:"stop_reason"`
				Content    []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}

		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}

		if evt.Type == "active-leaf" && evt.LeafUUID != "" {
			activeLeaf = evt.LeafUUID
		} else if evt.Type == "assistant" && evt.UUID != "" && evt.Message != nil {
			var sb strings.Builder
			for _, c := range evt.Message.Content {
				if c.Type == "text" && c.Text != "" {
					sb.WriteString(c.Text)
				}
			}
			txt := sb.String()
			stopReason := evt.StopReason
			if stopReason == "" && evt.Message.StopReason != "" {
				stopReason = evt.Message.StopReason
			}
			assistantTurns[evt.UUID] = struct {
				Text       string
				StopReason string
			}{
				Text:       txt,
				StopReason: stopReason,
			}
			if strings.TrimSpace(txt) != "" {
				lastAssistantWithText = evt.UUID
			}
		}
	}

	// 1. Try active leaf first
	if activeLeaf != "" {
		turn, ok := assistantTurns[activeLeaf]
		if ok {
			if turn.StopReason != "" && turn.StopReason != "end_turn" {
				return "", fmt.Errorf("%w: assistant turn %s is still generating (stop_reason: %s)", adapter.ErrTurnInProgress, adapter.ShortID(activeLeaf), turn.StopReason)
			}
			if strings.TrimSpace(turn.Text) != "" {
				return strings.TrimSpace(turn.Text), nil
			}
		}
		// If activeLeaf is not found in assistantTurns or has empty text (e.g. activeLeaf points to trailing system metadata),
		// fall back to lastAssistantWithText.
	}

	// 2. Fallback to latest assistant message with text
	if lastAssistantWithText != "" {
		turn := assistantTurns[lastAssistantWithText]
		if turn.StopReason != "" && turn.StopReason != "end_turn" {
			return "", fmt.Errorf("%w: assistant turn %s is still generating (stop_reason: %s)", adapter.ErrTurnInProgress, adapter.ShortID(lastAssistantWithText), turn.StopReason)
		}
		if strings.TrimSpace(turn.Text) != "" {
			return strings.TrimSpace(turn.Text), nil
		}
	}

	return "", fmt.Errorf("%w for qoder session %s", adapter.ErrNoOutputFound, cleanID)
}

// Deliver injects the transmission into Qoder via qoder-worker-runtime.
func (a *Adapter) Deliver(ctx context.Context, t adapter.Transmission) error {
	cliPath, err := a.FindCLI()
	if err != nil {
		return err
	}

	// Determine workspace directory from session
	sess, err := a.GetSession(t.ToSession)
	if err != nil {
		return err
	}
	ws := sess.Workspace
	if ws == "" {
		ws = "."
	}

	payload := t.FormatPayload()
	// qodercli -w <workspace> -r <session-id> --dangerously-skip-permissions -p "<prompt>"
	// -p stands for --print (non-interactive), prompt is passed as query positional argument.
	cmd := exec.CommandContext(ctx, cliPath, "-w", ws, "-r", sess.ID, "--dangerously-skip-permissions", "-p", payload)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("qoder execution timed out (context deadline exceeded)")
		}
		return fmt.Errorf("qoder delivery failed: %s: %w", string(out), err)
	}
	return nil
}

// ListSessions discovers sessions from ~/.qoder/projects/.
func (a *Adapter) ListSessions(workspace string, limit int) ([]adapter.Session, error) {
	projDir := a.projectsDir()
	entries, err := os.ReadDir(projDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read qoder projects directory: %w", err)
	}

	if limit <= 0 {
		limit = 30
	}

	targetSlug := ""
	if workspace != "" && workspace != adapter.WorkspaceNone {
		targetSlug = PathToSlug(workspace)
	}

	var allFiles []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if targetSlug != "" && entry.Name() != targetSlug {
			continue
		}

		pDir := filepath.Join(projDir, entry.Name())
		files, err := os.ReadDir(pDir)
		if err != nil {
			continue
		}

		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".jsonl") {
				allFiles = append(allFiles, filepath.Join(pDir, f.Name()))
			}
		}
	}

	// Sort files by ModTime descending
	type fileWithTime struct {
		path  string
		mtime int64
	}
	var fwt []fileWithTime
	for _, fp := range allFiles {
		if info, err := os.Stat(fp); err == nil {
			fwt = append(fwt, fileWithTime{path: fp, mtime: info.ModTime().Unix()})
		}
	}
	sort.Slice(fwt, func(i, j int) bool {
		return fwt[i].mtime > fwt[j].mtime
	})

	var sessions []adapter.Session
	dbSessions := a.loadDBSessions()
	for _, f := range fwt {
		meta, err := readSessionMetadata(f.path)
		if err != nil {
			continue
		}
		primaryWS := ""
		if len(meta.Workspaces) > 0 {
			primaryWS = meta.Workspaces[0]
		}

		title := meta.Title
		if dbSess, ok := dbSessions[meta.ID]; ok && dbSess.Title != "" {
			title = dbSess.Title
			if primaryWS == "" && dbSess.Cwd != "" {
				primaryWS = dbSess.Cwd
			}
		}

		if workspace == adapter.WorkspaceNone && primaryWS != "" {
			continue
		}
		sessions = append(sessions, adapter.Session{
			ID:        meta.ID,
			Agent:     adapter.AgentQoder,
			Title:     title,
			Workspace: primaryWS,
			UpdatedAt: meta.UpdatedAt,
		})
		if len(sessions) >= limit {
			break
		}
	}

	return sessions, nil
}
