package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
)

// Full-output retention is best effort: Session already degrades when a Put
// fails, appending retention_failed to the tool result. Creating the store is
// the same kind of convenience, and must not turn a resumable session into a
// restore failure — a vanished temp dir is the live example.
func TestRestoreSurvivesArtifactStoreCreationFailure(t *testing.T) {
	installArtifactStoreFactory(t, func() (artifactStore, error) {
		return nil, errors.New("temp dir is gone")
	})
	sess, err := RestoreSessionFromMetaWithConfig(
		newArtifactTestClient(),
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		artifactRestoreMeta(t),
		artifactRestoreConfig(t, t.TempDir()),
	)
	if err != nil {
		t.Fatalf("restore failed on artifact store creation: %v", err)
	}
	if sess.artifactStore != nil {
		t.Fatal("session kept an artifact store the factory did not return")
	}
	sess.Close()
}

func TestFreshSessionSurvivesArtifactStoreCreationFailure(t *testing.T) {
	installArtifactStoreFactory(t, func() (artifactStore, error) {
		return nil, errors.New("temp dir is gone")
	})
	sess := newArtifactTestRoot(t)
	if sess.artifactStore != nil {
		t.Fatal("session kept an artifact store the factory did not return")
	}
	sess.Close()
}

// The notice must reach the client, and it must arrive after SESSION_START:
// a warning emitted during construction is lost for good.
func TestArtifactStoreCreationFailureWarnsAfterSessionStart(t *testing.T) {
	installArtifactStoreFactory(t, func() (artifactStore, error) {
		return nil, errors.New("temp dir is gone")
	})
	sess := newArtifactTestRoot(t)
	sess.Close()

	sessionStartIdx, warningIdx := -1, -1
	for i, ev := range drainArtifactSessionEvents(sess) {
		switch ev.Kind {
		case events.EventSessionStart:
			if sessionStartIdx == -1 {
				sessionStartIdx = i
			}
		case events.EventWarning:
			if w, ok := ev.Data.(events.WarningData); ok &&
				strings.Contains(w.Message, "full tool output will not be retained") && warningIdx == -1 {
				warningIdx = i
			}
		}
	}
	if sessionStartIdx == -1 {
		t.Fatal("no SESSION_START on the stream")
	}
	if warningIdx == -1 {
		t.Fatal("no warning that full tool output will not be retained")
	}
	if warningIdx <= sessionStartIdx {
		t.Fatalf("warning (index %d) arrived before SESSION_START (index %d)", warningIdx, sessionStartIdx)
	}
}

// drainArtifactSessionEvents closes sess and returns its stream in emission
// order.
func drainArtifactSessionEvents(sess *Session) []events.SessionEvent {
	sess.Close()
	var evs []events.SessionEvent
	for ev := range sess.Events() {
		evs = append(evs, ev)
	}
	return evs
}
