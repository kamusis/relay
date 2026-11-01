package adapter

import (
	"strings"
	"testing"
)

func TestParseAgentType(t *testing.T) {
	tests := []struct {
		input   string
		want    AgentType
		wantErr bool
	}{
		{"antigravity", AgentAntigravity, false},
		{"agy", AgentAntigravity, false},
		{"AGY", AgentAntigravity, false},
		{"qoder", AgentQoder, false},
		{"qoderwork", AgentQoder, false},
		{"qw", AgentQoder, false},
		{"codex", AgentCodex, false},
		{"claude", AgentClaude, false},
		{"claudecode", AgentClaude, false},
		{"unknown-agent", AgentType("unknown-agent"), true},
	}

	for _, tt := range tests {
		got, err := ParseAgentType(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseAgentType(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseAgentType(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestResolveEndpoints(t *testing.T) {
	link := Link{
		Name: "test-link",
		SessionA: Session{
			ID:    "11112222-3333-4444-5555-666677778888",
			Agent: AgentAntigravity,
		},
		SessionB: Session{
			ID:    "aaaabbbb-cccc-dddd-eeee-ffff00001111",
			Agent: AgentQoder,
		},
	}

	// Resolve by agent name
	src, tgt, err := link.ResolveEndpoints("qoder")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src.Agent != AgentAntigravity || tgt.Agent != AgentQoder {
		t.Errorf("expected src=agy, tgt=qoder, got src=%s, tgt=%s", src.Agent, tgt.Agent)
	}

	// Resolve by short alias
	src, tgt, err = link.ResolveEndpoints("agy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src.Agent != AgentQoder || tgt.Agent != AgentAntigravity {
		t.Errorf("expected src=qoder, tgt=agy, got src=%s, tgt=%s", src.Agent, tgt.Agent)
	}

	// Resolve by session ID prefix
	src, tgt, err = link.ResolveEndpoints("aaaa")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tgt.ID != link.SessionB.ID {
		t.Errorf("expected target session B, got %s", tgt.ID)
	}

	// Unknown target
	_, _, err = link.ResolveEndpoints("codex")
	if err == nil {
		t.Error("expected error for unknown target agent in link, got nil")
	}

	// Verify endpoints are isolated copies (mutation does not alter original link)
	src.Title = "Mutated Title"
	if link.SessionA.Title == "Mutated Title" || link.SessionB.Title == "Mutated Title" {
		t.Errorf("expected ResolveEndpoints to return isolated copies, but original link was mutated")
	}
}

func TestLink_BidirectionalDriftUpdate(t *testing.T) {
	link := Link{
		Name: "test-bidirectional",
		SessionA: Session{
			ID:        "sess-agy-1",
			Agent:     AgentAntigravity,
			Title:     "Old Agy Title",
			Workspace: "/ws/mopheus",
		},
		SessionB: Session{
			ID:        "sess-qoder-2",
			Agent:     AgentQoder,
			Title:     "Old Qoder Title",
			Workspace: "/ws/mopheus",
		},
	}

	// 1. Forward direction: send to Qoder
	src, tgt, err := link.ResolveEndpoints("qoder")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src.ID != "sess-agy-1" || tgt.ID != "sess-qoder-2" {
		t.Fatalf("forward resolve mismatch: src=%s, tgt=%s", src.ID, tgt.ID)
	}

	// Simulate Qoder title drift
	freshQoder := Session{
		ID:        "sess-qoder-2",
		Agent:     AgentQoder,
		Title:     "Drifted Qoder Title",
		Workspace: "/ws/mopheus",
	}
	if link.SessionA.ID == freshQoder.ID {
		link.SessionA.Title = freshQoder.Title
	} else if link.SessionB.ID == freshQoder.ID {
		link.SessionB.Title = freshQoder.Title
	}

	if link.SessionA.Agent != AgentAntigravity || link.SessionB.Agent != AgentQoder {
		t.Fatalf("link endpoints corrupted after forward drift")
	}
	if link.SessionB.Title != "Drifted Qoder Title" {
		t.Fatalf("expected updated SessionB title, got %s", link.SessionB.Title)
	}

	// 2. Reverse direction: reply to Antigravity
	srcRev, tgtRev, err := link.ResolveEndpoints("agy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if srcRev.ID != "sess-qoder-2" || tgtRev.ID != "sess-agy-1" {
		t.Fatalf("reverse resolve mismatch: src=%s, tgt=%s", srcRev.ID, tgtRev.ID)
	}

	// Simulate Antigravity title drift
	freshAgy := Session{
		ID:        "sess-agy-1",
		Agent:     AgentAntigravity,
		Title:     "Drifted Agy Title",
		Workspace: "/ws/mopheus",
	}
	if link.SessionA.ID == freshAgy.ID {
		link.SessionA.Title = freshAgy.Title
	} else if link.SessionB.ID == freshAgy.ID {
		link.SessionB.Title = freshAgy.Title
	}

	// Verify endpoints never swapped or collapsed
	if link.SessionA.ID != "sess-agy-1" || link.SessionA.Agent != AgentAntigravity {
		t.Errorf("SessionA collapsed or corrupted: %+v", link.SessionA)
	}
	if link.SessionB.ID != "sess-qoder-2" || link.SessionB.Agent != AgentQoder {
		t.Errorf("SessionB collapsed or corrupted: %+v", link.SessionB)
	}
	if link.SessionA.Title != "Drifted Agy Title" {
		t.Errorf("expected SessionA title 'Drifted Agy Title', got '%s'", link.SessionA.Title)
	}
}

func TestFormatPayload(t *testing.T) {
	trans := Transmission{
		ID:          "tx-123",
		LinkName:    "moc-qoder",
		FromAgent:   AgentQoder,
		FromSession: "3c58333e-7024-49d8-a9c4-b2a89629f705",
		ToAgent:     AgentAntigravity,
		ToSession:   "d4ddbec2-3749-47b9-90da-edcf4bec323d",
		UserPrompt:  "请按方案 a 修复",
		Body:        "## Review Findings\n- 0 blocking\n- 1 warning",
	}

	payload := trans.FormatPayload()
	if !strings.Contains(payload, "[Relay from qoder (session: 3c58333e) via link 'moc-qoder']") {
		t.Errorf("payload missing header, got:\n%s", payload)
	}
	if !strings.Contains(payload, "User Directive: 请按方案 a 修复") {
		t.Errorf("payload missing directive, got:\n%s", payload)
	}
	if !strings.Contains(payload, "## Review Findings") {
		t.Errorf("payload missing body, got:\n%s", payload)
	}

	// Test without user prompt
	trans.UserPrompt = ""
	payloadNoPrompt := trans.FormatPayload()
	if strings.Contains(payloadNoPrompt, "User Directive:") {
		t.Errorf("payload should omit user directive when empty, got:\n%s", payloadNoPrompt)
	}
}
