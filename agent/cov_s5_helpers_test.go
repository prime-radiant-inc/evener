package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func TestS5Cov_SummarizeTaskPrompt(t *testing.T) {
	if got := summarizeTaskPrompt(""); got != "(no description)" {
		t.Errorf("empty → %q, want (no description)", got)
	}
	if got := summarizeTaskPrompt("Fix the bug. Then rest."); got != "Fix the bug." {
		t.Errorf("sentence break → %q, want 'Fix the bug.'", got)
	}
	long := strings.Repeat("word ", 40) // >120 chars, no sentence terminator
	got := summarizeTaskPrompt(long)
	if !strings.HasSuffix(got, "...") || len(got) > 121 {
		t.Errorf("long prompt should be truncated with ellipsis, got %q (len %d)", got, len(got))
	}
	if got := summarizeTaskPrompt("short and sweet"); got != "short and sweet" {
		t.Errorf("short → %q", got)
	}
}

func TestS5Cov_ToolDefinitionHelpers(t *testing.T) {
	defs := []llm.ToolDefinition{
		{Name: "a", Description: "does a"},
		{Name: "b"}, // no description
		{Name: "a"}, // duplicate
		{Name: ""},  // empty
	}
	entries := toolEntriesFromDefinitions(defs)
	if len(entries) != 4 || entries[1].Description != "(no description)" {
		t.Errorf("toolEntriesFromDefinitions = %+v", entries)
	}
	names := toolNamesFromDefinitions(defs)
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Errorf("toolNamesFromDefinitions = %v, want [a b] (deduped, empty dropped)", names)
	}
	set := toolNameSetFromDefinitions(defs)
	if !set["a"] || !set["b"] || set[""] {
		t.Errorf("toolNameSetFromDefinitions = %v", set)
	}
	if got := formatToolNamesForPrompt(nil); got != "none" {
		t.Errorf("empty names → %q, want none", got)
	}
	if got := formatToolNamesForPrompt([]string{"a", "b"}); got != "`a`, `b`" {
		t.Errorf("formatToolNamesForPrompt = %q", got)
	}
}

func TestS5Cov_LiveModelInfoFor(t *testing.T) {
	models := []llm.ModelInfo{{ID: "gpt-5"}, {ID: "Claude-X"}}
	if _, ok := liveModelInfoFor(models, ""); ok {
		t.Error("empty model should not match")
	}
	if info, ok := liveModelInfoFor(models, "gpt-5"); !ok || info.ID != "gpt-5" {
		t.Errorf("exact match failed: %+v %v", info, ok)
	}
	if info, ok := liveModelInfoFor(models, "claude-x"); !ok || info.ID != "Claude-X" {
		t.Errorf("case-insensitive match failed: %+v %v", info, ok)
	}
	if _, ok := liveModelInfoFor(models, "unknown"); ok {
		t.Error("unknown model should not match")
	}
}

func TestS5Cov_ResolveLiveModelProfile_NilGuards(t *testing.T) {
	if got := resolveLiveModelProfile(context.TODO(), nil, nil); got != nil {
		t.Error("nil client+profile should return the (nil) profile")
	}
}

func TestS5Cov_EnrichDiagnosticData(t *testing.T) {
	// Warning value + pointer.
	w := enrichDiagnosticData(events.EventWarning, events.WarningData{Message: "rate limit hit"})
	if wd, ok := w.(events.WarningData); !ok || wd.Source == "" {
		t.Errorf("warning value not enriched: %#v", w)
	}
	wp := enrichDiagnosticData(events.EventWarning, &events.WarningData{Message: "rate limit hit"})
	if wpd, ok := wp.(*events.WarningData); !ok || wpd.Source == "" {
		t.Errorf("warning pointer not enriched: %#v", wp)
	}
	// Nil pointer passes through untouched.
	var nilW *events.WarningData
	if got := enrichDiagnosticData(events.EventWarning, nilW); got != events.EventData(nilW) {
		t.Error("nil warning pointer should pass through")
	}
	// Error value + pointer.
	e := enrichDiagnosticData(events.EventError, events.ErrorData{Error: "rendezvous lost"})
	if ed, ok := e.(events.ErrorData); !ok || ed.Source == "" {
		t.Errorf("error value not enriched: %#v", e)
	}
	ep := enrichDiagnosticData(events.EventError, &events.ErrorData{Error: "rendezvous lost"})
	if epd, ok := ep.(*events.ErrorData); !ok || epd.Source == "" {
		t.Errorf("error pointer not enriched: %#v", ep)
	}
	var nilE *events.ErrorData
	if got := enrichDiagnosticData(events.EventError, nilE); got != events.EventData(nilE) {
		t.Error("nil error pointer should pass through")
	}
	// Unhandled kind returns data unchanged.
	if got := enrichDiagnosticData(events.EventContextCompaction, events.WarningData{Message: "x"}); got == nil {
		t.Error("unhandled kind should return data unchanged")
	}
}

func TestS5Cov_ProviderCauseFromError(t *testing.T) {
	if providerCauseFromError(nil, "m") != nil {
		t.Error("nil error → nil cause")
	}
	if providerCauseFromError(stubError("boom"), "m") != nil {
		t.Error("non-llm error → nil cause")
	}
	le := llm.ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)
	cause := providerCauseFromError(le, "gpt-5")
	if cause == nil || cause.Provider != "openai" || cause.Model != "gpt-5" || cause.Status != 429 {
		t.Errorf("provider cause = %+v", cause)
	}
}

func TestS5Cov_SetAPICallDiagnostic(t *testing.T) {
	setAPICallDiagnostic(nil, stubError("x")) // must not panic
	call := &transcript.APICall{}
	setAPICallDiagnostic(call, stubError("unknown provider: foo"))
	if call.Source == "" || call.Title == "" {
		t.Errorf("api call diagnostic not set: %+v", call)
	}
}

type stubError string

func (e stubError) Error() string { return string(e) }
