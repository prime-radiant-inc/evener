package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
)

// writeWatchFrameEvent dispatches on the event payload's concrete type. The value
// forms are exercised widely elsewhere; the POINTER forms (each non-nil and nil)
// are the uncovered arms — a nil pointer must render nothing, a non-nil pointer
// must render the same block as its value form.
func TestW2Watch_writeWatchFrameEventPointerForms(t *testing.T) {
	render := func(data events.EventData) string {
		var b strings.Builder
		writeWatchFrameEvent(&b, events.SessionEvent{Data: data})
		return b.String()
	}

	cases := []struct {
		name        string
		ptr         events.EventData
		nilPtr      events.EventData
		wantContain string
	}{
		{
			name:        "communicate",
			ptr:         &events.CommunicateData{Message: "hi there", EndTurn: true},
			nilPtr:      (*events.CommunicateData)(nil),
			wantContain: "kind: communicate",
		},
		{
			name:        "assistant.message",
			ptr:         &events.AssistantTextEndData{Text: "some text", Model: "m1"},
			nilPtr:      (*events.AssistantTextEndData)(nil),
			wantContain: "kind: assistant.message",
		},
		{
			name:        "assistant.tool",
			ptr:         &events.ToolCallEndData{ToolName: "read_file", CallID: "c1", Output: "out"},
			nilPtr:      (*events.ToolCallEndData)(nil),
			wantContain: "kind: assistant.tool",
		},
		{
			name:        "job.notification",
			ptr:         &events.JobFinishedData{JobID: "job_x", Status: "completed", OutputBytes: 42},
			nilPtr:      (*events.JobFinishedData)(nil),
			wantContain: "kind: job.notification",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render(tc.ptr)
			if !strings.Contains(got, tc.wantContain) {
				t.Fatalf("non-nil pointer %s rendered %q, want it to contain %q", tc.name, got, tc.wantContain)
			}
			if nilGot := render(tc.nilPtr); nilGot != "" {
				t.Fatalf("nil pointer %s rendered %q, want empty", tc.name, nilGot)
			}
		})
	}
}
