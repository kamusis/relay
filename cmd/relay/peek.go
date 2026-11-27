package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/kamusis/relay/pkg/link"
	"github.com/kamusis/relay/pkg/peek"
	"github.com/kamusis/relay/pkg/registry"
	"github.com/kamusis/relay/pkg/steer"
)

func executePeek(
	ctx context.Context,
	out io.Writer,
	reg *registry.Registry,
	linkStore *link.Store,
	steerStore *steer.Store,
	opts peek.Options,
	asJSON bool,
) error {
	report, err := peek.Inspect(ctx, reg, linkStore, steerStore, opts)
	if err != nil {
		return err
	}
	if asJSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to encode peek report JSON: %w", err)
		}
		_, err = fmt.Fprintln(out, string(data))
		return err
	}
	_, err = fmt.Fprint(out, peek.FormatReport(report))
	return err
}

func newPeekCmd() *cobra.Command {
	var (
		includeDiff  bool
		files        []string
		baseRef      string
		commitLimit  int
		maxDiffBytes int
		endpoint     string
		asJSON       bool
	)

	peekCmd := &cobra.Command{
		Use:   "peek [target]",
		Short: "Inspect live worktree progress, git diff, session state, and pending steers",
		Long: `Captures a read-only live progress snapshot for a worktree path, an explicit agent session
('<agent>:<session_id>'), or an established link name. Combines git branch/commits/dirty files/diff,
sibling worktrees, agent turn status (working/idle), and pending steer directives.`,
		Example: `  # Inspect current directory or a specific feature worktree
  relay peek .worktrees/fix-moc-1385
  relay peek .worktrees/fix-moc-1385 --diff -f server/internal/util/platform_darwin.go

  # Inspect a live agent session (even while the turn is still in progress)
  relay peek zed:8878e5d3
  relay peek qoder:e09937bb --diff --json

  # Inspect an established relay link
  relay peek review-mopheus`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) == 1 {
				target = args[0]
			}
			linkStore, _ := link.NewDefaultStore()
			steerStore, _ := steer.NewDefaultStore()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			return executePeek(ctx, cmd.OutOrStdout(), registry.Default, linkStore, steerStore, peek.Options{
				Target:       target,
				Endpoint:     endpoint,
				BaseRef:      baseRef,
				IncludeDiff:  includeDiff,
				Files:        files,
				CommitLimit:  commitLimit,
				MaxDiffBytes: maxDiffBytes,
			}, asJSON)
		},
	}

	peekCmd.Flags().BoolVarP(&includeDiff, "diff", "d", false, "Include unified git diff in output")
	peekCmd.Flags().StringSliceVarP(&files, "file", "f", nil, "Limit diff and diff-stat to specific file path(s)")
	peekCmd.Flags().StringVar(&baseRef, "base", "", "Base git ref for commit and diff comparison (auto-detects main/master)")
	peekCmd.Flags().IntVarP(&commitLimit, "commits", "n", 5, "Maximum number of recent commits to display")
	peekCmd.Flags().IntVar(&maxDiffBytes, "max-diff", 8000, "Maximum bytes of unified diff to output when --diff is set")
	peekCmd.Flags().StringVarP(&endpoint, "endpoint", "e", "", "Endpoint filter when target is a link name")
	peekCmd.Flags().BoolVar(&asJSON, "json", false, "Output structured JSON report")
	return peekCmd
}
