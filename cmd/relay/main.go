package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"

	"github.com/kamusis/relay/pkg/adapter"
	"github.com/kamusis/relay/pkg/adapter/antigravity"
	"github.com/kamusis/relay/pkg/adapter/qoder"
	"github.com/kamusis/relay/pkg/history"
	"github.com/kamusis/relay/pkg/link"
	"github.com/kamusis/relay/pkg/prompt"
	"github.com/kamusis/relay/pkg/registry"
)

func init() {
	// Register default agent adapters
	registry.Register(antigravity.New())
	registry.Register(qoder.New())
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "relay",
		Short: "Relay is a universal, cross-agent communication CLI for AI coding assistants",
		Long: `A universal, zero-dependency, stateless cross-agent communication CLI tool
that allows disparate AI coding agents (Antigravity, Qoder, Codex, Claude)
to directly pass messages, reviews, and tasks to each other in the background.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.AddCommand(newSessionCmd())
	rootCmd.AddCommand(newLinkCmd())
	rootCmd.AddCommand(newSendCmd())
	rootCmd.AddCommand(newLogCmd())
	rootCmd.AddCommand(newTemplateCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// formatRelativeTime turns a unix timestamp into human-readable relative duration.
func formatRelativeTime(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	diff := time.Now().Unix() - ts
	if diff < 0 {
		return "just now"
	}
	if diff < 60 {
		return fmt.Sprintf("%ds ago", diff)
	}
	if diff < 3600 {
		return fmt.Sprintf("%dm ago", diff/60)
	}
	if diff < 86400 {
		return fmt.Sprintf("%dh ago", diff/3600)
	}
	return fmt.Sprintf("%dd ago", diff/86400)
}

// ============================================================================
// 1. Session Commands (`relay session list`)
// ============================================================================

func newSessionCmd() *cobra.Command {
	sessionCmd := &cobra.Command{
		Use:   "session",
		Short: "Manage and discover agent conversation sessions",
	}

	var (
		agentFilter string
		workspace   string
		allWS       bool
		noWS        bool
		limit       int
	)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List recent conversation sessions from local agent adapters",
		RunE: func(cmd *cobra.Command, args []string) error {
			if noWS {
				workspace = adapter.WorkspaceNone
			} else if allWS {
				workspace = adapter.WorkspaceAll
			} else if workspace == "" {
				// Default to current working directory
				if cwd, err := os.Getwd(); err == nil {
					workspace = cwd
				}
			}

			var targetAgents []adapter.Adapter
			if agentFilter != "" {
				agType, err := adapter.ParseAgentType(agentFilter)
				if err != nil {
					return err
				}
				ad, err := registry.Get(agType)
				if err != nil {
					return err
				}
				targetAgents = append(targetAgents, ad)
			} else {
				targetAgents = registry.List()
			}

			// Aligned header with explicit visual terminal column widths
			fmt.Printf("%s  %s  %s  %s  %s\n",
				runewidth.FillRight("AGENT", 12),
				runewidth.FillRight("SESSION ID", 36),
				runewidth.FillRight("UPDATED", 9),
				runewidth.FillRight("TITLE", 45),
				"WORKSPACE",
			)

			count := 0
			for _, ad := range targetAgents {
				sessions, err := ad.ListSessions(workspace, limit)
				if err != nil {
					continue
				}
				for _, s := range sessions {
					count++
					agentStr := runewidth.FillRight(string(s.Agent), 12)
					idStr := runewidth.FillRight(s.ID, 36)
					updStr := runewidth.FillRight(formatRelativeTime(s.UpdatedAt), 9)

					// Safely truncate by visual width, preventing multi-byte Chinese corruption
					titleTrunc := runewidth.Truncate(s.Title, 43, "...")
					titleStr := runewidth.FillRight(titleTrunc, 45)

					ws := s.Workspace
					if len(ws) > 42 {
						ws = "..." + ws[len(ws)-39:]
					}

					fmt.Printf("%s  %s  %s  %s  %s\n", agentStr, idStr, updStr, titleStr, ws)
				}
			}

			if count == 0 {
				if noWS {
					fmt.Println("\nNo global / scratch sessions found (without workspace).")
				} else if workspace != "" && workspace != adapter.WorkspaceNone {
					fmt.Printf("\nNo active sessions found for workspace '%s'. Pass --all to list globally.\n", workspace)
				} else {
					fmt.Println("\nNo active sessions found.")
				}
			}
			return nil
		},
	}

	listCmd.Flags().StringVar(&agentFilter, "agent", "", "Filter by agent type (agy, qoder, etc.)")
	listCmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Filter sessions by workspace directory")
	listCmd.Flags().BoolVar(&allWS, "all", false, "List sessions across all workspaces globally")
	listCmd.Flags().BoolVar(&noWS, "no-workspace", false, "List only sessions with no associated workspace (global/scratch)")
	listCmd.Flags().BoolVarP(&noWS, "global", "g", false, "Alias for --no-workspace")
	listCmd.Flags().IntVarP(&limit, "limit", "n", 20, "Maximum number of sessions to return per agent")

	sessionCmd.AddCommand(listCmd)
	return sessionCmd
}

// ============================================================================
// 2. Link Commands (`relay link create/list/rm`)
// ============================================================================

func newLinkCmd() *cobra.Command {
	linkCmd := &cobra.Command{
		Use:   "link",
		Short: "Manage named communication links between agent sessions",
	}

	// 2.1 relay link create <name> <ep1> <ep2>
	var (
		agyFlag   string
		qoderFlag string
	)

	createCmd := &cobra.Command{
		Use:   "create <link_name> <agent1>:<session_id> <agent2>:<session_id>",
		Short: "Create an explicit communication link between two agent sessions",
		Example: `  relay link create moc-qoder agy:85fccbeb qoder:3c58333e
  relay link create pair agy:d4ddbec2 agy:85fccbeb
  relay link create moc-qoder --agy 85fccbeb --qoder 3c58333e`,
		Args: cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			linkName := args[0]

			var ep1Str, ep2Str string
			if len(args) == 3 {
				ep1Str = args[1]
				ep2Str = args[2]
			} else if agyFlag != "" && qoderFlag != "" {
				ep1Str = "agy:" + agyFlag
				ep2Str = "qoder:" + qoderFlag
			} else {
				return fmt.Errorf("must specify two endpoints, e.g.: relay link create %s agy:<id> qoder:<id>", linkName)
			}

			// Parse endpoints
			sessA, err := parseAndResolveEndpoint(ep1Str)
			if err != nil {
				return fmt.Errorf("invalid endpoint 1: %w", err)
			}
			sessB, err := parseAndResolveEndpoint(ep2Str)
			if err != nil {
				return fmt.Errorf("invalid endpoint 2: %w", err)
			}

			store, err := link.NewDefaultStore()
			if err != nil {
				return err
			}

			l := adapter.Link{
				Name:      linkName,
				SessionA:  *sessA,
				SessionB:  *sessB,
				CreatedAt: time.Now().Unix(),
				UpdatedAt: time.Now().Unix(),
			}

			if err := store.Create(l); err != nil {
				return err
			}

			fmt.Printf("Created link '%s':\n", linkName)
			fmt.Printf("  Endpoint A: %s:%s (%s)\n", sessA.Agent, adapter.ShortID(sessA.ID), sessA.Title)
			fmt.Printf("  Endpoint B: %s:%s (%s)\n", sessB.Agent, adapter.ShortID(sessB.ID), sessB.Title)
			return nil
		},
	}
	createCmd.Flags().StringVar(&agyFlag, "agy", "", "Antigravity session ID shorthand")
	createCmd.Flags().StringVar(&qoderFlag, "qoder", "", "Qoder session ID shorthand")

	// 2.2 relay link list
	var refresh bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all established communication links",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := link.NewDefaultStore()
			if err != nil {
				return err
			}

			links, err := store.List()
			if err != nil {
				return err
			}

			if len(links) == 0 {
				fmt.Println("No communication links established. Use 'relay link create <name> <ep1> <ep2>' to create one.")
				return nil
			}

			// Hist store for last relay timestamp lookup
			histStore, _ := history.NewDefaultStore()

			type linkRow struct {
				name        string
				epA         string
				epB         string
				lastRelay   string
				lastRelayTs int64
				createdAt   int64
			}

			maxName := runewidth.StringWidth("LINK")
			maxEPA := runewidth.StringWidth("ENDPOINT A")
			maxEPB := runewidth.StringWidth("ENDPOINT B")

			var rows []linkRow
			for _, l := range links {
				sessA := l.SessionA
				sessB := l.SessionB

				drifted := false
				// Automatically refresh & self-heal endpoint metadata
				if adA, err := registry.Get(sessA.Agent); err == nil {
					if fresh, err := adA.GetSession(sessA.ID); err == nil {
						if sessA.Title != fresh.Title || sessA.Workspace != fresh.Workspace {
							sessA.Title = fresh.Title
							sessA.Workspace = fresh.Workspace
							l.SessionA = sessA
							drifted = true
						}
					} else if refresh && sessA.Title != "[NOT FOUND]" {
						sessA.Title = "[NOT FOUND]"
						l.SessionA = sessA
						drifted = true
					}
				}
				if adB, err := registry.Get(sessB.Agent); err == nil {
					if fresh, err := adB.GetSession(sessB.ID); err == nil {
						if sessB.Title != fresh.Title || sessB.Workspace != fresh.Workspace {
							sessB.Title = fresh.Title
							sessB.Workspace = fresh.Workspace
							l.SessionB = sessB
							drifted = true
						}
					} else if refresh && sessB.Title != "[NOT FOUND]" {
						sessB.Title = "[NOT FOUND]"
						l.SessionB = sessB
						drifted = true
					}
				}
				if drifted {
					_ = store.Update(l)
				}

				lastRelayStr := "-"
				var lastRelayTs int64
				if histStore != nil {
					recent, _ := histStore.List(l.Name, 1)
					if len(recent) > 0 {
						lastRelayTs = recent[0].Timestamp
						lastRelayStr = formatRelativeTime(recent[0].Timestamp)
					}
				}

				titleATrunc := runewidth.Truncate(sessA.Title, 30, "...")
				titleBTrunc := runewidth.Truncate(sessB.Title, 30, "...")
				epADisplay := fmt.Sprintf("%s:%s (%s)", sessA.Agent, adapter.ShortID(sessA.ID), titleATrunc)
				epBDisplay := fmt.Sprintf("%s:%s (%s)", sessB.Agent, adapter.ShortID(sessB.ID), titleBTrunc)

				if w := runewidth.StringWidth(l.Name); w > maxName {
					maxName = w
				}
				if w := runewidth.StringWidth(epADisplay); w > maxEPA {
					maxEPA = w
				}
				if w := runewidth.StringWidth(epBDisplay); w > maxEPB {
					maxEPB = w
				}

				rows = append(rows, linkRow{
					name:        l.Name,
					epA:         epADisplay,
					epB:         epBDisplay,
					lastRelay:   lastRelayStr,
					lastRelayTs: lastRelayTs,
					createdAt:   l.CreatedAt,
				})
			}

			// Sort rows by LAST RELAY descending (newest activity first, fallback to CreatedAt)
			sort.Slice(rows, func(i, j int) bool {
				tI := rows[i].lastRelayTs
				if tI == 0 {
					tI = rows[i].createdAt
				}
				tJ := rows[j].lastRelayTs
				if tJ == 0 {
					tJ = rows[j].createdAt
				}
				if tI != tJ {
					return tI > tJ
				}
				return rows[i].name < rows[j].name
			})

			// Print visually aligned header
			fmt.Printf("%s  %s  %s  %s\n",
				runewidth.FillRight("LINK", maxName),
				runewidth.FillRight("ENDPOINT A", maxEPA),
				runewidth.FillRight("ENDPOINT B", maxEPB),
				"LAST RELAY",
			)

			// Print visually aligned rows
			for _, r := range rows {
				fmt.Printf("%s  %s  %s  %s\n",
					runewidth.FillRight(r.name, maxName),
					runewidth.FillRight(r.epA, maxEPA),
					runewidth.FillRight(r.epB, maxEPB),
					r.lastRelay,
				)
			}
			return nil
		},
	}
	listCmd.Flags().BoolVarP(&refresh, "refresh", "r", false, "Force live verification of session existence across adapters")

	// 2.3 relay link rm <name>
	rmCmd := &cobra.Command{
		Use:     "rm <link_name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove an established link",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := link.NewDefaultStore()
			if err != nil {
				return err
			}
			if err := store.Delete(args[0]); err != nil {
				return err
			}
			fmt.Printf("Link '%s' removed.\n", args[0])
			return nil
		},
	}

	linkCmd.AddCommand(createCmd)
	linkCmd.AddCommand(listCmd)
	linkCmd.AddCommand(rmCmd)
	return linkCmd
}

func parseAndResolveEndpoint(ep string) (*adapter.Session, error) {
	parts := strings.SplitN(ep, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return nil, fmt.Errorf("expected format '<agent>:<session_id>', got '%s'", ep)
	}

	agType, err := adapter.ParseAgentType(parts[0])
	if err != nil {
		return nil, err
	}

	ad, err := registry.Get(agType)
	if err != nil {
		return nil, err
	}

	sessID := strings.TrimSpace(parts[1])
	sess, err := ad.GetSession(sessID)
	if err != nil {
		if errors.Is(err, adapter.ErrAmbiguousSession) {
			return nil, fmt.Errorf("failed to resolve endpoint '%s': %w", ep, err)
		}
		return nil, fmt.Errorf("agent %s session '%s' not found: %w", agType, sessID, err)
	}
	return sess, nil
}

// ============================================================================
// 3. Send Command (`relay send <link_name> --to <target> [prompt]`)
// ============================================================================

func newSendCmd() *cobra.Command {
	var (
		toTarget     string
		msgFlag      string
		templateFlag string
		appendFlag   bool
		forceFlag    bool
	)

	sendCmd := &cobra.Command{
		Use:   "send <link_name> --to <target_agent_or_id> [-m message] [-t template] [--append] [--force]",
		Short: "Relay output or messages between linked agents",
		Example: `  # 1. Direct message mode: sends only the specified message (bypasses transcript & idle checks)
  relay send moc-qoder --to qoder -m "请对 commit 28ef9212 进行代码复审"

  # 2. Template-driven review mode: invokes /verification-before-completion
  relay send moc-qoder --to qoder -t review -m "工单: 拆解前端弹窗\nWorktree: .worktrees/split-dialog"

  # 3. Pure automatic forwarding: extracts latest output from counterpart
  relay send moc-qoder --to agy

  # 4. Forward and append: extracts latest output and annotates with directive
  relay send moc-qoder --to agy -m "重点检查内存泄露" --append`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			linkName := args[0]
			if strings.TrimSpace(toTarget) == "" {
				return fmt.Errorf("flag --to is required for link '%s'", linkName)
			}

			// Determine message: flag -m takes precedence, fallback to positional args
			message := strings.TrimSpace(msgFlag)
			if message == "" && len(args) > 1 {
				message = strings.TrimSpace(strings.Join(args[1:], " "))
			}

			// 1. Load Link
			store, err := link.NewDefaultStore()
			if err != nil {
				return err
			}
			l, err := store.Get(linkName)
			if err != nil {
				return err
			}

			// 2. Resolve source and target sessions
			srcSess, tgtSess, err := l.ResolveEndpoints(toTarget)
			if err != nil {
				return err
			}

			srcAdapter, err := registry.Get(srcSess.Agent)
			if err != nil {
				return err
			}
			tgtAdapter, err := registry.Get(tgtSess.Agent)
			if err != nil {
				return err
			}

			// 3. Pre-flight & Opportunistic Cache Self-Healing
			freshSrc, err := srcAdapter.GetSession(srcSess.ID)
			if err != nil {
				return fmt.Errorf("source endpoint '%s:%s' in link '%s' was not found: %w\nHint: run 'relay link rm %s' to clean up stale link",
					srcSess.Agent, adapter.ShortID(srcSess.ID), linkName, err, linkName)
			}
			freshTgt, err := tgtAdapter.GetSession(tgtSess.ID)
			if err != nil {
				return fmt.Errorf("target endpoint '%s:%s' in link '%s' was not found: %w\nHint: run 'relay link rm %s' to clean up stale link",
					tgtSess.Agent, adapter.ShortID(tgtSess.ID), linkName, err, linkName)
			}

			// Check for metadata drift and update cache
			drifted := false
			if freshSrc.Title != srcSess.Title || freshSrc.Workspace != srcSess.Workspace {
				srcSess.Title = freshSrc.Title
				srcSess.Workspace = freshSrc.Workspace
				drifted = true
			}
			if freshTgt.Title != tgtSess.Title || freshTgt.Workspace != tgtSess.Workspace {
				tgtSess.Title = freshTgt.Title
				tgtSess.Workspace = freshTgt.Workspace
				drifted = true
			}
			if drifted {
				if l.SessionA.ID == freshSrc.ID {
					l.SessionA.Title = freshSrc.Title
					l.SessionA.Workspace = freshSrc.Workspace
				} else if l.SessionA.ID == freshTgt.ID {
					l.SessionA.Title = freshTgt.Title
					l.SessionA.Workspace = freshTgt.Workspace
				}

				if l.SessionB.ID == freshSrc.ID {
					l.SessionB.Title = freshSrc.Title
					l.SessionB.Workspace = freshSrc.Workspace
				} else if l.SessionB.ID == freshTgt.ID {
					l.SessionB.Title = freshTgt.Title
					l.SessionB.Workspace = freshTgt.Workspace
				}

				_ = store.Update(*l)
			}

			// 4. Determine transmission payload based on mode
			var body string
			var directive string

			tmplData := prompt.TemplateData{
				Message:     message,
				Link:        linkName,
				FromAgent:   string(srcSess.Agent),
				FromSession: freshSrc.ID,
				ToAgent:     string(tgtSess.Agent),
				ToSession:   freshTgt.ID,
			}
			promptMgr := prompt.NewDefaultManager()

			if (message != "" || templateFlag != "") && !appendFlag {
				// Mode 1: Direct Message (-m "...", and/or -t <template>, without --append)
				// Sends message/template directly as transmission body without touching source transcript.
				if templateFlag != "" {
					rendered, err := promptMgr.Render(templateFlag, tmplData)
					if err != nil {
						return fmt.Errorf("failed to render template: %w", err)
					}
					body = rendered
					fmt.Printf("Relaying message with template '%s' to %s (%s)...\n",
						templateFlag, tgtSess.Agent, adapter.ShortID(freshTgt.ID))
				} else {
					body = message
					fmt.Printf("Relaying direct message to %s (%s)...\n",
						tgtSess.Agent, adapter.ShortID(freshTgt.ID))
				}
			} else {
				// Mode 2: Pure Forwarding (no message, no template) OR Mode 3: Forward + Append (-m "..." --append)
				output, err := srcAdapter.GetLatestAssistantOutput(freshSrc.ID)
				if err != nil {
					if errors.Is(err, adapter.ErrTurnInProgress) && !forceFlag {
						return fmt.Errorf("source agent '%s' (session %s) is currently busy generating output\nHint: please wait for the turn to complete, or use -m \"<message>\" to send a direct message without extracting transcript",
							srcSess.Agent, adapter.ShortID(freshSrc.ID))
					} else if !errors.Is(err, adapter.ErrTurnInProgress) {
						return fmt.Errorf("failed to extract output from source session: %w", err)
					}
				}
				if strings.TrimSpace(output) == "" {
					return fmt.Errorf("extracted output from source session '%s:%s' is empty\nHint: ensure the source agent has finished generating output, or use -m \"<message>\" to send a direct message",
						srcSess.Agent, adapter.ShortID(freshSrc.ID))
				}
				body = output
				directive = message
				if templateFlag != "" {
					tmplData.Output = output
					rendered, err := promptMgr.Render(templateFlag, tmplData)
					if err != nil {
						return fmt.Errorf("failed to render template: %w", err)
					}
					directive = rendered
				}
				fmt.Printf("Relaying latest output from %s (%s) -> %s (%s)...\n",
					srcSess.Agent, adapter.ShortID(freshSrc.ID), tgtSess.Agent, adapter.ShortID(freshTgt.ID))
			}

			// 5. Construct Transmission
			tx := adapter.Transmission{
				ID:          uuid.NewString(),
				LinkName:    linkName,
				FromAgent:   srcSess.Agent,
				FromSession: freshSrc.ID,
				ToAgent:     tgtSess.Agent,
				ToSession:   freshTgt.ID,
				UserPrompt:  directive,
				Body:        body,
				Timestamp:   time.Now().Unix(),
			}

			// 6. Inject payload into target session
			timeout := 300 * time.Second
			if custom := os.Getenv("RELAY_DELIVERY_TIMEOUT"); custom != "" {
				if d, err := time.ParseDuration(custom); err == nil && d > 0 {
					timeout = d
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			fmt.Printf("Waiting for %s to execute turn (this may take up to a minute for multi-tool tasks)...\n", tgtSess.Agent)

			deliverErr := tgtAdapter.Deliver(ctx, tx)
			if deliverErr != nil {
				tx.Status = adapter.TransmissionFailed
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					deliverErr = fmt.Errorf("execution timed out after %v", timeout)
				}
				tx.Error = deliverErr.Error()
			} else {
				tx.Status = adapter.TransmissionDelivered
			}

			// 7. Audit Log persistence
			histStore, err := history.NewDefaultStore()
			if err == nil {
				if appendErr := histStore.Append(&tx); appendErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to persist audit log: %v\n", appendErr)
				}
			}

			if deliverErr != nil {
				return fmt.Errorf("delivery failed to %s: %w", tgtSess.Agent, deliverErr)
			}

			// Print snippet preview
			snippet := strings.ReplaceAll(body, "\n", " ")
			snippet = runewidth.Truncate(snippet, 80, "...")

			fmt.Println("Relay delivered successfully.")
			fmt.Printf("  Transmission ID: %s\n", tx.ID)
			fmt.Printf("  From:            %s (session %s)\n", srcSess.Agent, adapter.ShortID(freshSrc.ID))
			fmt.Printf("  To:              %s (session %s)\n", tgtSess.Agent, adapter.ShortID(freshTgt.ID))
			if directive != "" {
				fmt.Printf("  Directive:       %s\n", directive)
			}
			fmt.Printf("  Relayed Body:    %s (%d bytes)\n", snippet, len(body))
			return nil
		},
	}

	sendCmd.Flags().StringVar(&toTarget, "to", "", "Target agent (agy, qoder) or session ID in the link")
	sendCmd.Flags().StringVarP(&msgFlag, "message", "m", "", "Explicit message content to deliver directly")
	sendCmd.Flags().StringVarP(&templateFlag, "template", "t", "", "Optional prompt template name or path (e.g. 'review', 'followup')")
	sendCmd.Flags().BoolVar(&appendFlag, "append", false, "Append message to the latest output extracted from source session")
	sendCmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "Bypass busy state checks on source session when extracting output")
	_ = sendCmd.MarkFlagRequired("to")
	return sendCmd
}

// ============================================================================
// 4. Template Commands (`relay template list`, `relay template show <name>`)
// ============================================================================

func newTemplateCmd() *cobra.Command {
	templateCmd := &cobra.Command{
		Use:   "template",
		Short: "Manage and preview prompt templates for relay transmissions",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all available prompt templates (built-in and custom)",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr := prompt.NewDefaultManager()
			templates, err := mgr.List()
			if err != nil {
				return err
			}

			if len(templates) == 0 {
				fmt.Println("No templates found.")
				return nil
			}

			maxName := 12
			maxSrc := 10
			for _, t := range templates {
				if len(t.Name) > maxName {
					maxName = len(t.Name)
				}
				if len(t.Source) > maxSrc {
					maxSrc = len(t.Source)
				}
			}

			fmt.Printf("%s  %s  %s\n",
				runewidth.FillRight("NAME", maxName),
				runewidth.FillRight("SOURCE", maxSrc),
				"DESCRIPTION",
			)

			for _, t := range templates {
				fmt.Printf("%s  %s  %s\n",
					runewidth.FillRight(t.Name, maxName),
					runewidth.FillRight(t.Source, maxSrc),
					t.Description,
				)
			}
			return nil
		},
	}

	showCmd := &cobra.Command{
		Use:   "show <template_name_or_file>",
		Short: "Display the content and metadata of a prompt template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr := prompt.NewDefaultManager()
			t, err := mgr.Get(args[0])
			if err != nil {
				return err
			}

			fmt.Printf("Template:    %s\n", t.Name)
			fmt.Printf("Source:      %s\n", t.Source)
			fmt.Printf("Description: %s\n", t.Description)
			fmt.Println("\n--- Template Content ---")
			fmt.Println(t.Content)
			return nil
		},
	}

	templateCmd.AddCommand(listCmd)
	templateCmd.AddCommand(showCmd)
	return templateCmd
}

// ============================================================================
// 4. Log Command (`relay log [link_name]`)
// ============================================================================

func newLogCmd() *cobra.Command {
	var (
		limit  int
		showID string
	)

	logCmd := &cobra.Command{
		Use:   "log [link_name]",
		Short: "Inspect cross-agent transmission audit history",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			histStore, err := history.NewDefaultStore()
			if err != nil {
				return err
			}

			if showID != "" {
				tx, err := histStore.Get(showID)
				if err != nil {
					return err
				}
				fmt.Printf("Transmission: %s\n", tx.ID)
				statusStr := string(tx.Status)
				if statusStr == "" {
					statusStr = "-"
				}
				fmt.Printf("Status:       %s\n", statusStr)
				if tx.Error != "" {
					fmt.Printf("Error:        %s\n", tx.Error)
				}
				fmt.Printf("Link:         %s\n", tx.LinkName)
				fmt.Printf("Direction:    %s (%s) -> %s (%s)\n",
					tx.FromAgent, adapter.ShortID(tx.FromSession), tx.ToAgent, adapter.ShortID(tx.ToSession))
				fmt.Printf("Time:         %s\n", time.Unix(tx.Timestamp, 0).Format("2006-01-02 15:04:05"))
				if tx.UserPrompt != "" {
					fmt.Printf("Directive:    %s\n", tx.UserPrompt)
				}
				fmt.Println("\n--- Relayed Payload ---")
				fmt.Println(tx.Body)
				return nil
			}

			var linkFilter string
			if len(args) == 1 {
				linkFilter = args[0]
			}

			records, err := histStore.List(linkFilter, limit)
			if err != nil {
				return err
			}

			if len(records) == 0 {
				if linkFilter != "" {
					fmt.Printf("No transmissions logged for link '%s'.\n", linkFilter)
				} else {
					fmt.Println("No transmissions logged.")
				}
				return nil
			}

			fmt.Printf("%s  %s  %s  %s  %s  %s\n",
				runewidth.FillRight("TIME", 19),
				runewidth.FillRight("LINK", 12),
				runewidth.FillRight("DIRECTION", 15),
				runewidth.FillRight("STATUS", 10),
				runewidth.FillRight("PROMPT", 20),
				"BODY SNIPPET",
			)

			for _, tx := range records {
				tStr := time.Unix(tx.Timestamp, 0).Format("2006-01-02 15:04:05")
				dir := fmt.Sprintf("%s -> %s", tx.FromAgent, tx.ToAgent)
				statusStr := string(tx.Status)
				if statusStr == "" {
					statusStr = "-"
				}
				prompt := runewidth.Truncate(tx.UserPrompt, 18, "...")
				snippet := strings.ReplaceAll(tx.Body, "\n", " ")
				snippet = runewidth.Truncate(snippet, 35, "...")

				fmt.Printf("%s  %s  %s  %s  %s  %s\n",
					runewidth.FillRight(tStr, 19),
					runewidth.FillRight(tx.LinkName, 12),
					runewidth.FillRight(dir, 15),
					runewidth.FillRight(statusStr, 10),
					runewidth.FillRight(prompt, 20),
					snippet,
				)
			}
			return nil
		},
	}

	logCmd.Flags().IntVarP(&limit, "limit", "n", 20, "Number of recent transmissions to display")
	logCmd.Flags().StringVar(&showID, "show", "", "Show full payload for a transmission UUID")
	return logCmd
}
