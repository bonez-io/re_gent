package cli

import (
	"fmt"
	"sort"

	"github.com/bonez-io/re_gent/internal/capture"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/bonez-io/re_gent/internal/style"
	"github.com/spf13/cobra"
)

// RepairCmd creates the repair command.
func RepairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Recompute derived data for history already recorded",
		Long: `Recompute derived data for history already recorded.

re_gent derives two things from the same diff, at different times:

  rgt show    diffs at query time, so it always reflects the binary you are
              running. Nothing to repair.
  rgt blame   is annotated at write time and stored beside each (step, file),
              which is what makes a query O(1) — and what makes a map a
              permanent record of the diff that produced it.

Only stored data can go stale, so blame is the only thing repair reaches.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(repairBlameCmd())
	return cmd
}

func repairBlameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "blame",
		Short: "Recompute the stored blame map for every recorded step",
		Long: `Recompute the stored blame map for every recorded step.

Walks each session ref from its root and rewrites the blame sidecar for every
(step, file) using the current diff. History, workspace, and every canonical
object are left untouched — only derived blame maps are rewritten.

Run this after upgrading past a diff fix. Blame maps written before re_gent
corrected LineDiff name the line *after* the one an edit changed, and no amount
of rebuilding the binary fixes a map already on disk.

Interrupting is safe: each map is written atomically, so a half-finished run
leaves every map readable. Just run it again — maps that are already correct are
recognised and left alone, so a second run rewrites nothing.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}

			// Which files changed is the part a user can act on: it names the
			// files whose blame they may have read and believed.
			changedPaths := map[string]bool{}
			repair := &capture.BlameRepair{
				Store: s,
				Progress: func(_ store.Hash, path string, rewritten bool) {
					if rewritten {
						changedPaths[path] = true
					}
				},
			}

			report, runErr := repair.Run(cmd.Context())
			printBlameRepairReport(report, changedPaths)
			return runErr
		},
	}
}

func printBlameRepairReport(report capture.BlameRepairReport, changedPaths map[string]bool) {
	if report.Sessions == 0 {
		fmt.Println("No sessions recorded — nothing to repair.")
		return
	}

	scope := fmt.Sprintf("%s across %s in %s",
		plural(report.Checked(), "blame map"),
		plural(report.Steps, "step"),
		plural(report.Sessions, "session"))

	// A run that repairs nothing has to say so. Exiting 0 in silence reads as
	// "it worked", and the user cannot tell that from "there was nothing to do".
	if report.Rewritten == 0 {
		fmt.Printf("%s %s were already correct.\n", style.Success("Nothing to repair:"), scope)
		return
	}

	fmt.Printf("%s %d rewritten, %d already correct (%s).\n",
		style.Success("Blame repaired:"), report.Rewritten, report.Unchanged, scope)

	paths := make([]string, 0, len(changedPaths))
	for path := range changedPaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	const shown = 10
	for i, path := range paths {
		if i == shown {
			fmt.Printf("  %s\n", style.DimText(fmt.Sprintf("… and %d more", len(paths)-shown)))
			break
		}
		fmt.Printf("  %s\n", path)
	}
}
