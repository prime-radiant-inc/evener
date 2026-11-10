package schema

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

func TestCovHookInfoAnnouncement(t *testing.T) {
	tests := []struct {
		name string
		info HookInfo
		want string
	}{
		{
			name: "all labels",
			info: HookInfo{
				Event:      "pre",
				PluginName: "myplugin",
				Matcher:    "exec_command",
				HookType:   "pre",
				ExitCode:   0,
			},
			want: "pre hook myplugin exec_command pre exit 0",
		},
		{
			name: "default event label",
			info: HookInfo{ExitCode: 1},
			want: "hook hook exit 1",
		},
		{
			name: "trim and omit empty labels",
			info: HookInfo{Event: "  post  ", PluginName: "  ", ExitCode: 2},
			want: "post hook exit 2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.Announcement(); got != tc.want {
				t.Fatalf("Announcement() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCovNewTurnPreservesMessage(t *testing.T) {
	msg := llm.User("hello")
	turn := NewTurn(TurnUserInput, msg)
	if turn.Kind != TurnUserInput {
		t.Fatalf("Kind = %v, want %v", turn.Kind, TurnUserInput)
	}
	if !reflect.DeepEqual(turn.Message, msg) {
		t.Fatalf("Message = %#v, want %#v", turn.Message, msg)
	}
	if got := turn.Message.Text(); got != "hello" {
		t.Fatalf("Message.Text() = %q, want %q", got, "hello")
	}
	if turn.Timestamp.IsZero() || turn.Timestamp.Location() != time.UTC {
		t.Fatalf("Timestamp = %v, want non-zero UTC", turn.Timestamp)
	}
}

// An unwritten turn must not alias durable entry 0. Every write path stamps the
// spent Seq over this default, including a real entry 0, so the sentinel is
// safe by construction and a producer that forgets to stamp cannot make a fold
// record name entry 0.
func TestNewTurnDefaultsToNoTranscriptEntrySeq(t *testing.T) {
	turn := NewTurn(TurnUserInput, llm.User("hi"))
	if turn.Seq != NoTranscriptEntrySeq {
		t.Fatalf("NewTurn Seq = %d, want NoTranscriptEntrySeq (%d)", turn.Seq, NoTranscriptEntrySeq)
	}
}
