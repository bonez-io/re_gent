package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/insight/search"
	"github.com/bonez-io/re_gent/internal/style"
	"github.com/spf13/cobra"
)

// WorkCmd creates the work command: list and inspect work items.
func WorkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "work",
		Short: "List and inspect work items read from recorded sessions",
		Long: `List and inspect work items read from recorded sessions.

A work item is a run of turns in one session in pursuit of one goal: what was
wanted, how it went, what is true at the end, and whether it finished. Items
are produced by the insight worker (see rgt insight) and link to the steps,
files, and entities that built them, so every claim can be checked with
rgt show. In server mode the items live on the server and are read from it.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(workListCmd(), workShowCmd())
	return cmd
}

func workListCmd() *cobra.Command {
	var status, session string
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List work items, newest first",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if status != "" && !index.ValidWorkItemStatus(status) {
				return fmt.Errorf("status %q: want wip, done, failed, abandoned, or superseded", status)
			}
			var items []search.WorkItem
			if client, ok, err := insightServerClient(); err != nil {
				return err
			} else if ok {
				q := url.Values{}
				q.Set("status", status)
				q.Set("session", session)
				q.Set("limit", strconv.Itoa(limit))
				if err := client.APIGet(cmd.Context(), "work?"+q.Encode(), &items); err != nil {
					return err
				}
			} else {
				s, err := openStoreFromCWD()
				if err != nil {
					return err
				}
				idx, err := index.Open(s)
				if err != nil {
					return err
				}
				defer func() { _ = idx.Close() }()
				rows, err := idx.ListWorkItems(index.WorkItemFilter{Status: status, SessionID: session, Limit: limit})
				if err != nil {
					return err
				}
				items = search.DescribeAll(idx, rows)
			}
			out := cmd.OutOrStdout()
			if asJSON {
				if items == nil {
					items = []search.WorkItem{}
				}
				return json.NewEncoder(out).Encode(items)
			}
			if len(items) == 0 {
				fmt.Fprintln(out, "No work items yet. Check `rgt insight status`.")
				return nil
			}
			for _, w := range items {
				printWorkItemLine(out, w)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "only items with this status (wip, done, failed, abandoned, superseded)")
	cmd.Flags().StringVar(&session, "session", "", "only items from this session")
	cmd.Flags().IntVar(&limit, "limit", 20, "maximum items to show")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func workShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "show <work-item-id>",
		Short:        "Show one work item with its entities, files, and steps",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var item search.WorkItem
			if client, ok, err := insightServerClient(); err != nil {
				return err
			} else if ok {
				if err := client.APIGet(cmd.Context(), "work/"+url.PathEscape(args[0]), &item); err != nil {
					return err
				}
			} else {
				s, err := openStoreFromCWD()
				if err != nil {
					return err
				}
				idx, err := index.Open(s)
				if err != nil {
					return err
				}
				defer func() { _ = idx.Close() }()
				w, ok, err := idx.GetWorkItem(args[0])
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("no work item %q", args[0])
				}
				item = search.Describe(idx, w)
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(item)
			}
			printWorkItem(out, item)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func printWorkItemLine(w io.Writer, item search.WorkItem) {
	fmt.Fprintf(w, "%s  %-10s %s\n", style.DimText(shortID(item.ID)), styleStatus(item.Status), item.Goal)
	fmt.Fprintf(w, "            %s\n", style.DimText(fmt.Sprintf("%s · session %s · %s", item.StartTS.Local().Format("2006-01-02 15:04"), item.SessionID, readingStamp(item))))
}

func printWorkItem(w io.Writer, item search.WorkItem) {
	fmt.Fprintf(w, "%s %s\n", style.Success("Work item"), item.ID)
	fmt.Fprintf(w, "  status    %s", styleStatus(item.Status))
	if item.Open {
		fmt.Fprintf(w, " %s", style.DimText("(open: the session's next turn extends it)"))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  session   %s (%s)\n", item.SessionID, item.Origin)
	span := item.StartTS.Local().Format("2006-01-02 15:04")
	if item.EndTS != nil {
		span += " → " + item.EndTS.Local().Format("2006-01-02 15:04")
	}
	fmt.Fprintf(w, "  when      %s\n", span)
	if item.ContinuesWorkItemID != "" {
		fmt.Fprintf(w, "  continues %s\n", item.ContinuesWorkItemID)
	}
	fmt.Fprintf(w, "  reading   %s\n", style.DimText(readingStamp(item)))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s\n  %s\n\n", style.Success("Goal"), wrap(item.Goal, "  "))
	if item.Approach != "" {
		fmt.Fprintf(w, "%s\n  %s\n\n", style.Success("Approach"), wrap(item.Approach, "  "))
	}
	if item.Outcome != "" {
		fmt.Fprintf(w, "%s\n  %s\n\n", style.Success("Outcome"), wrap(item.Outcome, "  "))
	}

	if len(item.Entities) > 0 {
		fmt.Fprintln(w, style.Success("Entities"))
		for _, l := range item.Entities {
			ref := ""
			if l.Ref != "" && l.Ref != l.Name {
				ref = "  " + style.DimText(l.Ref)
			}
			fmt.Fprintf(w, "  %-14s %s%s\n", l.Type, l.Name, ref)
			fmt.Fprintf(w, "  %-14s %s\n", "", style.DimText(fmt.Sprintf("%s · %.0f%% · %s · evidence %s", l.Role, l.Confidence*100, l.Source, shortID(l.Evidence))))
		}
		fmt.Fprintln(w)
	}

	if len(item.Files) > 0 {
		fmt.Fprintln(w, style.Success("Files changed"))
		for _, f := range item.Files {
			fmt.Fprintf(w, "  %s\n", f)
		}
		fmt.Fprintln(w)
	}

	steps := item.Steps
	switch len(steps) {
	case 0:
		fmt.Fprintf(w, "%s none (the turns used no tools)\n", style.Success("Steps"))
	case 1:
		fmt.Fprintf(w, "%s %s  %s\n", style.Success("Steps"), shortID(steps[0]), style.DimText("rgt show "+shortID(steps[0])))
	default:
		fmt.Fprintf(w, "%s %d, %s → %s  %s\n", style.Success("Steps"), len(steps), shortID(steps[0]), shortID(steps[len(steps)-1]),
			style.DimText(fmt.Sprintf("rgt show %s", shortID(steps[len(steps)-1]))))
	}
}

func styleStatus(status string) string {
	switch status {
	case index.WorkItemDone:
		return style.Success(status)
	case index.WorkItemWIP:
		return style.Warning(status)
	case index.WorkItemFailed:
		return style.Error(status)
	}
	return style.DimText(status)
}

func readingStamp(item search.WorkItem) string {
	return fmt.Sprintf("read by %s %s, prompt v%s, %s", item.Provider, item.Model, item.PromptVersion, item.UpdatedAt.Local().Format(time.RFC3339))
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func wrap(text, indent string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), "\n", "\n"+indent)
}

// withTimeout is the budget for one server call from these commands.
func withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 60*time.Second)
}
