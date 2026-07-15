package agent

import (
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

func TestNewSessionUsesCompactSessionID(t *testing.T) {
	sess, err := NewSession(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := identifier.ValidateSessionID(sess.ID()); err != nil {
		t.Fatalf("session ID %q: %v", sess.ID(), err)
	}
}
