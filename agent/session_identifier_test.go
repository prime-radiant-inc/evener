package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
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

func TestNewSessionReleasesOwnershipWhenInitializationFails(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close() //nolint:errcheck
	client := llm.NewClient()
	client.Use(logger)
	var sessionID string
	cfg := SessionConfig{
		StateDir: stateDir,
		AcquireSessionOwnership: func(id string) error {
			sessionID = id
			return logger.ReserveSession(id)
		},
	}
	cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "new_job_manager" {
			return errors.New("injected initialization failure")
		}
		return nil
	}
	if sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg); err == nil {
		sess.Close()
		t.Fatal("NewSession succeeded after injected initialization failure")
	}
	if sessionID == "" {
		t.Fatal("ownership callback was not called")
	}
	contender, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close() //nolint:errcheck
	if err := contender.ReserveSession(sessionID); err != nil {
		t.Fatalf("failed NewSession retained ownership: %v", err)
	}
}

func TestRestoreSessionReloadsMetadataAfterOwnershipAcquisition(t *testing.T) {
	stateDir := t.TempDir()
	meta := schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB"}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(stateDir, sessionsSubdir, meta.ID+".meta.json")
	acquired := false
	_, err := RestoreSessionFromMetaWithConfig(
		llm.NewClient(),
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{
			StateDir: stateDir,
			AcquireSessionOwnership: func(id string) error {
				acquired = true
				if id != meta.ID {
					t.Fatalf("ownership ID = %q, want %q", id, meta.ID)
				}
				return os.Remove(metaPath)
			},
		},
	)
	if err == nil {
		t.Fatal("restore succeeded after metadata was deleted under ownership")
	}
	if !acquired {
		t.Fatal("restore did not acquire ownership")
	}
	transcriptPath := filepath.Join(stateDir, sessionsSubdir, meta.ID+".transcript.jsonl")
	if _, statErr := os.Stat(transcriptPath); !os.IsNotExist(statErr) {
		t.Fatalf("failed revalidation created transcript: %v", statErr)
	}
}
