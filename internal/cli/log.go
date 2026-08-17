package cli

import (
	"fmt"
	"os"

	"github.com/regent-vcs/regent/internal/index"
	"github.com/spf13/cobra"
)

// LogCmd creates the log command
func LogCmd() *cobra.Command {
	var sessionID string
	var limit int
	var oneline bool
	var jsonOut bool
	var stat bool
	var conversation bool
	var files bool
	var conversationOnly bool
	var filesOnly bool
	var graph bool

	cmd := &cobra.Command{
		Use:          "log [session-id]",
		Short:        "Show step history",
		Long:         "Display steps in reverse-chronological order with tool names and files affected.",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}

			idx, err := index.Open(s)
			if err != nil {
				return err
			}
			defer func() { _ = idx.Close() }()

			// Parse session ID from args or flag or auto-detect
			if len(args) > 0 {
				sessionID = args[0]
			}

			if sessionID == "" {
				sessions, err := idx.ListHeadedSessions()
				if err != nil {
					return err
				}
				if len(sessions) == 0 {
					return explainEmptyLog(idx, "")
				}
				sessionID = sessions[0].ID
			}

			// Handle filter flags
			if conversationOnly && filesOnly {
				return fmt.Errorf("--conversation-only and --files-only are mutually exclusive")
			}

			// Default: show conversation (unless explicitly disabled or files-only set)
			if !cmd.Flags().Changed("conversation") && !filesOnly {
				conversation = true
			}

			// Apply filters
			if conversationOnly {
				conversation = true
				files = false
			} else if filesOnly {
				conversation = false
				files = true
			}

			steps, err := idx.ListSteps(sessionID, limit)
			if err != nil {
				return err
			}

			if len(steps) == 0 {
				return explainEmptyLog(idx, sessionID)
			}

			// Enrich steps with files, args, results, and optionally file diffs and graph
			enriched, err := enrichSteps(s, steps, files, graph)
			if err != nil {
				return fmt.Errorf("enrich steps: %w", err)
			}

			// Determine formatter
			var formatter LogFormatter
			if oneline {
				formatter = &OnelineFormatter{}
			} else if jsonOut {
				formatter = &JSONFormatter{}
			} else if stat {
				formatter = &StatFormatter{}
			} else {
				formatter = &DefaultFormatter{}
			}

			// Format and output
			return formatter.Format(enriched, sessionID, conversation, files, os.Stdout)
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "Session ID to show (defaults to most recent)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Maximum number of steps to show")
	cmd.Flags().BoolVar(&oneline, "oneline", false, "Show one line per step (compact)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&stat, "stat", false, "Show file statistics")
	cmd.Flags().BoolVar(&conversation, "conversation", false, "Show conversation transcript (deprecated, now default)")
	cmd.Flags().BoolVar(&files, "files", false, "Show file change statistics (deprecated, now default)")
	cmd.Flags().BoolVar(&conversationOnly, "conversation-only", false, "Show only conversation (hide files)")
	cmd.Flags().BoolVar(&filesOnly, "files-only", false, "Show only files (hide conversation)")
	cmd.Flags().BoolVar(&graph, "graph", false, "Show step lineage as ASCII graph")

	return cmd
}

// explainEmptyLog says why there is nothing to print, and points at the command
// that would explain that particular nothing.
//
// log used to answer every empty case with "No sessions found.", because it
// decided by counting *headed* sessions — the ones with at least one step. A
// session that was captured but never completed a turn has no head step, so it
// counted as no session at all, and a user whose hooks had demonstrably just
// run was told capture had never happened and sent to `rgt doctor`. Doctor then
// correctly reports that everything is wired, leaving two commands that
// disagree and no way to tell which is lying.
//
// The two facts have different causes and different remedies:
//
//   - no sessions at all: capture never ran here, which is a wiring question
//     and the one thing doctor exists to answer.
//   - sessions but no steps: capture ran and had nothing to record. Nothing is
//     broken; `rgt sessions` shows what was captured.
//
// sessionID is "" when log was auto-detecting, and the id the user named
// otherwise — a name that is not recorded here is a third fact again, and
// answering it with "recorded no steps" would have them hunting a session that
// was never there.
func explainEmptyLog(idx *index.DB, sessionID string) error {
	sessions, err := idx.ListAllSessions()
	if err != nil {
		return err
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions recorded here.")
		fmt.Println("Nothing has ever been captured in this repository, which usually means no agent hook is wired.")
		fmt.Println("  - Check the wiring: rgt doctor")
		return nil
	}

	if sessionID == "" {
		// ListAllSessions is ordered most-recently-seen first, so this is the
		// session the user just worked in — the one they came here to read.
		sessionID = sessions[0].ID
	} else if !hasSession(sessions, sessionID) {
		fmt.Printf("No session %s recorded here.\n", sessionID)
		fmt.Println("  - Sessions recorded here: rgt sessions")
		return nil
	}

	fmt.Printf("Session %s recorded no steps.\n", sessionID)
	fmt.Println("Capture ran for it, but no turn finished with anything to record.")
	fmt.Println("  - See what was captured: rgt sessions")
	return nil
}

func hasSession(sessions []index.SessionInfo, id string) bool {
	for _, session := range sessions {
		if session.ID == id {
			return true
		}
	}
	return false
}
