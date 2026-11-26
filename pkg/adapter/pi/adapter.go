package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kamusis/relay/pkg/adapter"
	"github.com/mattn/go-runewidth"
)

// Config configures the Pi Coding Agent adapter paths.
type Config struct {
	AgentDir   string // Defaults to $PI_CODING_AGENT_DIR or ~/.pi/agent
	SessionDir string // Optional override; defaults to $PI_CODING_AGENT_SESSION_DIR or <AgentDir>/sessions
	CLIPath    string // Optional explicit path to the pi CLI binary
}

// Adapter implements the adapter.Adapter interface for Pi Coding Agent (pi-agent / pi-acp).
type Adapter struct {
	agentDir   string
	sessionDir string
	cliPath    string
}

// New creates a new Pi Coding Agent Adapter.
func New(cfg ...Config) *Adapter {
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}

	agentDir := c.AgentDir
	if agentDir == "" {
		if envDir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); envDir != "" {
			agentDir = envDir
		} else {
			home, _ := os.UserHomeDir()
			agentDir = filepath.Join(home, ".pi", "agent")
		}
	}

	sessionDir := c.SessionDir
	if sessionDir == "" {
		if c.AgentDir == "" {
			if envSessDir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_SESSION_DIR")); envSessDir != "" {
				sessionDir = envSessDir
			}
		}
		if sessionDir == "" {
			sessionDir = filepath.Join(agentDir, "sessions")
		}
	}

	return &Adapter{
		agentDir:   agentDir,
		sessionDir: sessionDir,
		cliPath:    c.CLIPath,
	}
}

// Type returns the unique identifier for Pi Coding Agent.
func (a *Adapter) Type() adapter.AgentType {
	return adapter.AgentPi
}

// FindCLI locates the pi CLI binary on the system.
func (a *Adapter) FindCLI() (string, error) {
	if a.cliPath != "" {
		if _, err := os.Stat(a.cliPath); err == nil {
			return a.cliPath, nil
		}
		return "", fmt.Errorf("configured pi CLI binary not found at %s", a.cliPath)
	}

	if custom := strings.TrimSpace(os.Getenv("PI_CLI_PATH")); custom != "" {
		if _, err := os.Stat(custom); err == nil {
			return custom, nil
		}
	}

	var candidates []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "pi"))
	}
	if a.agentDir != "" {
		candidates = append(candidates, filepath.Join(a.agentDir, "bin", "pi"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}

	if p, err := exec.LookPath("pi"); err == nil {
		return p, nil
	}

	return "", fmt.Errorf("pi CLI binary not found in PATH, ~/.local/bin/pi, or %s", filepath.Join(a.agentDir, "bin", "pi"))
}

// sessionHeader represents line 1 of a Pi session JSONL file.
type sessionHeader struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
}

// sessionFileEntry binds a discovered JSONL file to its parsed header and file mtime.
type sessionFileEntry struct {
	path  string
	id    string
	cwd   string
	mtime int64
}

// walkSessionFiles discovers all valid Pi session JSONL files under sessionDir.
func (a *Adapter) walkSessionFiles() ([]sessionFileEntry, error) {
	if _, err := os.Stat(a.sessionDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to stat pi sessions dir %s: %w", a.sessionDir, err)
	}

	var entries []sessionFileEntry
	err := filepath.WalkDir(a.sessionDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		hdr, ok := readSessionHeader(path)
		if !ok {
			return nil
		}
		info, err := d.Info()
		mtime := int64(0)
		if err == nil {
			mtime = info.ModTime().Unix()
		}
		entries = append(entries, sessionFileEntry{
			path:  path,
			id:    hdr.ID,
			cwd:   cleanWorkspace(hdr.Cwd),
			mtime: mtime,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk pi sessions dir %s: %w", a.sessionDir, err)
	}
	return entries, nil
}

// cleanWorkspace normalizes non-empty workspace paths while preserving empty values.
func cleanWorkspace(ws string) string {
	trimmed := strings.TrimSpace(ws)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}

// readSessionHeader reads only the first non-empty line of a Pi JSONL file and verifies it is a session header.
func readSessionHeader(jsonlPath string) (sessionHeader, bool) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return sessionHeader{}, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 16*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var hdr sessionHeader
		if err := json.Unmarshal([]byte(line), &hdr); err != nil {
			return sessionHeader{}, false
		}
		if hdr.Type != "session" || strings.TrimSpace(hdr.ID) == "" {
			return sessionHeader{}, false
		}
		return hdr, true
	}
	return sessionHeader{}, false
}

// locateSessionFile resolves a full or prefix session ID to a single Pi JSONL file.
func (a *Adapter) locateSessionFile(sessionID string) (sessionFileEntry, error) {
	entries, err := a.walkSessionFiles()
	if err != nil {
		return sessionFileEntry{}, fmt.Errorf("%w: %v", adapter.ErrSessionNotFound, err)
	}

	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		return sessionFileEntry{}, fmt.Errorf("%w: empty pi session ID", adapter.ErrSessionNotFound)
	}

	var exactMatches []sessionFileEntry
	prefixMatches := make(map[string]sessionFileEntry)

	for _, e := range entries {
		if e.id == cleanID {
			exactMatches = append(exactMatches, e)
		} else if strings.HasPrefix(e.id, cleanID) {
			if prev, exists := prefixMatches[e.id]; !exists || e.mtime > prev.mtime {
				prefixMatches[e.id] = e
			}
		}
	}

	if len(exactMatches) == 1 {
		return exactMatches[0], nil
	}
	if len(exactMatches) > 1 {
		wsSet := make(map[string]struct{})
		var workspaces []string
		for _, em := range exactMatches {
			if _, seen := wsSet[em.cwd]; !seen {
				wsSet[em.cwd] = struct{}{}
				workspaces = append(workspaces, em.cwd)
			}
		}
		if len(wsSet) > 1 {
			sort.Strings(workspaces)
			return sessionFileEntry{}, fmt.Errorf("%w: exact pi session ID '%s' exists across multiple workspaces: %s",
				adapter.ErrAmbiguousSession, cleanID, strings.Join(workspaces, ", "))
		}
		best := exactMatches[0]
		for _, em := range exactMatches[1:] {
			if em.mtime > best.mtime {
				best = em
			}
		}
		return best, nil
	}

	if len(prefixMatches) == 1 {
		for _, m := range prefixMatches {
			return m, nil
		}
	}
	if len(prefixMatches) > 1 {
		ids := make([]string, 0, len(prefixMatches))
		for id := range prefixMatches {
			ids = append(ids, adapter.ShortID(id))
		}
		sort.Strings(ids)
		return sessionFileEntry{}, fmt.Errorf("%w: short ID prefix '%s' matches %d pi sessions: %s",
			adapter.ErrAmbiguousSession, cleanID, len(prefixMatches), strings.Join(ids, ", "))
	}

	return sessionFileEntry{}, fmt.Errorf("%w: pi session %s not found", adapter.ErrSessionNotFound, cleanID)
}

// contentBlock models a single block in a Pi message content array.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// extractTextContent extracts concatenated text from Pi's polymorphic content field
// (which can be either a JSON string or an array of content blocks).
func extractTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return plain
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return ""
}

// parsedSessionMeta holds metadata extracted from a Pi JSONL session file.
type parsedSessionMeta struct {
	ID        string
	Title     string
	Workspace string
	UpdatedAt int64
}

// readSessionMetadata scans a Pi JSONL file to extract ID, workspace, display title, and update time.
func readSessionMetadata(entry sessionFileEntry) (*parsedSessionMeta, error) {
	f, err := os.Open(entry.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	mtime := entry.mtime
	if mtime == 0 {
		if info, statErr := f.Stat(); statErr == nil {
			mtime = info.ModTime().Unix()
		}
	}

	meta := &parsedSessionMeta{
		ID:        entry.id,
		Workspace: entry.cwd,
		UpdatedAt: mtime,
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 32*1024*1024)

	var sessionName string
	var firstUserPrompt string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var rec struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Message *struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}

		switch rec.Type {
		case "session_info":
			if trimmed := strings.TrimSpace(rec.Name); trimmed != "" {
				sessionName = trimmed
			}
		case "message":
			if firstUserPrompt == "" && rec.Message != nil && rec.Message.Role == "user" {
				if txt := strings.TrimSpace(extractTextContent(rec.Message.Content)); txt != "" {
					firstUserPrompt = txt
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan pi session %s: %w", entry.path, err)
	}

	switch {
	case sessionName != "":
		meta.Title = sessionName
	case firstUserPrompt != "":
		firstLine := strings.TrimSpace(strings.Split(firstUserPrompt, "\n")[0])
		meta.Title = runewidth.Truncate(firstLine, 60, "...")
	default:
		meta.Title = "Untitled Session"
	}

	return meta, nil
}

// GetSession verifies session existence and returns fresh metadata.
func (a *Adapter) GetSession(sessionID string) (*adapter.Session, error) {
	entry, err := a.locateSessionFile(sessionID)
	if err != nil {
		return nil, err
	}

	meta, err := readSessionMetadata(entry)
	if err != nil {
		return nil, fmt.Errorf("failed to read pi session metadata: %w", err)
	}

	return &adapter.Session{
		ID:        meta.ID,
		Agent:     adapter.AgentPi,
		Title:     meta.Title,
		Workspace: meta.Workspace,
		UpdatedAt: meta.UpdatedAt,
	}, nil
}

// treeEntry captures the fields needed to walk Pi's active session branch and inspect turns.
type treeEntry struct {
	Type         string
	ID           string
	ParentID     string
	Role         string
	StopReason   string
	ErrorMessage string
	Text         string
}

// GetLatestAssistantOutput walks the active branch from the current leaf and extracts
// the latest completed assistant response text.
func (a *Adapter) GetLatestAssistantOutput(sessionID string) (string, error) {
	entry, err := a.locateSessionFile(sessionID)
	if err != nil {
		return "", err
	}

	f, err := os.Open(entry.path)
	if err != nil {
		return "", fmt.Errorf("failed to open pi session jsonl: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 32*1024*1024)

	byID := make(map[string]treeEntry)
	var leafID string
	var linearEntries []treeEntry // fallback for legacy v1 sessions without entry IDs

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw struct {
			Type     string  `json:"type"`
			ID       string  `json:"id"`
			ParentID *string `json:"parentId"`
			Message  *struct {
				Role         string          `json:"role"`
				StopReason   string          `json:"stopReason"`
				ErrorMessage string          `json:"errorMessage"`
				Content      json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		if raw.Type == "session" {
			continue
		}

		te := treeEntry{
			Type: raw.Type,
			ID:   raw.ID,
		}
		if raw.ParentID != nil {
			te.ParentID = *raw.ParentID
		}
		if raw.Type == "message" && raw.Message != nil {
			te.Role = raw.Message.Role
			te.StopReason = raw.Message.StopReason
			te.ErrorMessage = raw.Message.ErrorMessage
			if raw.Message.Role == "assistant" {
				te.Text = extractTextContent(raw.Message.Content)
			}
		}

		if te.ID != "" {
			byID[te.ID] = te
			leafID = te.ID
		} else {
			linearEntries = append(linearEntries, te)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("failed to scan pi session jsonl: %w", err)
	}

	// Build active branch from leaf to root.
	var activeRev []treeEntry
	if leafID != "" {
		visited := make(map[string]struct{})
		curr := leafID
		for curr != "" {
			if _, seen := visited[curr]; seen {
				break
			}
			visited[curr] = struct{}{}
			node, ok := byID[curr]
			if !ok {
				break
			}
			activeRev = append(activeRev, node)
			curr = node.ParentID
		}
	} else {
		// Legacy v1 fallback: reverse linear order.
		for i := len(linearEntries) - 1; i >= 0; i-- {
			activeRev = append(activeRev, linearEntries[i])
		}
	}

	// Inspect conversational messages from newest to oldest along the active branch.
	for _, node := range activeRev {
		if node.Type != "message" {
			continue
		}
		switch node.Role {
		case "system":
			continue
		case "user", "toolResult", "bashExecution":
			return "", fmt.Errorf("%w: latest active entry is %s (awaiting assistant completion)", adapter.ErrTurnInProgress, node.Role)
		case "assistant":
			switch node.StopReason {
			case "toolUse", "pending", "deferred":
				return "", fmt.Errorf("%w: assistant turn %s has stopReason %q", adapter.ErrTurnInProgress, adapter.ShortID(node.ID), node.StopReason)
			case "error", "aborted":
				if node.ErrorMessage != "" {
					return "", fmt.Errorf("%w: latest pi assistant turn ended with %s (%s)", adapter.ErrNoOutputFound, node.StopReason, node.ErrorMessage)
				}
				return "", fmt.Errorf("%w: latest pi assistant turn ended with %s", adapter.ErrNoOutputFound, node.StopReason)
			default:
				trimmed := strings.TrimSpace(node.Text)
				if trimmed == "" {
					return "", fmt.Errorf("%w: latest pi assistant turn has no text output", adapter.ErrNoOutputFound)
				}
				return trimmed, nil
			}
		}
	}

	return "", fmt.Errorf("%w for pi session %s", adapter.ErrNoOutputFound, entry.id)
}

// Deliver injects the transmission into a Pi session via headless print mode (`pi -p --session <file>`).
func (a *Adapter) Deliver(ctx context.Context, t adapter.Transmission) error {
	cliPath, err := a.FindCLI()
	if err != nil {
		return err
	}

	entry, err := a.locateSessionFile(t.ToSession)
	if err != nil {
		return err
	}

	payload := t.FormatPayload()
	// Use `--` before the prompt so payloads starting with `-` (e.g. markdown dividers) are never parsed as flags.
	cmd := exec.CommandContext(ctx, cliPath, "-p", "--session", entry.path, "--", payload)
	if entry.cwd != "" {
		cmd.Dir = entry.cwd
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("pi execution timed out (context deadline exceeded)")
		}
		return fmt.Errorf("pi delivery failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ListSessions discovers local Pi sessions, optionally filtered by workspace directory.
func (a *Adapter) ListSessions(workspace string, limit int) ([]adapter.Session, error) {
	entries, err := a.walkSessionFiles()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	if limit <= 0 {
		limit = 30
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].mtime != entries[j].mtime {
			return entries[i].mtime > entries[j].mtime
		}
		return entries[i].id < entries[j].id
	})

	var wsMatcher adapter.WorkspaceMatcher
	filterByWS := workspace != adapter.WorkspaceAll && workspace != adapter.WorkspaceNone
	if filterByWS {
		wsMatcher = adapter.NewWorkspaceMatcher(workspace)
	}

	var sessions []adapter.Session
	for _, entry := range entries {
		if workspace == adapter.WorkspaceNone {
			if entry.cwd != "" {
				continue
			}
		} else if filterByWS {
			if !wsMatcher.Matches(entry.cwd) {
				continue
			}
		}

		meta, err := readSessionMetadata(entry)
		if err != nil {
			continue
		}

		sessions = append(sessions, adapter.Session{
			ID:        meta.ID,
			Agent:     adapter.AgentPi,
			Title:     meta.Title,
			Workspace: meta.Workspace,
			UpdatedAt: meta.UpdatedAt,
		})
		if len(sessions) >= limit {
			break
		}
	}

	return sessions, nil
}
