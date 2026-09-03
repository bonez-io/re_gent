package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/insight"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/bonez-io/re_gent/internal/style"
	"github.com/spf13/cobra"
)

// InsightCmd creates the insight command: the switch, the worker, and the
// status of RFC 0007's derived layer.
func InsightCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "insight",
		Short: "Read recorded sessions into searchable work items and entities",
		Long: `Read recorded sessions into searchable work items and entities.

Insight is off by default. It has two halves: the repository enables it in
.regent/config.toml (committed, so the policy travels with the code), and each
person configures a model provider in ~/.regent/config.toml (private, so no
repository decides where anyone's sessions are sent). With both in place, every
finished agent turn queues a job and a detached worker reads it.

Nothing here runs inside an agent turn. The hook writes one row and, at most,
starts the worker; the worker does its reading in its own process and writes
to .regent/log/insight.log.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(insightStatusCmd(), insightRunCmd(), insightEnableCmd(), insightDisableCmd(), insightRebuildCmd())
	return cmd
}

func insightStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show whether insight is on, what it would call, and what it has read",
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
			return printInsightStatus(cmd.OutOrStdout(), s, idx)
		},
	}
}

func printInsightStatus(w io.Writer, s *store.Store, idx *index.DB) error {
	settings, settingsErr := insight.Load(s)

	switch {
	case settingsErr != nil:
		fmt.Fprintf(w, "%s %v\n", style.Error("Insight: configuration error:"), settingsErr)
	case settings.Active():
		fmt.Fprintf(w, "%s\n", style.Success("Insight: on"))
	case settings.Enabled:
		fmt.Fprintf(w, "%s enabled for this repository, but you have no model provider configured\n", style.Warning("Insight: idle:"))
	default:
		fmt.Fprintf(w, "%s\n", style.DimText("Insight: off (run `rgt insight enable`)"))
	}

	if settingsErr == nil {
		fmt.Fprintf(w, "  repository   enabled=%t  scrub.capture=%s  work_item_idle=%s\n",
			settings.Enabled, settings.Scrub.Capture, settings.WorkItemIdle)
		if settings.Model.Provider == "" {
			fmt.Fprintf(w, "  model        %s\n", style.DimText("none — add [insight.model] to ~/.regent/config.toml"))
		} else {
			fmt.Fprintf(w, "  model        %s %s  (%s)\n", settings.Model.Provider, settings.Model.Model, settings.ModelKey())
		}
		if settings.Embedding.Provider == "" {
			fmt.Fprintf(w, "  embedding    %s\n", style.DimText("none — search will be full-text only"))
		} else {
			fmt.Fprintf(w, "  embedding    %s %s  (%s)\n", settings.Embedding.Provider, settings.Embedding.Model, settings.EmbeddingKey())
		}
	}

	if !insight.HasProcessor() {
		fmt.Fprintf(w, "  worker       %s\n", style.Warning("this rgt has no read pipeline yet; jobs queue and wait for one"))
	}
	if pid, alive := insight.Holder(s.Root); alive {
		fmt.Fprintf(w, "  worker       running (pid %d)\n", pid)
	}

	counts, err := idx.InsightJobCounts()
	if err != nil {
		return fmt.Errorf("count jobs: %w", err)
	}
	fmt.Fprintf(w, "  queue        %s\n", formatInsightJobCounts(counts))
	if failed, ok, err := idx.LastFailedInsightJob(); err == nil && ok {
		fmt.Fprintf(w, "  last failure job %d (%s, session %s): %s\n", failed.ID, failed.Kind, failed.SessionID, failed.LastError)
	}

	cov, err := idx.InsightCoverage()
	if err != nil {
		return fmt.Errorf("coverage: %w", err)
	}
	fmt.Fprintf(w, "  indexed      %d of %d messages full-text", cov.MessagesIndexed, cov.Messages)
	if cov.MessagesIndexed < cov.Messages {
		fmt.Fprintf(w, "  %s", style.DimText("(run `rgt insight rebuild` to index the rest)"))
	}
	fmt.Fprintln(w)
	entities := fmt.Sprintf("%d entities", cov.Entities)
	if cov.Entities == 1 {
		entities = "1 entity"
	}
	fmt.Fprintf(w, "  read         %s (%d embedded), %s across %s\n",
		plural(cov.WorkItems, "work item"), cov.WorkItemsEmbedded, entities, plural(cov.Sessions, "session"))
	if settingsErr == nil && settings.HasEmbedding() && cov.WorkItemsEmbedded < cov.WorkItems {
		fmt.Fprintf(w, "               %s\n", style.DimText(fmt.Sprintf("%d work items have no vector; see log/%s for the embedding error, then `rgt insight rebuild`", cov.WorkItems-cov.WorkItemsEmbedded, insight.LogFileName)))
	}
	return nil
}

func formatInsightJobCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "empty"
	}
	order := []string{index.InsightJobQueued, index.InsightJobRunning, index.InsightJobDone, index.InsightJobFailed}
	var parts []string
	seen := map[string]bool{}
	for _, state := range order {
		if n, ok := counts[state]; ok {
			parts = append(parts, fmt.Sprintf("%d %s", n, state))
			seen[state] = true
		}
	}
	var rest []string
	for state, n := range counts {
		if !seen[state] {
			rest = append(rest, fmt.Sprintf("%d %s", n, state))
		}
	}
	sort.Strings(rest)
	return strings.Join(append(parts, rest...), ", ")
}

func insightRunCmd() *cobra.Command {
	var detach bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Drain the insight queue now",
		Long: `Drain the insight queue now.

Runs the worker in the foreground until the queue is empty. With --detach it
starts the worker as a background process, the way a hook does, and returns.
Only one worker runs per repository; a second run reports the first and exits.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if detach {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				exe, err := os.Executable()
				if err != nil {
					return fmt.Errorf("locate rgt: %w", err)
				}
				if pid, alive := insight.Holder(s.Root); alive {
					fmt.Fprintf(out, "Insight worker already running (pid %d).\n", pid)
					return nil
				}
				if err := insight.Spawn(exe, cwd, s.Root); err != nil {
					return err
				}
				fmt.Fprintf(out, "Insight worker started; it logs to %s/log/%s.\n", s.Root, insight.LogFileName)
				return nil
			}

			idx, err := index.Open(s)
			if err != nil {
				return err
			}
			defer func() { _ = idx.Close() }()

			settings, err := insight.Load(s)
			if err != nil {
				return err
			}
			if !settings.Active() {
				return errors.New("insight is not active here: check `rgt insight status`")
			}

			worker := &insight.Worker{Store: s, Index: idx}
			processor, perr := insight.NewProcessor(s, idx, settings)
			if perr != nil && !errors.Is(perr, insight.ErrNoProcessor) {
				return perr
			}
			worker.Processor = processor

			report, held, err := worker.Run(cmd.Context())
			if !held {
				pid, _ := insight.Holder(s.Root)
				fmt.Fprintf(out, "Insight worker already running (pid %d); nothing to do.\n", pid)
				return nil
			}
			if errors.Is(err, insight.ErrNoProcessor) {
				counts, cerr := idx.InsightJobCounts()
				if cerr != nil {
					return cerr
				}
				fmt.Fprintf(out, "%s %s.\n", style.Warning("No read pipeline in this rgt;"),
					fmt.Sprintf("queue left as is (%s)", formatInsightJobCounts(counts)))
				return nil
			}
			printInsightRunReport(out, report)
			return err
		},
	}
	cmd.Flags().BoolVar(&detach, "detach", false, "start the worker in the background and return")
	return cmd
}

func printInsightRunReport(w io.Writer, report insight.Report) {
	if report.Done == 0 && report.Retried == 0 && report.Failed == 0 {
		fmt.Fprintln(w, "Queue is empty; nothing to read.")
		return
	}
	fmt.Fprintf(w, "%s %d done, %d retried, %d failed.\n", style.Success("Insight:"), report.Done, report.Retried, report.Failed)
}

func insightEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "Turn insight on for this repository",
		Long: `Turn insight on for this repository.

Writes [insight] enabled = true to .regent/config.toml, which is committed, so
the setting travels with the repository. Each contributor still needs a model
provider in their own ~/.regent/config.toml before anything runs for them.

Also indexes every recorded message for full-text search, so literal text is
findable before any model has read anything.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := refuseInsightInServerMode(cwd); err != nil {
				return err
			}
			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}
			cfg, err := s.ReadRepoConfig()
			if err != nil {
				return err
			}
			cfg.Insight.Enabled = true
			if err := s.WriteRepoConfig(cfg); err != nil {
				return err
			}

			idx, err := index.Open(s)
			if err != nil {
				return err
			}
			defer func() { _ = idx.Close() }()
			if err := idx.RebuildInsightFTS(); err != nil {
				return err
			}
			if err := idx.SetInsightMeta("enabled_at", time.Now().UTC().Format(time.RFC3339)); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s [insight] enabled = true written to %s/config.toml.\n", style.Success("Insight enabled:"), s.Root)

			settings, err := insight.Load(s)
			if err != nil {
				return err
			}
			if settings.Model.Provider == "" {
				fmt.Fprintf(out, "\nNo model provider is configured for you yet. Add one to ~/.regent/config.toml:\n\n%s\n", insightProviderExample)
			}
			return printInsightStatus(out, s, idx)
		},
	}
}

const insightProviderExample = `  [insight.model]
  provider = "anthropic"            # anthropic | openai-compatible | command
  model = "claude-haiku-4-5-20251001"
  api_key_env = "ANTHROPIC_API_KEY" # the variable's name; the value is read at call time

  [insight.embedding]
  provider = "openai-compatible"    # openai-compatible | command
  model = "nomic-embed-text"
  base_url = "http://localhost:11434/v1"`

func insightDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "disable",
		Short:        "Turn insight off for this repository",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}
			cfg, err := s.ReadRepoConfig()
			if err != nil {
				return err
			}
			if !cfg.Insight.Enabled {
				fmt.Fprintln(cmd.OutOrStdout(), "Insight is already off.")
				return nil
			}
			cfg.Insight.Enabled = false
			if err := s.WriteRepoConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s hooks stop queueing; what was already read stays searchable.\n", style.Success("Insight disabled:"))
			return nil
		},
	}
}

func insightRebuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Re-index full-text search and queue every session to be read again",
		Long: `Re-index full-text search and queue every session to be read again.

Rebuilds the full-text indexes from the recorded messages, drops queued and
failed jobs, and queues one job per recorded session. The worker then reads
every session from its first step. Existing work items are replaced as each
session is read; nothing recorded is touched.`,
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

			if err := idx.RebuildInsightFTS(); err != nil {
				return err
			}
			queued, err := idx.EnqueueSessionInsightJobs()
			if err != nil {
				return fmt.Errorf("queue sessions: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s full-text index rebuilt; %s queued.\n", style.Success("Insight:"), plural(queued, "session"))

			settings, err := insight.Load(s)
			if err != nil {
				return err
			}
			switch {
			case !settings.Active():
				fmt.Fprintln(out, "Insight is not active here, so the queue waits; see `rgt insight status`.")
			case !insight.HasProcessor():
				fmt.Fprintln(out, style.Warning("This rgt has no read pipeline yet; the queue waits for one."))
			case queued > 0:
				fmt.Fprintln(out, "Run `rgt insight run` to read them now, or `rgt insight run --detach` to do it in the background.")
			}
			return nil
		},
	}
}

// refuseInsightInServerMode keeps v1 local-only (RFC 0007): in server mode
// the index is a disposable cache and the server is the source of truth, so
// the worker would have to run there.
func refuseInsightInServerMode(cwd string) error {
	cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
	if err != nil {
		return nil
	}
	if !cfg.Enabled() {
		return nil
	}
	return fmt.Errorf("insight is local mode only for now: this repository is connected to %s, and a server-mode index is a cache the server can replace. Server-side insight is a follow-up to RFC 0007", cfg.ServerURL)
}
