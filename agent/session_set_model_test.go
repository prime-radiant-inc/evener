package agent

// Tests for the error return added to Session.SetModel: unknown instance
// refs report the resolver's error without mutating the session, valid
// switches still apply, and the switched profile survives a crash-restore
// round trip through the flushed meta.json.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// fakeEnumerableAdapter is a fakeAdapter that implements llm.ModelLister with
// a fixed model set, standing in for a provider instance whose models can be
// live-enumerated (mirrors an API-key-backed instance in production).
type fakeEnumerableAdapter struct {
	fakeAdapter
	models []llm.ModelInfo
}

func (a *fakeEnumerableAdapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return a.models, nil
}

// fakeUnenumerableAdapter is a fakeAdapter that fails model enumeration with
// an arbitrary error, standing in for a non-enumerable (e.g. OAuth-backed)
// instance whose ListModels either isn't implemented or fails for any reason.
// Per spec, the switch path fails open unconditionally on any enumeration
// error class.
type fakeUnenumerableAdapter struct {
	fakeAdapter
	err error
}

func (a *fakeUnenumerableAdapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.err != nil {
		return nil, a.err
	}
	return nil, fmt.Errorf("provider %s: enumeration exploded", a.name)
}

// TestSetModel_UnknownModelOnEnumerableInstance_Rejected verifies (a): a
// switch to a model absent from an enumerable instance's live model set is
// rejected, the error names the instance, and it names a live alternative
// model ID drawn from the enumerated list (Task 3: formatModelAlternatives).
func TestSetModel_UnknownModelOnEnumerableInstance_Rejected(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeEnumerableAdapter{
			fakeAdapter: fakeAdapter{name: "anthropic"},
			models:      []llm.ModelInfo{{ID: "claude-opus-4-6", Provider: "anthropic"}},
		}),
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			ResolveProfile:   testResolver,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)

	err := sess.SetModel("anthropic/not-a-real-model")
	if err == nil {
		t.Fatal("SetModel with unknown model on enumerable instance = nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("error = %q, want it to name the instance %q", err.Error(), "anthropic")
	}
	if !strings.Contains(err.Error(), "claude-opus-4-6") {
		t.Fatalf("error = %q, want it to name a live alternative %q", err.Error(), "claude-opus-4-6")
	}
}

// TestSetModel_NonEnumerableInstance_AcceptsUnlistedModel verifies (b): a
// non-enumerable instance (ListModels unimplemented) accepts a model the
// session has never seen enumerated.
func TestSetModel_NonEnumerableInstance_AcceptsUnlistedModel(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "anthropic"}), // no ListModels: non-enumerable
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			ResolveProfile:   testResolver,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)

	if err := sess.SetModel("anthropic/whatever-unlisted-model"); err != nil {
		t.Fatalf("SetModel on non-enumerable instance: %v", err)
	}
	if got := sess.currentProfile().ID(); got != "anthropic" {
		t.Fatalf("ID() = %q, want anthropic", got)
	}
}

// TestSetModel_EnumerationFailure_FailsOpenUnconditionally verifies (b): an
// enumeration failure of any error class (not just launchcheck's allowlisted
// messages) fails open and accepts the switch. This keeps the dead-credentials
// failure mode from blocking a switch.
func TestSetModel_EnumerationFailure_FailsOpenUnconditionally(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeUnenumerableAdapter{
			fakeAdapter: fakeAdapter{name: "anthropic"},
			err:         errors.New("401 unauthorized: invalid api key"), // not on launchcheck's allowlist
		}),
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			ResolveProfile:   testResolver,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)

	if err := sess.SetModel("anthropic/claude-opus-4-6"); err != nil {
		t.Fatalf("SetModel with enumeration failure should fail open, got error: %v", err)
	}
}

// documentTurn and audioTurn build a user-role Turn carrying a document (or
// audio) ContentPart directly, so unrepresentable-history tests don't depend
// on the tool path that produces these parts in production.
func documentTurn() schema.Turn {
	return schema.NewTurn(schema.TurnUserInput, llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentPart{
			{Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: []byte("pdf-bytes"), MediaType: "application/pdf"}},
		},
	})
}

func audioTurn() schema.Turn {
	return schema.NewTurn(schema.TurnUserInput, llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentPart{
			{Kind: llm.ContentAudio, Audio: &llm.AudioData{Data: []byte("wav-bytes"), MediaType: "audio/wav"}},
		},
	})
}

// TestSetModel_DocumentInHistory_RejectedForHardErrorAndCompatTargets verifies
// (c): a document in history rejects a switch to anthropic-family, google, and
// openai-compat targets, naming the "document" kind.
func TestSetModel_DocumentInHistory_RejectedForHardErrorAndCompatTargets(t *testing.T) {
	t.Parallel()
	targets := []string{"anthropic/claude-opus-4-6", "google/gemini-3-pro", "kimi/kimi-k3"}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			sess := newSession(t,
				withProfile(NewOpenAIProfile("gpt-5.4")),
				withAdapter(&fakeAdapter{name: "openai"}),
				withAdapter(&fakeAdapter{name: "anthropic"}),
				withAdapter(&fakeAdapter{name: "google"}),
				withAdapter(&fakeAdapter{name: "kimi"}),
				withConfig(SessionConfig{
					NoProjectPrompts: true,
					ResolveProfile:   testResolver,
					testOnly:         testConfig{skipGitSnapshot: true},
				}),
			)
			sess.appendTurn(schema.TurnUserInput, documentTurn().Message)

			err := sess.SetModel(target)
			if err == nil {
				t.Fatalf("SetModel(%q) with document in history = nil error, want non-nil", target)
			}
			if !strings.Contains(err.Error(), "document") {
				t.Fatalf("error = %q, want it to name the document kind", err.Error())
			}
		})
	}
}

// TestSetModel_DocumentInHistory_AcceptedForResponsesTarget verifies (d): the
// same history containing a document is accepted for an openai Responses
// target, which carries documents.
func TestSetModel_DocumentInHistory_AcceptedForResponsesTarget(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)
	sess.appendTurn(schema.TurnUserInput, documentTurn().Message)

	if err := sess.SetModel("gpt-4.1-mini"); err != nil {
		t.Fatalf("SetModel same-provider (Responses) switch with document in history: %v", err)
	}
}

// TestSetModel_AudioInHistory_RejectedForAllTargetsIncludingResponses verifies
// (e): audio in history is rejected for anthropic-family, google,
// openai-compat, AND openai Responses targets.
func TestSetModel_AudioInHistory_RejectedForAllTargetsIncludingResponses(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name   string
		target string
	}
	cases := []testCase{
		{"anthropic-family", "anthropic/claude-opus-4-6"},
		{"google", "google/gemini-3-pro"},
		{"openai-compat", "kimi/kimi-k3"},
		{"openai-responses-same-provider", "gpt-4.1-mini"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := newSession(t,
				withProfile(NewOpenAIProfile("gpt-5.4")),
				withAdapter(&fakeAdapter{name: "openai"}),
				withAdapter(&fakeAdapter{name: "anthropic"}),
				withAdapter(&fakeAdapter{name: "google"}),
				withAdapter(&fakeAdapter{name: "kimi"}),
				withConfig(SessionConfig{
					NoProjectPrompts: true,
					ResolveProfile:   testResolver,
					testOnly:         testConfig{skipGitSnapshot: true},
				}),
			)
			sess.appendTurn(schema.TurnUserInput, audioTurn().Message)

			err := sess.SetModel(tc.target)
			if err == nil {
				t.Fatalf("SetModel(%q) with audio in history = nil error, want non-nil", tc.target)
			}
			if !strings.Contains(err.Error(), "audio") {
				t.Fatalf("error = %q, want it to name the audio kind", err.Error())
			}
		})
	}
}

// unrepresentableTargetResolver resolves the anthropic-builder targets whose
// misclassification was the bricking bug the unification fix closes:
// kimi-anthropic and minimax both route through the anthropic request builder,
// which hard-errors on document/audio. Before the fix,
// unrepresentableContentKinds' hand-maintained anthropic branch omitted both, so
// a switch into them with a document or audio already in history passed the
// preflight and then hard-failed at every subsequent turn — a bricked session.
func unrepresentableTargetResolver(ref string) (*provider.Profile, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return nil, nil
	}
	provider := strings.ToLower(parts[0])
	model := parts[1]
	switch provider {
	case "openai":
		return NewOpenAIProfile(model), nil
	case "kimi-anthropic":
		return newKimiAnthropicProfile(model), nil
	case "minimax":
		return newMiniMaxProfile(model), nil
	}
	return nil, nil
}

// TestUnrepresentableContentKinds_FamilyTable pins the derived per-tag policy so
// the switch preflight and the N4 builderFamily classifier cannot drift again.
// zai/deepseek/together are openai-compat thinking-format tags carried defensively
// in builderFamily; they are unit-covered here because they are not constructible
// as live profiles (they are thinking_format values, not provider types).
func TestUnrepresentableContentKinds_FamilyTable(t *testing.T) {
	t.Parallel()
	doc := llm.ContentDocument
	aud := llm.ContentAudio
	both := map[llm.ContentKind]bool{doc: true, aud: true}
	audioOnly := map[llm.ContentKind]bool{aud: true}
	cases := []struct {
		tag  string
		want map[llm.ContentKind]bool
	}{
		{"anthropic", both},
		{"kimi-anthropic", both},
		{"openrouter-anthropic", both},
		{"minimax", both},
		{"google", both},
		{"openai-compatible", both},
		{"kimi", both},
		{"glm", both},
		{"zai", both},
		{"deepseek", both},
		{"together", both},
		{"ollama", both},
		{"openrouter", both},
		{"openai", audioOnly},
		{"some-future-provider", nil},
	}
	for _, tc := range cases {
		got := unrepresentableContentKinds(tc.tag)
		if len(got) != len(tc.want) {
			t.Errorf("unrepresentableContentKinds(%q) = %v, want %v", tc.tag, got, tc.want)
			continue
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Errorf("unrepresentableContentKinds(%q)[%v] = %v, want %v", tc.tag, k, got[k], v)
			}
		}
	}
}

// TestSetModel_DocumentInHistory_RejectedForNewlyClassifiedTargets is the
// end-to-end regression guard for the bricking bug: a document in history must
// reject a switch into kimi-anthropic/minimax (both route through the anthropic
// builder). Before the fix, unrepresentableContentKinds returned nil for both,
// so the switch was NOT rejected and every subsequent turn hard-failed.
func TestSetModel_DocumentInHistory_RejectedForNewlyClassifiedTargets(t *testing.T) {
	t.Parallel()
	targets := []string{
		"kimi-anthropic/kimi-k3",
		"minimax/MiniMax-M2.7",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			sess := newSession(t,
				withProfile(NewOpenAIProfile("gpt-5.4")),
				withAdapter(&fakeAdapter{name: "openai"}),
				withConfig(SessionConfig{
					NoProjectPrompts: true,
					ResolveProfile:   unrepresentableTargetResolver,
					testOnly:         testConfig{skipGitSnapshot: true},
				}),
			)
			sess.appendTurn(schema.TurnUserInput, documentTurn().Message)

			err := sess.SetModel(target)
			if err == nil {
				t.Fatalf("SetModel(%q) with document in history = nil error, want non-nil (was bricking before fix)", target)
			}
			if !strings.Contains(err.Error(), "document") {
				t.Fatalf("error = %q, want it to name the document kind", err.Error())
			}
		})
	}
}

// TestSetModel_AudioInHistory_RejectedForAnthropicBuilderTargets verifies the
// same unification for audio: kimi-anthropic and minimax route through the
// anthropic builder, which hard-errors on audio, so an audio part in history
// must reject the switch (was silently allowed → bricked session before the fix).
func TestSetModel_AudioInHistory_RejectedForAnthropicBuilderTargets(t *testing.T) {
	t.Parallel()
	targets := []string{"kimi-anthropic/kimi-k3", "minimax/MiniMax-M2.7"}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			sess := newSession(t,
				withProfile(NewOpenAIProfile("gpt-5.4")),
				withAdapter(&fakeAdapter{name: "openai"}),
				withConfig(SessionConfig{
					NoProjectPrompts: true,
					ResolveProfile:   unrepresentableTargetResolver,
					testOnly:         testConfig{skipGitSnapshot: true},
				}),
			)
			sess.appendTurn(schema.TurnUserInput, audioTurn().Message)

			err := sess.SetModel(target)
			if err == nil {
				t.Fatalf("SetModel(%q) with audio in history = nil error, want non-nil (was bricking before fix)", target)
			}
			if !strings.Contains(err.Error(), "audio") {
				t.Fatalf("error = %q, want it to name the audio kind", err.Error())
			}
		})
	}
}

// rejectionTestResolver behaves like testResolver for the anthropic/google
// instances the byte-identical rejection test configures, but — like
// unknownInstanceResolver — returns an error naming the configured instances
// for any other prefix, so an unknown-instance switch actually rejects
// instead of silently falling through to a same-provider WithModel.
func rejectionTestResolver(ref string) (*provider.Profile, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 2 {
		switch strings.ToLower(parts[0]) {
		case "anthropic":
			return newAnthropicProfile(parts[1]), nil
		case "google":
			return newGeminiProfile(parts[1]), nil
		}
	}
	return nil, fmt.Errorf("unknown instance %q; configured instances: openai, anthropic, google", ref)
}

// TestSetModel_RejectionLeavesProfileMetaAndHistoryUnchanged verifies (f):
// every rejection path (unknown instance, unknown model on an enumerable
// instance, and unrepresentable history) leaves the profile, the flushed
// meta.json, and the history byte-identical.
func TestSetModel_RejectionLeavesProfileMetaAndHistoryUnchanged(t *testing.T) {
	t.Parallel()

	newRejectionSession := func(t *testing.T) *Session {
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})
		c.Register(&fakeAdapter{name: "anthropic"})
		c.Register(&fakeEnumerableAdapter{
			fakeAdapter: fakeAdapter{name: "google"},
			models:      []llm.ModelInfo{{ID: "gemini-3-pro", Provider: "google"}},
		})
		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			NoProjectPrompts: true,
			StateDir:         dir,
			ResolveProfile:   rejectionTestResolver,
			testOnly:         testConfig{skipGitSnapshot: true},
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		t.Cleanup(func() { sess.Close() })
		return sess
	}

	// readMeta reads the on-disk meta.json without triggering a save, so a
	// rejected SetModel that (correctly) never calls maybeAutoSave leaves the
	// file's updated_at, and everything else, untouched.
	readMeta := func(t *testing.T, sess *Session) []byte {
		t.Helper()
		path := filepath.Join(sess.stateDir, "sessions", sess.ID()+".meta.json")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read meta.json: %v", err)
		}
		return b
	}

	rejections := []struct {
		name  string
		setup func(sess *Session)
		model string
	}{
		{name: "unknown-instance", setup: func(sess *Session) {}, model: "bogus/some-model"},
		{name: "unknown-model-on-enumerable-instance", setup: func(sess *Session) {}, model: "google/not-a-real-model"},
		{name: "unrepresentable-document", setup: func(sess *Session) {
			sess.appendTurn(schema.TurnUserInput, documentTurn().Message)
		}, model: "anthropic/claude-opus-4-6"},
	}

	for _, rc := range rejections {
		t.Run(rc.name, func(t *testing.T) {
			t.Parallel()
			sess := newRejectionSession(t)
			rc.setup(sess)
			// Establish a baseline meta.json on disk before SetModel runs, so
			// the "after" read below proves a rejected SetModel never called
			// maybeAutoSave (rather than merely producing an identical
			// re-save).
			sess.maybeAutoSave()

			beforeProfile := sess.currentProfile()
			beforeMeta := readMeta(t, sess)
			beforeHistory := append([]schema.Turn(nil), sess.history...)

			if err := sess.SetModel(rc.model); err == nil {
				t.Fatalf("SetModel(%q) = nil error, want a rejection", rc.model)
			}

			afterProfile := sess.currentProfile()
			if afterProfile.ID() != beforeProfile.ID() || afterProfile.Model() != beforeProfile.Model() {
				t.Fatalf("profile changed: before=%s/%s after=%s/%s",
					beforeProfile.ID(), beforeProfile.Model(), afterProfile.ID(), afterProfile.Model())
			}

			afterMeta := readMeta(t, sess)
			if !bytes.Equal(afterMeta, beforeMeta) {
				t.Fatalf("meta.json changed on rejection:\nbefore=%s\nafter=%s", beforeMeta, afterMeta)
			}

			if len(sess.history) != len(beforeHistory) {
				t.Fatalf("history length changed: before=%d after=%d", len(beforeHistory), len(sess.history))
			}
			for i := range beforeHistory {
				if sess.history[i].Message.Text() != beforeHistory[i].Message.Text() ||
					len(sess.history[i].Message.Content) != len(beforeHistory[i].Message.Content) {
					t.Fatalf("history[%d] changed", i)
				}
			}
		})
	}
}

// TestSetModel_CrossTagSwitch_DropsInvalidatedFallbacksAndSurfacesNames
// verifies (g): after a successful cross-tag switch, model_fallbacks entries
// that no longer validate against the new profile are dropped, and the
// dropped names are surfaced (for the Task 5 marker's warning line) via
// DroppedModelFallbacksFromLastSwitch.
func TestSetModel_CrossTagSwitch_DropsInvalidatedFallbacksAndSurfacesNames(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withAdapter(&fakeAdapter{name: "anthropic"}),
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			ResolveProfile:   testResolver,
			// "openai/gpt-4.1-mini" self-prefix-validates fine against the
			// launch openai profile; after switching to anthropic below, the
			// same qualified entry names a different provider than the
			// session is now on, so it no longer validates (cross-tag).
			ModelFallbacks: []string{"openai/gpt-4.1-mini"},
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)

	if err := sess.SetModel("anthropic/claude-opus-4-6"); err != nil {
		t.Fatalf("SetModel cross-tag switch: %v", err)
	}

	dropped := sess.DroppedModelFallbacksFromLastSwitch()
	if len(dropped) != 1 || dropped[0] != "openai/gpt-4.1-mini" {
		t.Fatalf("DroppedModelFallbacksFromLastSwitch() = %v, want [openai/gpt-4.1-mini]", dropped)
	}
	if len(sess.cfg.ModelFallbacks) != 0 {
		t.Fatalf("cfg.ModelFallbacks after drop = %v, want empty", sess.cfg.ModelFallbacks)
	}
}

// TestSetModel_SameTagSwitch_KeepsValidFallbacks verifies (g): a same-tag
// switch keeps valid model_fallbacks entries and reports nothing dropped.
func TestSetModel_SameTagSwitch_KeepsValidFallbacks(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.4")),
		withAdapter(&fakeAdapter{name: "openai"}),
		withConfig(SessionConfig{
			NoProjectPrompts: true,
			ModelFallbacks:   []string{"openai/gpt-4.1"},
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)

	if err := sess.SetModel("gpt-4.1-mini"); err != nil {
		t.Fatalf("SetModel same-tag switch: %v", err)
	}

	if dropped := sess.DroppedModelFallbacksFromLastSwitch(); len(dropped) != 0 {
		t.Fatalf("DroppedModelFallbacksFromLastSwitch() = %v, want none dropped", dropped)
	}
	if len(sess.cfg.ModelFallbacks) != 1 || sess.cfg.ModelFallbacks[0] != "openai/gpt-4.1" {
		t.Fatalf("cfg.ModelFallbacks after same-tag switch = %v, want [openai/gpt-4.1]", sess.cfg.ModelFallbacks)
	}
}
