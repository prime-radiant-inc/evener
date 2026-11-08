package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/server"
)

func TestServeAskFenceBootInitializedBeforeIdentityProjection(t *testing.T) {
	// Not parallel: the existing serve fixture installs process-wide provider config.
	deps, state, args := newClearServeDeps(t)
	prepare := deps.prepareAppIdentity
	observed := 0
	deps.prepareAppIdentity = func(sourceID, threadID, ref, path string) (server.PreparedAppIdentity, error) {
		sess := state.session(observed)
		if sess == nil || sess.ID() != threadID {
			t.Fatalf("identity projected before owned session: %s", threadID)
		}
		generation := sess.BootGeneration()
		if generation == "" {
			t.Fatal("identity projected before ask-fence generation initialization")
		}
		data, err := os.ReadFile(schema.BootGenerationPath(sess.StateDir(), threadID))
		if err != nil || strings.TrimSpace(string(data)) != generation {
			t.Fatalf("session generation=%q durable=%q error=%v", generation, data, err)
		}
		observed++
		return prepare(sourceID, threadID, ref, path)
	}
	obs := runClearAttempt(t, deps, state, args, nil)
	if obs.clearErr != nil {
		t.Fatal(obs.clearErr)
	}
	if observed != 2 {
		t.Fatalf("identity projections=%d, want fresh and clear replacement", observed)
	}
}

func TestRunAskFenceBootInitializedBeforeProviderInput(t *testing.T) {
	// Not parallel: runNewSession is a process-wide constructor seam.
	old := runNewSession
	oldRestore := runRestoreSession
	t.Cleanup(func() { runNewSession = old; runRestoreSession = oldRestore })
	var sess *agent.Session
	runNewSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		var err error
		sess, err = old(client, profile, env, cfg)
		return sess, err
	}
	runRestoreSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, meta schema.SessionMeta, cfg agent.RestoreSessionConfig) (*agent.Session, error) {
		var err error
		sess, err = oldRestore(client, profile, env, meta, cfg)
		return sess, err
	}
	stateDir := t.TempDir()
	observed := 0
	adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		observed++
		generation := sess.BootGeneration()
		data, err := os.ReadFile(schema.BootGenerationPath(stateDir, sess.ID()))
		if generation == "" || err != nil || strings.TrimSpace(string(data)) != generation {
			t.Errorf("provider received input before durable boot initialization: session=%q durable=%q error=%v", generation, data, err)
		}
		return scriptedCommunicate("done-sentinel")
	}}}
	installRunScriptedProvider(t, adapter)
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), runConfig{prompt: "input-sentinel", model: "openai/gpt-test", workDir: t.TempDir(), stateDir: stateDir, noDefaultMarketplaces: true, stdout: &stdout, stderr: &stderr}); err != nil {
		t.Fatal(err)
	}
	if observed != 1 {
		t.Fatal("provider input scenario did not execute")
	}
	if sess.BootGeneration() != "1" {
		t.Fatalf("fresh generation=%q", sess.BootGeneration())
	}
	adapter.mu.Lock()
	adapter.steps = append(adapter.steps, func(llm.Request) llm.Response {
		observed++
		data, err := os.ReadFile(schema.BootGenerationPath(stateDir, sess.ID()))
		if sess.BootGeneration() != "2" || err != nil || strings.TrimSpace(string(data)) != "2" {
			t.Errorf("resumed provider input generation=%q durable=%q err=%v", sess.BootGeneration(), data, err)
		}
		return scriptedCommunicate("resumed-done")
	})
	adapter.mu.Unlock()
	if err := run(context.Background(), runConfig{prompt: "followup", resume: sess.ID(), model: "openai/gpt-test", workDir: t.TempDir(), stateDir: stateDir, noDefaultMarketplaces: true, stdout: &stdout, stderr: &stderr}); err != nil {
		t.Fatal(err)
	}
	if observed != 2 {
		t.Fatalf("provider inputs=%d, want fresh and restore", observed)
	}
}
