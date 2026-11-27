package peek

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
	"github.com/kamusis/relay/pkg/adapter/qoder"
	"github.com/kamusis/relay/pkg/link"
	"github.com/kamusis/relay/pkg/registry"
	"github.com/kamusis/relay/pkg/steer"
)

func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func TestInspectWorktreeAndSessionAndSteers(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	runGitTest(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "app.go"), []byte("package app\n\nfunc Hello() string { return \"hi\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "app.go")
	runGitTest(t, repo, "commit", "-m", "chore: initial commit")

	// Create secondary feature worktree with a new commit + dirty edit
	wt := filepath.Join(tmp, "wt-feat")
	runGitTest(t, repo, "worktree", "add", wt, "-b", "feat/demo", "main")
	if err := os.WriteFile(filepath.Join(wt, "app.go"), []byte("package app\n\nfunc Hello() string { return \"hello\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, wt, "add", "app.go")
	runGitTest(t, wt, "commit", "-m", "feat: update greeting")
	if err := os.WriteFile(filepath.Join(wt, "app.go"), []byte("package app\n\nfunc Hello() string { return \"hello world\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Setup a real Qoder fixture session pointing at wt (in-progress turn -> working)
	qoderBase := filepath.Join(tmp, "qoder")
	qoderProj := filepath.Join(qoderBase, "projects", qoder.PathToSlug(wt))
	if err := os.MkdirAll(qoderProj, 0o755); err != nil {
		t.Fatal(err)
	}
	sessID := "aaaaaaaa-1111-2222-3333-444444444444"
	jsonl := `{"type":"workspace-directories","sessionId":"` + sessID + `","directories":["` + wt + `"]}` + "\n" +
		`{"type":"last-prompt","lastPrompt":"Implement demo"}` + "\n" +
		`{"type":"assistant","uuid":"leaf-1","stop_reason":"tool_use","message":{"role":"assistant","content":[{"type":"text","text":"Editing app.go..."}]}}` + "\n" +
		`{"type":"active-leaf","leafUuid":"leaf-1"}` + "\n"
	if err := os.WriteFile(filepath.Join(qoderProj, sessID+".jsonl"), []byte(jsonl), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := registry.New()
	reg.Register(qoder.New(qoder.Config{BaseDir: qoderBase, DBPath: ":none:"}))

	steerStore := steer.NewStore(filepath.Join(tmp, "steer.jsonl"))
	if _, err := steerStore.Enqueue(steer.Message{
		Target:   wt,
		Severity: steer.SeverityBlocking,
		Body:     "Use absolute binary path",
	}); err != nil {
		t.Fatalf("Enqueue steer: %v", err)
	}

	// 1. Inspect by worktree path with --diff
	rep, err := Inspect(context.Background(), reg, nil, steerStore, Options{
		Target:      wt,
		IncludeDiff: true,
		Files:       []string{"app.go"},
	})
	if err != nil {
		t.Fatalf("Inspect(wt): %v", err)
	}
	if rep.ResolvedKind != "worktree" || rep.Git == nil {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if rep.Git.Branch != "feat/demo" || rep.Git.BaseRef != "main" {
		t.Fatalf("branch/base = %q/%q, want feat/demo/main", rep.Git.Branch, rep.Git.BaseRef)
	}
	if len(rep.Git.Commits) != 1 || rep.Git.Commits[0].Subject != "feat: update greeting" {
		t.Fatalf("unexpected commits: %+v", rep.Git.Commits)
	}
	if len(rep.Git.StatusLines) != 1 {
		t.Fatalf("expected 1 dirty file, got %v", rep.Git.StatusLines)
	}
	if !strings.Contains(rep.Git.Diff, "hello world") {
		t.Fatalf("expected diff to contain 'hello world', got:\n%s", rep.Git.Diff)
	}
	if len(rep.Git.Worktrees) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(rep.Git.Worktrees))
	}
	if len(rep.Sessions) != 1 || rep.Sessions[0].State != "working" {
		t.Fatalf("expected 1 working qoder session, got %+v", rep.Sessions)
	}
	if len(rep.PendingSteers) != 1 || rep.PendingSteers[0].Severity != steer.SeverityBlocking {
		t.Fatalf("expected 1 pending blocking steer, got %+v", rep.PendingSteers)
	}

	formatted := FormatReport(rep)
	for _, want := range []string{"feat/demo", "feat: update greeting", "Pending Steers (1)", "[BLOCKING]", "hello world"} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("FormatReport missing %q:\n%s", want, formatted)
		}
	}

	// 2. Inspect by session endpoint "qoder:<prefix>"
	repSess, err := Inspect(context.Background(), reg, nil, steerStore, Options{
		Target: "qoder:aaaaaaaa",
	})
	if err != nil {
		t.Fatalf("Inspect(session): %v", err)
	}
	if repSess.ResolvedKind != "session" || len(repSess.Sessions) != 1 || repSess.Git == nil {
		t.Fatalf("unexpected session report: %+v", repSess)
	}

	// 3. Inspect by link name
	linkStore := link.NewStore(filepath.Join(tmp, "links.json"))
	if err := linkStore.Create(adapter.Link{
		Name: "watch-demo",
		SessionA: adapter.Session{
			ID:        sessID,
			Agent:     adapter.AgentQoder,
			Title:     "Qoder worker",
			Workspace: wt,
		},
		SessionB: adapter.Session{
			ID:        sessID,
			Agent:     adapter.AgentQoder,
			Title:     "Qoder peer",
			Workspace: wt,
		},
	}); err != nil {
		t.Fatalf("linkStore.Create: %v", err)
	}
	repLink, err := Inspect(context.Background(), reg, linkStore, steerStore, Options{
		Target:   "watch-demo",
		Endpoint: "qoder:aaaaaaaa",
	})
	if err == nil {
		t.Fatalf("expected ambiguous endpoint error when both link endpoints match")
	}
	repLink, err = Inspect(context.Background(), reg, linkStore, steerStore, Options{
		Target: "watch-demo",
	})
	if err != nil {
		t.Fatalf("Inspect(link): %v", err)
	}
	if repLink.ResolvedKind != "link" || repLink.Git == nil {
		t.Fatalf("unexpected link report: %+v", repLink)
	}
}
