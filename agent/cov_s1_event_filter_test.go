package agent

import (
	"testing"

	"primeradiant.com/serf/agent/events"
)

func TestS1Cov_watchEventFilterMatches(t *testing.T) {
	toolEnd := func(name, errText string) events.SessionEvent {
		return events.SessionEvent{
			Kind: events.EventToolCallEnd,
			Data: events.ToolCallEndData{ToolName: name, Error: errText},
		}
	}

	// A nil filter matches everything.
	if !watchEventFilterMatches(nil, events.SessionEvent{Kind: events.EventCommunicate}) {
		t.Fatal("nil filter must match")
	}
	// A non-tool-call event never matches a concrete filter.
	if watchEventFilterMatches(&watchEventFilter{ToolName: "read_file"}, events.SessionEvent{Kind: events.EventCommunicate}) {
		t.Fatal("non-tool-call event must not match")
	}
	// Wrong data shape → false.
	if watchEventFilterMatches(&watchEventFilter{}, events.SessionEvent{Kind: events.EventToolCallEnd, Data: events.JobStartedData{}}) {
		t.Fatal("mismatched data type must not match")
	}
	// A nil pointer-typed payload → false.
	if watchEventFilterMatches(&watchEventFilter{}, events.SessionEvent{Kind: events.EventToolCallEnd, Data: (*events.ToolCallEndData)(nil)}) {
		t.Fatal("nil pointer payload must not match")
	}
	// A non-nil pointer payload with matching tool name → true.
	ptr := &events.ToolCallEndData{ToolName: "read_file"}
	if !watchEventFilterMatches(&watchEventFilter{ToolName: "read_file"}, events.SessionEvent{Kind: events.EventToolCallEnd, Data: ptr}) {
		t.Fatal("pointer payload with matching tool name must match")
	}
	// Tool name mismatch → false.
	if watchEventFilterMatches(&watchEventFilter{ToolName: "edit_file"}, toolEnd("read_file", "")) {
		t.Fatal("tool name mismatch must not match")
	}
	// Status mismatch (filter wants error, event is ok) → false.
	if watchEventFilterMatches(&watchEventFilter{Status: "error"}, toolEnd("read_file", "")) {
		t.Fatal("status mismatch must not match")
	}
	// Full match on name + error status → true.
	if !watchEventFilterMatches(&watchEventFilter{ToolName: "read_file", Status: "error"}, toolEnd("read_file", "boom")) {
		t.Fatal("name+status match must succeed")
	}
}
