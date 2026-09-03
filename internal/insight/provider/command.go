package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/bonez-io/re_gent/internal/config"
)

// stderrExcerptLen bounds how much of a failed command's stderr an error
// carries. Enough to see the actual failure, never the whole log.
const stderrExcerptLen = 2048

// commandReader runs a local program with the request on stdin and expects
// a JSON reply on stdout. This is how "choose the agent" is satisfied
// without re_gent knowing any agent's SDK: command = ["claude", "-p",
// "--output-format", "json"] or ["codex", "exec", "--json"].
type commandReader struct {
	cfg config.InsightModelConfig
}

func (r *commandReader) Read(ctx context.Context, instructions string, request []byte) ([]byte, error) {
	if len(r.cfg.Command) == 0 {
		return nil, fmt.Errorf("command: no command configured")
	}

	var stdin bytes.Buffer
	stdin.WriteString(instructions)
	stdin.WriteString("\n\n")
	stdin.Write(request)
	stdin.WriteString("\n")

	out, err := runCommand(ctx, r.cfg.Command, stdin.Bytes())
	if err != nil {
		return nil, fmt.Errorf("command: %w", err)
	}

	// An agent CLI may exit 0 or 1 and report its failure inside its JSON
	// wrapper (claude -p writes {"is_error": true, "result": "<why>"}). Say
	// why rather than "no JSON object in reply".
	if why, ok := wrapperError(out); ok {
		return nil, fmt.Errorf("command: %s reported: %s", r.cfg.Command[0], why)
	}

	obj, err := extractJSON(string(out))
	if err != nil {
		return nil, fmt.Errorf("command: %w", err)
	}
	return obj, nil
}

// commandEmbedder runs a local program with {"texts": [...]} on stdin and
// expects {"embeddings": [[...], ...]} on stdout, ordered like the input.
type commandEmbedder struct {
	cfg config.InsightEmbeddingConfig
}

type commandEmbedRequest struct {
	Texts []string `json:"texts"`
}

type commandEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func (e *commandEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(e.cfg.Command) == 0 {
		return nil, fmt.Errorf("command: no command configured")
	}
	if len(texts) == 0 {
		return nil, nil
	}

	stdin, err := json.Marshal(commandEmbedRequest{Texts: texts})
	if err != nil {
		return nil, fmt.Errorf("command embeddings: %w", err)
	}

	out, err := runCommand(ctx, e.cfg.Command, stdin)
	if err != nil {
		return nil, fmt.Errorf("command embeddings: %w", err)
	}

	var parsed commandEmbedResponse
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("command embeddings: parse reply: %w", err)
	}
	if len(parsed.Embeddings) != len(texts) {
		return nil, fmt.Errorf("command embeddings: got %d vectors for %d texts", len(parsed.Embeddings), len(texts))
	}

	var wantDim int
	if e.cfg.Dimensions > 0 {
		wantDim = e.cfg.Dimensions
	}
	for _, v := range parsed.Embeddings {
		if wantDim == 0 {
			wantDim = len(v)
		}
		if len(v) != wantDim {
			return nil, fmt.Errorf("command embeddings: vector length %d, want %d", len(v), wantDim)
		}
	}

	return parsed.Embeddings, nil
}

// runCommand runs name(args...) with stdin on its standard input and
// returns stdout. A non-zero exit carries the last stderrExcerptLen bytes
// of stderr in the returned error.
func runCommand(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if why, ok := wrapperError(stdout.Bytes()); ok {
			return nil, fmt.Errorf("%s: %w: %s", argv[0], err, why)
		}
		excerpt := lastBytes(bytes.TrimSpace(stderr.Bytes()), stderrExcerptLen)
		if len(excerpt) == 0 {
			// Some programs put their last words on stdout.
			excerpt = lastBytes(bytes.TrimSpace(stdout.Bytes()), stderrExcerptLen)
		}
		if len(excerpt) == 0 {
			return nil, fmt.Errorf("%s: %w", argv[0], err)
		}
		return nil, fmt.Errorf("%s: %w: %s", argv[0], err, excerpt)
	}

	return stdout.Bytes(), nil
}

// wrapperError recognises an agent CLI's own failure report: a JSON object
// with "is_error": true, whose "result" (or "error") string says why.
func wrapperError(out []byte) (string, bool) {
	var wrapper struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &wrapper); err != nil || !wrapper.IsError {
		return "", false
	}
	why := wrapper.Result
	if why == "" {
		why = wrapper.Error
	}
	if why == "" {
		why = "unspecified error"
	}
	return why, true
}

// lastBytes returns the trailing n bytes of b, or all of it if shorter.
func lastBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}
