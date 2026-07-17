package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

func TestNewSessionAcquiresOwnershipBeforePersistingIdentity(t *testing.T) {
	stateDir := t.TempDir()
	wantErr := errors.New("ownership refused")
	var sessionID string

	sess, err := NewSession(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir: stateDir,
		AcquireSessionOwnership: func(id string) error {
			sessionID = id
			for _, path := range []string{
				filepath.Join(stateDir, sessionsSubdir, id),
				filepath.Join(stateDir, sessionsSubdir, id+".meta.json"),
				filepath.Join(stateDir, sessionsSubdir, id+".transcript.jsonl"),
			} {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("session artifact %s existed before ownership acquisition: %v", path, statErr)
				}
			}
			return wantErr
		},
	})
	if sess != nil {
		sess.Close()
		t.Fatal("NewSession returned a session after ownership refusal")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("NewSession error = %v, want ownership refusal", err)
	}
	if sessionID == "" {
		t.Fatal("NewSession did not present its generated ID for ownership acquisition")
	}
}

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
