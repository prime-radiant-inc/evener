package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
)

// findSessionTranscriptsTool's Exec wrapper renders the structured envelope to
// text (the no-match path) and surfaces execFindSessionTranscripts errors.
func TestW2Tail_FindSessionTranscriptsTool_Exec(t *testing.T) {
	deps := &toolDeps{stateDir: t.TempDir(), sessionID: "sess-x"}
	tl := findSessionTranscriptsTool(deps)

	// No sessions on disk → the tool renders the "no matching sessions" text.
	out, err := tl.Exec(context.Background(), execenv.NewLocalExecutionEnvironment(t.TempDir()), map[string]any{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	s, ok := out.(string)
	if !ok || !strings.Contains(s, "No matching sessions") {
		t.Fatalf("Exec output = %#v", out)
	}

	// An unresolvable children_of ref surfaces an error through the wrapper.
	if _, err := tl.Exec(context.Background(), execenv.NewLocalExecutionEnvironment(t.TempDir()), map[string]any{
		"children_of": "local:bad/id",
	}); err == nil {
		t.Fatalf("expected error for bad children_of ref")
	}
}
