package registry

import (
	"context"
	"testing"

	"github.com/kamusis/relay/pkg/adapter"
)

type mockAdapter struct {
	agentType adapter.AgentType
}

func (m *mockAdapter) Type() adapter.AgentType {
	return m.agentType
}

func (m *mockAdapter) GetSession(sessionID string) (*adapter.Session, error) {
	return nil, nil
}

func (m *mockAdapter) GetLatestAssistantOutput(sessionID string) (string, error) {
	return "", nil
}

func (m *mockAdapter) Deliver(ctx context.Context, t adapter.Transmission) error {
	return nil
}

func (m *mockAdapter) ListSessions(workspace string, limit int) ([]adapter.Session, error) {
	return nil, nil
}

func TestRegistry(t *testing.T) {
	r := New()
	agy := &mockAdapter{agentType: adapter.AgentAntigravity}
	qoder := &mockAdapter{agentType: adapter.AgentQoder}

	r.Register(agy)
	r.Register(qoder)

	gotAgy, err := r.Get(adapter.AgentAntigravity)
	if err != nil {
		t.Fatalf("expected to get antigravity adapter, got error: %v", err)
	}
	if gotAgy.Type() != adapter.AgentAntigravity {
		t.Errorf("expected antigravity, got %v", gotAgy.Type())
	}

	gotQoder, err := r.Get(adapter.AgentQoder)
	if err != nil {
		t.Fatalf("expected to get qoder adapter, got error: %v", err)
	}
	if gotQoder.Type() != adapter.AgentQoder {
		t.Errorf("expected qoder, got %v", gotQoder.Type())
	}

	_, err = r.Get(adapter.AgentCodex)
	if err == nil {
		t.Errorf("expected error for unregistered agent, got nil")
	}

	list := r.List()
	if len(list) != 2 {
		t.Errorf("expected 2 registered adapters, got %d", len(list))
	}
}
