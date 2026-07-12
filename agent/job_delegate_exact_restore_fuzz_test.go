//go:build serffuzz

package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzJobDelegateExactRestoreCoverage replays one deterministic restore program.
// The input is intentionally ignored: this target exists to retain exact seed
// coverage for restore-only branches without turning them into a broad fuzzer.
func FuzzJobDelegateExactRestoreCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		jdExactAssessmentFailures(t)
		jdExactProfileAndPureHelpers(t)
		jdExactRestoreCollisions(t)
	})
}

func jdExactAssessmentFailures(t *testing.T) {
	t.Helper()

	t.Run("missing-meta", func(t *testing.T) {
		s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
		rec := seedStoppedDelegateRestoreRecord(t, s)
		childID := rec.DelegateRestore.ChildSessionID
		if err := os.Remove(filepath.Join(s.stateDir, sessionsSubdir, childID+".meta.json")); err != nil {
			t.Fatal(err)
		}
		if got := s.assessDelegateResumability(rec, delegateResumabilityPreflight).Reason; got != notResumableMissingChildSessionMeta {
			t.Fatalf("reason = %q, want %q", got, notResumableMissingChildSessionMeta)
		}
	})

	t.Run("mismatched-meta", func(t *testing.T) {
		s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
		rec := seedStoppedDelegateRestoreRecord(t, s)
		childID := rec.DelegateRestore.ChildSessionID
		meta, err := schema.LoadSessionMeta(s.stateDir, childID)
		if err != nil {
			t.Fatal(err)
		}
		meta.ID = "different-child"
		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.stateDir, sessionsSubdir, childID+".meta.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := s.assessDelegateResumability(rec, delegateResumabilityPreflight).Reason; got != notResumableCorruptChildSessionMeta {
			t.Fatalf("reason = %q, want corrupt meta", got)
		}
	})

	for _, tc := range []struct{ name, body, want string }{
		{"transcript-mismatch", `{"kind":"header","session_id":"other"}` + "\n", notResumableTranscriptSessionMismatch},
		{"transcript-corrupt", "not-json\n", notResumableCorruptChildTranscript},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newLeanDelegateRestorePreflightSession(t, llm.NewClient())
			rec := seedStoppedDelegateRestoreRecord(t, s)
			path := filepath.Join(s.stateDir, sessionsSubdir, rec.DelegateRestore.ChildSessionID+".transcript.jsonl")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := s.assessDelegateResumability(rec, delegateResumabilityPreflight).Reason; got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func jdExactProfileAndPureHelpers(t *testing.T) {
	t.Helper()
	base := NewOpenAIProfile("base")
	s := &Session{profile: base}
	s.resolveProfile = func(ref string) (*provider.Profile, error) {
		if ref != "other/model" {
			t.Fatalf("profile ref = %q", ref)
		}
		return NewOpenAIProfile("resolved"), nil
	}
	if got, err := s.resolveDelegateRestoreProfileRef(base, "other", "model"); err != nil || got == nil {
		t.Fatalf("cross-profile resolution = (%v, %v)", got, err)
	}
	s.resolveProfile = nil
	if _, err := s.resolveDelegateRestoreProfileRef(base, "other", "model"); err == nil {
		t.Fatal("missing cross-profile resolver was accepted")
	}

	if _, ok := delegateRestoreLocalEnvPolicy(nil); ok {
		t.Fatal("nil local environment descriptor accepted")
	}
	if _, ok := delegateRestoreWorkingDir(nil); ok {
		t.Fatal("nil working directory descriptor accepted")
	}
	if got := restoredDelegateAllowedTools(&jobstore.DelegateRestoreDescriptor{FrozenToolNames: []string{"*"}}); got != nil {
		t.Fatalf("wildcard tools = %v, want nil", got)
	}
	if err := (*Session)(nil).validateRestoredDelegateRequiredTools(&jobstore.DelegateRestoreDescriptor{FrozenToolNames: []string{"missing"}}); err == nil {
		t.Fatal("nil session tool registry accepted")
	}
	if err := validateRestoredDelegateTools(nil, &jobstore.DelegateRestoreDescriptor{FrozenToolNames: []string{"missing"}}); err == nil {
		t.Fatal("missing restored child tool accepted")
	}

	root := t.TempDir()
	parent := execenv.NewLocalExecutionEnvironment(root)
	parent.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeRestricted, Network: true}
	s = &Session{env: parent}
	loose := &jobstore.DelegateRestoreDescriptor{Sandbox: &jobstore.SandboxSnapshot{Mode: sandbox.ModeReadOnly.String()}}
	if _, reason := s.resolveRestoredDelegateSandbox(loose, root); reason != notResumableSandboxUnsatisfiable {
		t.Fatalf("loose sandbox reason = %q", reason)
	}
	off := &jobstore.DelegateRestoreDescriptor{WorkingDir: root, LocalEnvPolicy: "default"}
	if rp, reason := s.resolveRestoredDelegateSandbox(off, root); rp != nil || reason != "" {
		t.Fatalf("off sandbox = (%v, %q)", rp, reason)
	}
	if _, err := s.restoreDelegateChildEnvironment(&jobstore.DelegateRestoreDescriptor{
		WorkingDir: root, LocalEnvPolicy: "default", Sandbox: &jobstore.SandboxSnapshot{Mode: "corrupt"},
	}, "dlg"); err == nil || !strings.Contains(err.Error(), notResumableSandboxUnsatisfiable) {
		t.Fatalf("corrupt sandbox restore error = %v", err)
	}
}

func jdExactRestoreCollisions(t *testing.T) {
	t.Helper()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	s := newDelegateRestorePreflightSession(t, client)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	preflight := requireDelegateRestorePreflight(t, s, rec)
	childID := rec.DelegateRestore.ChildSessionID

	existing := &subagent{id: childID, sess: &Session{}}
	s.subagents.track(existing)
	if got, err := s.restoreTerminalDelegateChild(rec, childID, preflight); err != nil || got != existing {
		t.Fatalf("existing restore = (%p, %v), want %p", got, err, existing)
	}
	s.subagents.mu.Lock()
	delete(s.subagents.subs, childID)
	s.subagents.mu.Unlock()

	afterClaim, beforeTrack := false, false
	s.delegateRestoreAfterClaim = func() { afterClaim = true }
	s.delegateRestoreBeforeTrack = func() {
		beforeTrack = true
		s.subagents.track(&subagent{id: childID, sess: nil})
	}
	t.Cleanup(func() {
		s.subagents.mu.Lock()
		delete(s.subagents.subs, childID)
		s.subagents.mu.Unlock()
	})
	if _, err := s.restoreTerminalDelegateChildClaimed(rec, childID, preflight); err == nil || !strings.Contains(err.Error(), "unavailable retained runtime") {
		t.Fatalf("collision error = %v", err)
	}
	if !afterClaim || !beforeTrack {
		t.Fatalf("restore hooks = afterClaim:%t beforeTrack:%t", afterClaim, beforeTrack)
	}

	old := delegateRestoreSession
	delegateRestoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, RestoreSessionConfig) (*Session, error) {
		return &Session{}, errors.New("exact restore failure")
	}
	t.Cleanup(func() { delegateRestoreSession = old })
}
