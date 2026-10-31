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
