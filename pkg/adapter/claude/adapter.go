package claude

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

	"github.com/kamusis/relay/pkg/adapter"
	"github.com/mattn/go-runewidth"
)

// Config configures paths for the Claude Code adapter.
type Config struct {
	ClaudeDir string // Defaults to $CLAUDE_CONFIG_DIR or ~/.claude
	CLIPath   string // Optional explicit path to claude binary
}

// Adapter implements adapter.Adapter and adapter.Asker for Anthropic Claude Code.
type Adapter struct {
	claudeDir string
	cliPath   string
}

// New creates a new Claude Code Adapter.
func New(cfg ...Config) *Adapter {
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}

	claudeDir := strings.TrimSpace(c.ClaudeDir)
	if claudeDir == "" {
		if envDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); envDir != "" {
			claudeDir = envDir
		} else {
			home, _ := os.UserHomeDir()
			claudeDir = filepath.Join(home, ".claude")
		}
	}

	return &Adapter{
		claudeDir: claudeDir,
		cliPath:   strings.TrimSpace(c.CLIPath),
	}
}

// Type returns adapter.AgentClaude.
func (a *Adapter) Type() adapter.AgentType {
	return adapter.AgentClaude
}

// FindCLI locates the claude CLI binary on the host.
func (a *Adapter) FindCLI() (string, error) {
	if a.cliPath != "" {
		if _, err := os.Stat(a.cliPath); err == nil {
			return a.cliPath, nil
		}
		return "", fmt.Errorf("configured claude CLI binary not found at %s", a.cliPath)
	}

	if custom := strings.TrimSpace(os.Getenv("CLAUDE_CLI_PATH")); custom != "" {
		if _, err := os.Stat(custom); err == nil {
			return custom, nil
		}
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidate := filepath.Join(home, ".local", "bin", "claude")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("claude CLI binary not found in PATH or ~/.local/bin/claude")
}

func (a *Adapter) projectsDir() string {
	return filepath.Join(a.claudeDir, "projects")
}

// PathToSlug converts an absolute workspace path to Claude Code's project directory slug.
func PathToSlug(path string) string {
	cleaned := filepath.Clean(path)
	var sb strings.Builder
	for _, r := range cleaned {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	return sb.String()
}

type jsonlEntry struct {
	Type       string          `json:"type"`
	SessionID  string          `json:"sessionId"`
	Cwd        string          `json:"cwd"`
	LastPrompt string          `json:"lastPrompt"`
	Content    string          `json:"content"`
	Message    json.RawMessage `json:"message"`
}

type messageEnvelope struct {
	Role       string          `json:"role"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func extractMessageText(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, strings.TrimSpace(b.Text))
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func (a *Adapter) findSessionFile(sessionID string) (string, string, error) {
	cleanID := strings.TrimSpace(sessionID)
	if cleanID == "" {
		return "", "", fmt.Errorf("%w: empty session ID", adapter.ErrSessionNotFound)
	}

	projDir := a.projectsDir()
	slugDirs, err := os.ReadDir(projDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("%w: %s", adapter.ErrSessionNotFound, cleanID)
		}
		return "", "", fmt.Errorf("failed to read claude projects directory: %w", err)
	}

	type match struct {
		path string
		id   string
	}
	var matches []match
	for _, sd := range slugDirs {
		if !sd.IsDir() {
			continue
		}
		dirPath := filepath.Join(projDir, sd.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			id := strings.TrimSuffix(f.Name(), ".jsonl")
			if id == cleanID {
				return filepath.Join(dirPath, f.Name()), id, nil
			}
			if strings.HasPrefix(id, cleanID) {
				matches = append(matches, match{path: filepath.Join(dirPath, f.Name()), id: id})
			}
		}
	}

	if len(matches) == 0 {
		return "", "", fmt.Errorf("%w: %s", adapter.ErrSessionNotFound, cleanID)
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, adapter.ShortID(m.id))
		}
		return "", "", fmt.Errorf("%w: claude prefix '%s' matched %d sessions (%s)", adapter.ErrAmbiguousSession, cleanID, len(matches), strings.Join(ids, ", "))
	}
	return matches[0].path, matches[0].id, nil
}

func parseSessionMetadata(path, fallbackID string) (adapter.Session, error) {
	info, err := os.Stat(path)
	if err != nil {
		return adapter.Session{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return adapter.Session{}, err
	}
	defer f.Close()

	sess := adapter.Session{
		ID:        fallbackID,
		Agent:     adapter.AgentClaude,
		UpdatedAt: info.ModTime().Unix(),
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e jsonlEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if sess.ID == "" && e.SessionID != "" {
			sess.ID = e.SessionID
		}
		if sess.Workspace == "" && e.Cwd != "" {
			sess.Workspace = filepath.Clean(e.Cwd)
		}
		if e.Type == "last-prompt" && strings.TrimSpace(e.LastPrompt) != "" {
			sess.Title = truncateTitle(e.LastPrompt)
		} else if sess.Title == "" && e.Type == "user" && len(e.Message) > 0 {
			var msg messageEnvelope
			if err := json.Unmarshal(e.Message, &msg); err == nil {
				if txt := extractMessageText(msg.Content); txt != "" {
					sess.Title = truncateTitle(txt)
				}
			}
		} else if sess.Title == "" && e.Type == "queue-operation" && strings.TrimSpace(e.Content) != "" {
			sess.Title = truncateTitle(e.Content)
		}
	}
	if sess.Title == "" {
		sess.Title = "Claude Session " + adapter.ShortID(sess.ID)
	}
	return sess, nil
}

func truncateTitle(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " "))
	return runewidth.Truncate(s, 72, "...")
}

// GetSession resolves a Claude Code session by exact ID or unique prefix.
func (a *Adapter) GetSession(sessionID string) (*adapter.Session, error) {
	path, id, err := a.findSessionFile(sessionID)
	if err != nil {
		return nil, err
	}
	sess, err := parseSessionMetadata(path, id)
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// GetLatestAssistantOutput extracts the latest completed assistant turn from a Claude Code JSONL transcript.
func (a *Adapter) GetLatestAssistantOutput(sessionID string) (string, error) {
	path, id, err := a.findSessionFile(sessionID)
	if err != nil {
		return "", err
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open claude session %s: %w", id, err)
	}
	defer f.Close()

	var (
		lastText       string
		lastStopReason string
		sawAssistant   bool
	)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e jsonlEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.Type != "assistant" && len(e.Message) == 0 {
			continue
		}
		var msg messageEnvelope
		if err := json.Unmarshal(e.Message, &msg); err != nil {
			continue
		}
		if msg.Role != "assistant" {
			continue
		}
		sawAssistant = true
		lastStopReason = strings.TrimSpace(msg.StopReason)
		if txt := extractMessageText(msg.Content); txt != "" {
			lastText = txt
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("failed to scan claude session %s: %w", id, err)
	}
	if !sawAssistant {
		return "", fmt.Errorf("%w for claude session %s", adapter.ErrNoOutputFound, id)
	}
	if lastStopReason == "tool_use" {
		return "", fmt.Errorf("%w: claude session %s is executing tools (stop_reason: tool_use)", adapter.ErrTurnInProgress, adapter.ShortID(id))
	}
	if strings.TrimSpace(lastText) == "" {
		return "", fmt.Errorf("%w for claude session %s", adapter.ErrNoOutputFound, id)
	}
	return strings.TrimSpace(lastText), nil
}

// Deliver injects a transmission into an existing Claude Code session headlessly.
func (a *Adapter) Deliver(ctx context.Context, t adapter.Transmission) error {
	_, err := a.Ask(ctx, adapter.AskRequest{
		SessionID: t.ToSession,
		Prompt:    t.FormatPayload(),
	})
	return err
}

// Ask executes a direct linkless headless Claude Code turn and returns the session ID and final output.
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

	args := []string{"--dangerously-skip-permissions", "--output-format", "json"}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	args = append(args, "-p", req.Prompt)

	cmd := exec.CommandContext(ctx, cliPath, args...)
	cmd.Dir = ws
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("claude execution timed out (context deadline exceeded)")
		}
		return nil, fmt.Errorf("claude ask failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	parsedSID, result, isErr, ok := adapter.ParseCLIResultJSON(out)
	if parsedSID != "" {
		sessionID = parsedSID
	}
	if ok && isErr {
		return nil, fmt.Errorf("claude returned an error: %s", result)
	}
	if result == "" && sessionID != "" {
		if fallback, fbErr := a.GetLatestAssistantOutput(sessionID); fbErr == nil {
			result = fallback
		}
	}
	if result == "" && !ok {
		result = strings.TrimSpace(string(out))
	}
	return &adapter.AskResponse{
		SessionID: sessionID,
		Output:    result,
	}, nil
}

// ListSessions discovers Claude Code sessions from ~/.claude/projects/, optionally filtered by workspace.
func (a *Adapter) ListSessions(workspace string, limit int) ([]adapter.Session, error) {
	projDir := a.projectsDir()
	slugDirs, err := os.ReadDir(projDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read claude projects directory: %w", err)
	}
	if limit <= 0 {
		limit = 30
	}

	var targetSlugs map[string]struct{}
	if workspace != "" && workspace != adapter.WorkspaceNone {
		variants := adapter.WorkspacePathVariants(workspace)
		targetSlugs = make(map[string]struct{}, len(variants))
		for _, v := range variants {
			targetSlugs[PathToSlug(v)] = struct{}{}
		}
	}

	type fileCandidate struct {
		path  string
		id    string
		mtime int64
	}
	var candidates []fileCandidate
	for _, sd := range slugDirs {
		if !sd.IsDir() {
			continue
		}
		if len(targetSlugs) > 0 {
			if _, ok := targetSlugs[sd.Name()]; !ok {
				continue
			}
		}
		dirPath := filepath.Join(projDir, sd.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			candidates = append(candidates, fileCandidate{
				path:  filepath.Join(dirPath, f.Name()),
				id:    strings.TrimSuffix(f.Name(), ".jsonl"),
				mtime: info.ModTime().Unix(),
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime > candidates[j].mtime
	})

	var sessions []adapter.Session
	for _, c := range candidates {
		sess, err := parseSessionMetadata(c.path, c.id)
		if err != nil {
			continue
		}
		if workspace == adapter.WorkspaceNone && sess.Workspace != "" {
			continue
		}
		sessions = append(sessions, sess)
		if len(sessions) >= limit {
			break
		}
	}
	return sessions, nil
}
