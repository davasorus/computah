package agent

import (
	"os"
	"testing"
)

func TestLoadSessionEdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		expectedLength int
		description    string
	}{
		{
			name:           "Valid Transcript",
			content:        `{"role":"user","content":"hello"}` + "\n" + `{"role":"assistant","content":"hi"}`,
			expectedLength: 2,
			description:    "Standard two-message transcript",
		},
		{
			name:           "Truncated Last Line",
			content:        `{"role":"user","content":"hello"}` + "\n" + `{"role":"assistant","content":"hi"`, // Missing closing brace
			expectedLength: 1,
			description:    "Should ignore the malformed/truncated last line",
		},
		{
			name:           "Orphaned Tool Result",
			content:        `{"role":"user","content":"help"}` + "\n" + `{"role":"tool","tool_call_id":"1","content":"result"}`,
			expectedLength: 1,
			description:    "Tool result without a preceding assistant call should be dropped",
		},
		{
			name:           "Incomplete Tool Group (Missing one of many)",
			content:        `{"role":"user","content":"run"}` + "\n" + `{"role":"assistant","content":"","tool_calls":[{"id":"1","function":{"name":"cmd","arguments":"{}"},{"id":"2","function":{"name":"other","arguments":"{}"}]}` + "\n" + `{"role":"tool","tool_call_id":"1","content":"result for 1"}`,
			expectedLength: 1,
			description:    "Assistant has 2 tool calls, but only 1 result is present. Should drop the incomplete group.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile, err := os.CreateTemp("", "session_test_*")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(tmpFile.Name())

			if _, err := tmpFile.WriteString(tt.content); err != nil {
				t.Fatal(err)
			}
			tmpFile.Close()

			msgs, err := loadSession(tmpFile.Name(), 100)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(msgs) != tt.expectedLength {
				t.Errorf("%s: expected %d messages, got %d (%s)", tt.name, tt.expectedLength, len(msgs), tt.description)
			}
		})
	}
}
