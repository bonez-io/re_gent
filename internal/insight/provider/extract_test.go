package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustHaveWorkItems(t *testing.T, obj []byte) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(obj, &m); err != nil {
		t.Fatalf("result is not a JSON object: %v (%s)", err, obj)
	}
	if _, ok := m["work_items"]; !ok {
		t.Fatalf("result has no work_items key: %s", obj)
	}
}

func TestExtractJSON_Bare(t *testing.T) {
	obj, err := extractJSON(`{"work_items": []}`)
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)
}

func TestExtractJSON_Fenced(t *testing.T) {
	text := "```json\n{\"work_items\": [{\"id\": null}]}\n```"
	obj, err := extractJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)

	// A bare ``` fence with no language tag.
	text = "```\n{\"work_items\": []}\n```"
	obj, err = extractJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)
}

func TestExtractJSON_Prose(t *testing.T) {
	text := "Sure, here is the result:\n\n{\"work_items\": [{\"goal\": \"ship it\"}]}\n\nLet me know if you need anything else."
	obj, err := extractJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)

	var m map[string]json.RawMessage
	if err := json.Unmarshal(obj, &m); err != nil {
		t.Fatal(err)
	}
}

func TestExtractJSON_ProseWithBraceInString(t *testing.T) {
	// A brace inside a string value must not terminate the slice early.
	text := `blah blah {"work_items": [{"goal": "handle {braces} in strings"}]} trailing prose`
	obj, err := extractJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)

	var decoded struct {
		WorkItems []struct {
			Goal string `json:"goal"`
		} `json:"work_items"`
	}
	if err := json.Unmarshal(obj, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.WorkItems[0].Goal != "handle {braces} in strings" {
		t.Fatalf("goal not preserved: %+v", decoded)
	}
}

func TestExtractJSON_ResultWrapper(t *testing.T) {
	// claude -p --output-format json wraps the reply in a "result" string.
	inner := `{"work_items": [{"id": "abc"}]}`
	wrapper := map[string]any{
		"type":   "result",
		"result": inner,
		"cost":   0.01,
	}
	b, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}

	obj, err := extractJSON(string(b))
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)
}

func TestExtractJSON_ResultWrapperFenced(t *testing.T) {
	// The result string itself may contain a fenced block.
	inner := "```json\n{\"work_items\": []}\n```"
	wrapper := map[string]any{"result": inner}
	b, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}

	obj, err := extractJSON(string(b))
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)
}

func TestExtractJSON_JSONLDirectWorkItems(t *testing.T) {
	lines := []string{
		`{"type": "item.started", "id": "1"}`,
		`{"type": "item.progress", "id": "1"}`,
		`{"type": "item.completed", "id": "1", "work_items": [{"goal": "done"}]}`,
	}
	text := strings.Join(lines, "\n")

	obj, err := extractJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)
}

func TestExtractJSON_JSONLResultField(t *testing.T) {
	lines := []string{
		`{"type": "item.started"}`,
		`{"type": "item.completed", "result": "{\"work_items\": [{\"goal\": \"via result\"}]}"}`,
	}
	text := strings.Join(lines, "\n")

	obj, err := extractJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)
}

func TestExtractJSON_JSONLTextField(t *testing.T) {
	lines := []string{
		`{"type": "item.started"}`,
		`{"type": "message", "text": "{\"work_items\": []}"}`,
	}
	text := strings.Join(lines, "\n")

	obj, err := extractJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)
}

func TestExtractJSON_JSONLPicksLastQualifyingLine(t *testing.T) {
	lines := []string{
		`{"type": "message", "content": "{\"work_items\": [{\"goal\": \"first, wrong\"}]}"}`,
		`{"type": "noise"}`,
		`{"type": "message", "content": "{\"work_items\": [{\"goal\": \"last, right\"}]}"}`,
	}
	text := strings.Join(lines, "\n")

	obj, err := extractJSON(text)
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct {
		WorkItems []struct {
			Goal string `json:"goal"`
		} `json:"work_items"`
	}
	if err := json.Unmarshal(obj, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.WorkItems[0].Goal != "last, right" {
		t.Fatalf("expected last qualifying line, got %+v", decoded)
	}
}

func TestExtractJSON_NoJSON(t *testing.T) {
	_, err := extractJSON("just some prose, no object here at all")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no JSON object in reply") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractJSON_Empty(t *testing.T) {
	_, err := extractJSON("   ")
	if err == nil {
		t.Fatal("expected an error")
	}
}
