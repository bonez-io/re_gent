package provider

import (
	"encoding/json"
	"errors"
	"strings"
)

// errNoJSON is returned by extractJSON when no reply text yields a JSON
// object at all. Callers wrap it with their own context.
var errNoJSON = errors.New("no JSON object in reply")

// extractJSON finds the single top-level JSON object a model reply is
// carrying, however it is wrapped, and returns its raw bytes ready for
// json.Unmarshal.
//
// The reply text may be:
//   - a bare JSON object,
//   - fenced in ```json ... ``` (or a bare ``` fence),
//   - surrounded by prose,
//   - a "command" provider's own wrapper, e.g. claude -p
//     --output-format json's {"result": "<text>", ...}, where the real
//     reply is the string value of a top-level "result" field, itself one
//     of the above, or
//   - codex exec --json's JSONL: one JSON object per line, where the reply
//     is the last line whose object either carries a "work_items" key
//     directly, or carries a "result"/"text"/"content" string that itself
//     resolves to one.
//
// Strategy: try the whole text as a JSON object; if it has a "work_items"
// key, that is the reply; if it has a string "result" field, recurse into
// that string. Failing a whole-text parse, scan lines from the end for a
// JSONL event that resolves the same way. Failing that, fall back to
// slicing between the first "{" and its matching last "}" and take that
// literally. Failing all of that, report errNoJSON.
func extractJSON(text string) ([]byte, error) {
	text = stripFences(strings.TrimSpace(text))
	if text == "" {
		return nil, errNoJSON
	}

	if raw, ok := asJSONObject(text); ok {
		if obj, ok := unwrap(raw); ok {
			return obj, nil
		}
	} else if obj, ok := scanJSONL(text); ok {
		return obj, nil
	}

	if sliced, ok := sliceBraces(text); ok {
		if raw, ok := asJSONObject(sliced); ok {
			if obj, ok := unwrap(raw); ok {
				return obj, nil
			}
			return raw, nil
		}
	}

	return nil, errNoJSON
}

// unwrap resolves a parsed JSON object to the Appendix A reply it carries:
// itself, if it already has "work_items"; the recursively-extracted value
// of a string "result" field, if present; or, failing both, the object
// itself, on the assumption that a Reader with no wrapper shape at all is
// just handing back the reply directly.
func unwrap(raw []byte) ([]byte, bool) {
	if hasKey(raw, "work_items") {
		return raw, true
	}
	if result, ok := stringField(raw, "result"); ok {
		if obj, err := extractJSON(result); err == nil {
			return obj, true
		}
		return nil, false
	}
	return raw, true
}

// scanJSONL scans text as newline-separated JSON events, from the last
// line back, for one that resolves to a "work_items" reply directly or
// through its "result", "text", or "content" string field.
func scanJSONL(text string) ([]byte, bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		raw, ok := asJSONObject(line)
		if !ok {
			continue
		}
		if hasKey(raw, "work_items") {
			return raw, true
		}
		for _, field := range []string{"result", "text", "content"} {
			s, ok := stringField(raw, field)
			if !ok {
				continue
			}
			if obj, err := extractJSON(s); err == nil && hasKey(obj, "work_items") {
				return obj, true
			}
		}
	}
	return nil, false
}

// stripFences removes a leading/trailing ``` or ```json code fence, if
// present, and returns the text unchanged otherwise.
func stripFences(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	rest := strings.TrimPrefix(text, "```")
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		firstLine := strings.TrimSpace(rest[:nl])
		if firstLine == "" || strings.EqualFold(firstLine, "json") {
			rest = rest[nl+1:]
		}
	}
	rest = strings.TrimSpace(rest)
	rest = strings.TrimSuffix(rest, "```")
	return strings.TrimSpace(rest)
}

// asJSONObject reports whether text is, in its entirety, a JSON object
// (not an array, string, or scalar), returning its raw bytes.
func asJSONObject(text string) ([]byte, bool) {
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, false
	}
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return nil, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	return raw, true
}

// hasKey reports whether the JSON object in raw has the named top-level key.
func hasKey(raw []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// stringField returns the named top-level field of the JSON object in raw,
// if present and a JSON string.
func stringField(raw []byte, key string) (string, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", false
	}
	v, ok := m[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	return s, true
}

// sliceBraces returns the text between the first "{" and its matching last
// "}", tracking string literals so a brace inside a JSON string value does
// not end the scan early.
func sliceBraces(text string) (string, bool) {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return "", false
	}

	depth := 0
	inString := false
	escaped := false
	end := -1
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return "", false
	}
	return text[start : end+1], true
}
