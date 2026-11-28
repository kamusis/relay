package codex

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

// Config configures paths for the OpenAI Codex CLI adapter.
type Config struct {
	CodexDir string // Defaults to $CODEX_HOME or ~/.codex
	CLIPath  string // Optional explicit path to codex binary
}

// Adapter implements adapter.Adapter and adapter.Asker for OpenAI Codex CLI.
type Adapter struct {
	codexDir string
	cliPath  string
}

// New creates a new Codex CLI Adapter.
func New(cfg ...Config) *Adapter {
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}

	codexDir := strings.TrimSpace(c.CodexDir)
	if codexDir == "" {
		if envDir := strings.TrimSpace(os.Getenv("CODEX_HOME")); envDir != "" {
			codexDir = envDir
		} else {
			home, _ := os.UserHomeDir()
			codexDir = filepath.Join(home, ".codex")
		}
	}

	return &Adapter{
		codexDir: codexDir,
		cliPath:  strings.TrimSpace(c.CLIPath),
	}
}

// Type returns adapter.AgentCodex.
func (a *Adapter) Type() adapter.AgentType {
	return adapter.AgentCodex
}

// FindCLI locates the codex CLI binary on the host.
func (a *Adapter) FindCLI() (string, error) {
	if a.cliPath != "" {
		if _, err := os.Stat(a.cliPath); err == nil {
			return a.cliPath, nil
		}
		return "", fmt.Errorf("configured codex CLI binary not found at %s", a.cliPath)
	}

	if custom := strings.TrimSpace(os.Getenv("CODEX_CLI_PATH")); custom != "" {
		if _, err := os.Stat(custom); err == nil {
			return custom, nil
		}
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidate := filepath.Join(home, ".local", "bin", "codex")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	if p, err := exec.LookPath("codex"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("codex CLI binary not found in PATH or ~/.local/bin/codex")
}

func (a *Adapter) sessionsDir() string {
	return filepath.Join(a.codexDir, "sessions")
}

type rolloutLine struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type sessionMetaPayload struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
}

type responseItemPayload struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type eventMsgPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type sessionFileEntry struct {
	path  string
	id    string
	cwd   string
	mtime int64
}

func readSessionMeta(path string) (id, cwd string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for i := 0; i < 10 && scanner.Scan(); i++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rl rolloutLine
		if err := json.Unmarshal([]byte(line), &rl); err != nil {
			continue
		}
		if rl.Type != "session_meta" {
			continue
		}
		var meta sessionMetaPayload
		if err := json.Unmarshal(rl.Payload, &meta); err != nil {
			return "", "", false
		}
		sid := strings.TrimSpace(meta.ID)
		if sid == "" {
			sid = strings.TrimSpace(meta.SessionID)
		}
		if sid == "" {
			return "", "", false
		}
		cleanedCwd := ""
		if strings.TrimSpace(meta.Cwd) != "" {
			cleanedCwd = filepath.Clean(strings.TrimSpace(meta.Cwd))
		}
		return sid, cleanedCwd, true
	}
	return "", "", false
}

func (a *Adapter) walkSessionFiles() ([]sessionFileEntry, error) {
	root := a.sessionsDir()
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to stat codex sessions dir %s: %w", root, err)
	}

	var entries []sessionFileEntry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		id, cwd, ok := readSessionMeta(path)
		if !ok {
			return nil
		}
		mtime := int64(0)
		if info, err := d.Info(); err == nil {
			mtime = info.ModTime().Unix()
		}
		entries = append(entries, sessionFileEntry{
			path:  path,
			id:    id,
			cwd:   cwd,
			mtime: mtime,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk codex sessions: %w", err)
	}
	return entries, nil
}

func (a *Adapter) findSessionEntry(sessionID string) (*sessionFileEntry, error) {
	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		return nil, fmt.Errorf("%w: empty session ID", adapter.ErrSessionNotFound)
	}
	entries, err := a.walkSessionFiles()
	if err != nil {
		return nil, err
	}

	var matches []sessionFileEntry
	for _, e := range entries {
		if e.id == cleanID {
			cp := e
			return &cp, nil
		}
		if strings.HasPrefix(e.id, cleanID) {
			matches = append(matches, e)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: %s", adapter.ErrSessionNotFound, cleanID)
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, adapter.ShortID(m.id))
		}
		return nil, fmt.Errorf("%w: codex prefix '%s' matched %d sessions (%s)", adapter.ErrAmbiguousSession, cleanID, len(matches), strings.Join(ids, ", "))
	}
	return &matches[0], nil
}

func extractTitle(path, id string) string {
	f, err := os.Open(path)
	if err != nil {
		return "Codex Session " + adapter.ShortID(id)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var rl rolloutLine
		if err := json.Unmarshal([]byte(strings.TrimSpace(scanner.Text())), &rl); err != nil {
			continue
		}
		if rl.Type == "event_msg" {
			var em eventMsgPayload
			if err := json.Unmarshal(rl.Payload, &em); err == nil && em.Type == "user_message" && strings.TrimSpace(em.Message) != "" {
				return truncateTitle(em.Message)
			}
		}
		if rl.Type == "response_item" {
			var item responseItemPayload
			if err := json.Unmarshal(rl.Payload, &item); err == nil && item.Type == "message" && item.Role == "user" {
				for _, c := range item.Content {
					if strings.TrimSpace(c.Text) != "" {
						return truncateTitle(c.Text)
					}
				}
			}
		}
	}
	return "Codex Session " + adapter.ShortID(id)
}

func truncateTitle(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " "))
	return runewidth.Truncate(s, 72, "...")
}

// GetSession resolves a Codex session by exact ID or unique prefix.
func (a *Adapter) GetSession(sessionID string) (*adapter.Session, error) {
	entry, err := a.findSessionEntry(sessionID)
	if err != nil {
		return nil, err
	}
	return &adapter.Session{
		ID:        entry.id,
		Agent:     adapter.AgentCodex,
		Title:     extractTitle(entry.path, entry.id),
		Workspace: entry.cwd,
		UpdatedAt: entry.mtime,
	}, nil
}

// GetLatestAssistantOutput extracts the latest assistant message from a Codex rollout JSONL file.
func (a *Adapter) GetLatestAssistantOutput(sessionID string) (string, error) {
	entry, err := a.findSessionEntry(sessionID)
	if err != nil {
		return "", err
	}
	f, err := os.Open(entry.path)
	if err != nil {
		return "", fmt.Errorf("failed to open codex session %s: %w", entry.id, err)
	}
	defer f.Close()

	var lastAssistant string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var rl rolloutLine
		if err := json.Unmarshal([]byte(strings.TrimSpace(scanner.Text())), &rl); err != nil {
			continue
		}
		switch rl.Type {
		case "event_msg":
			var em eventMsgPayload
			if err := json.Unmarshal(rl.Payload, &em); err == nil && em.Type == "agent_message" && strings.TrimSpace(em.Message) != "" {
				lastAssistant = strings.TrimSpace(em.Message)
			}
		case "response_item":
			var item responseItemPayload
			if err := json.Unmarshal(rl.Payload, &item); err == nil && item.Type == "message" && item.Role == "assistant" {
				var parts []string
				for _, c := range item.Content {
					if strings.TrimSpace(c.Text) != "" {
						parts = append(parts, strings.TrimSpace(c.Text))
					}
				}
				if joined := strings.Join(parts, "\n"); joined != "" {
					lastAssistant = joined
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("failed to scan codex session %s: %w", entry.id, err)
	}
	if lastAssistant == "" {
		return "", fmt.Errorf("%w for codex session %s", adapter.ErrNoOutputFound, entry.id)
	}
	return lastAssistant, nil
}

// Deliver injects a transmission into an existing Codex session headlessly.
func (a *Adapter) Deliver(ctx context.Context, t adapter.Transmission) error {
	_, err := a.Ask(ctx, adapter.AskRequest{
		SessionID: t.ToSession,
		Prompt:    t.FormatPayload(),
	})
	return err
}

// parseCodexJSONStream extracts thread_id and the last agent_message text from `codex exec --json` stdout.
func parseCodexJSONStream(raw []byte) (threadID, lastMsg string) {
	type streamEvent struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
		Item     struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.ThreadID != "" && threadID == "" {
			threadID = strings.TrimSpace(ev.ThreadID)
		}
		if ev.Type == "item.completed" && ev.Item.Type == "agent_message" && strings.TrimSpace(ev.Item.Text) != "" {
			lastMsg = strings.TrimSpace(ev.Item.Text)
		}
	}
	return threadID, lastMsg
}

// Ask executes a direct linkless headless Codex turn (`codex exec` or `codex exec resume`)
// and returns the resulting thread/session ID and final assistant response.
func (a *Adapter) Ask(ctx context.Context, req adapter.AskRequest) (*adapter.AskResponse, error) {
	cliPath, err := a.FindCLI()
	if err != nil {
		return nil, err
	}

	ws := strings.TrimSpace(req.Workspace)
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID != "" {
		sess, err := a.GetSession(sessionID)
		if err != nil {
			return nil, err
		}
		sessionID = sess.ID
		if ws == "" {
			ws = sess.Workspace
		}
	}
	if ws == "" {
		ws = "."
	}

	tmpOut, err := os.CreateTemp("", "relay-codex-last-*.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to create codex output temp file: %w", err)
	}
	lastMsgPath := tmpOut.Name()
	_ = tmpOut.Close()
	defer os.Remove(lastMsgPath)

	var args []string
	if sessionID != "" {
		args = []string{
			"exec", "resume", sessionID,
			"--dangerously-bypass-approvals-and-sandbox",
			"--skip-git-repo-check",
			"--json",
			"-o", lastMsgPath,
			req.Prompt,
		}
	} else {
		args = []string{
			"exec",
			"-C", ws,
			"--dangerously-bypass-approvals-and-sandbox",
			"--skip-git-repo-check",
			"--json",
			"-o", lastMsgPath,
			req.Prompt,
		}
	}

	cmd := exec.CommandContext(ctx, cliPath, args...)
	cmd.Dir = ws
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("codex execution timed out (context deadline exceeded)")
		}
		return nil, fmt.Errorf("codex ask failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	parsedTID, streamMsg := parseCodexJSONStream(out)
	if parsedTID != "" {
		sessionID = parsedTID
	}

	var result string
	if fileData, readErr := os.ReadFile(lastMsgPath); readErr == nil && strings.TrimSpace(string(fileData)) != "" {
		result = strings.TrimSpace(string(fileData))
	} else if streamMsg != "" {
		result = streamMsg
	} else if sessionID != "" {
		if fb, fbErr := a.GetLatestAssistantOutput(sessionID); fbErr == nil {
			result = fb
		}
	}
	if result == "" {
		result = strings.TrimSpace(string(out))
	}

	return &adapter.AskResponse{
		SessionID: sessionID,
		Output:    result,
	}, nil
}

// ListSessions discovers Codex rollout sessions under ~/.codex/sessions, optionally filtered by workspace.
func (a *Adapter) ListSessions(workspace string, limit int) ([]adapter.Session, error) {
	entries, err := a.walkSessionFiles()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 30
	}

	var targetSet map[string]struct{}
	if workspace != "" && workspace != adapter.WorkspaceNone {
		variants := adapter.WorkspacePathVariants(workspace)
		targetSet = make(map[string]struct{}, len(variants))
		for _, v := range variants {
			targetSet[filepath.Clean(v)] = struct{}{}
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].mtime > entries[j].mtime
	})

	var sessions []adapter.Session
	for _, e := range entries {
		if workspace == adapter.WorkspaceNone {
			if e.cwd != "" {
				continue
			}
		} else if len(targetSet) > 0 {
			matched := false
			for _, v := range adapter.WorkspacePathVariants(e.cwd) {
				if _, ok := targetSet[filepath.Clean(v)]; ok {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		sessions = append(sessions, adapter.Session{
			ID:        e.id,
			Agent:     adapter.AgentCodex,
			Title:     extractTitle(e.path, e.id),
			Workspace: e.cwd,
			UpdatedAt: e.mtime,
		})
		if len(sessions) >= limit {
			break
		}
	}
	return sessions, nil
}
