// Package usage reads agent-host transcripts and aggregates the API usage they
// record: input/output tokens, cache creation/read tokens, and the number of
// API calls that produced them.
//
// The transcript holds accounting that per-tool hooks never see, so this is the
// only place re_gent can learn what a turn actually cost. Only numeric counters
// are read: prompts, tool arguments, tool results and assistant text are parsed
// past and discarded, and no transcript content is ever returned or logged.
//
// Claude Code writes the main session to <dir>/<session-id>.jsonl and each
// subagent it spawns to <dir>/<session-id>/subagents/agent-<agent-id>.jsonl.
// Subagents may spawn subagents, so Collect walks that tree recursively.
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bonez-io/re_gent/internal/store"
)

const (
	// maxLineBytes bounds a single transcript line. Transcript lines embed whole
	// file contents, so they are routinely large; anything past this is skipped
	// rather than buffered, so one pathological line cannot exhaust hook memory.
	maxLineBytes = 8 << 20

	// maxSubagentDepth bounds recursion into nested subagent transcripts.
	maxSubagentDepth = 8

	// maxSubagentFiles bounds how many subagent transcripts one Collect reads,
	// so a hook never turns into an unbounded directory walk.
	maxSubagentFiles = 1024
)

// Subagent is the usage of one subagent transcript reached from the main one.
type Subagent struct {
	ID    string      // agent id, taken from the agent-<id>.jsonl filename
	Type  string      // agent type from the sidecar meta file, when present
	Depth int         // 1 for a direct subagent, 2 for a subagent of a subagent
	Usage store.Usage // this transcript alone, excluding its own subagents
}

// Report is the usage found in one transcript and, recursively, in every
// subagent transcript it spawned.
type Report struct {
	Total     store.Usage // Main plus every entry in Subagents
	Main      store.Usage // the named transcript alone
	Subagents []Subagent
}

// Collect aggregates usage from transcriptPath and its subagent transcripts.
//
// It degrades rather than failing: an empty path yields an empty report and no
// error, unparseable lines are skipped, and an unreadable subagent transcript is
// reported through the returned error while the rest of the report is still
// returned. Only a failure to read the main transcript yields an empty report.
// Callers should use the report even when err is non-nil.
func Collect(transcriptPath string) (Report, error) {
	if transcriptPath == "" {
		return Report{}, nil
	}

	main, err := parseFile(transcriptPath)
	if err != nil {
		return Report{}, err
	}

	report := Report{Main: main, Total: main}

	budget := maxSubagentFiles
	subagents, problems := collectSubagents(transcriptPath, 1, &budget)
	for _, sub := range subagents {
		report.Total = report.Total.Add(sub.Usage)
	}
	report.Total.Subagents = int64(len(subagents))
	report.Subagents = subagents

	return report, errors.Join(problems...)
}

// collectSubagents reads the subagent transcripts spawned by parentPath and,
// depth permitting, the transcripts their own subagents spawned.
func collectSubagents(parentPath string, depth int, budget *int) ([]Subagent, []error) {
	if depth > maxSubagentDepth || *budget <= 0 {
		return nil, nil
	}

	dir := subagentDir(parentPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // no subagents: the common case, not a problem
		}
		return nil, []error{fmt.Errorf("read subagent dir: %w", err)}
	}

	var (
		subagents []Subagent
		problems  []error
	)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id, ok := subagentID(entry.Name())
		if !ok {
			continue
		}
		if *budget <= 0 {
			problems = append(problems, fmt.Errorf("subagent transcript limit %d reached at depth %d", maxSubagentFiles, depth))
			break
		}
		*budget--

		path := filepath.Join(dir, entry.Name())
		totals, err := parseFile(path)
		if err != nil {
			// One unreadable subagent must not discard the rest of the report.
			problems = append(problems, err)
			continue
		}

		subagents = append(subagents, Subagent{
			ID:    id,
			Type:  readAgentType(path),
			Depth: depth,
			Usage: totals,
		})

		nested, nestedProblems := collectSubagents(path, depth+1, budget)
		subagents = append(subagents, nested...)
		problems = append(problems, nestedProblems...)
	}

	return subagents, problems
}

// subagentDir returns the directory holding the subagent transcripts spawned by
// the transcript at path: "<dir>/<name>.jsonl" -> "<dir>/<name>/subagents".
func subagentDir(path string) string {
	return filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
}

// subagentID extracts "<id>" from an "agent-<id>.jsonl" transcript filename.
func subagentID(name string) (string, bool) {
	if !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
	return id, id != ""
}

// readAgentType reads the agent type from the sidecar meta file written next to
// a subagent transcript. The type is host metadata ("general-purpose"), not
// conversation content. A missing or malformed meta file is not an error: the
// type is descriptive only.
func readAgentType(transcriptPath string) string {
	metaPath := strings.TrimSuffix(transcriptPath, ".jsonl") + ".meta.json"
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}
	var meta struct {
		AgentType string `json:"agentType"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	return meta.AgentType
}

// transcriptEntry is the narrow view of a transcript line we need: the id of the
// API request and its usage counters. Every other field, including all message
// content, is left unparsed.
type transcriptEntry struct {
	RequestID string `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Usage *struct {
			InputTokens         int64 `json:"input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// parseFile sums the usage counters in one transcript file.
//
// Lines that do not parse are skipped: the host appends to this file while we
// read it, so a truncated tail is expected rather than exceptional.
func parseFile(path string) (store.Usage, error) {
	f, err := os.Open(path)
	if err != nil {
		return store.Usage{}, fmt.Errorf("open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	var (
		totals store.Usage
		seen   = map[string]struct{}{}
		reader = bufio.NewReader(f)
	)
	for {
		line, err := readLine(reader)
		if len(line) > 0 {
			accumulate(&totals, seen, line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return totals, nil
			}
			return totals, fmt.Errorf("read transcript: %w", err)
		}
	}
}

// accumulate adds one transcript line's usage counters to totals.
//
// A single API response is written to the transcript as several lines when it
// carries several content blocks, and every one of those lines repeats the same
// usage block. Summing lines would therefore multiply the real cost, so each
// request is counted once, keyed by request id and falling back to the API
// message id.
func accumulate(totals *store.Usage, seen map[string]struct{}, line []byte) {
	var entry transcriptEntry
	if err := json.Unmarshal(line, &entry); err != nil || entry.Message.Usage == nil {
		return
	}

	key := entry.RequestID
	if key == "" {
		key = entry.Message.ID
	}
	if key != "" {
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
	}

	totals.InputTokens += entry.Message.Usage.InputTokens
	totals.OutputTokens += entry.Message.Usage.OutputTokens
	totals.CacheCreationTokens += entry.Message.Usage.CacheCreationTokens
	totals.CacheReadTokens += entry.Message.Usage.CacheReadTokens
	totals.APICalls++
}

// readLine returns the next line without its terminator. Lines longer than
// maxLineBytes are dropped instead of buffered, and reading continues with the
// next line. The returned error is io.EOF once the file is exhausted.
func readLine(reader *bufio.Reader) ([]byte, error) {
	var (
		line     []byte
		overlong bool
	)
	for {
		chunk, err := reader.ReadSlice('\n')
		if !overlong {
			if len(line)+len(chunk) > maxLineBytes {
				overlong = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if overlong {
			return nil, err
		}
		return bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r")), err
	}
}
