package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseInlineToolCalls(t *testing.T) {
	// A single fenced json tool call.
	in := "I'll read the file.\n```json\n{\"name\": \"read_file\", \"arguments\": {\"path\": \"main.go\"}}\n```\n"
	calls := parseInlineToolCalls(in)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Function.Name != "read_file" {
		t.Errorf("wrong name: %q", calls[0].Function.Name)
	}
	if calls[0].Type != "function" || !strings.HasPrefix(calls[0].ID, "inline_") {
		t.Errorf("call metadata wrong: %+v", calls[0])
	}
	// Arguments should be valid JSON containing the path.
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v", err)
	}
	if args["path"] != "main.go" {
		t.Errorf("args not preserved: %v", args)
	}
}

func TestParseInlineToolCallsMultiple(t *testing.T) {
	in := "```json\n{\"name\":\"a\",\"arguments\":{}}\n```\nsome text\n```json\n{\"name\":\"b\",\"arguments\":{\"x\":1}}\n```"
	calls := parseInlineToolCalls(in)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Function.Name != "a" || calls[1].Function.Name != "b" {
		t.Errorf("names wrong: %q %q", calls[0].Function.Name, calls[1].Function.Name)
	}
	// IDs should be unique per call.
	if calls[0].ID == calls[1].ID {
		t.Errorf("IDs should differ: %s == %s", calls[0].ID, calls[1].ID)
	}
}

func TestParseInlineToolCallsNoneCases(t *testing.T) {
	cases := map[string]string{
		"plain prose":        "Just some regular text with no tool call.",
		"fence without json": "```\necho hello\n```",
		"malformed json":     "```json\n{name: read_file}\n```", // not valid JSON
		"missing name":       "```json\n{\"arguments\":{\"x\":1}}\n```",
		"empty":              "",
		"non-object json":    "```json\n[1,2,3]\n```",
	}
	for label, in := range cases {
		if calls := parseInlineToolCalls(in); len(calls) != 0 {
			t.Errorf("%s: expected 0 calls, got %d (%+v)", label, len(calls), calls)
		}
	}
}

// A fenced block without the explicit "json" tag but with a valid object
// should still parse (the parser strips an optional "json" prefix and just
// needs a leading brace).
func TestParseInlineToolCallsBareFence(t *testing.T) {
	in := "```\n{\"name\":\"list_dir\",\"arguments\":{\"path\":\".\"}}\n```"
	calls := parseInlineToolCalls(in)
	if len(calls) != 1 || calls[0].Function.Name != "list_dir" {
		t.Errorf("bare-fence object should parse, got %+v", calls)
	}
}
