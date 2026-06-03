package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/sessionlog"
	"primeradiant.com/serf/agent/provider"
)

func testOpenAIProfileWithContextWindow(contextWindow int) *provider.Profile {
	return testProfile("openai", "test", contextWindow)
}

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
