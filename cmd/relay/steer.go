package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"

	"github.com/kamusis/relay/pkg/adapter"
	"github.com/kamusis/relay/pkg/steer"
)

func executeSteerEnqueue(
	out, errOut io.Writer,
	store *steer.Store,
	homeDir string,
	target, body, from string,
	blocking bool,
) error {
	sev := steer.SeverityAdvisory
	if blocking {
		sev = steer.SeverityBlocking
	}
	msg, err := store.Enqueue(steer.Message{
		Target:   target,
		From:     from,
		Severity: sev,
		Body:     body,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Steer queued.\n")
	fmt.Fprintf(out, "  ID:       %s\n", msg.ID)
	fmt.Fprintf(out, "  Target:   %s\n", msg.Target)
	fmt.Fprintf(out, "  Severity: %s\n", msg.Severity)
	if msg.From != "" {
		fmt.Fprintf(out, "  From:     %s\n", msg.From)
	}
	fmt.Fprintf(out, "  Message:  %s\n", runewidth.Truncate(strings.ReplaceAll(msg.Body, "\n", " "), 100, "..."))
	if errOut != nil {
		if warn := steer.MissingHookWarning(msg.Target, homeDir); warn != "" {
			fmt.Fprintln(errOut, warn)
		}
	}
	return nil
}

func executeSteerHookInstall(
	out io.Writer,
	agentSelector, scope, homeDir, projectDir string,
	uninstall, statusOnly bool,
) error {
	if uninstall && statusOnly {
		return fmt.Errorf("--uninstall and --status cannot be used together")
	}
	specs, err := steer.ResolveHookAgents(agentSelector)
	if err != nil {
		return err
	}

	for _, spec := range specs {
		settingsPath, err := steer.HookSettingsPath(spec, scope, homeDir, projectDir)
		if err != nil {
			return err
		}
		switch {
		case statusOnly:
			state := "not installed"
			if steer.IsHookInstalled(settingsPath) {
				state = "installed"
			}
			fmt.Fprintf(out, "%-8s  %-13s  %s\n", spec.Agent, state, settingsPath)
		case uninstall:
			removed, err := steer.UninstallHook(settingsPath)
			if err != nil {
				return err
			}
			if removed {
				fmt.Fprintf(out, "Removed relay steer PostToolUse hook for %s from %s\n", spec.Agent, settingsPath)
			} else {
				fmt.Fprintf(out, "No relay steer PostToolUse hook found for %s in %s\n", spec.Agent, settingsPath)
			}
		default:
			changed, err := steer.InstallHook(settingsPath, spec.Matcher, steer.HookDrainCommand)
			if err != nil {
				return err
			}
			if changed {
				fmt.Fprintf(out, "Installed relay steer PostToolUse hook for %s in %s (takes effect on the next session)\n", spec.Agent, settingsPath)
			} else {
				fmt.Fprintf(out, "Relay steer PostToolUse hook is already installed for %s in %s\n", spec.Agent, settingsPath)
			}
		}
	}
	return nil
}

func executeSteerDrain(
	in io.Reader,
	out io.Writer,
	store *steer.Store,
	target, workspace, format string,
	readHookStdin bool,
	getenv func(string) string,
) error {
	// Validate output format before draining so invalid flags never consume pending directives.
	normFormat := strings.ToLower(strings.TrimSpace(format))
	if normFormat == "" {
		normFormat = "text"
	}
	if normFormat != "text" && normFormat != "hook-json" {
		return fmt.Errorf("invalid --format '%s': expected 'text' or 'hook-json'", format)
	}

	var hookIn steer.HookInput
	if readHookStdin {
		parsed, err := steer.ParseHookInput(in)
		if err != nil {
			return err
		}
		hookIn = parsed
	}

	targets := steer.ResolveDrainTargets(target, workspace, hookIn, getenv)
	if len(targets) == 0 {
		return nil
	}

	msgs, err := store.Drain(targets)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}

	if normFormat == "hook-json" {
		payload, err := steer.FormatHookJSON(msgs)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(payload))
		return err
	}
	_, err = fmt.Fprintln(out, steer.FormatText(msgs))
	return err
}

// truncateLeftVisual keeps the trailing runes of s within maxWidth terminal columns,
// prefixing "..." when truncated without ever splitting a UTF-8 rune.
func truncateLeftVisual(s string, maxWidth int) string {
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	const ellipsis = "..."
	budget := maxWidth - len(ellipsis)
	if budget <= 0 {
		return ellipsis[:maxWidth]
	}
	runes := []rune(s)
	width := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		w := runewidth.RuneWidth(runes[i])
		if width+w > budget {
			break
		}
		width += w
		start = i
	}
	return ellipsis + string(runes[start:])
}

// resolveSteerTargetAndBody parses target and message body identically for `relay steer` and `relay steer add`.
func resolveSteerTargetAndBody(wsFlag, msgFlag string, args []string) (target, body string, err error) {
	ws := strings.TrimSpace(wsFlag)
	if ws != "" {
		target = steer.WorkspaceTarget(ws)
	} else if len(args) > 0 {
		target = args[0]
	}
	if strings.TrimSpace(target) == "" {
		return "", "", fmt.Errorf("target session ('<agent>:<id>') or worktree path is required")
	}

	body = strings.TrimSpace(msgFlag)
	if body == "" {
		if ws == "" && len(args) > 1 {
			body = strings.TrimSpace(strings.Join(args[1:], " "))
		} else if ws != "" && len(args) > 0 {
			body = strings.TrimSpace(strings.Join(args, " "))
		}
	}
	if body == "" {
		return "", "", fmt.Errorf("flag -m/--message is required")
	}
	return target, body, nil
}

func executeSteerList(
	out io.Writer,
	store *steer.Store,
	target string,
	includeAll, asJSON bool,
) error {
	var queries []string
	if strings.TrimSpace(target) != "" {
		queries = []string{target}
	}
	msgs, err := store.List(queries, includeAll)
	if err != nil {
		return err
	}
	if asJSON {
		if msgs == nil {
			msgs = []steer.Message{}
		}
		data, err := json.MarshalIndent(msgs, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(data))
		return err
	}
	if len(msgs) == 0 {
		fmt.Fprintln(out, "No steering directives found.")
		return nil
	}

	fmt.Fprintf(out, "%s  %s  %s  %s  %s  %s\n",
		runewidth.FillRight("ID", 8),
		runewidth.FillRight("STATUS", 9),
		runewidth.FillRight("SEVERITY", 9),
		runewidth.FillRight("TIME", 8),
		runewidth.FillRight("TARGET", 32),
		"MESSAGE",
	)
	for _, m := range msgs {
		tStr := "-"
		if m.CreatedAt > 0 {
			tStr = time.Unix(m.CreatedAt, 0).Format("15:04:05")
		}
		tgt := truncateLeftVisual(m.Target, 32)
		body := runewidth.Truncate(strings.ReplaceAll(m.Body, "\n", " "), 60, "...")
		fmt.Fprintf(out, "%s  %s  %s  %s  %s  %s\n",
			runewidth.FillRight(adapter.ShortID(m.ID), 8),
			runewidth.FillRight(string(m.Status), 9),
			runewidth.FillRight(string(m.Severity), 9),
			runewidth.FillRight(tStr, 8),
			runewidth.FillRight(tgt, 32),
			body,
		)
	}
	return nil
}

func newSteerCmd() *cobra.Command {
	var (
		msgFlag      string
		fromFlag     string
		blockingFlag bool
		wsFlag       string
	)

	enqueueRunE := func(cmd *cobra.Command, args []string) error {
		target, body, err := resolveSteerTargetAndBody(wsFlag, msgFlag, args)
		if err != nil {
			return err
		}

		store, err := steer.NewDefaultStore()
		if err != nil {
			return err
		}
		homeDir, _ := os.UserHomeDir()
		return executeSteerEnqueue(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, homeDir, target, body, fromFlag, blockingFlag)
	}

	steerCmd := &cobra.Command{
		Use:   "steer <target> -m <message> [--blocking] [--from <sender>]",
		Short: "Queue and drain mid-turn steering directives for hook-capable agents",
		Long: `Enqueues high-signal mid-turn corrections for hook-capable agents (Claude Code, Qoder CLI, Pi)
and drains them atomically inside PostToolUse hooks. Targets may be an agent session
('<agent>:<session_id>') or a workspace/worktree directory path.`,
		Example: `  # Queue a mid-turn correction targeting a feature worktree
  relay steer .worktrees/fix-moc-1385 -m "Use /usr/sbin/ioreg instead of bare ioreg" --blocking

  # Queue a correction targeting a specific Qoder or Claude session
  relay steer qoder:e09937bb -m "Add empty-PATH test assertion"

  # Drain pending steers inside a PostToolUse hook (prints JSON only when non-empty)
  relay steer drain --format hook-json --read-hook-stdin

  # Print PostToolUse hook configuration for Claude Code / Qoder / Pi
  relay steer hook-snippet --agent claude`,
		Args: cobra.ArbitraryArgs,
		RunE: enqueueRunE,
	}
	steerCmd.Flags().StringVarP(&msgFlag, "message", "m", "", "Steering directive content")
	steerCmd.Flags().StringVar(&fromFlag, "from", "", "Optional sender label (e.g. agy:17f5151f or supervisor)")
	steerCmd.Flags().BoolVar(&blockingFlag, "blocking", false, "Mark directive as blocking (sets decision=block in PostToolUse hook JSON)")
	steerCmd.Flags().StringVarP(&wsFlag, "workspace", "w", "", "Target workspace/worktree directory explicitly")

	// Explicit `relay steer add` alias (handy if a workspace folder name collides with a subcommand)
	var (
		addMsg      string
		addFrom     string
		addBlocking bool
		addWS       string
	)
	addCmd := &cobra.Command{
		Use:   "add <target> -m <message>",
		Short: "Enqueue a mid-turn steering directive (explicit alias for 'relay steer')",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, body, err := resolveSteerTargetAndBody(addWS, addMsg, args)
			if err != nil {
				return err
			}
			store, err := steer.NewDefaultStore()
			if err != nil {
				return err
			}
			homeDir, _ := os.UserHomeDir()
			return executeSteerEnqueue(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, homeDir, target, body, addFrom, addBlocking)
		},
	}
	addCmd.Flags().StringVarP(&addMsg, "message", "m", "", "Steering directive content")
	addCmd.Flags().StringVar(&addFrom, "from", "", "Optional sender label")
	addCmd.Flags().BoolVar(&addBlocking, "blocking", false, "Mark directive as blocking")
	addCmd.Flags().StringVarP(&addWS, "workspace", "w", "", "Target workspace/worktree directory explicitly")

	// `relay steer list [target] [--all] [--json]`
	var (
		listAll  bool
		listJSON bool
	)
	listCmd := &cobra.Command{
		Use:     "list [target]",
		Aliases: []string{"ls"},
		Short:   "List pending (or all historical) steering directives",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) == 1 {
				target = args[0]
			}
			store, err := steer.NewDefaultStore()
			if err != nil {
				return err
			}
			return executeSteerList(cmd.OutOrStdout(), store, target, listAll, listJSON)
		},
	}
	listCmd.Flags().BoolVar(&listAll, "all", false, "Include consumed and cleared directives")
	listCmd.Flags().BoolVar(&listJSON, "json", false, "Output directives as JSON")

	// `relay steer drain [--target <t>] [--workspace <dir>] [--format text|hook-json] [--read-hook-stdin]`
	var (
		drainTarget    string
		drainWorkspace string
		drainFormat    string
		drainHookStdin bool
	)
	drainCmd := &cobra.Command{
		Use:   "drain",
		Short: "Atomically consume pending steering directives (designed for PostToolUse hooks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := steer.NewDefaultStore()
			if err != nil {
				return err
			}
			return executeSteerDrain(
				cmd.InOrStdin(),
				cmd.OutOrStdout(),
				store,
				drainTarget,
				drainWorkspace,
				drainFormat,
				drainHookStdin,
				os.Getenv,
			)
		},
	}
	drainCmd.Flags().StringVar(&drainTarget, "target", "", "Explicit target session ('<agent>:<id>') or worktree path")
	drainCmd.Flags().StringVarP(&drainWorkspace, "workspace", "w", "", "Explicit workspace/worktree directory")
	drainCmd.Flags().StringVar(&drainFormat, "format", "text", "Output format: text, hook-json")
	drainCmd.Flags().BoolVar(&drainHookStdin, "read-hook-stdin", false, "Read PostToolUse hook JSON metadata (session_id, cwd) from stdin")

	// `relay steer clear [target] [--id <prefix>]`
	var clearID string
	clearCmd := &cobra.Command{
		Use:   "clear [target]",
		Short: "Cancel pending steering directives without consuming them",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var queries []string
			if len(args) == 1 {
				queries = []string{args[0]}
			}
			store, err := steer.NewDefaultStore()
			if err != nil {
				return err
			}
			n, err := store.Clear(queries, clearID)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Cleared %d pending steering directive(s).\n", n)
			return nil
		},
	}
	clearCmd.Flags().StringVar(&clearID, "id", "", "Clear only the pending directive matching this UUID prefix")

	// `relay steer hook-snippet [--agent claude|qoder|pi]`
	var hookAgent string
	hookSnippetCmd := &cobra.Command{
		Use:   "hook-snippet",
		Short: "Print ready-to-paste PostToolUse hook setup for Claude Code, Qoder CLI, or Pi",
		RunE: func(cmd *cobra.Command, args []string) error {
			snippet, err := steer.HookSnippet(hookAgent)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), snippet)
			return err
		},
	}
	hookSnippetCmd.Flags().StringVar(&hookAgent, "agent", "claude", "Target hook-capable agent: claude, qoder, pi")

	// `relay steer hook-install [--agent claude|qoder|all] [--scope user|project] [--dir <path>] [--uninstall|--status]`
	var (
		installAgent     string
		installScope     string
		installDir       string
		installUninstall bool
		installStatus    bool
	)
	hookInstallCmd := &cobra.Command{
		Use:   "hook-install",
		Short: "Install, inspect, or remove the PostToolUse steering hook in Claude Code / Qoder settings.json",
		Example: `  # Install globally for both Claude Code and Qoder CLI (idempotent, preserves existing hooks)
  relay steer hook-install --agent all

  # Install only for Qoder CLI in user settings (~/.qoder/settings.json)
  relay steer hook-install --agent qoder

  # Install into a specific project (.claude/settings.json)
  relay steer hook-install --agent claude --scope project --dir /path/to/repo

  # Check installation status or remove hook
  relay steer hook-install --status
  relay steer hook-install --agent all --uninstall`,
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, _ := os.UserHomeDir()
			return executeSteerHookInstall(
				cmd.OutOrStdout(),
				installAgent,
				installScope,
				homeDir,
				installDir,
				installUninstall,
				installStatus,
			)
		},
	}
	hookInstallCmd.Flags().StringVar(&installAgent, "agent", "all", "Target agent: claude, qoder, or all")
	hookInstallCmd.Flags().StringVar(&installScope, "scope", "user", "Settings scope: user (~/.<agent>/settings.json) or project (<dir>/.<agent>/settings.json)")
	hookInstallCmd.Flags().StringVar(&installDir, "dir", "", "Project directory when --scope project is used (defaults to cwd)")
	hookInstallCmd.Flags().BoolVar(&installUninstall, "uninstall", false, "Remove the relay steer PostToolUse hook from settings.json")
	hookInstallCmd.Flags().BoolVar(&installStatus, "status", false, "Check whether the relay steer PostToolUse hook is installed")

	steerCmd.AddCommand(addCmd)
	steerCmd.AddCommand(listCmd)
	steerCmd.AddCommand(drainCmd)
	steerCmd.AddCommand(clearCmd)
	steerCmd.AddCommand(hookSnippetCmd)
	steerCmd.AddCommand(hookInstallCmd)
	return steerCmd
}
