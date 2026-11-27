package peek

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kamusis/relay/pkg/adapter"
	"github.com/kamusis/relay/pkg/link"
	"github.com/kamusis/relay/pkg/registry"
	"github.com/kamusis/relay/pkg/steer"
	"github.com/mattn/go-runewidth"
)

const (
	defaultCommitLimit  = 5
	defaultMaxDiffBytes = 8000
	defaultSessionLimit = 5
)

// Options controls how Inspect resolves and renders a live progress snapshot.
type Options struct {
	Target       string   // Directory/worktree path, "<agent>:<session_id>", or "<link_name>" (empty -> cwd)
	Endpoint     string   // Optional endpoint filter when Target is a link name
	BaseRef      string   // Optional git base ref (auto-detects main/master when empty)
	IncludeDiff  bool     // Include unified git diff in report
	Files        []string // Optional file path filters for git diff
	CommitLimit  int      // Max commits to include (default 5)
	MaxDiffBytes int      // Max bytes for unified diff output (default 8000)
}

// SessionSnapshot captures an agent session's metadata and live turn state.
type SessionSnapshot struct {
	Agent         adapter.AgentType `json:"agent"`
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Workspace     string            `json:"workspace,omitempty"`
	UpdatedAt     int64             `json:"updated_at"`
	State         string            `json:"state"` // "working", "idle", "empty", "unknown"
	OutputBytes   int               `json:"output_bytes,omitempty"`
	OutputPreview string            `json:"output_preview,omitempty"`
}

// CommitInfo summarizes one git commit.
type CommitInfo struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// WorktreeSummary summarizes one git worktree belonging to the inspected repository.
type WorktreeSummary struct {
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	HeadSHA     string `json:"head_sha"`
	HeadSubject string `json:"head_subject,omitempty"`
	DirtyCount  int    `json:"dirty_count"`
	Current     bool   `json:"current,omitempty"`
}

// GitSnapshot captures the physical git/worktree progress of a workspace.
type GitSnapshot struct {
	Root          string            `json:"root"`
	Branch        string            `json:"branch"`
	HeadSHA       string            `json:"head_sha"`
	BaseRef       string            `json:"base_ref,omitempty"`
	Commits       []CommitInfo      `json:"commits,omitempty"`
	StatusLines   []string          `json:"status_lines,omitempty"`
	DiffStat      string            `json:"diff_stat,omitempty"`
	Diff          string            `json:"diff,omitempty"`
	DiffTruncated bool              `json:"diff_truncated,omitempty"`
	Worktrees     []WorktreeSummary `json:"worktrees,omitempty"`
}

// Report is the structured snapshot produced by relay peek.
type Report struct {
	Target        string            `json:"target"`
	ResolvedKind  string            `json:"resolved_kind"` // "worktree", "session", "link"
	Workspace     string            `json:"workspace,omitempty"`
	Sessions      []SessionSnapshot `json:"sessions,omitempty"`
	Git           *GitSnapshot      `json:"git,omitempty"`
	PendingSteers []steer.Message   `json:"pending_steers,omitempty"`
	Timestamp     int64             `json:"timestamp"`
}

// Inspect resolves opts.Target into a worktree, agent session, or link and gathers
// its live session activity, git/worktree state, and pending steer queue.
func Inspect(
	ctx context.Context,
	reg *registry.Registry,
	linkStore *link.Store,
	steerStore *steer.Store,
	opts Options,
) (*Report, error) {
	if opts.CommitLimit <= 0 {
		opts.CommitLimit = defaultCommitLimit
	}
	if opts.MaxDiffBytes <= 0 {
		opts.MaxDiffBytes = defaultMaxDiffBytes
	}
	if baseRef := strings.TrimSpace(opts.BaseRef); baseRef != "" && strings.HasPrefix(baseRef, "-") {
		return nil, fmt.Errorf("invalid --base ref '%s': must not start with '-'", baseRef)
	}

	rawTarget := strings.TrimSpace(opts.Target)
	if rawTarget == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve current directory: %w", err)
		}
		rawTarget = cwd
	}

	// Accept symmetric "ws:<path>" target notation matching `relay steer`.
	dirCandidate := rawTarget
	if strings.HasPrefix(rawTarget, steer.WorkspacePrefix) {
		dirCandidate = strings.TrimSpace(strings.TrimPrefix(rawTarget, steer.WorkspacePrefix))
	}

	report := &Report{
		Target:    rawTarget,
		Timestamp: time.Now().Unix(),
	}

	var steerQueries []string

	// 1. Existing directory/worktree path (including "ws:<path>")
	if info, err := os.Stat(dirCandidate); err == nil && info.IsDir() {
		absDir, absErr := filepath.Abs(dirCandidate)
		if absErr == nil {
			dirCandidate = absDir
		}
		report.ResolvedKind = "worktree"
		report.Workspace = dirCandidate
		steerQueries = append(steerQueries, steer.WorkspaceTarget(dirCandidate))

		gitSnap, gitErr := inspectGit(ctx, dirCandidate, opts)
		if gitErr != nil {
			return nil, gitErr
		}
		if gitSnap != nil {
			report.Git = gitSnap
			if gitSnap.Root != "" && gitSnap.Root != dirCandidate {
				steerQueries = append(steerQueries, steer.WorkspaceTarget(gitSnap.Root))
			}
		}
		if reg != nil {
			report.Sessions = discoverWorkspaceSessions(reg, dirCandidate)
			for _, s := range report.Sessions {
				steerQueries = append(steerQueries, fmt.Sprintf("%s:%s", s.Agent, s.ID))
			}
		}
	} else if agType, sessID, ok := parseEndpointSpec(rawTarget); ok {
		// 2. Explicit "<agent>:<session_id>" endpoint
		if reg == nil {
			return nil, fmt.Errorf("registry unavailable to resolve session '%s'", rawTarget)
		}
		ad, err := reg.Get(agType)
		if err != nil {
			return nil, err
		}
		sess, err := ad.GetSession(sessID)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve session '%s': %w", rawTarget, err)
		}

		report.ResolvedKind = "session"
		snap := probeSession(ad, *sess)
		report.Sessions = []SessionSnapshot{snap}
		steerQueries = append(steerQueries, fmt.Sprintf("%s:%s", snap.Agent, snap.ID))

		if sess.Workspace != "" {
			report.Workspace = sess.Workspace
			steerQueries = append(steerQueries, steer.WorkspaceTarget(sess.Workspace))
			if st, statErr := os.Stat(sess.Workspace); statErr == nil && st.IsDir() {
				gitSnap, gitErr := inspectGit(ctx, sess.Workspace, opts)
				if gitErr != nil {
					return nil, gitErr
				}
				report.Git = gitSnap
			}
		}
	} else if linkStore != nil {
		// 3. Named link lookup
		l, err := linkStore.Get(rawTarget)
		if err != nil {
			return nil, fmt.Errorf("target '%s' is neither an existing directory, '<agent>:<session_id>', nor a known link: %w", rawTarget, err)
		}

		report.ResolvedKind = "link"
		var endpoints []adapter.Session
		if strings.TrimSpace(opts.Endpoint) != "" {
			matched, _, resErr := l.ResolveSource(opts.Endpoint)
			if resErr != nil {
				return nil, resErr
			}
			endpoints = append(endpoints, *matched)
		} else {
			endpoints = append(endpoints, l.SessionA, l.SessionB)
		}

		for _, ep := range endpoints {
			if reg != nil {
				if ad, adErr := reg.Get(ep.Agent); adErr == nil {
					if fresh, getErr := ad.GetSession(ep.ID); getErr == nil {
						ep = *fresh
					}
					report.Sessions = append(report.Sessions, probeSession(ad, ep))
				}
			}
			steerQueries = append(steerQueries, fmt.Sprintf("%s:%s", ep.Agent, ep.ID))
			if ep.Workspace != "" {
				steerQueries = append(steerQueries, steer.WorkspaceTarget(ep.Workspace))
				if report.Workspace == "" {
					if st, statErr := os.Stat(ep.Workspace); statErr == nil && st.IsDir() {
						report.Workspace = ep.Workspace
						gitSnap, gitErr := inspectGit(ctx, ep.Workspace, opts)
						if gitErr != nil {
							return nil, gitErr
						}
						report.Git = gitSnap
					}
				}
			}
		}
	} else {
		return nil, fmt.Errorf("target '%s' is not an existing directory or '<agent>:<session_id>'", rawTarget)
	}

	// Include secondary worktree paths when querying pending steers so supervisors
	// peeking a repo root immediately see steers queued on a feature worktree.
	if report.Git != nil {
		for _, wt := range report.Git.Worktrees {
			if wt.Path != "" {
				steerQueries = append(steerQueries, steer.WorkspaceTarget(wt.Path))
			}
		}
	}

	if steerStore != nil && len(steerQueries) > 0 {
		if pending, err := steerStore.List(steerQueries, false); err == nil {
			report.PendingSteers = pending
		}
	}

	return report, nil
}

func parseEndpointSpec(raw string) (adapter.AgentType, string, bool) {
	idx := strings.IndexByte(raw, ':')
	if idx <= 1 {
		return "", "", false
	}
	agType, err := adapter.ParseAgentType(raw[:idx])
	if err != nil {
		return "", "", false
	}
	sessID := strings.TrimSpace(raw[idx+1:])
	if sessID == "" {
		return "", "", false
	}
	return agType, sessID, true
}

func discoverWorkspaceSessions(reg *registry.Registry, workspace string) []SessionSnapshot {
	var out []SessionSnapshot
	for _, ad := range reg.List() {
		sessions, err := ad.ListSessions(workspace, defaultSessionLimit)
		if err != nil {
			continue
		}
		for _, s := range sessions {
			out = append(out, probeSession(ad, s))
		}
	}
	return out
}

func probeSession(ad adapter.Adapter, s adapter.Session) SessionSnapshot {
	snap := SessionSnapshot{
		Agent:     s.Agent,
		ID:        s.ID,
		Title:     s.Title,
		Workspace: s.Workspace,
		UpdatedAt: s.UpdatedAt,
		State:     "unknown",
	}

	output, err := ad.GetLatestAssistantOutput(s.ID)
	switch {
	case err == nil:
		snap.State = "idle"
		snap.OutputBytes = len(output)
		oneLine := strings.Join(strings.Fields(output), " ")
		snap.OutputPreview = runewidth.Truncate(oneLine, 120, "...")
	case errors.Is(err, adapter.ErrTurnInProgress):
		snap.State = "working"
	case errors.Is(err, adapter.ErrNoOutputFound):
		snap.State = "empty"
	default:
		snap.State = "unknown"
	}
	return snap
}

func inspectGit(ctx context.Context, dir string, opts Options) (*GitSnapshot, error) {
	root, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(root) == "" {
		return nil, nil
	}
	root = filepath.Clean(strings.TrimSpace(root))

	branch, _ := runGit(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	headSHA, _ := runGit(ctx, root, "rev-parse", "--short", "HEAD")

	snap := &GitSnapshot{
		Root:    root,
		Branch:  strings.TrimSpace(branch),
		HeadSHA: strings.TrimSpace(headSHA),
	}

	baseRef := strings.TrimSpace(opts.BaseRef)
	if baseRef != "" {
		if _, verifyErr := runGit(ctx, root, "rev-parse", "--verify", "--quiet", "--end-of-options", baseRef); verifyErr != nil {
			return nil, fmt.Errorf("invalid --base ref '%s': not a valid git revision", baseRef)
		}
	} else {
		baseRef = detectBaseRef(ctx, root, snap.Branch)
	}
	snap.BaseRef = baseRef

	// Commits since base (or recent HEAD commits if on base / no base).
	logArgs := []string{"log", "--oneline", fmt.Sprintf("-n%d", opts.CommitLimit)}
	if baseRef != "" && baseRef != snap.Branch {
		logArgs = append(logArgs, baseRef+"..HEAD")
	}
	if logOut, err := runGit(ctx, root, logArgs...); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(logOut), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			sha, subj, _ := strings.Cut(line, " ")
			snap.Commits = append(snap.Commits, CommitInfo{
				SHA:     sha,
				Subject: strings.TrimSpace(subj),
			})
		}
	}

	// Dirty files via porcelain status.
	if statusOut, err := runGit(ctx, root, "status", "--porcelain"); err == nil {
		for _, line := range strings.Split(strings.TrimRight(statusOut, "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				snap.StatusLines = append(snap.StatusLines, line)
			}
		}
	}

	// Diff stat (branch + working tree vs base, or working tree vs HEAD when on base).
	diffBase := ""
	if baseRef != "" && baseRef != snap.Branch {
		diffBase = baseRef
	}
	statArgs := []string{"diff", "--stat"}
	if diffBase != "" {
		statArgs = append(statArgs, diffBase)
	}
	if len(opts.Files) > 0 {
		statArgs = append(statArgs, "--")
		statArgs = append(statArgs, opts.Files...)
	}
	if statOut, err := runGit(ctx, root, statArgs...); err == nil {
		snap.DiffStat = strings.TrimRight(statOut, "\n")
	}

	// Optional unified diff truncated on a valid UTF-8 rune boundary.
	if opts.IncludeDiff {
		diffArgs := []string{"diff"}
		if diffBase != "" {
			diffArgs = append(diffArgs, diffBase)
		}
		if len(opts.Files) > 0 {
			diffArgs = append(diffArgs, "--")
			diffArgs = append(diffArgs, opts.Files...)
		}
		if diffOut, err := runGit(ctx, root, diffArgs...); err == nil {
			diffOut = strings.TrimRight(diffOut, "\n")
			snap.Diff, snap.DiffTruncated = truncateUTF8(diffOut, opts.MaxDiffBytes)
		}
	}

	snap.Worktrees = listWorktrees(ctx, root)
	return snap, nil
}

// truncateUTF8 caps s to at most maxBytes without splitting a multi-byte UTF-8 rune.
func truncateUTF8(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

func detectBaseRef(ctx context.Context, root, currentBranch string) string {
	for _, candidate := range []string{"main", "master", "origin/main", "origin/master"} {
		if candidate == currentBranch {
			return candidate
		}
		if _, err := runGit(ctx, root, "rev-parse", "--verify", "--quiet", candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func listWorktrees(ctx context.Context, currentRoot string) []WorktreeSummary {
	out, err := runGit(ctx, currentRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}

	resolvedCurrent := currentRoot
	if r, evalErr := filepath.EvalSymlinks(currentRoot); evalErr == nil {
		resolvedCurrent = filepath.Clean(r)
	}

	var (
		result []WorktreeSummary
		cur    *WorktreeSummary
	)
	flush := func() {
		if cur == nil || cur.Path == "" {
			return
		}
		resolved := cur.Path
		if r, evalErr := filepath.EvalSymlinks(cur.Path); evalErr == nil {
			resolved = filepath.Clean(r)
		}
		cur.Current = resolved == resolvedCurrent
		if st, statErr := os.Stat(cur.Path); statErr == nil && st.IsDir() {
			if subj, logErr := runGit(ctx, cur.Path, "log", "-1", "--format=%s"); logErr == nil {
				cur.HeadSubject = strings.TrimSpace(subj)
			}
			if statusOut, stErr := runGit(ctx, cur.Path, "status", "--porcelain"); stErr == nil {
				for _, line := range strings.Split(strings.TrimRight(statusOut, "\n"), "\n") {
					if strings.TrimSpace(line) != "" {
						cur.DirtyCount++
					}
				}
			}
		}
		result = append(result, *cur)
		cur = nil
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &WorktreeSummary{Path: filepath.Clean(strings.TrimPrefix(line, "worktree "))}
		case cur != nil && strings.HasPrefix(line, "HEAD "):
			sha := strings.TrimPrefix(line, "HEAD ")
			if len(sha) > 9 {
				sha = sha[:9]
			}
			cur.HeadSHA = sha
		case cur != nil && strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case cur != nil && line == "detached":
			cur.Branch = "(detached)"
		}
	}
	flush()
	return result
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// FormatReport renders a human- and agent-friendly Markdown/text snapshot of Report.
func FormatReport(r *Report) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "=== Relay Peek (%s) ===\n", r.ResolvedKind)
	fmt.Fprintf(&sb, "Target:    %s\n", r.Target)
	if r.Workspace != "" {
		fmt.Fprintf(&sb, "Workspace: %s\n", r.Workspace)
	}

	if len(r.Sessions) > 0 {
		sb.WriteString("\n## Sessions\n")
		for _, s := range r.Sessions {
			title := runewidth.Truncate(s.Title, 50, "...")
			fmt.Fprintf(&sb, "- %s:%s [%s] %s", s.Agent, adapter.ShortID(s.ID), s.State, title)
			if s.OutputPreview != "" {
				fmt.Fprintf(&sb, "\n  latest: %s", s.OutputPreview)
			}
			sb.WriteByte('\n')
		}
	}

	if r.Git != nil {
		sb.WriteString("\n## Git Progress\n")
		fmt.Fprintf(&sb, "Branch: %s (HEAD %s", r.Git.Branch, r.Git.HeadSHA)
		if r.Git.BaseRef != "" {
			fmt.Fprintf(&sb, ", base %s", r.Git.BaseRef)
		}
		sb.WriteString(")\n")

		if len(r.Git.Commits) > 0 {
			sb.WriteString("Commits:\n")
			for _, c := range r.Git.Commits {
				fmt.Fprintf(&sb, "  %s %s\n", c.SHA, c.Subject)
			}
		}

		if len(r.Git.StatusLines) > 0 {
			fmt.Fprintf(&sb, "Working Tree (%d dirty):\n", len(r.Git.StatusLines))
			for _, l := range r.Git.StatusLines {
				fmt.Fprintf(&sb, "  %s\n", l)
			}
		} else {
			sb.WriteString("Working Tree: clean\n")
		}

		if r.Git.DiffStat != "" {
			sb.WriteString("Diff Stat:\n")
			for _, l := range strings.Split(r.Git.DiffStat, "\n") {
				fmt.Fprintf(&sb, "  %s\n", l)
			}
		}

		if r.Git.Diff != "" {
			sb.WriteString("\n## Unified Diff\n")
			sb.WriteString(r.Git.Diff)
			sb.WriteByte('\n')
			if r.Git.DiffTruncated {
				sb.WriteString("... (diff truncated; increase --max-diff or filter with --file)\n")
			}
		}

		if len(r.Git.Worktrees) > 1 {
			sb.WriteString("\n## Worktrees\n")
			for _, wt := range r.Git.Worktrees {
				marker := " "
				if wt.Current {
					marker = "*"
				}
				fmt.Fprintf(&sb, "%s %s [%s @ %s, dirty=%d]", marker, wt.Path, wt.Branch, wt.HeadSHA, wt.DirtyCount)
				if wt.HeadSubject != "" {
					fmt.Fprintf(&sb, " %s", runewidth.Truncate(wt.HeadSubject, 60, "..."))
				}
				sb.WriteByte('\n')
			}
		}
	}

	if len(r.PendingSteers) > 0 {
		fmt.Fprintf(&sb, "\n## Pending Steers (%d)\n", len(r.PendingSteers))
		for _, m := range r.PendingSteers {
			tag := ""
			if m.Severity == steer.SeverityBlocking {
				tag = " [BLOCKING]"
			}
			fmt.Fprintf(&sb, "- %s%s (%s): %s\n", adapter.ShortID(m.ID), tag, m.Target, runewidth.Truncate(m.Body, 100, "..."))
		}
	}

	return strings.TrimRight(sb.String(), "\n") + "\n"
}
