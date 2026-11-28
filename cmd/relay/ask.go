package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/kamusis/relay/pkg/adapter"
	"github.com/kamusis/relay/pkg/history"
	"github.com/kamusis/relay/pkg/prompt"
	"github.com/kamusis/relay/pkg/registry"
)

const defaultAskTimeout = 600 * time.Second

type askOptions struct {
	Target    string
	Workspace string
	Template  string
	Message   string
	ForceNew  bool
	From      string
	Timeout   time.Duration
	AsJSON    bool
}

type askJSONOutput struct {
	Agent          adapter.AgentType `json:"agent"`
	SessionID      string            `json:"session_id"`
	Workspace      string            `json:"workspace"`
	TransmissionID string            `json:"transmission_id"`
	Output         string            `json:"output"`
}

// parseAskTarget splits "<agent>" or "<agent>:<session_id>" into canonical AgentType and optional session ID.
func parseAskTarget(raw string) (adapter.AgentType, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", fmt.Errorf("target agent ('qoder', 'claude', 'codex', 'pi', or '<agent>:<session_id>') is required")
	}

	agentPart := trimmed
	sessionID := ""
	if idx := strings.IndexByte(trimmed, ':'); idx >= 0 {
		agentPart = strings.TrimSpace(trimmed[:idx])
		sessionID = strings.TrimSpace(trimmed[idx+1:])
		if sessionID == "" {
			return "", "", fmt.Errorf("invalid target '%s': session ID after ':' cannot be empty", raw)
		}
	}

	agType, err := adapter.ParseAgentType(agentPart)
	if err != nil {
		return "", "", err
	}
	return agType, sessionID, nil
}

func resolveAskTimeout(flagVal time.Duration, getenv func(string) string) time.Duration {
	if flagVal > 0 {
		return flagVal
	}
	if getenv != nil {
		if custom := strings.TrimSpace(getenv("RELAY_DELIVERY_TIMEOUT")); custom != "" {
			if d, err := time.ParseDuration(custom); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultAskTimeout
}

func executeAsk(
	ctx context.Context,
	in io.Reader,
	out, errOut io.Writer,
	reg *registry.Registry,
	histStore *history.Store,
	promptMgr *prompt.Manager,
	opts askOptions,
) error {
	agType, sessionID, err := parseAskTarget(opts.Target)
	if err != nil {
		return err
	}
	if opts.ForceNew && sessionID != "" {
		return fmt.Errorf("--new cannot be combined with an explicit session ID (%s:%s)", agType, sessionID)
	}

	ad, err := reg.Get(agType)
	if err != nil {
		return err
	}
	asker, ok := ad.(adapter.Asker)
	if !ok {
		return fmt.Errorf("agent '%s' does not support direct linkless 'relay ask' (supported headless targets: qoder, claude, codex, pi)", agType)
	}

	ws := strings.TrimSpace(opts.Workspace)
	if ws == "" && sessionID == "" {
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			ws = cwd
		}
	}
	if ws != "" {
		if abs, absErr := filepath.Abs(ws); absErr == nil {
			ws = abs
		}
	}

	// Reuse the newest session for this workspace unless --new or an explicit session ID was supplied.
	if sessionID == "" && !opts.ForceNew && ws != "" {
		if sessions, listErr := ad.ListSessions(ws, 1); listErr == nil && len(sessions) > 0 {
			sessionID = sessions[0].ID
		}
	}

	msg := opts.Message
	if strings.TrimSpace(msg) == "-" {
		if in == nil {
			return fmt.Errorf("stdin reader is unavailable for -m -")
		}
		raw, readErr := io.ReadAll(in)
		if readErr != nil {
			return fmt.Errorf("failed to read message from stdin: %w", readErr)
		}
		msg = string(raw)
	}
	msg = strings.TrimSpace(msg)

	finalPrompt := msg
	if strings.TrimSpace(opts.Template) != "" {
		if promptMgr == nil {
			promptMgr = prompt.NewDefaultManager()
		}
		rendered, renderErr := promptMgr.Render(opts.Template, prompt.TemplateData{
			Message:   msg,
			FromAgent: strings.TrimSpace(opts.From),
			ToAgent:   string(agType),
			ToSession: sessionID,
		})
		if renderErr != nil {
			return renderErr
		}
		finalPrompt = rendered
	}
	if strings.TrimSpace(finalPrompt) == "" {
		return fmt.Errorf("a prompt message (-m / positional text) or template (-t) is required")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultAskTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	fromAgent := adapter.AgentCLI
	fromSession := strings.TrimSpace(opts.From)
	if fromSession != "" {
		if parts := strings.SplitN(fromSession, ":", 2); len(parts) == 2 {
			if parsed, parseErr := adapter.ParseAgentType(parts[0]); parseErr == nil {
				fromAgent = parsed
				fromSession = strings.TrimSpace(parts[1])
			}
		} else if parsed, parseErr := adapter.ParseAgentType(fromSession); parseErr == nil {
			fromAgent = parsed
			fromSession = ""
		}
	}

	tx := adapter.Transmission{
		ID:          uuid.NewString(),
		LinkName:    "ask",
		FromAgent:   fromAgent,
		FromSession: fromSession,
		ToAgent:     agType,
		ToSession:   sessionID,
		UserPrompt:  finalPrompt,
		Timestamp:   time.Now().Unix(),
	}

	resp, askErr := asker.Ask(runCtx, adapter.AskRequest{
		SessionID: sessionID,
		Workspace: ws,
		Prompt:    finalPrompt,
	})
	if askErr != nil {
		tx.Status = adapter.TransmissionFailed
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			askErr = fmt.Errorf("execution timed out after %v", timeout)
		}
		tx.Error = askErr.Error()
		if histStore != nil {
			_ = histStore.Append(&tx)
		}
		return fmt.Errorf("ask %s failed: %w", agType, askErr)
	}

	if resp.SessionID != "" {
		tx.ToSession = resp.SessionID
	}
	tx.Body = resp.Output
	tx.Status = adapter.TransmissionDelivered
	if histStore != nil {
		if logErr := histStore.Append(&tx); logErr != nil && errOut != nil {
			fmt.Fprintf(errOut, "Warning: failed to persist audit log: %v\n", logErr)
		}
	}

	if opts.AsJSON {
		payload := askJSONOutput{
			Agent:          agType,
			SessionID:      tx.ToSession,
			Workspace:      ws,
			TransmissionID: tx.ID,
			Output:         resp.Output,
		}
		data, marshalErr := json.MarshalIndent(payload, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		_, err = fmt.Fprintln(out, string(data))
		return err
	}

	if errOut != nil {
		targetLabel := string(agType)
		if tx.ToSession != "" {
			targetLabel = fmt.Sprintf("%s:%s", agType, adapter.ShortID(tx.ToSession))
		}
		fmt.Fprintf(errOut, "[relay ask] session=%s tx=%s\n", targetLabel, adapter.ShortID(tx.ID))
	}
	_, err = fmt.Fprintln(out, resp.Output)
	return err
}

func newAskCmd() *cobra.Command {
	var (
		wsFlag       string
		templateFlag string
		msgFlag      string
		forceNew     bool
		fromFlag     string
		timeoutFlag  time.Duration
		asJSON       bool
	)

	askCmd := &cobra.Command{
		Use:   "ask <agent[:session_id]> [-w <workspace>] [-t <template>] [-m <message>]",
		Short: "Send a direct, linkless prompt to a headless agent and print its reply to stdout",
		Long: `Sends a prompt directly to a headless-capable target agent (qoder, claude, codex, pi)
without requiring a pre-existing bidirectional link or source session adapter.

When given a bare agent name (e.g. 'qoder'), Relay automatically reuses the latest session
for the target workspace (or creates a new session if none exists). Pass --new to force a
fresh session, or pass '<agent>:<session_id>' to resume an exact session across review rounds.`,
		Example: `  # Round 1: peer-review a worktree with Qoder (auto-reuses or creates Qoder session)
  relay ask qoder -w .worktrees/fix-auth -t review -m "Review auth token refresh fix"

  # Round 2: continue the exact reviewer session returned on stderr
  relay ask qoder:2a037e6d -t followup -m "Fixed B1 and B2; go test -race passes"

  # Ask Claude Code or Codex directly in a fresh session
  relay ask claude --new -w . -m "Check for nil pointer edge cases in pkg/steer"
  relay ask codex -w . -t review -m "Verify concurrency safety"

  # Structured JSON output for scripts and orchestrators
  relay ask qoder -w . -m "Summarize git diff" --json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg := msgFlag
			if strings.TrimSpace(msg) == "" && len(args) > 1 {
				msg = strings.Join(args[1:], " ")
			}

			histStore, err := history.NewDefaultStore()
			if err != nil {
				return err
			}

			return executeAsk(
				cmd.Context(),
				cmd.InOrStdin(),
				cmd.OutOrStdout(),
				cmd.ErrOrStderr(),
				registry.Default,
				histStore,
				prompt.NewDefaultManager(),
				askOptions{
					Target:    args[0],
					Workspace: wsFlag,
					Template:  templateFlag,
					Message:   msg,
					ForceNew:  forceNew,
					From:      fromFlag,
					Timeout:   resolveAskTimeout(timeoutFlag, os.Getenv),
					AsJSON:    asJSON,
				},
			)
		},
	}

	askCmd.Flags().StringVarP(&wsFlag, "workspace", "w", "", "Workspace/worktree directory for target agent (defaults to cwd)")
	askCmd.Flags().StringVarP(&templateFlag, "template", "t", "", "Prompt template name (e.g. review, followup, supervise) or file path")
	askCmd.Flags().StringVarP(&msgFlag, "message", "m", "", "Prompt message (use '-' to read from stdin)")
	askCmd.Flags().BoolVar(&forceNew, "new", false, "Force a fresh target session instead of reusing the latest workspace session")
	askCmd.Flags().StringVar(&fromFlag, "from", "", "Optional caller label recorded in audit history (e.g. claude:652e7f08 or agy)")
	askCmd.Flags().DurationVar(&timeoutFlag, "timeout", 0, "Execution timeout override (defaults to RELAY_DELIVERY_TIMEOUT or 600s)")
	askCmd.Flags().BoolVar(&asJSON, "json", false, "Output structured JSON (agent, session_id, workspace, transmission_id, output)")

	return askCmd
}
