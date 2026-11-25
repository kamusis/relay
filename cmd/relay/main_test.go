package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
	"github.com/kamusis/relay/pkg/adapter/pi"
	"github.com/kamusis/relay/pkg/adapter/qoder"
	"github.com/kamusis/relay/pkg/history"
	"github.com/kamusis/relay/pkg/link"
	"github.com/kamusis/relay/pkg/registry"
)

func TestTemplateCommands(t *testing.T) {
	// Test template list
	templateCmd := newTemplateCmd()
	var buf bytes.Buffer
	templateCmd.SetOut(&buf)
	templateCmd.SetArgs([]string{"list"})
	if err := templateCmd.Execute(); err != nil {
		t.Fatalf("template list failed: %v", err)
	}

	// Test template show review
	showBuf := bytes.Buffer{}
	templateCmd.SetOut(&showBuf)
	templateCmd.SetArgs([]string{"show", "review"})
	if err := templateCmd.Execute(); err != nil {
		t.Fatalf("template show review failed: %v", err)
	}

	// Test template show followup
	showBuf2 := bytes.Buffer{}
	templateCmd.SetOut(&showBuf2)
	templateCmd.SetArgs([]string{"show", "followup"})
	if err := templateCmd.Execute(); err != nil {
		t.Fatalf("template show followup failed: %v", err)
	}
}

func TestSendFlags(t *testing.T) {
	sendCmd := newSendCmd()
	if sendCmd.Flags().Lookup("template") == nil {
		t.Fatalf("expected --template flag to be registered on send command")
	}
	if sendCmd.Flags().ShorthandLookup("t") == nil {
		t.Fatalf("expected -t shorthand flag to be registered on send command")
	}
	if sendCmd.Flags().Lookup("message") == nil {
		t.Fatalf("expected --message flag to be registered on send command")
	}
	if sendCmd.Flags().ShorthandLookup("m") == nil {
		t.Fatalf("expected -m shorthand flag to be registered on send command")
	}
}

func TestLinkCreateFlagsAndEndpoints(t *testing.T) {
	linkCmd := newLinkCmd()
	createCmd, _, err := linkCmd.Find([]string{"create"})
	if err != nil || createCmd == nil {
		t.Fatalf("failed to find link create subcommand: %v", err)
	}
	for _, flagName := range []string{"agy", "qoder", "pi"} {
		if createCmd.Flags().Lookup(flagName) == nil {
			t.Fatalf("expected --%s flag to be registered on link create command", flagName)
		}
	}

	ep1, ep2, err := resolveLinkCreateEndpoints("demo", []string{"demo"}, "", "q123", "p456")
	if err != nil {
		t.Fatalf("unexpected error for --qoder + --pi: %v", err)
	}
	if ep1 != "qoder:q123" || ep2 != "pi:p456" {
		t.Errorf("unexpected endpoints: %s, %s", ep1, ep2)
	}

	ep1, ep2, err = resolveLinkCreateEndpoints("demo", []string{"demo"}, "a111", "", "p456")
	if err != nil {
		t.Fatalf("unexpected error for --agy + --pi: %v", err)
	}
	if ep1 != "agy:a111" || ep2 != "pi:p456" {
		t.Errorf("unexpected endpoints: %s, %s", ep1, ep2)
	}

	if _, _, err := resolveLinkCreateEndpoints("demo", []string{"demo"}, "a1", "q2", "p3"); err == nil {
		t.Errorf("expected error when three shorthand flags are passed")
	}
	if _, _, err := resolveLinkCreateEndpoints("demo", []string{"demo", "extra"}, "", "q2", "p3"); err == nil {
		t.Errorf("expected error when mixing positional arg and shorthand flags")
	}
}

func TestPullFlags(t *testing.T) {
	pullCmd := newPullCmd()
	if pullCmd.Flags().Lookup("from") == nil {
		t.Fatalf("expected --from flag to be registered on pull command")
	}
	if pullCmd.Flags().Lookup("force") != nil {
		t.Fatalf("expected dead --force flag not to be registered on pull command")
	}
}

func setupPullFixtures(t *testing.T, qoderJSONL string) (*registry.Registry, *link.Store, *history.Store) {
	t.Helper()
	tmp := t.TempDir()

	// Real Pi session fixture
	piAgentDir := filepath.Join(tmp, "pi-agent")
	piSessDir := filepath.Join(piAgentDir, "sessions", "--tmp-repo--")
	if err := os.MkdirAll(piSessDir, 0o755); err != nil {
		t.Fatalf("mkdir pi sess dir: %v", err)
	}
	piContent := `{"type":"session","version":3,"id":"01a212b0-ccd1-714a-bba7-599bb4b4b497","cwd":"/tmp/repo"}
{"type":"session_info","id":"si1","parentId":null,"name":"Updated Pi Title"}
`
	if err := os.WriteFile(filepath.Join(piSessDir, "s.jsonl"), []byte(piContent), 0o600); err != nil {
		t.Fatalf("write pi session: %v", err)
	}

	// Real Qoder session fixture
	qoderBaseDir := filepath.Join(tmp, "qoder")
	qoderProjDir := filepath.Join(qoderBaseDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(qoderProjDir, 0o755); err != nil {
		t.Fatalf("mkdir qoder proj dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(qoderProjDir, "3c58333e-7024-49d8-a9c4-b2a89629f705.jsonl"), []byte(qoderJSONL), 0o600); err != nil {
		t.Fatalf("write qoder session: %v", err)
	}

	reg := registry.New()
	reg.Register(pi.New(pi.Config{AgentDir: piAgentDir}))
	reg.Register(qoder.New(qoder.Config{BaseDir: qoderBaseDir, DBPath: ":none:"}))

	linkStore := link.NewStore(filepath.Join(tmp, "links.json"))
	histStore := history.NewStore(filepath.Join(tmp, "history.jsonl"))

	err := linkStore.Create(adapter.Link{
		Name: "review-repo",
		SessionA: adapter.Session{
			ID:        "01a212b0-ccd1-714a-bba7-599bb4b4b497",
			Agent:     adapter.AgentPi,
			Title:     "Stale Pi Title",
			Workspace: "/tmp/repo",
		},
		SessionB: adapter.Session{
			ID:        "3c58333e-7024-49d8-a9c4-b2a89629f705",
			Agent:     adapter.AgentQoder,
			Title:     "Qoder Reviewer",
			Workspace: "/tmp/repo",
		},
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	return reg, linkStore, histStore
}

func TestExecutePull_SuccessAuditAndMetadataHeal(t *testing.T) {
	qoderJSONL := `{"type":"workspace-directories","directories":["/tmp/repo"]}
{"type":"last-prompt","lastPrompt":"Qoder Reviewer"}
{"type":"assistant","uuid":"leaf-1","stop_reason":"end_turn","message":{"role":"assistant","content":[{"type":"text","text":"## Review\nVerdict: APPROVED"}]}}
{"type":"active-leaf","leafUuid":"leaf-1"}
`
	reg, linkStore, histStore := setupPullFixtures(t, qoderJSONL)

	var out, errOut bytes.Buffer
	err := executePull(&out, &errOut, reg, linkStore, histStore, "review-repo", "qoder")
	if err != nil {
		t.Fatalf("executePull failed: %v", err)
	}

	// Stdout contains pure extracted markdown
	if got := strings.TrimSpace(out.String()); got != "## Review\nVerdict: APPROVED" {
		t.Errorf("unexpected stdout:\n%s", out.String())
	}
	// Stderr contains provenance summary
	if !strings.Contains(errOut.String(), "Pulled from qoder (3c58333e) -> pi (01a212b0)") {
		t.Errorf("unexpected stderr:\n%s", errOut.String())
	}

	// Audit history persisted
	records, err := histStore.List("review-repo", 5)
	if err != nil || len(records) != 1 {
		t.Fatalf("expected 1 audit record, got %d (err=%v)", len(records), err)
	}
	tx := records[0]
	if tx.FromAgent != adapter.AgentQoder || tx.ToAgent != adapter.AgentPi || tx.Status != adapter.TransmissionDelivered || tx.UserPrompt != "[pull]" {
		t.Errorf("unexpected audit transmission: %+v", tx)
	}

	// Opportunistic cache self-healing updated drifted Pi title
	healed, err := linkStore.Get("review-repo")
	if err != nil {
		t.Fatalf("reload link: %v", err)
	}
	if healed.SessionA.Title != "Updated Pi Title" {
		t.Errorf("expected drifted Pi title to be healed, got %q", healed.SessionA.Title)
	}
}

func TestExecutePull_BusyGuardBlocksPrematureRead(t *testing.T) {
	qoderJSONL := `{"type":"workspace-directories","directories":["/tmp/repo"]}
{"type":"last-prompt","lastPrompt":"Qoder Reviewer"}
{"type":"assistant","uuid":"leaf-1","stop_reason":"tool_use","message":{"role":"assistant","content":[{"type":"text","text":"Still running tests..."}]}}
{"type":"active-leaf","leafUuid":"leaf-1"}
`
	reg, linkStore, histStore := setupPullFixtures(t, qoderJSONL)

	var out, errOut bytes.Buffer
	err := executePull(&out, &errOut, reg, linkStore, histStore, "review-repo", "qoder")
	if err == nil {
		t.Fatalf("expected busy guard error, got nil")
	}
	if !strings.Contains(err.Error(), "currently busy generating output") {
		t.Errorf("unexpected error: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected empty stdout on busy error, got %q", out.String())
	}
	records, _ := histStore.List("review-repo", 5)
	if len(records) != 0 {
		t.Errorf("expected 0 audit records on busy error, got %d", len(records))
	}
}
