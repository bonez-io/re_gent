package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
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
rgt show.`,
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
			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}
			idx, err := index.Open(s)
			if err != nil {
				return err
			}
			defer func() { _ = idx.Close() }()

			if status != "" && !index.ValidWorkItemStatus(status) {
				return fmt.Errorf("status %q: want wip, done, failed, abandoned, or superseded", status)
			}
			items, err := idx.ListWorkItems(index.WorkItemFilter{Status: status, SessionID: session, Limit: limit})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(workItemsJSON(idx, items))
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
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(workItemsJSON(idx, []index.WorkItem{w})[0])
			}
			return printWorkItem(out, idx, w)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func printWorkItemLine(w io.Writer, item index.WorkItem) {
	fmt.Fprintf(w, "%s  %-10s %s\n", style.DimText(shortID(item.ID)), styleStatus(item.Status), item.Goal)
	fmt.Fprintf(w, "            %s\n", style.DimText(fmt.Sprintf("%s · session %s · %s", item.StartTS.Local().Format("2006-01-02 15:04"), item.SessionID, readingStamp(item))))
}

func printWorkItem(w io.Writer, idx *index.DB, item index.WorkItem) error {
	fmt.Fprintf(w, "%s %s\n", style.Success("Work item"), item.ID)
	fmt.Fprintf(w, "  status    %s", styleStatus(item.Status))
	if item.Open() {
		fmt.Fprintf(w, " %s", style.DimText("(open: the session's next turn extends it)"))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  session   %s (%s)\n", item.SessionID, item.Origin)
	span := item.StartTS.Local().Format("2006-01-02 15:04")
	if !item.EndTS.IsZero() {
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

	links, err := idx.WorkItemLinks(item.ID)
	if err != nil {
		return err
	}
	if len(links) > 0 {
		fmt.Fprintln(w, style.Success("Entities"))
		for _, l := range links {
			ref := ""
			if l.Ref != "" && l.Ref != l.Name {
				ref = "  " + style.DimText(l.Ref)
			}
			fmt.Fprintf(w, "  %-14s %s%s\n", l.Type, l.Name, ref)
			fmt.Fprintf(w, "  %-14s %s\n", "", style.DimText(fmt.Sprintf("%s · %.0f%% · %s · evidence %s", l.Role, l.Confidence*100, l.Source, shortID(l.EvidenceStepID))))
		}
		fmt.Fprintln(w)
	}

	files, err := idx.WorkItemFiles(item.ID)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		fmt.Fprintln(w, style.Success("Files changed"))
		for _, f := range files {
			fmt.Fprintf(w, "  %s\n", f)
		}
		fmt.Fprintln(w)
	}

	steps, err := idx.WorkItemSteps(item.ID)
	if err != nil {
		return err
	}
	switch len(steps) {
	case 0:
		fmt.Fprintf(w, "%s none (the turns used no tools)\n", style.Success("Steps"))
	case 1:
		fmt.Fprintf(w, "%s %s  %s\n", style.Success("Steps"), shortID(steps[0]), style.DimText("rgt show "+shortID(steps[0])))
	default:
		fmt.Fprintf(w, "%s %d, %s → %s  %s\n", style.Success("Steps"), len(steps), shortID(steps[0]), shortID(steps[len(steps)-1]),
			style.DimText(fmt.Sprintf("rgt show %s", shortID(steps[len(steps)-1]))))
	}
	return nil
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

func readingStamp(item index.WorkItem) string {
	return fmt.Sprintf("read by %s %s, prompt v%s, %s", item.ModelProvider, item.ModelName, item.PromptVersion, item.UpdatedAt.Local().Format(time.RFC3339))
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

type workItemJSON struct {
	ID                  string           `json:"id"`
	SessionID           string           `json:"session_id"`
	Origin              string           `json:"origin"`
	Status              string           `json:"status"`
	Open                bool             `json:"open"`
	Goal                string           `json:"goal"`
	Approach            string           `json:"approach"`
	Outcome             string           `json:"outcome"`
	ContinuesWorkItemID string           `json:"continues_work_item_id,omitempty"`
	StartStepID         string           `json:"start_step_id,omitempty"`
	EndStepID           string           `json:"end_step_id,omitempty"`
	StartTS             time.Time        `json:"start_ts"`
	EndTS               *time.Time       `json:"end_ts,omitempty"`
	Provider            string           `json:"provider"`
	Model               string           `json:"model"`
	PromptVersion       string           `json:"prompt_version"`
	UpdatedAt           time.Time        `json:"updated_at"`
	Entities            []entityLinkJSON `json:"entities"`
	Files               []string         `json:"files"`
	Steps               []string         `json:"steps"`
}

type entityLinkJSON struct {
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	Ref        string  `json:"ref,omitempty"`
	Role       string  `json:"role"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
	Evidence   string  `json:"evidence_step_id"`
}

func workItemsJSON(idx *index.DB, items []index.WorkItem) []workItemJSON {
	out := make([]workItemJSON, 0, len(items))
	for _, w := range items {
		j := workItemJSON{
			ID: w.ID, SessionID: w.SessionID, Origin: w.Origin, Status: w.Status, Open: w.Open(),
			Goal: w.Goal, Approach: w.Approach, Outcome: w.Outcome, ContinuesWorkItemID: w.ContinuesWorkItemID,
			StartStepID: w.StartStepID, EndStepID: w.EndStepID, StartTS: w.StartTS,
			Provider: w.ModelProvider, Model: w.ModelName, PromptVersion: w.PromptVersion, UpdatedAt: w.UpdatedAt,
			Entities: []entityLinkJSON{}, Files: []string{}, Steps: []string{},
		}
		if !w.EndTS.IsZero() {
			end := w.EndTS
			j.EndTS = &end
		}
		links, _ := idx.WorkItemLinks(w.ID)
		for _, l := range links {
			j.Entities = append(j.Entities, entityLinkJSON{Type: l.Type, Name: l.Name, Ref: l.Ref, Role: l.Role, Confidence: l.Confidence, Source: l.Source, Evidence: l.EvidenceStepID})
		}
		if files, _ := idx.WorkItemFiles(w.ID); files != nil {
			j.Files = files
		}
		if steps, _ := idx.WorkItemSteps(w.ID); steps != nil {
			j.Steps = steps
		}
		out = append(out, j)
	}
	return out
}
