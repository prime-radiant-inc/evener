package agent

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestCompactionWriteFailureStillResetsEnvironmentTracker(t *testing.T) {
	s := newScriptedSummaryCompactSession(t, "env-compaction-write-error", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, withDir(t.TempDir()))
	if err := s.maybeAppendEnvironmentContext(); err != nil {
		t.Fatal(err)
	}
	if !s.envTracker.State().HasSent {
		t.Fatal("environment tracker did not record the initial block")
	}
	s.transcriptReady = true
	if err := s.transcript.Close(); err != nil {
		t.Fatal(err)
	}
	s.handleCompactionTurn(schema.NewTurn(schema.TurnSummary, llm.Assistant("summary")))
	if s.envTracker.State().HasSent {
		t.Fatal("environment tracker retained a snapshot after compaction replaced history")
	}
}
