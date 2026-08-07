package contextmgr

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestCheckpointRecoveryPointerNamesOnlyAvailableTools pins the rule ruled
// 2026-08-06 on the compaction artifacts: a checkpoint tells the agent how to
// recover the detail it folded away, and that instruction may only name
// transcript tools the session actually serves. A persistent session is not
// automatically one that can read its own transcript — a typed agent's tools:
// allowlist drops either transcript tool while the session id stays real.
func TestCheckpointRecoveryPointerNamesOnlyAvailableTools(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("do the work")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	for _, tc := range []struct {
		name    string
		tools   []string
		want    []string
		notWant []string
	}{
		{
			name:    "no transcript tools",
			tools:   nil,
			want:    []string{"01ABC"},
			notWant: []string{"read_transcript", "find_session_transcripts"},
		},
		{
			name:    "read only",
			tools:   []string{"read_transcript"},
			want:    []string{"read_transcript"},
			notWant: []string{"find_session_transcripts"},
		},
		{
			name:  "both",
			tools: []string{"read_transcript", "find_session_transcripts"},
			want:  []string{"read_transcript", "find_session_transcripts"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := &CompactionMeta{SessionID: "01ABC", AvailableTranscriptTools: tc.tools}
			text := checkpoint(history, 1, meta, "communicate")[0].Message.Text()
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("checkpoint = %q, want it to contain %q", text, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(text, notWant) {
					t.Errorf("checkpoint names a tool this session cannot call (%q): %q", notWant, text)
				}
			}
		})
	}
}
