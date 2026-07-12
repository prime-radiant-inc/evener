package agent

// Tests for the error return added to Session.SetModel: unknown instance
// refs report the resolver's error without mutating the session, valid
// switches still apply, and the switched profile survives a crash-restore
// round trip through the flushed meta.json.

import (
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// unknownInstanceResolver mirrors the production resolver's behavior for an
// unknown instance ref: it returns an error naming the configured instances.
func unknownInstanceResolver(ref string) (*provider.Profile, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 2 && parts[0] == "openai" {
		return NewOpenAIProfile(parts[1]), nil
	}
	return nil, fmt.Errorf("unknown instance %q; configured instances: openai", ref)
}

// TestSetModel_UnknownInstance_ReturnsErrorAndLeavesProfileUnchanged verifies
// that SetModel with an unknown instance ref returns a non-nil error whose
// text lists configured instances, and that the session's profile is
// unchanged afterward.
func TestSetModel_UnknownInstance_ReturnsErrorAndLeavesProfileUnchanged(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			ResolveProfile:   unknownInstanceResolver,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)

	before := sess.currentProfile()

	err := sess.SetModel("bogus/some-model")
	if err == nil {
		t.Fatal("SetModel with unknown instance ref = nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "configured instances") {
		t.Fatalf("error = %q, want it to list configured instances", err.Error())
	}

	after := sess.currentProfile()
	if after.ID() != before.ID() || after.Model() != before.Model() {
		t.Fatalf("profile changed after failed SetModel: before=%s/%s after=%s/%s",
			before.ID(), before.Model(), after.ID(), after.Model())
	}
}

// TestSetModel_SameProvider_ReturnsNilAndChangesModel verifies that a valid
// same-provider switch returns nil and the profile's Model() changes.
func TestSetModel_SameProvider_ReturnsNilAndChangesModel(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)

	err := sess.SetModel("gpt-4.1-mini")
	if err != nil {
		t.Fatalf("SetModel same-provider switch: %v", err)
	}
	if got := sess.currentProfile().Model(); got != "gpt-4.1-mini" {
		t.Fatalf("Model() = %q, want gpt-4.1-mini", got)
	}
}

// TestSetModel_CrossProvider_ReturnsNilAndSwapsProfileID verifies that a valid
// cross-provider switch via an injected resolver returns nil and swaps
// profile.ID().
func TestSetModel_CrossProvider_ReturnsNilAndSwapsProfileID(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "anthropic"}),
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			ResolveProfile:   testResolver,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)

	err := sess.SetModel("anthropic/claude-opus-4-6")
	if err != nil {
		t.Fatalf("SetModel cross-provider switch: %v", err)
	}
	if got := sess.currentProfile().ID(); got != "anthropic" {
		t.Fatalf("ID() = %q, want anthropic", got)
	}
}

// TestSetModel_CrashRestore_SwitchedModelSurvives verifies that after a
// successful SetModel, the flushed meta.json reflects the switched model,
// and RestoreSessionFromMetaWithConfig from that meta produces a session
// whose profile is the switched model (not the launch model).
func TestSetModel_CrashRestore_SwitchedModelSurvives(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		StateDir:         dir,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if err := sess.SetModel("gpt-4.1-mini"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	sessID := sess.ID()
	sess.Close()

	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta.Model != "gpt-4.1-mini" {
		t.Fatalf("flushed meta.Model = %q, want gpt-4.1-mini", meta.Model)
	}

	// The caller resolves the profile from the persisted meta before restoring
	// (mirrors production: cmd/serf reconstructs the profile from meta.Model).
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile(meta.Model), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if got := restored.currentProfile().Model(); got != "gpt-4.1-mini" {
		t.Fatalf("restored profile Model() = %q, want gpt-4.1-mini", got)
	}
}
