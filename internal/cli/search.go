package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/insight"
	"github.com/bonez-io/re_gent/internal/insight/provider"
	"github.com/bonez-io/re_gent/internal/insight/search"
	"github.com/bonez-io/re_gent/internal/style"
	"github.com/spf13/cobra"
)

// SearchCmd creates the search command.
func SearchCmd() *cobra.Command {
	var file, entity, status, session, since string
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Find work items and sessions by meaning, text, entity, or file",
		Long: `Find work items and sessions by meaning, text, entity, or file.

Search is hybrid: full-text over work items, entities, and every recorded
message, plus semantic similarity when an embedding provider is configured,
fused by reciprocal rank. It never calls a model except to embed the query,
and works with no embedding provider at all.

A message that matches but belongs to a session the worker has not read yet
is still returned, marked "not yet read", with the step to rgt show. Nothing
is omitted for lack of a summary. In server mode the search runs on the
server, over what every machine has pushed.`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := search.Query{File: file, Entity: entity, Status: status, Session: session, Limit: limit}
			if len(args) == 1 {
				q.Text = strings.TrimSpace(args[0])
			}
			if since != "" {
				d, err := time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("--since %q: want a duration such as 72h", since)
				}
				q.Since = time.Now().Add(-d)
			}
			if err := q.Validate(); err != nil {
				return err
			}

			var res search.Result
			if client, ok, err := insightServerClient(); err != nil {
				return err
			} else if ok {
				ctx, cancel := withTimeout(cmd.Context())
				defer cancel()
				if err := client.APIGet(ctx, "search?"+searchQueryValues(q).Encode(), &res); err != nil {
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
				var embedder search.Embedder
				var info provider.Info
				if settings, err := insight.Load(s); err == nil && settings.HasEmbedding() {
					if e, i, err := provider.NewEmbedder(settings.Embedding); err == nil {
						embedder, info = e, i
					} else {
						res.Notes = append(res.Notes, "embeddings unavailable ("+err.Error()+"); full-text only")
					}
				}
				notes := res.Notes
				res, err = search.Run(cmd.Context(), idx, embedder, info.Provider, info.Model, q)
				if err != nil {
					return err
				}
				res.Notes = append(notes, res.Notes...)
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			printSearchResult(cmd.OutOrStdout(), res)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "only work items that changed this path")
	cmd.Flags().StringVar(&entity, "entity", "", "only work items linked to this entity: a name, type:name, or ref")
	cmd.Flags().StringVar(&status, "status", "", "only this status")
	cmd.Flags().StringVar(&session, "session", "", "only this session")
	cmd.Flags().StringVar(&since, "since", "", "only items started within this duration, e.g. 72h")
	cmd.Flags().IntVar(&limit, "limit", 10, "maximum results")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// searchQueryValues encodes a query for the server's /search route.
func searchQueryValues(q search.Query) url.Values {
	v := url.Values{}
	v.Set("q", q.Text)
	v.Set("file", q.File)
	v.Set("entity", q.Entity)
	v.Set("status", q.Status)
	v.Set("session", q.Session)
	if !q.Since.IsZero() {
		v.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	v.Set("limit", strconv.Itoa(q.Limit))
	return v
}

func printSearchResult(w io.Writer, res search.Result) {
	for _, n := range res.Notes {
		fmt.Fprintln(w, style.Warning("note:"), n)
	}
	if len(res.Hits) == 0 && len(res.NotYetRead) == 0 {
		fmt.Fprintln(w, "Nothing matched.")
		return
	}
	for i, h := range res.Hits {
		fmt.Fprintf(w, "%2d. %s  %-10s %s\n", i+1, style.DimText(shortID(h.Item.ID)), styleStatus(h.Item.Status), h.Item.Goal)
		detail := fmt.Sprintf("%s · session %s · matched %s", h.Item.StartTS.Local().Format("2006-01-02"), h.Item.SessionID, strings.Join(h.Matched, ", "))
		fmt.Fprintf(w, "    %s\n", style.DimText(detail))
		if h.Item.Outcome != "" {
			fmt.Fprintf(w, "    %s\n", clipLine(h.Item.Outcome, 110))
		}
	}
	if len(res.NotYetRead) > 0 {
		fmt.Fprintf(w, "\n%s\n", style.Warning("Not yet read into work items (text match only):"))
		for _, u := range res.NotYetRead {
			where := "session " + u.SessionID
			if u.StepID != "" {
				where += "  rgt show " + shortID(u.StepID)
			}
			fmt.Fprintf(w, "  %s\n    %s\n", style.DimText(where), clipLine(u.Snippet, 110))
		}
	}
}

func clipLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}
