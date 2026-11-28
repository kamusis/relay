package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kamusis/relay/pkg/adapter"
	"github.com/kamusis/relay/pkg/history"
	"github.com/kamusis/relay/pkg/prompt"
	"github.com/kamusis/relay/pkg/registry"
)

type stubAskAdapter struct {
	agentType adapter.AgentType
	sessions  []adapter.Session
	lastReq   adapter.AskRequest
	resp      *adapter.AskResponse
	err       error
}

func (s *stubAskAdapter) Type() adapter.AgentType { return s.agentType }
func (s *stubAskAdapter) GetSession(id string) (*adapter.Session, error) {
	for _, sess := range s.sessions {
		if sess.ID == id || strings.HasPrefix(sess.ID, id) {
			cp := sess
			return &cp, nil
		}
	}
	return nil, adapter.ErrSessionNotFound
}
func (s *stubAskAdapter) GetLatestAssistantOutput(string) (string, error) { return "", nil }
func (s *stubAskAdapter) Deliver(context.Context, adapter.Transmission) error {
	return nil
}
func (s *stubAskAdapter) ListSessions(string, int) ([]adapter.Session, error) {
	return s.sessions, nil
}
func (s *stubAskAdapter) Ask(_ context.Context, req adapter.AskRequest) (*adapter.AskResponse, error) {
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

type stubNonAskerAdapter struct {
	agentType adapter.AgentType
}

func (s *stubNonAskerAdapter) Type() adapter.AgentType { return s.agentType }
func (s *stubNonAskerAdapter) GetSession(string) (*adapter.Session, error) {
	return nil, adapter.ErrSessionNotFound
}
func (s *stubNonAskerAdapter) GetLatestAssistantOutput(string) (string, error)     { return "", nil }
func (s *stubNonAskerAdapter) Deliver(context.Context, adapter.Transmission) error { return nil }
func (s *stubNonAskerAdapter) ListSessions(string, int) ([]adapter.Session, error) { return nil, nil }

func TestExecuteAskAutoReuseNewExplicitAndAudit(t *testing.T) {
	tmp := t.TempDir()
	histStore := history.NewStore(filepath.Join(tmp, "history.jsonl"))
	promptMgr := prompt.NewManager("")

	qoderStub := &stubAskAdapter{
		agentType: adapter.AgentQoder,
		sessions: []adapter.Session{
			{ID: "2a037e6d-9a69-4484-afd6-57a579b8e67b", Agent: adapter.AgentQoder, Workspace: tmp},
		},
		resp: &adapter.AskResponse{
			SessionID: "2a037e6d-9a69-4484-afd6-57a579b8e67b",
			Output:    "Review looks solid.\nVerdict: APPROVED",
		},
	}
	reg := registry.New()
	reg.Register(qoderStub)
	reg.Register(&stubNonAskerAdapter{agentType: adapter.AgentZed})

	// 1. Bare agent reuses latest workspace session, renders template, prints pure reply to stdout, and logs to history.jsonl
	var stdout, stderr bytes.Buffer
	err := executeAsk(context.Background(), nil, &stdout, &stderr, reg, histStore, promptMgr, askOptions{
		Target:    "qoder",
		Workspace: tmp,
		Template:  "review",
		Message:   "Check lock safety",
		From:      "claude:652e7f08",
	})
	if err != nil {
		t.Fatalf("executeAsk(reuse) error = %v", err)
	}
	if qoderStub.lastReq.SessionID != "2a037e6d-9a69-4484-afd6-57a579b8e67b" {
		t.Fatalf("expected auto-reused session ID, got %q", qoderStub.lastReq.SessionID)
	}
	if !strings.Contains(qoderStub.lastReq.Prompt, "Check lock safety") || !strings.Contains(qoderStub.lastReq.Prompt, "Verdict: APPROVED") {
		t.Fatalf("expected rendered review template prompt, got:\n%s", qoderStub.lastReq.Prompt)
	}
	if strings.TrimSpace(stdout.String()) != "Review looks solid.\nVerdict: APPROVED" {
		t.Fatalf("unexpected stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "[relay ask] session=qoder:2a037e6d") {
		t.Fatalf("expected session hint on stderr, got %q", stderr.String())
	}

	records, err := histStore.List("ask", 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("expected 1 ask audit record, got (%+v, %v)", records, err)
	}
	if records[0].FromAgent != adapter.AgentClaude || records[0].FromSession != "652e7f08" || records[0].Status != adapter.TransmissionDelivered {
		t.Fatalf("unexpected audit record: %+v", records[0])
	}

	// 2. --new forces empty SessionID and supports --json output
	qoderStub.resp = &adapter.AskResponse{
		SessionID: "9e0bb726-8f81-47af-b2f5-de32477bb258",
		Output:    "Fresh review OK",
	}
	var jsonOut bytes.Buffer
	if err := executeAsk(context.Background(), nil, &jsonOut, nil, reg, histStore, promptMgr, askOptions{
		Target:    "qoder",
		Workspace: tmp,
		Message:   "Quick sanity check",
		ForceNew:  true,
		AsJSON:    true,
	}); err != nil {
		t.Fatalf("executeAsk(--new --json) error = %v", err)
	}
	if qoderStub.lastReq.SessionID != "" {
		t.Fatalf("expected empty SessionID with --new, got %q", qoderStub.lastReq.SessionID)
	}
	var parsed askJSONOutput
	if err := json.Unmarshal(jsonOut.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, jsonOut.String())
	}
	if parsed.SessionID != "9e0bb726-8f81-47af-b2f5-de32477bb258" || parsed.Output != "Fresh review OK" {
		t.Fatalf("unexpected JSON output: %+v", parsed)
	}

	// 3. Reject --new combined with explicit session ID
	if err := executeAsk(context.Background(), nil, &stdout, &stderr, reg, histStore, promptMgr, askOptions{
		Target:   "qoder:2a037e6d",
		Message:  "hi",
		ForceNew: true,
	}); err == nil {
		t.Fatal("expected error when combining --new with explicit session ID")
	}

	// 4. Reject non-Asker adapter (e.g. zed) with clear guidance
	if err := executeAsk(context.Background(), nil, &stdout, &stderr, reg, histStore, promptMgr, askOptions{
		Target:  "zed",
		Message: "hi",
	}); err == nil || !strings.Contains(err.Error(), "does not support direct linkless") {
		t.Fatalf("expected non-Asker guidance error for zed, got %v", err)
	}

	// 5. Failed Ask records failed transmission in history
	qoderStub.err = errors.New("cli crashed")
	if err := executeAsk(context.Background(), nil, &stdout, &stderr, reg, histStore, promptMgr, askOptions{
		Target:  "qoder:2a037e6d",
		Message: "fail me",
	}); err == nil {
		t.Fatal("expected error from failing adapter")
	}
	allRecs, _ := histStore.List("ask", 10)
	if len(allRecs) != 3 || allRecs[0].Status != adapter.TransmissionFailed {
		t.Fatalf("expected failed transmission logged first, got %+v", allRecs)
	}
}

func TestResolveAskTimeoutAndStdinMessage(t *testing.T) {
	if got := resolveAskTimeout(45*time.Second, func(string) string { return "900s" }); got != 45*time.Second {
		t.Fatalf("flag timeout should win, got %v", got)
	}
	if got := resolveAskTimeout(0, func(string) string { return "900s" }); got != 900*time.Second {
		t.Fatalf("env timeout should apply, got %v", got)
	}
	if got := resolveAskTimeout(0, func(string) string { return "" }); got != defaultAskTimeout {
		t.Fatalf("default timeout should be %v, got %v", defaultAskTimeout, got)
	}

	// Verify -m - reads message from stdin
	stub := &stubAskAdapter{
		agentType: adapter.AgentClaude,
		resp:      &adapter.AskResponse{SessionID: "sess-1", Output: "ok"},
	}
	reg := registry.New()
	reg.Register(stub)
	var out bytes.Buffer
	// Verify bare --from agy parses to FromAgent=antigravity, FromSession=""
	histStore := history.NewStore(filepath.Join(t.TempDir(), "history.jsonl"))
	if err := executeAsk(context.Background(), strings.NewReader("stdin review body\n"), &out, nil, reg, histStore, prompt.NewManager(""), askOptions{
		Target:  "claude",
		Message: "-",
		From:    "agy",
	}); err != nil {
		t.Fatalf("stdin message error = %v", err)
	}
	if stub.lastReq.Prompt != "stdin review body" {
		t.Fatalf("expected prompt from stdin, got %q", stub.lastReq.Prompt)
	}
	recs, err := histStore.List("ask", 5)
	if err != nil || len(recs) != 1 || recs[0].FromAgent != adapter.AgentAntigravity || recs[0].FromSession != "" {
		t.Fatalf("expected bare --from agy recorded as antigravity with empty session, got %+v, err=%v", recs, err)
	}
}
