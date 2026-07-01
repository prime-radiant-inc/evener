package msgrender

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func TestCheckmarkFor(t *testing.T) {
	if got := checkmarkFor(false, "boom"); got != "✕" {
		t.Fatalf("error checkmark = %q, want ✕", got)
	}
	if got := checkmarkFor(true, ""); got != "✓" {
		t.Fatalf("done checkmark = %q, want ✓", got)
	}
	if got := checkmarkFor(false, ""); got != "·" {
		t.Fatalf("in-progress checkmark = %q, want ·", got)
	}
}

func TestStateColorForToolDone(t *testing.T) {
	th := tuitheme.ActiveTheme()
	if got := stateColorForToolDone(true, "boom"); got != th.StateAwaiting {
		t.Fatalf("error color = %v, want StateAwaiting", got)
	}
	if got := stateColorForToolDone(true, ""); got != th.StateIdle {
		t.Fatalf("done color = %v, want StateIdle", got)
	}
	if got := stateColorForToolDone(false, ""); got != th.StateProcessing {
		t.Fatalf("in-progress color = %v, want StateProcessing", got)
	}
}

func TestIndentBlock(t *testing.T) {
	got := indentBlock("a\nb", 2)
	if got != "  a\n  b" {
		t.Fatalf("indentBlock = %q, want two-space indent on each line", got)
	}
}

func TestFormatDur_Ranges(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Microsecond, "<1ms"},
		{250 * time.Millisecond, "250ms"},
		{1500 * time.Millisecond, "1.5s"},
	}
	for _, tc := range tests {
		if got := formatDur(tc.d); got != tc.want {
			t.Errorf("formatDur(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestWrapText_HardBreaksUnbreakableWord(t *testing.T) {
	// No space within the budget forces a hard break at the budget boundary.
	lines := wrapText("abcdefghij", 4, 4)
	if len(lines) < 2 {
		t.Fatalf("expected multiple hard-broken lines, got %v", lines)
	}
	for _, l := range lines {
		if len(l) > 4 {
			t.Fatalf("line %q exceeds budget 4", l)
		}
	}
}

func TestRenderSelectedMessage(t *testing.T) {
	if got := RenderSelectedMessage("body", false); got != "body" {
		t.Fatalf("unfocused = %q, want passthrough", got)
	}
	if got := RenderSelectedMessage("", true); got != "" {
		t.Fatalf("empty focused = %q, want empty", got)
	}
	got := RenderSelectedMessage("line1\nline2", true)
	if !strings.HasPrefix(got, SelectionPrefix) {
		t.Fatalf("focused render = %q, want selection prefix", got)
	}
}

func TestRenderMessage_AcrossKinds(t *testing.T) {
	withTestColorProfile(t)

	tests := []struct {
		name      string
		msg       transcript.ChatMessage
		focused   bool
		wantEmpty bool
	}{
		{"user", transcript.ChatMessage{Kind: transcript.MsgUser, Text: "hi"}, false, false},
		{"user focused", transcript.ChatMessage{Kind: transcript.MsgUser, Text: "hi"}, true, false},
		{"assistant", transcript.ChatMessage{Kind: transcript.MsgAssistant, Text: "answer"}, false, false},
		{"assistant empty", transcript.ChatMessage{Kind: transcript.MsgAssistant, Text: "   "}, false, true},
		{"reasoning streaming", transcript.ChatMessage{Kind: transcript.MsgReasoning, Text: "thinking"}, false, false},
		{"reasoning collapsed", transcript.ChatMessage{Kind: transcript.MsgReasoning, Text: "thinking hard", Done: true}, false, false},
		{"reasoning empty", transcript.ChatMessage{Kind: transcript.MsgReasoning, Text: ""}, false, true},
		{"communicate", transcript.ChatMessage{Kind: transcript.MsgCommunicate, Text: "response"}, false, false},
		{"tool hidden", transcript.ChatMessage{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "communicate", Hidden: true}}, false, true},
		{"tool nil", transcript.ChatMessage{Kind: transcript.MsgTool, Tool: nil}, false, true},
		{"tool visible", transcript.ChatMessage{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Name: "read_file", Done: true}}, false, false},
		{"system", transcript.ChatMessage{Kind: transcript.MsgSystem, Text: "note"}, false, false},
		{"steering", transcript.ChatMessage{Kind: transcript.MsgSteering, Text: "steer"}, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderMessage(tc.msg, 40, tc.focused)
			if tc.wantEmpty && got != "" {
				t.Fatalf("RenderMessage = %q, want empty", got)
			}
			if !tc.wantEmpty && got == "" {
				t.Fatal("RenderMessage = empty, want content")
			}
		})
	}
}

func TestRenderMessage_PendingAndFailedPrefix(t *testing.T) {
	withTestColorProfile(t)

	pending := RenderMessage(transcript.ChatMessage{Kind: transcript.MsgSystem, Text: "x", Pending: true}, 40, false)
	if pending == "" {
		t.Fatal("pending render empty")
	}
	failed := RenderMessage(transcript.ChatMessage{Kind: transcript.MsgSystem, Text: "x", Failed: true, Reason: "timeout"}, 40, false)
	if !strings.Contains(failed, "failed: timeout") {
		t.Fatalf("failed render = %q, want reason suffix", failed)
	}
}
