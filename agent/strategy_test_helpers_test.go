package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/sessionlog"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// mustNewSessionLog is a test helper that creates a sessionlog.SessionLog or
// fails the test.
func mustNewSessionLog(t *testing.T, path string) *sessionlog.SessionLog {
	t.Helper()
	log, err := sessionlog.NewSessionLog(path)
	if err != nil {
		t.Fatalf("NewSessionLog(%q): %v", path, err)
	}
	return log
}

// toolResultContent returns the first string tool-result content in a turn, or
// "" if none. Used by transcript assertions.
func toolResultContent(t schema.Turn) string {
	for _, p := range t.Message.Content {
		if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
			if s, ok := p.ToolResult.Content.(string); ok {
				return s
			}
		}
	}
	return ""
}
