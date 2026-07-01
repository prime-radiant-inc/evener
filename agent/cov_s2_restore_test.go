package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestS2Cov_RestoreSession_NilArgGuards covers the nil client/profile/env guards
// in RestoreSessionFromMetaWithConfig.
func TestS2Cov_RestoreSession_NilArgGuards(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	profile := NewOpenAIProfile("gpt-5.2")
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	meta := schema.SessionMeta{ID: "01RESTOREGUARD0000000001", ProfileID: "openai", Model: "gpt-5.2"}

	if _, err := RestoreSessionFromMetaWithConfig(nil, profile, env, meta, RestoreSessionConfig{}); err == nil || !strings.Contains(err.Error(), "llm client is nil") {
		t.Fatalf("nil client err = %v", err)
	}
	if _, err := RestoreSessionFromMetaWithConfig(client, nil, env, meta, RestoreSessionConfig{}); err == nil || !strings.Contains(err.Error(), "profile is nil") {
		t.Fatalf("nil profile err = %v", err)
	}
	if _, err := RestoreSessionFromMetaWithConfig(client, profile, nil, meta, RestoreSessionConfig{}); err == nil || !strings.Contains(err.Error(), "execution environment is nil") {
		t.Fatalf("nil env err = %v", err)
	}
}
