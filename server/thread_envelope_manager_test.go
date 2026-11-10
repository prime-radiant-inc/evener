package server

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// TestThreadEnvelopeSeedProjectsManagerFromMeta mirrors the Access half of the
// facetMeta sample: the daemon owns the start-time Manager property, persists it
// in session meta, and the envelope carries it to thread/read, so a client
// reading a live session sees Manager from the session itself, not from a
// hub-side preference.
func TestThreadEnvelopeSeedProjectsManagerFromMeta(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	source := &stubThreadEnvelopeSource{}
	source.meta.Manager = true
	publishEnvelope(srv, source)

	thread := readThreadOverWire(t, srv, "local:th_1")
	if !thread.Evener.Manager {
		t.Fatal("thread.Evener.Manager = false, want the meta's start-time Manager")
	}
}

// TestThreadEnvelopeSeedLeavesManagerFalseWhenNotStarted mirrors the absent
// half: an ordinary session reads Manager false, never invented.
func TestThreadEnvelopeSeedLeavesManagerFalseWhenNotStarted(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	publishEnvelope(srv, &stubThreadEnvelopeSource{})

	thread := readThreadOverWire(t, srv, "local:th_1")
	if thread.Evener.Manager {
		t.Fatal("thread.Evener.Manager = true, want false for an ordinary session")
	}
}

// TestManagerMetaComesFromSessionConfig pins the production link the stub tests
// cannot see: a session constructed with the flag reports it in Meta(), which
// is what liveThreadEnvelopeSource samples, so the daemon reports Manager from
// the session itself rather than any hub-side record.
func TestManagerMetaComesFromSessionConfig(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient()
	for _, want := range []bool{true, false} {
		sess, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{
			StateDir: filepath.Join(root, "state"),
			Manager:  want,
			LLMSleep: noSleep,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := sess.Meta().Manager; got != want {
			t.Fatalf("Meta().Manager = %v, want the session's start-time %v", got, want)
		}
		sess.Close()
	}
}
