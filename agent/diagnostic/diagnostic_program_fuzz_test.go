//go:build serffuzz

package diagnostic

import (
	"errors"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzDiagnosticClassificationProgram replays each user-facing classification
// family through the public conversion APIs. The byte input varies the selected
// cases while the table preserves a concise semantic oracle for every keyword
// and explicit source override.
func FuzzDiagnosticClassificationProgram(f *testing.F) {
	for i := range diagnosticProgramMessages {
		f.Add(uint8(i))
	}

	f.Fuzz(func(t *testing.T, variant uint8) {
		classification := diagnosticProgramMessages[int(variant)%len(diagnosticProgramMessages)]
		assertDiagnosticProgramInfo(t, "Classify", Classify(classification.message), classification.source, classification.title)
		assertDiagnosticProgramInfo(t, "FromError plain", FromError(errors.New(classification.message)), classification.source, classification.title)
		assertDiagnosticProgramInfo(t, "defaultForSource fallback", defaultForSource(Source("invalid"), classification.message), classification.source, classification.title)

		override := diagnosticProgramOverrides[int(variant)%len(diagnosticProgramOverrides)]
		assertDiagnosticProgramInfo(t, "FromFields", FromFields(override.source, "", "", override.message), override.wantSource, override.wantTitle)
		if got := FromFields(override.source, "  custom title  ", "  custom hint  ", override.message); got.Title != "custom title" || got.Hint != "custom hint" {
			t.Fatalf("FromFields overrides = %+v", got)
		}

		assertDiagnosticProgramInfo(t, "FromError nil", FromError(nil), SourceSerf, "Serf error")
		assertDiagnosticProgramInfo(t, "FromError configuration", FromError(&llm.ConfigurationError{Message: classification.message}), SourceSerf, "Serf configuration error")
		assertDiagnosticProgramInfo(t, "FromError provider", FromError(llm.ErrorFromHTTPStatus("scripted", 500, classification.message, nil, nil)), SourceProvider, "Provider error")
	})
}

type diagnosticProgramMessage struct {
	message string
	source  Source
	title   string
}

var diagnosticProgramMessages = []diagnosticProgramMessage{
	{"unknown provider", SourceSerf, "Serf configuration error"},
	{"configuration error", SourceSerf, "Serf configuration error"},
	{"must use provider/model", SourceSerf, "Serf configuration error"},
	{"no model:", SourceSerf, "Serf configuration error"},
	{"rendezvous", SourceHub, "Hub error"},
	{"daemon spawn", SourceHub, "Hub error"},
	{"resume timed out", SourceHub, "Hub error"},
	{"process exited before rendezvous", SourceHub, "Hub error"},
	{"appwire", SourceHub, "Hub error"},
	{"websocket", SourceHub, "Hub error"},
	{"stream failed", SourceHub, "Hub error"},
	{"source not found", SourceHub, "Hub error"},
	{"local daemon unavailable", SourceHub, "Hub error"},
	{"session unavailable", SourceHub, "Hub error"},
	{"provider unavailable", SourceProvider, "Provider error"},
	{"api key", SourceProvider, "Provider error"},
	{"rate limit", SourceProvider, "Provider error"},
	{"quota", SourceProvider, "Provider error"},
	{"unauthorized", SourceProvider, "Provider error"},
	{"invalid_grant", SourceProvider, "Provider error"},
	{"token endpoint", SourceProvider, "Provider error"},
	{"stream ended without", SourceProvider, "Provider error"},
	{"stream error", SourceProvider, "Provider error"},
	{"missing response in finish event", SourceProvider, "Provider error"},
	{"ordinary local failure", SourceSerf, "Serf error"},
}

type diagnosticProgramOverride struct {
	source     string
	message    string
	wantSource Source
	wantTitle  string
}

var diagnosticProgramOverrides = []diagnosticProgramOverride{
	{"provider", "ordinary local failure", SourceProvider, "Provider error"},
	{"serf", "unknown provider", SourceSerf, "Serf configuration error"},
	{"serf", "ordinary local failure", SourceSerf, "Serf error"},
	{"hub", "ordinary local failure", SourceHub, "Hub error"},
	{"ui", "ordinary local failure", SourceUI, "UI error"},
	{"hook", "ordinary local failure", SourceHook, "Hook message"},
	{"mcp", "ordinary local failure", SourceMCP, "MCP server error"},
	{"not a source", "rate limit", SourceProvider, "Provider error"},
}

func assertDiagnosticProgramInfo(t *testing.T, operation string, got Info, wantSource Source, wantTitle string) {
	t.Helper()
	if got.Source != wantSource || got.Title != wantTitle || got.Hint == "" {
		t.Fatalf("%s = %+v, want source=%q title=%q with hint", operation, got, wantSource, wantTitle)
	}
}
