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

Insight is off by default. It has two halves: a switch for the project, and a
model provider. In local mode the switch is [insight] enabled in
.regent/config.toml (committed, so the policy travels with the code) and the
provider is yours, in ~/.regent/config.toml (private, so no repository decides
where anyone's sessions are sent). With both in place, every finished agent
turn queues a job and a detached worker reads it.

In server mode the server holds both: the switch is per project, the providers
are the server's (insight.toml under its data directory), and reading happens
there when a turn is pushed. The commands below then talk to the server.

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
			if client, ok, err := insightServerClient(); err != nil {
				return err
			} else if ok {
				var st insight.Status
				ctx, cancel := withTimeout(cmd.Context())
				defer cancel()
				if err := client.APIGet(ctx, "insight/status", &st); err != nil {
					return err
				}
				printInsightStatus(cmd.OutOrStdout(), st, true)
				return nil
			}
			s, idx, err := openInsightLocal()
			if err != nil {
				return err
			}
			defer func() { _ = idx.Close() }()
			st, err := localInsightStatus(s, idx)
			if err != nil {
				return err
			}
			printInsightStatus(cmd.OutOrStdout(), st, false)
			return nil
		},
	}
}

func openInsightLocal() (*store.Store, *index.DB, error) {
	s, err := openStoreFromCWD()
	if err != nil {
		return nil, nil, err
	}
	idx, err := index.Open(s)
	if err != nil {
		return nil, nil, err
	}
	return s, idx, nil
}

func localInsightStatus(s *store.Store, idx *index.DB) (insight.Status, error) {
	settings, settingsErr := insight.Load(s)
	return insight.Collect(s, idx, settings, settingsErr, "~/.regent/config.toml")
}

func printInsightStatus(w io.Writer, st insight.Status, server bool) {
	switch {
	case st.ConfigError != "":
		fmt.Fprintf(w, "%s %s\n", style.Error("Insight: configuration error:"), st.ConfigError)
	case st.Active:
		fmt.Fprintf(w, "%s\n", style.Success("Insight: on"))
	case st.Enabled && server:
		fmt.Fprintf(w, "%s enabled for this project, but the server has no model provider configured\n", style.Warning("Insight: idle:"))
	case st.Enabled:
		fmt.Fprintf(w, "%s enabled for this repository, but you have no model provider configured\n", style.Warning("Insight: idle:"))
	default:
		fmt.Fprintf(w, "%s\n", style.DimText("Insight: off (run `rgt insight enable`)"))
	}

	if st.ConfigError == "" {
		where := "repository"
		if server {
			where = "project"
		}
		fmt.Fprintf(w, "  %-12s enabled=%t  scrub.capture=%s  work_item_idle=%s\n", where, st.Enabled, st.ScrubCapture, st.WorkItemIdle)
		if st.Model.Provider == "" {
			fmt.Fprintf(w, "  model        %s\n", style.DimText("none — add [insight.model] to "+st.ProvidersFrom))
		} else {
			fmt.Fprintf(w, "  model        %s %s  (%s)\n", st.Model.Provider, st.Model.Model, st.Model.Key)
		}
		if st.Embedding.Provider == "" {
			fmt.Fprintf(w, "  embedding    %s\n", style.DimText("none — search will be full-text only"))
		} else {
			fmt.Fprintf(w, "  embedding    %s %s  (%s)\n", st.Embedding.Provider, st.Embedding.Model, st.Embedding.Key)
		}
	}

	if !st.HasProcessor {
		fmt.Fprintf(w, "  worker       %s\n", style.Warning("this build has no read pipeline; jobs queue and wait for one"))
	}
	if st.WorkerPID != 0 {
		fmt.Fprintf(w, "  worker       running (pid %d)\n", st.WorkerPID)
	}
	fmt.Fprintf(w, "  queue        %s\n", formatInsightJobCounts(st.Queue))
	if st.LastFailure != nil {
		fmt.Fprintf(w, "  last failure job %d (%s, session %s): %s\n", st.LastFailure.ID, st.LastFailure.Kind, st.LastFailure.SessionID, st.LastFailure.Error)
	}

	cov := st.Coverage
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
	if st.Embedding.Provider != "" && cov.WorkItemsEmbedded < cov.WorkItems {
		fmt.Fprintf(w, "               %s\n", style.DimText(fmt.Sprintf("%d work items have no vector; see log/%s for the embedding error, then `rgt insight rebuild`", cov.WorkItems-cov.WorkItemsEmbedded, insight.LogFileName)))
	}
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
Only one worker runs per repository; a second run reports the first and exits.

In server mode this asks the server to read whatever has been pushed and not
yet read; the server's worker does the reading.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if client, ok, err := insightServerClient(); err != nil {
				return err
			} else if ok {
				var st insight.Status
				ctx, cancel := withTimeout(cmd.Context())
				defer cancel()
				if err := client.APIPost(ctx, "insight/run", map[string]any{}, &st); err != nil {
					return err
				}
				fmt.Fprintln(out, "Asked the server to read what has been pushed; its worker runs in the background.")
				printInsightStatus(out, st, true)
				return nil
			}

			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}
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
	cmd.Flags().BoolVar(&detach, "detach", false, "start the worker in the background and return (local mode)")
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

Local mode: writes [insight] enabled = true to .regent/config.toml, which is
committed, so the setting travels with the repository. Each contributor still
needs a model provider in their own ~/.regent/config.toml before anything runs
for them.

Server mode: turns the project on at the server, which then mirrors and reads
every pushed session with its own providers.

Also indexes every recorded message for full-text search, so literal text is
findable before any model has read anything.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if client, ok, err := insightServerClient(); err != nil {
				return err
			} else if ok {
				var st insight.Status
				ctx, cancel := withTimeout(cmd.Context())
				defer cancel()
				if err := client.APIPost(ctx, "insight/settings", map[string]any{"enabled": true}, &st); err != nil {
					return err
				}
				fmt.Fprintf(out, "%s the server reads this project's pushed sessions from now on.\n", style.Success("Insight enabled on the server:"))
				if st.Model.Provider == "" && st.ConfigError == "" {
					fmt.Fprintf(out, "\nThe server has no model provider yet. Put one in %s:\n\n%s\n", st.ProvidersFrom, insightServerProviderExample)
				}
				printInsightStatus(out, st, true)
				return nil
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

			fmt.Fprintf(out, "%s [insight] enabled = true written to %s/config.toml.\n", style.Success("Insight enabled:"), s.Root)
			st, err := localInsightStatus(s, idx)
			if err != nil {
				return err
			}
			if st.Model.Provider == "" && st.ConfigError == "" {
				fmt.Fprintf(out, "\nNo model provider is configured for you yet. Add one to ~/.regent/config.toml:\n\n%s\n", insightProviderExample)
			}
			printInsightStatus(out, st, false)
			return nil
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

const insightServerProviderExample = `  [model]
  provider = "anthropic"            # anthropic | openai-compatible | command
  model = "claude-haiku-4-5-20251001"
  api_key_env = "ANTHROPIC_API_KEY" # set in the server's environment

  [embedding]
  provider = "openai-compatible"    # openai-compatible | command
  model = "text-embedding-3-small"
  base_url = "https://api.openai.com/v1"
  api_key_env = "OPENAI_API_KEY"`

func insightDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "disable",
		Short:        "Turn insight off for this repository",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if client, ok, err := insightServerClient(); err != nil {
				return err
			} else if ok {
				ctx, cancel := withTimeout(cmd.Context())
				defer cancel()
				if err := client.APIPost(ctx, "insight/settings", map[string]any{"enabled": false}, nil); err != nil {
					return err
				}
				fmt.Fprintf(out, "%s the server stops reading this project; what was already read stays searchable.\n", style.Success("Insight disabled on the server:"))
				return nil
			}
			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}
			cfg, err := s.ReadRepoConfig()
			if err != nil {
				return err
			}
			if !cfg.Insight.Enabled {
				fmt.Fprintln(out, "Insight is already off.")
				return nil
			}
			cfg.Insight.Enabled = false
			if err := s.WriteRepoConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s hooks stop queueing; what was already read stays searchable.\n", style.Success("Insight disabled:"))
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
session is read; nothing recorded is touched. In server mode the server does
all of this over what has been pushed.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if client, ok, err := insightServerClient(); err != nil {
				return err
			} else if ok {
				var st insight.Status
				ctx, cancel := withTimeout(cmd.Context())
				defer cancel()
				if err := client.APIPost(ctx, "insight/rebuild", map[string]any{}, &st); err != nil {
					return err
				}
				fmt.Fprintf(out, "%s the server re-indexed full-text search and queued every pushed session.\n", style.Success("Insight:"))
				printInsightStatus(out, st, true)
				return nil
			}

			s, idx, err := openInsightLocal()
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

// insightServerClient returns a client for the server this repository is
// connected to, or ok=false in local mode. In server mode the index is a
// disposable cache and the server is the source of truth, so work items are
// read and produced there.
func insightServerClient() (*remote.HTTPClient, bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, false, err
	}
	cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
	if err != nil || !cfg.Enabled() {
		return nil, false, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, false, fmt.Errorf("server-mode config: %w", err)
	}
	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		return nil, false, err
	}
	return client, true, nil
}
