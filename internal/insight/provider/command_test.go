package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/config"
)

func TestCommandReader_Read(t *testing.T) {
	// Echoes a claude -p --output-format json style wrapper, ignoring
	// stdin's content but proving it was piped in by echoing its length.
	script := `cat > /dev/null; echo '{"type":"result","result":"{\"work_items\": [{\"goal\": \"cmd\"}]}"}'`
	cfg := config.InsightModelConfig{Provider: "command", Command: []string{"sh", "-c", script}}
	reader, info, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Provider != "command" {
		t.Fatalf("unexpected info: %+v", info)
	}

	obj, err := reader.Read(context.Background(), "instructions", []byte(`{"turns": []}`))
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)
}

func TestCommandReader_StdinCarriesInstructionsAndRequest(t *testing.T) {
	// Cat stdin back out as the "reply is the whole object" case, after
	// wrapping it so it is valid JSON: the script greps for our markers.
	script := `input=$(cat); ` +
		`case "$input" in *"MARK-INSTR"*"MARK-REQ"*) echo '{"work_items": []}' ;; ` +
		`*) echo "missing markers: $input" >&2; exit 1 ;; esac`
	cfg := config.InsightModelConfig{Provider: "command", Command: []string{"sh", "-c", script}}
	reader, _, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}

	obj, err := reader.Read(context.Background(), "MARK-INSTR system prompt", []byte(`{"MARK-REQ": true}`))
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)
}

func TestCommandReader_NonZeroExit(t *testing.T) {
	script := `echo "boom: something went wrong" 1>&2; exit 3`
	cfg := config.InsightModelConfig{Provider: "command", Command: []string{"sh", "-c", script}}
	reader, _, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, err = reader.Read(context.Background(), "sys", []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "boom: something went wrong") {
		t.Fatalf("expected stderr excerpt in error, got: %v", err)
	}
}

func TestCommandReader_NonZeroExitLargeStderr(t *testing.T) {
	// The error must carry only the last ~2KB of stderr, not all of it.
	script := `for i in $(seq 1 500); do echo "line-$i-filler-text-padding-out-the-buffer"; done 1>&2; exit 1`
	cfg := config.InsightModelConfig{Provider: "command", Command: []string{"sh", "-c", script}}
	reader, _, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, err = reader.Read(context.Background(), "sys", []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "line-1-filler") {
		t.Fatalf("error should be truncated to the tail of stderr, got early content: %v", err)
	}
	if !strings.Contains(err.Error(), "line-500-filler") {
		t.Fatalf("expected the tail of stderr in the error: %v", err)
	}
}

func TestCommandReader_MissingCommand(t *testing.T) {
	cfg := config.InsightModelConfig{Provider: "command"}
	reader, _, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(context.Background(), "sys", []byte(`{}`)); err == nil {
		t.Fatal("expected an error for an empty command")
	}
}

func TestCommandEmbedder_Embed(t *testing.T) {
	// The script doesn't need to actually parse stdin for this test: it
	// only has to prove texts were piped in (grep for a marker) and reply
	// with as many vectors as the fixed two-text input the test sends.
	script := `input=$(cat); ` +
		`case "$input" in *"MARK-A"*"MARK-B"*) echo '{"embeddings": [[1,2,3],[4,5,6]]}' ;; ` +
		`*) echo "missing texts: $input" 1>&2; exit 1 ;; esac`
	cfg := config.InsightEmbeddingConfig{Provider: "command", Command: []string{"sh", "-c", script}}
	embedder, info, err := NewEmbedder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Provider != "command" {
		t.Fatalf("unexpected info: %+v", info)
	}

	vecs, err := embedder.Embed(context.Background(), []string{"MARK-A", "MARK-B"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
	if vecs[0][0] != 1 || vecs[1][0] != 4 {
		t.Fatalf("unexpected vectors: %v", vecs)
	}
}

func TestCommandEmbedder_ReplyLengthMismatch(t *testing.T) {
	script := `cat > /dev/null; echo '{"embeddings": [[1,2,3]]}'`
	cfg := config.InsightEmbeddingConfig{Provider: "command", Command: []string{"sh", "-c", script}}
	embedder, _, err := NewEmbedder(cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, err = embedder.Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected a length mismatch error")
	}
}
