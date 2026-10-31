package adapter

import (
	"context"
	"fmt"
	"strings"
)

// AgentType identifies the AI coding assistant platform.
type AgentType string

const (
	// AgentAntigravity represents Google Antigravity.
	AgentAntigravity AgentType = "antigravity"
	// AgentQoder represents Qoder / QoderWork.
	AgentQoder AgentType = "qoder"
	// AgentCodex represents OpenAI Codex.
	AgentCodex AgentType = "codex"
	// AgentClaude represents Anthropic Claude Code.
	AgentClaude AgentType = "claude"
)

const (
	// WorkspaceAll represents listing sessions across all workspaces without filtering.
	WorkspaceAll = ""
	// WorkspaceNone represents listing strictly sessions with no associated workspace.
	WorkspaceNone = ":none:"
)

// ParseAgentType normalizes user-provided agent strings (including shorthands) into canonical AgentType.
func ParseAgentType(s string) (AgentType, error) {
	norm := strings.ToLower(strings.TrimSpace(s))
	switch norm {
	case "antigravity", "agy":
		return AgentAntigravity, nil
	case "qoder", "qoderwork", "qw":
		return AgentQoder, nil
	case "codex":
		return AgentCodex, nil
	case "claude", "claudecode":
		return AgentClaude, nil
	default:
		return AgentType(norm), fmt.Errorf("unknown agent type: %s", s)
	}
}

// Session represents an active or historical agent conversation context.
// It serves as both the live query result and the cached endpoint stored in links.json.
type Session struct {
	ID        string    `json:"id"`
	Agent     AgentType `json:"agent"`
	Title     string    `json:"title"`     // Human-readable title (cached for list display)
	Workspace string    `json:"workspace"` // Associated workspace path (cached for list display)
	UpdatedAt int64     `json:"updated_at"`
}

// Link represents an explicit named communication channel between two agent sessions.
type Link struct {
	Name      string  `json:"name"`       // Unique link name, e.g. "moc-qoder", "hotfix"
	SessionA  Session `json:"session_a"`  // Endpoint A
	SessionB  Session `json:"session_b"`  // Endpoint B
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// ResolveEndpoints determines the source and target sessions given a user-specified target identifier.
// The target identifier can be an AgentType (e.g. "agy", "qoder") or a Session ID / prefix.
func (l *Link) ResolveEndpoints(targetIdent string) (source, target *Session, err error) {
	trimmed := strings.TrimSpace(targetIdent)
	targetAgent, parseErr := ParseAgentType(trimmed)

	var matchA, matchB bool
	if parseErr == nil {
		if l.SessionA.Agent == targetAgent {
			matchA = true
		}
		if l.SessionB.Agent == targetAgent {
			matchB = true
		}
	}

	// Match by Session ID exact or prefix
	if !matchA && !matchB {
		if strings.HasPrefix(l.SessionA.ID, trimmed) {
			matchA = true
		}
		if strings.HasPrefix(l.SessionB.ID, trimmed) {
			matchB = true
		}
	}

	if matchA && matchB {
		return nil, nil, fmt.Errorf("target '%s' is ambiguous in link '%s': both endpoints match (use full session ID to disambiguate)", targetIdent, l.Name)
	}
	if !matchA && !matchB {
		return nil, nil, fmt.Errorf("target '%s' does not match any endpoint in link '%s' (%s:%s, %s:%s)",
			targetIdent, l.Name, l.SessionA.Agent, ShortID(l.SessionA.ID), l.SessionB.Agent, ShortID(l.SessionB.ID))
	}

	if matchA {
		return &l.SessionB, &l.SessionA, nil
	}
	return &l.SessionA, &l.SessionB, nil
}

// TransmissionStatus represents the delivery outcome of a transmission.
type TransmissionStatus string

const (
	TransmissionDelivered TransmissionStatus = "delivered"
	TransmissionFailed    TransmissionStatus = "failed"
)

// Transmission represents an audited historical record of a cross-agent relay.
type Transmission struct {
	ID          string             `json:"id"`                    // Unique transmission event UUID
	LinkName    string             `json:"link_name"`             // Link channel name (e.g. "moc-qoder")
	FromAgent   AgentType          `json:"from_agent"`            // Sender agent (e.g. "qoder")
	FromSession string             `json:"from_session"`          // Sender session ID
	ToAgent     AgentType          `json:"to_agent"`              // Recipient agent (e.g. "antigravity")
	ToSession   string             `json:"to_session"`            // Recipient session ID
	UserPrompt  string             `json:"user_prompt,omitempty"` // User-supplied directive (e.g. "按方案 a 修复")
	Body        string             `json:"body"`                  // Extracted markdown output from source agent
	Timestamp   int64              `json:"timestamp"`             // Event Unix timestamp
	Status      TransmissionStatus `json:"status,omitempty"`      // Delivery outcome: "delivered" or "failed"
	Error       string             `json:"error,omitempty"`       // Delivery error description if failed
}

// FormatPayload formats the transmission into a structured message envelope for the target agent.
func (t *Transmission) FormatPayload() string {
	fromShort := ShortID(t.FromSession)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Relay from %s (session: %s) via link '%s']\n\n", t.FromAgent, fromShort, t.LinkName))
	if strings.TrimSpace(t.UserPrompt) != "" {
		sb.WriteString(fmt.Sprintf("User Directive: %s\n\n", strings.TrimSpace(t.UserPrompt)))
	}
	sb.WriteString("---\n")
	sb.WriteString(strings.TrimSpace(t.Body))
	return sb.String()
}

// ShortID returns the first 8 characters of an ID for compact human-readable display.
func ShortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// Adapter defines the contract every AI agent driver must implement.
type Adapter interface {
	// Type returns the unique identifier for this agent type.
	Type() AgentType

	// GetSession verifies session existence and returns fresh metadata.
	// Returns ErrSessionNotFound if deleted.
	GetSession(sessionID string) (*Session, error)

	// GetLatestAssistantOutput extracts the latest completed assistant turn.
	// Returns ErrTurnInProgress if the agent is still generating.
	// Returns ErrNoOutputFound if no assistant message exists.
	GetLatestAssistantOutput(sessionID string) (string, error)

	// Deliver formats and injects the transmission into the target session.
	Deliver(ctx context.Context, t Transmission) error

	// ListSessions discovers sessions available locally, optionally filtered by workspace.
	ListSessions(workspace string, limit int) ([]Session, error)
}
