package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

// This literal is independent of runtime snapshot/restore construction.
const skillActivationSnapshotFixture = `{"revision":7,"inventory":{"scope:alpha":{"ordinary":{"identity":{"name":"scope:alpha","declared_name":"alpha","source":"/fixture/alpha/SKILL.md","file_digest":"file-1","rendered_digest":"render-1"},"description":"fixture-description","controls":{"disable_model_invocation":false,"user_invocable":false},"route":"user_slash","invocation_id":"inv-1","user_authorized":true},"preload":{"name":"scope:alpha","description":"frozen-description","source":"/fixture/frozen/SKILL.md","file_digest":"frozen-file","rendered_digest":"frozen-render"}}},"obligations":[{"invocation_id":"inv-1","tool_call_id":"tool-1","client_mutation_id":"mutation-1","atomic_group_id":"group-1","identity":{"name":"scope:alpha","declared_name":"alpha","source":"/fixture/alpha/SKILL.md","file_digest":"file-1","rendered_digest":"render-1"},"route":"user_slash"}],"pinned_note_gen":19,"next_operation_gen":23}`

func TestSkillActivation_RestoreSessionFromMeta(t *testing.T) {
	var meta schema.SessionMeta
	if err := json.Unmarshal([]byte(`{"id":"resume-skills","profile_id":"openai","model":"gpt-5.2","pinned_note":"opaque-note","skills":`+skillActivationSnapshotFixture+`}`), &meta); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := schema.SaveSessionMeta(dir, meta); err != nil {
		t.Fatal(err)
	}
	saved, err := schema.LoadSessionMeta(dir, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), saved, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	actual, err := json.Marshal(s.Meta())
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(actual, &wire); err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal([]byte(skillActivationSnapshotFixture), &want); err != nil {
		t.Fatal(err)
	}
	if raw, ok := wire["skills"]; ok {
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RestoreSessionFromMeta lifecycle mismatch: got=%s want=%s", wire["skills"], skillActivationSnapshotFixture)
	}
	if s.PinnedNote() != "opaque-note" || s.pinnedNoteGen != 19 {
		t.Fatalf("note=%q gen=%d", s.PinnedNote(), s.pinnedNoteGen)
	}
}

func TestSkillActivation_PolicyMatrix(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		for _, user := range []bool{false, true} {
			controls := skill.InvocationControls{DisableModelInvocation: disabled, UserInvocable: user}
			for _, authorized := range []bool{false, true} {
				for _, route := range []string{"user_slash", "user_selection", "model_tool", "compaction_reload", "role_preload"} {
					want := route == "role_preload"
					switch route {
					case "user_slash", "user_selection":
						want = user
					case "model_tool", "compaction_reload":
						want = authorized || !disabled
					}
					if got := skillInvocationAllowed(route, controls, authorized); got != want {
						t.Fatalf("route=%s disabled=%v user=%v auth=%v got=%v", route, disabled, user, authorized, got)
					}
				}
				if skillInvocationAllowed("client_claim", controls, authorized) {
					t.Fatal("unknown route authorized")
				}
			}
		}
	}
}

func activationSource(t *testing.T, dir, controls, body string) skill.Descriptor {
	t.Helper()
	path := filepath.Join(dir, "SKILL.md")
	data := []byte("---\nname: alpha\ndescription: fixture-description\n" + controls + "---\n" + body)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	d, _, err := skill.Parse(data, path)
	if err != nil {
		t.Fatal(err)
	}
	d.CatalogName = "scope:alpha"
	return d
}

func activationRecord(d skill.Descriptor, authorized bool) schema.OrdinarySkillActivation {
	return schema.OrdinarySkillActivation{
		Identity: schema.SkillContentIdentity{Name: d.CatalogName, DeclaredName: d.Meta.Name, Source: d.Meta.SkillFile, FileDigest: "previous-file", RenderedDigest: "previous-render"},
		Controls: schema.SkillInvocationControls{UserInvocable: true}, Route: "user_slash", InvocationID: "previous-invocation", UserAuthorized: authorized,
	}
}

func requireActivationError(t *testing.T, err error, code string) {
	t.Helper()
	var failure *skillActivationError
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("error=%v want typed code %s", err, code)
	}
}

func TestSkillActivation_PrepareCurrentControlsAndExactSource(t *testing.T) {
	s := newTestSession(t)
	dir := t.TempDir()
	original := activationSource(t, dir, "", "opaque-initial-body")
	prior := activationRecord(original, true)
	s.skills = skill.Catalog{Entries: map[string]skill.Descriptor{original.CatalogName: original}}
	s.skillLifecycle.Inventory[original.CatalogName] = schema.SkillInventoryEntry{Ordinary: &prior}
	// Discovery and the previous activation allow user invocation, but the current
	// source denies it. Authorization never overrides CURRENT user-invocable.
	activationSource(t, dir, "disable-model-invocation: true\nuser-invocable: false\n", "opaque-current-body")
	before := s.Meta().Skills
	batch, err := s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: "alpha", Route: "user_slash"}})
	requireActivationError(t, err, "policy_denied")
	if batch != nil || !reflect.DeepEqual(s.Meta().Skills, before) {
		t.Fatal("failed reinvocation changed prior state")
	}
	for _, route := range []string{"model_tool", "compaction_reload"} {
		batch, err = s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: "alpha", Route: route, InvocationID: "inv-new"}})
		if err != nil || len(batch.Items) != 1 {
			t.Fatalf("authorized %s: batch=%+v err=%v", route, batch, err)
		}
		item := batch.Items[0]
		if item.Loaded.Body != "opaque-current-body" || item.Loaded.Descriptor.CatalogName != "scope:alpha" || item.Invocation.InvocationID != "inv-new" {
			t.Fatalf("prepared=%+v", item)
		}
		data, err := os.ReadFile(original.Meta.SkillFile)
		if err != nil {
			t.Fatal(err)
		}
		fileHash := sha256.Sum256(data)
		renderedHash := sha256.Sum256([]byte(item.Rendered.Content))
		if item.Loaded.Digest != hex.EncodeToString(fileHash[:]) || item.Rendered.Digest != hex.EncodeToString(renderedHash[:]) {
			t.Fatal("identity digests do not match actual source/rendered bytes")
		}
	}
	if !reflect.DeepEqual(s.Meta().Skills, before) {
		t.Fatal("preparation prematurely published activation")
	}
	replacement := activationSource(t, t.TempDir(), "disable-model-invocation: true\n", "opaque-replacement-body")
	s.skills.Entries[original.CatalogName] = replacement
	_, err = s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: "alpha", Route: "model_tool"}})
	requireActivationError(t, err, "policy_denied")
	// Continuation is reconstructed from source, not the current collision winner.
	batch, err = s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: "alpha", Source: &prior.Identity, Route: "compaction_reload"}})
	if err != nil || batch.Items[0].Loaded.Body != "opaque-current-body" {
		t.Fatalf("continuation retargeted: batch=%+v err=%v", batch, err)
	}
	if err := os.Remove(original.Meta.SkillFile); err != nil {
		t.Fatal(err)
	}
	_, err = s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: "alpha", Source: &prior.Identity, Route: "compaction_reload"}})
	requireActivationError(t, err, "source_missing")
	if !reflect.DeepEqual(s.Meta().Skills, before) {
		t.Fatal("replacement or missing source erased authorization record")
	}
}

func TestSkillActivation_PrepareFailureIsAtomic(t *testing.T) {
	for _, tc := range []struct{ name, contents, code string }{
		{"invalid_metadata", "---\nname: alpha\ndescription: fixture\nuser-invocable: invalid\n---\nbody", "invalid_metadata"},
		{"source_changed", "---\nname: renamed\ndescription: fixture\n---\nbody", "source_changed"},
		{"source_missing", "", "source_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(t)
			first := activationSource(t, t.TempDir(), "", "opaque-first")
			second := activationSource(t, t.TempDir(), "", "opaque-second")
			second.CatalogName = "other:alpha"
			prior := activationRecord(second, true)
			s.skills = skill.Catalog{Entries: map[string]skill.Descriptor{first.CatalogName: first, second.CatalogName: second}}
			s.skillLifecycle.Inventory[second.CatalogName] = schema.SkillInventoryEntry{Ordinary: &prior}
			before := s.Meta().Skills
			if tc.contents == "" {
				if err := os.Remove(second.Meta.SkillFile); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(second.Meta.SkillFile, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			batch, err := s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: first.CatalogName, Route: "user_selection"}, {Name: second.CatalogName, Route: "user_selection"}})
			requireActivationError(t, err, tc.code)
			if batch != nil || !reflect.DeepEqual(s.Meta().Skills, before) {
				t.Fatal("partial batch mutated inventory")
			}
		})
	}
}

func TestSkillActivation_PrepareFrozenAndLegacyDoNotAuthorize(t *testing.T) {
	s := newTestSession(t)
	d := activationSource(t, t.TempDir(), "disable-model-invocation: true\nuser-invocable: false\n", "opaque-frozen")
	s.skills = skill.Catalog{Entries: map[string]skill.Descriptor{d.CatalogName: d}}
	s.skillLifecycle.Inventory[d.CatalogName] = schema.SkillInventoryEntry{Preload: &schema.FrozenSkillPreload{Name: d.CatalogName, Source: d.Meta.SkillFile}}
	_, err := s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: "alpha", Route: "model_tool"}})
	requireActivationError(t, err, "policy_denied")
	batch, err := s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: "alpha", Route: "role_preload"}})
	if err != nil || len(batch.Items) != 1 {
		t.Fatalf("role preparation: %v", err)
	}
	if s.skillLifecycle.Inventory[d.CatalogName].Ordinary != nil {
		t.Fatal("role preparation granted ordinary authorization")
	}
	legacy := schema.SkillContentIdentity{Name: d.CatalogName}
	_, err = s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: "alpha", Source: &legacy, Route: "compaction_reload"}})
	requireActivationError(t, err, "invalid_metadata")
}

func TestSkillActivation_ContinuationWithoutCatalog(t *testing.T) {
	s := newTestSession(t)
	d := activationSource(t, t.TempDir(), "disable-model-invocation: true\n", "opaque-materialized-builtin")
	prior := activationRecord(d, true)
	s.skills = skill.Catalog{}
	s.skillLifecycle.Inventory[d.CatalogName] = schema.SkillInventoryEntry{Ordinary: &prior}
	s.stateDir = t.TempDir()
	if err := s.saveMeta(); err != nil {
		t.Fatal(err)
	}
	meta, err := schema.LoadSessionMeta(s.stateDir, s.id)
	if err != nil {
		t.Fatal(err)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, s.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	// The fixture source is outside every restored discovery root, like a
	// formerly materialized builtin that is absent from the new catalog.
	if _, err := restored.skills.ResolveExact(d.CatalogName); err == nil {
		t.Fatal("fixture unexpectedly rediscovered")
	}
	s = restored
	batch, err := s.prepareSkillActivations(context.Background(), []skillInvocation{{Source: &prior.Identity, Route: "compaction_reload"}})
	if err != nil || batch.Items[0].Loaded.Descriptor.Meta.SkillFile != d.Meta.SkillFile {
		t.Fatalf("exact continuation: %v", err)
	}
	if err := os.Remove(d.Meta.SkillFile); err != nil {
		t.Fatal(err)
	}
	_, err = s.prepareSkillActivations(context.Background(), []skillInvocation{{Source: &prior.Identity, Route: "compaction_reload"}})
	requireActivationError(t, err, "source_missing")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source error lost cause: %v", err)
	}
}

func TestSkillActivation_PrepareUnknownAndCancelled(t *testing.T) {
	s := newTestSession(t)
	s.skills = skill.Catalog{}
	_, err := s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: "missing", Route: "model_tool"}})
	requireActivationError(t, err, "source_missing")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.prepareSkillActivations(ctx, []skillInvocation{{Name: "missing", Route: "model_tool"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestSkillActivation_SaveMetaAndSnapshotIsolation(t *testing.T) {
	s := newTestSession(t)
	if err := json.Unmarshal([]byte(skillActivationSnapshotFixture), &s.skillLifecycle); err != nil {
		t.Fatal(err)
	}
	s.pinnedNote = "opaque-note"
	s.pinnedNoteGen = 31 // Existing note generation, not stale lifecycle gen 19, wins.
	snapshot := s.Meta()
	snapshot.Skills.Inventory["scope:alpha"].Ordinary.Identity.Source = "mutated-ordinary"
	snapshot.Skills.Inventory["scope:alpha"].Preload.Source = "mutated-preload"
	snapshot.Skills.Obligations[0].Identity.Source = "mutated-obligation"
	delete(snapshot.Skills.Inventory, "scope:alpha")
	if s.skillLifecycle.Inventory["scope:alpha"].Ordinary.Identity.Source != "/fixture/alpha/SKILL.md" || s.skillLifecycle.Inventory["scope:alpha"].Preload.Source != "/fixture/frozen/SKILL.md" || s.skillLifecycle.Obligations[0].Identity.Source != "/fixture/alpha/SKILL.md" {
		t.Fatal("Meta aliased runtime state")
	}
	s.stateDir = t.TempDir()
	if err := s.saveMeta(); err != nil {
		t.Fatal(err)
	}
	saved, err := schema.LoadSessionMeta(s.stateDir, s.id)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Skills.PinnedNoteGen != 31 || saved.PinnedNote != "opaque-note" {
		t.Fatalf("note/gen not authoritative: %+v", saved)
	}
	// Restore copies the caller-owned snapshot, including pointed-to records.
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), saved, s.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	saved.Skills.Inventory["scope:alpha"].Ordinary.Controls.UserInvocable = true
	saved.Skills.Inventory["scope:alpha"].Preload.Source = "mutated-preload"
	saved.Skills.Obligations[0].InvocationID = "mutated-invocation"
	if restored.skillLifecycle.Inventory["scope:alpha"].Ordinary.Controls.UserInvocable || restored.skillLifecycle.Inventory["scope:alpha"].Preload.Source != "/fixture/frozen/SKILL.md" || restored.skillLifecycle.Obligations[0].InvocationID != "inv-1" {
		t.Fatal("restore aliased caller metadata")
	}
	if restored.pinnedNoteGen != 31 {
		t.Fatalf("restored note generation=%d", restored.pinnedNoteGen)
	}
}

func TestSkillActivation_SaveMetaReturnsFilesystemFailure(t *testing.T) {
	s := newTestSession(t)
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("opaque-blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	s.stateDir = blocker
	err := s.saveMeta()
	if _, ok := errors.AsType[*os.PathError](err); !ok {
		t.Fatalf("save must return real filesystem error: %v", err)
	}
	// Avoid a warning from Close retrying the intentional failure fixture.
	s.stateDir = ""
	if err := s.saveMeta(); err != nil {
		t.Fatalf("unpersisted session save=%v", err)
	}
}

func TestSkillActivation_LegacyRestoreHasEmptyOrdinaryState(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{ID: "legacy-skill-session", ProfileID: "openai", Model: "gpt-5.2", PinnedNote: "opaque-legacy-note"}
	dir := t.TempDir()
	if err := schema.SaveSessionMeta(dir, meta); err != nil {
		t.Fatal(err)
	}
	saved, err := schema.LoadSessionMeta(dir, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), saved, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.skillLifecycle.Inventory == nil || len(s.skillLifecycle.Inventory) != 0 || len(s.skillLifecycle.Obligations) != 0 || s.pinnedNoteGen != 0 {
		t.Fatalf("legacy state inferred: %+v gen=%d", s.skillLifecycle, s.pinnedNoteGen)
	}
	if s.PinnedNote() != "opaque-legacy-note" {
		t.Fatal("legacy note lost")
	}
}

func TestSkillActivation_RecordTurnCopiesTypedState(t *testing.T) {
	dir := t.TempDir()
	s := newSession(t, withDir(dir), withConfig(SessionConfig{StateDir: dir, MaxSubagentDepth: 1}))
	path := s.TranscriptPath()
	state := &schema.SkillTurnState{
		Input:       &schema.SkillInputRecord{OriginalText: "opaque-original", Arguments: "opaque-args", Names: []string{"scope:alpha"}, AtomicGroupID: "group-1"},
		Outcomes:    []schema.SkillActivationOutcome{{Revision: 7, SessionID: s.id, InvocationID: "inv-1", Status: "already_present", Identity: schema.SkillContentIdentity{Name: "scope:alpha"}, Activation: &schema.OrdinarySkillActivation{InvocationID: "inv-1"}, PreviousIdentity: &schema.SkillContentIdentity{Source: "previous-source"}, PreviousControls: &schema.SkillInvocationControls{UserInvocable: true}}},
		Obligations: []schema.SkillDeliveryObligation{{InvocationID: "inv-1", AtomicGroupID: "group-1"}},
	}
	live := schema.Turn{Kind: schema.TurnUserInput, SkillState: state}
	s.recordTurn(live, live)
	state.Input.Names[0] = "mutated"
	state.Outcomes[0].Status = "delivered"
	state.Outcomes[0].Activation.InvocationID = "mutated"
	state.Outcomes[0].PreviousIdentity.Source = "mutated"
	state.Outcomes[0].PreviousControls.UserInvocable = false
	state.Obligations[0].InvocationID = "mutated"
	s.mu.Lock()
	got := s.history[len(s.history)-1].SkillState
	s.mu.Unlock()
	if got.Input.Names[0] != "scope:alpha" || got.Outcomes[0].Status != "already_present" || got.Outcomes[0].Activation.InvocationID != "inv-1" || got.Outcomes[0].PreviousIdentity.Source != "previous-source" || !got.Outcomes[0].PreviousControls.UserInvocable || got.Obligations[0].InvocationID != "inv-1" {
		t.Fatalf("typed turn state aliased: %+v", got)
	}
	if len(s.skillLifecycle.Inventory) != 0 {
		t.Fatal("recordTurn inferred inventory from provisional outcome")
	}
	s.Close()
	data, err := readTranscriptFull(path)
	if err != nil {
		t.Fatal(err)
	}
	var recorded *schema.SkillTurnState
	for _, entry := range data.Entries {
		if entry.Turn.SkillState != nil {
			recorded = entry.Turn.SkillState
		}
	}
	if !reflect.DeepEqual(recorded, got) {
		t.Fatalf("durable typed state=%+v live=%+v", recorded, got)
	}
}

func TestSkillActivation_PrepareReloadsRelaxedControls(t *testing.T) {
	s := newTestSession(t)
	dir := t.TempDir()
	d := activationSource(t, dir, "disable-model-invocation: true\nuser-invocable: false\n", "opaque-hidden")
	s.skills = skill.Catalog{Entries: map[string]skill.Descriptor{d.CatalogName: d}}
	activationSource(t, dir, "", "opaque-relaxed")
	for _, route := range []string{"user_selection", "model_tool"} {
		batch, err := s.prepareSkillActivations(context.Background(), []skillInvocation{{Name: "alpha", Route: route}})
		if err != nil || batch.Items[0].Loaded.Body != "opaque-relaxed" {
			t.Fatalf("current relaxed controls not used for %s: %v", route, err)
		}
	}
	if len(s.skillLifecycle.Inventory) != 0 {
		t.Fatal("preparation granted authorization")
	}
}

// --- Task 5: provider-boundary route tests -------------------------------
//
// These tests drive the real session against a scripted provider adapter (the
// only fake boundary) and assert on captured provider requests, typed turn
// state, lifecycle inventory, and typed events. Expected instruction bytes and
// digests come from the fixtures these tests write, never from the production
// renderer.

// skillEnvelope is one complete typed skill-context carrier found in a
// provider-boundary document, with the exact carrier bytes preserved so digest
// assertions stay independent of the production renderer.
type skillEnvelope struct {
	Raw string
	Doc skill.SkillDocument
}

// extractSkillEnvelopes finds every complete skill-context envelope embedded
// in content, whether the content is exactly one envelope (a tool result or
// context message) or a larger document (the system prompt's activated-skills
// section).
func extractSkillEnvelopes(t *testing.T, content string) []skillEnvelope {
	t.Helper()
	const open, closeTag = "<skill-context>\n", "\n</skill-context>"
	var out []skillEnvelope
	rest := content
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return out
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, closeTag)
		if j < 0 {
			t.Fatalf("unterminated skill-context envelope in %.200q", content)
		}
		raw := open + rest[:j] + closeTag
		var doc skill.SkillDocument
		if err := json.Unmarshal([]byte(rest[:j]), &doc); err != nil {
			t.Fatalf("decoding skill-context envelope: %v", err)
		}
		out = append(out, skillEnvelope{Raw: raw, Doc: doc})
		rest = rest[j+len(closeTag):]
	}
}

// requestSkillEnvelopes collects the envelopes carried by a provider request's
// text, system, and tool-result parts.
func requestSkillEnvelopes(t *testing.T, req llm.Request) []skillEnvelope {
	t.Helper()
	var out []skillEnvelope
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			switch part.Kind {
			case llm.ContentText:
				out = append(out, extractSkillEnvelopes(t, part.Text)...)
			case llm.ContentToolResult:
				if part.ToolResult == nil {
					continue
				}
				if content, ok := part.ToolResult.Content.(string); ok {
					out = append(out, extractSkillEnvelopes(t, content)...)
				}
			}
		}
	}
	return out
}

// requireSingleEnvelope asserts the request carries exactly one complete
// envelope and that it delivers the fixture's complete original body with
// canonical identity.
func requireSingleEnvelope(t *testing.T, req llm.Request, name, body, source string) skillEnvelope {
	t.Helper()
	envs := requestSkillEnvelopes(t, req)
	if len(envs) != 1 {
		t.Fatalf("request carries %d skill envelopes, want exactly 1", len(envs))
	}
	env := envs[0]
	if env.Doc.Name != name {
		t.Fatalf("envelope name = %q, want canonical %q", env.Doc.Name, name)
	}
	if env.Doc.Instructions != body {
		t.Fatalf("envelope instructions are not the complete original body (%d bytes, want %d)", len(env.Doc.Instructions), len(body))
	}
	if env.Doc.Source != source {
		t.Fatalf("envelope source = %q, want %q", env.Doc.Source, source)
	}
	if env.Doc.BaseDirectory != filepath.Dir(source) {
		t.Fatalf("envelope base directory = %q, want %q", env.Doc.BaseDirectory, filepath.Dir(source))
	}
	return env
}

// captureEvents records the session's events until stop closes the session
// and drains the channel.
func captureEvents(s *Session) (seen *[]events.SessionEvent, stop func()) {
	evs := &[]events.SessionEvent{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range s.Events() {
			*evs = append(*evs, ev)
		}
	}()
	return evs, func() { s.Close(); <-done }
}

func skillActivatedEventNames(evs []events.SessionEvent) []string {
	var names []string
	for _, ev := range evs {
		if ev.Kind != events.EventSkillActivated {
			continue
		}
		if d, ok := ev.Data.(events.SkillActivatedData); ok {
			names = append(names, d.Name)
		}
	}
	return names
}

func hasEventKind(evs []events.SessionEvent, kind events.EventKind) bool {
	for _, ev := range evs {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

// skillTurnStates snapshots the typed skill state of every recorded turn.
func skillTurnStates(s *Session) []schema.SkillTurnState {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []schema.SkillTurnState
	for _, turn := range s.history {
		if turn.SkillState != nil {
			out = append(out, *turn.SkillState.Clone())
		}
	}
	return out
}

// lifecycleInventory snapshots the session's skill lifecycle inventory.
func lifecycleInventory(s *Session) map[string]schema.SkillInventoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skillLifecycle.Clone().Inventory
}

// lifecycleObligations snapshots the session's outstanding delivery obligations.
func lifecycleObligations(s *Session) []schema.SkillDeliveryObligation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skillLifecycle.Clone().Obligations
}

// requireOrdinaryActivation asserts the inventory's ordinary record for name
// and verifies its recorded digests against the fixture file bytes and the
// envelope bytes actually delivered to the provider.
func requireOrdinaryActivation(t *testing.T, s *Session, name, source, route string, userAuthorized bool, delivered skillEnvelope) schema.OrdinarySkillActivation {
	t.Helper()
	entry, ok := lifecycleInventory(s)[name]
	if !ok || entry.Ordinary == nil {
		t.Fatalf("inventory missing ordinary activation for %q: %+v", name, lifecycleInventory(s))
	}
	ordinary := *entry.Ordinary
	if ordinary.Route != route || ordinary.UserAuthorized != userAuthorized {
		t.Fatalf("ordinary activation route=%q authorized=%v, want %q/%v", ordinary.Route, ordinary.UserAuthorized, route, userAuthorized)
	}
	if ordinary.InvocationID == "" {
		t.Fatal("ordinary activation has no invocation identity")
	}
	if ordinary.Identity.Name != name || ordinary.Identity.Source != source || ordinary.Identity.DeclaredName == "" {
		t.Fatalf("ordinary identity = %+v, want name %q source %q", ordinary.Identity, name, source)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	fileHash := sha256.Sum256(data)
	if ordinary.Identity.FileDigest != hex.EncodeToString(fileHash[:]) {
		t.Fatalf("recorded file digest %q does not match fixture bytes", ordinary.Identity.FileDigest)
	}
	renderedHash := sha256.Sum256([]byte(delivered.Raw))
	if ordinary.Identity.RenderedDigest != hex.EncodeToString(renderedHash[:]) {
		t.Fatalf("recorded rendered digest %q does not match the delivered envelope bytes", ordinary.Identity.RenderedDigest)
	}
	return ordinary
}

func userMessageTexts(req llm.Request) []string {
	var out []string
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleUser {
			out = append(out, msg.Text())
		}
	}
	return out
}

func TestSkillActivation_Routes(t *testing.T) {
	t.Parallel()
	const fixtureHeader = "---\nname: opaque\ndescription: fixture\n---\n"

	t.Run("tool", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		body := strings.Repeat("BODY_7f2a\n", 64)
		writeSkillMD(t, root, "opaque", fixtureHeader+body)
		source := filepath.Join(root, "skills", "opaque", "SKILL.md")
		calls := 0
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			calls++
			if calls == 1 {
				return toolCallResponse(useSkillCall("skill-1", "opaque"))
			}
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
			t.Fatal(err)
		}
		stop()
		if len(adapter.Requests()) != 2 {
			t.Fatalf("requests=%d", len(adapter.Requests()))
		}
		delivered := requireSingleEnvelope(t, adapter.Requests()[1], "opaque", body, source)
		if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "opaque" {
			t.Fatalf("activation events = %v, want [opaque]", names)
		}
		ordinary := requireOrdinaryActivation(t, s, "opaque", source, "model_tool", false, delivered)

		// Typed saved state links the delivery to the model's tool call.
		states := skillTurnStates(s)
		foundLink := false
		for _, state := range states {
			for _, outcome := range state.Outcomes {
				if outcome.ToolCallID == "skill-1" && outcome.Identity.Name == "opaque" && outcome.InvocationID == ordinary.InvocationID {
					foundLink = true
				}
			}
			for _, obligation := range state.Obligations {
				if obligation.ToolCallID == "skill-1" && obligation.Identity.Name == "opaque" {
					foundLink = true
				}
			}
		}
		if !foundLink {
			t.Fatalf("no typed outcome/obligation links tool call skill-1: %+v", states)
		}
	})

	t.Run("slash", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		body := strings.Repeat("BODY_7f2a\n", 64)
		writeSkillMD(t, root, "opaque", fixtureHeader+body)
		source := filepath.Join(root, "skills", "opaque", "SKILL.md")
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "/opaque REQUEST_93d2", nil); err != nil {
			t.Fatal(err)
		}
		stop()
		if len(adapter.Requests()) != 1 {
			t.Fatalf("requests=%d", len(adapter.Requests()))
		}
		req := adapter.Requests()[0]
		delivered := requireSingleEnvelope(t, req, "opaque", body, source)

		// The original request stays separately identifiable as user input.
		var originals []string
		for _, text := range userMessageTexts(req) {
			if text == "/opaque REQUEST_93d2" {
				originals = append(originals, text)
			}
		}
		if len(originals) != 1 {
			t.Fatalf("request user messages = %q, want exactly one original request", userMessageTexts(req))
		}
		if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "opaque" {
			t.Fatalf("activation events = %v, want [opaque]", names)
		}
		requireOrdinaryActivation(t, s, "opaque", source, "user_slash", true, delivered)

		// The typed saved input records the original request and arguments.
		states := skillTurnStates(s)
		if len(states) == 0 || states[0].Input == nil {
			t.Fatalf("no typed skill input recorded: %+v", states)
		}
		input := states[0].Input
		if input.OriginalText != "/opaque REQUEST_93d2" || input.Arguments != "REQUEST_93d2" {
			t.Fatalf("typed input = %+v", input)
		}
		if len(input.Names) != 1 || input.Names[0] != "opaque" || input.AtomicGroupID == "" {
			t.Fatalf("typed input names/group = %+v", input)
		}
	})

	t.Run("preload", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		body := strings.Repeat("BODY_7f2a\n", 64)
		writeSkillMD(t, root, "opaque", fixtureHeader+body)
		source := filepath.Join(root, "skills", "opaque", "SKILL.md")
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
		s.pluginAgents["preload-role"] = plugin.Agent{
			Name: "preload-role", PluginName: "test", AllTools: true,
			SystemPrompt: "preload role", Skills: []string{"opaque"},
		}
		result, err := s.spawnAgent(context.Background(), "child task", "", "", 0, "preload-role", "", nil, nil)
		if err != nil {
			t.Fatalf("spawnAgent: %v", err)
		}
		var spawned struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal([]byte(result.(string)), &spawned); err != nil {
			t.Fatalf("unmarshal spawn result: %v", err)
		}
		sub := s.getSub(spawned.AgentID)
		if sub == nil {
			t.Fatalf("subagent %q not tracked", spawned.AgentID)
		}
		select {
		case <-sub.done:
		case <-time.After(30 * time.Second): // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
			t.Fatal("timed out waiting for preload child")
		}

		if len(adapter.Requests()) != 1 {
			t.Fatalf("requests=%d", len(adapter.Requests()))
		}
		req := adapter.Requests()[0]
		if len(req.Messages) == 0 || req.Messages[0].Role != llm.RoleSystem {
			t.Fatalf("child request missing system prompt")
		}
		delivered := requireSingleEnvelope(t, req, "opaque", body, source)

		// A new delegate's inventory is seeded by exactly its own role preload,
		// with independent provenance and no ordinary authorization.
		child := sub.sess
		childEntry, ok := lifecycleInventory(child)["opaque"]
		if !ok || childEntry.Preload == nil {
			t.Fatalf("child inventory missing preload metadata: %+v", lifecycleInventory(child))
		}
		preload := *childEntry.Preload
		if preload.Name != "opaque" || preload.Source != source || preload.Description == "" {
			t.Fatalf("preload metadata = %+v", preload)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		fileHash := sha256.Sum256(data)
		renderedHash := sha256.Sum256([]byte(delivered.Raw))
		if preload.FileDigest != hex.EncodeToString(fileHash[:]) || preload.RenderedDigest != hex.EncodeToString(renderedHash[:]) {
			t.Fatalf("preload digests do not match fixture/delivered bytes: %+v", preload)
		}
		if childEntry.Ordinary != nil {
			t.Fatalf("role preload granted ordinary authorization: %+v", childEntry.Ordinary)
		}
		if got := lifecycleInventory(child); len(got) != 1 {
			t.Fatalf("child inventory = %+v, want exactly its own role preload", got)
		}
		if got := lifecycleInventory(s); len(got) != 0 {
			t.Fatalf("parent inventory imported child preload: %+v", got)
		}
	})
}

func TestSkillActivation_Failure(t *testing.T) {
	t.Parallel()
	const fixtureHeader = "---\nname: opaque\ndescription: fixture\n---\n"

	t.Run("deleted_source_slash_dispatches_nothing", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		writeSkillMD(t, root, "opaque", fixtureHeader+"BODY_7f2a\n")
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
		if err := os.Remove(filepath.Join(root, "skills", "opaque", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
		evs, stop := captureEvents(s)
		_, err := s.ProcessInput(context.Background(), "/opaque REQUEST_93d2", nil)
		stop()
		if len(adapter.Requests()) != 0 {
			t.Fatalf("known failed activation dispatched %d dependent requests", len(adapter.Requests()))
		}
		if names := skillActivatedEventNames(*evs); len(names) != 0 {
			t.Fatalf("failed activation emitted success events: %v", names)
		}
		if err == nil && !hasEventKind(*evs, events.EventError) {
			t.Fatal("failed activation produced no visible failure")
		}
		// The original input remains recorded for correction and explicit retry.
		s.mu.Lock()
		var userTurns []schema.Turn
		for _, turn := range s.history {
			if turn.Kind == schema.TurnUserInput {
				userTurns = append(userTurns, turn)
			}
		}
		s.mu.Unlock()
		if len(userTurns) != 1 || userTurns[0].Message.Text() != "/opaque REQUEST_93d2" {
			t.Fatalf("failed activation lost the original input: %+v", userTurns)
		}
	})

	t.Run("deleted_source_tool_has_no_success_event", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		writeSkillMD(t, root, "opaque", fixtureHeader+"BODY_7f2a\n")
		calls := 0
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			calls++
			if calls == 1 {
				return toolCallResponse(useSkillCall("skill-1", "opaque"))
			}
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
		if err := os.Remove(filepath.Join(root, "skills", "opaque", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
			t.Fatal(err)
		}
		stop()
		if names := skillActivatedEventNames(*evs); len(names) != 0 {
			t.Fatalf("failed activation emitted success events: %v", names)
		}
		if len(adapter.Requests()) != 2 {
			t.Fatalf("requests=%d", len(adapter.Requests()))
		}
		// The failed tool call produced an error result for the model and a
		// typed failed outcome, never an inventory entry.
		req := adapter.Requests()[1]
		errorSeen := false
		for _, msg := range req.Messages {
			for _, part := range msg.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == "skill-1" && part.ToolResult.IsError {
					errorSeen = true
				}
			}
		}
		if !errorSeen {
			t.Fatal("deleted-source use_skill did not return an error result")
		}
		if got := lifecycleInventory(s); len(got) != 0 {
			t.Fatalf("failed activation recorded inventory: %+v", got)
		}
		states := skillTurnStates(s)
		failedOutcome := false
		for _, state := range states {
			for _, outcome := range state.Outcomes {
				if outcome.ToolCallID == "skill-1" && outcome.Status == "failed" && outcome.ErrorCode == "source_missing" {
					failedOutcome = true
				}
			}
		}
		if !failedOutcome {
			t.Fatalf("no typed source_missing failure outcome: %+v", states)
		}
	})

	t.Run("user_denied_slash_dispatches_nothing", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\nuser-invocable: false\n---\nBODY_7f2a\n")
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
		evs, stop := captureEvents(s)
		_, err := s.ProcessInput(context.Background(), "/opaque REQUEST_93d2", nil)
		stop()
		if len(adapter.Requests()) != 0 {
			t.Fatalf("policy-denied activation dispatched %d requests", len(adapter.Requests()))
		}
		if names := skillActivatedEventNames(*evs); len(names) != 0 {
			t.Fatalf("denied activation emitted success events: %v", names)
		}
		if err == nil && !hasEventKind(*evs, events.EventError) {
			t.Fatal("denied activation produced no visible failure")
		}
		if got := lifecycleInventory(s); len(got) != 0 {
			t.Fatalf("denied activation recorded inventory: %+v", got)
		}
	})

	t.Run("unknown_slash_keeps_ordinary_input_behavior", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "/nosuch REQUEST_93d2", nil); err != nil {
			t.Fatal(err)
		}
		stop()
		if len(adapter.Requests()) != 1 {
			t.Fatalf("requests=%d", len(adapter.Requests()))
		}
		texts := userMessageTexts(adapter.Requests()[0])
		if len(texts) == 0 || texts[len(texts)-1] != "/nosuch REQUEST_93d2" {
			t.Fatalf("unknown slash did not flow as ordinary input: %q", texts)
		}
		if names := skillActivatedEventNames(*evs); len(names) != 0 {
			t.Fatalf("unknown slash emitted activation events: %v", names)
		}
		if states := skillTurnStates(s); len(states) != 0 {
			t.Fatalf("unknown slash recorded typed skill state: %+v", states)
		}
	})

	t.Run("model_generated_slash_without_authorization", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		// User-only skill: the model route requires disable-model-invocation:false
		// or prior user authorization; this source has neither.
		writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\ndisable-model-invocation: true\n---\nBODY_7f2a\n")
		calls := 0
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			calls++
			if calls == 1 {
				return toolCallResponse(useSkillCall("skill-1", "opaque"))
			}
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
		evs, stop := captureEvents(s)
		// A plain inline mention is ordinary prose, never a user_slash route.
		if _, err := s.ProcessInput(context.Background(), "please apply /opaque here", nil); err != nil {
			t.Fatal(err)
		}
		stop()
		if len(adapter.Requests()) != 2 {
			t.Fatalf("requests=%d", len(adapter.Requests()))
		}
		texts := userMessageTexts(adapter.Requests()[0])
		if len(texts) == 0 || texts[len(texts)-1] != "please apply /opaque here" {
			t.Fatalf("inline mention did not flow as ordinary prose: %q", texts)
		}
		if names := skillActivatedEventNames(*evs); len(names) != 0 {
			t.Fatalf("unauthorized model invocation emitted success events: %v", names)
		}
		if got := lifecycleInventory(s); len(got) != 0 {
			t.Fatalf("unauthorized model invocation recorded inventory: %+v", got)
		}
		states := skillTurnStates(s)
		deniedOutcome := false
		for _, state := range states {
			if state.Input != nil {
				t.Fatalf("plain prose recorded a typed skill input: %+v", state.Input)
			}
			for _, outcome := range state.Outcomes {
				if outcome.ToolCallID == "skill-1" && outcome.Status == "failed" && outcome.ErrorCode == "policy_denied" {
					deniedOutcome = true
				}
			}
		}
		if !deniedOutcome {
			t.Fatalf("no typed policy_denied outcome: %+v", states)
		}
	})
}

func TestSkillActivation_Resolution(t *testing.T) {
	t.Parallel()

	writePluginSkill := func(t *testing.T, pluginDir, pluginName, skillName, body string) {
		t.Helper()
		dir := filepath.Join(pluginDir, "skills", skillName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+skillName+"\ndescription: fixture\n---\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"), []byte(`{"name":"`+pluginName+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("unique_suffix_resolves_through_full_catalog", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		pluginDir := t.TempDir()
		writePluginSkill(t, pluginDir, "plug", "probe", "BODY_7f2a\n")
		source := filepath.Join(pluginDir, "skills", "probe", "SKILL.md")
		calls := 0
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			calls++
			if calls == 1 {
				return toolCallResponse(useSkillCall("skill-1", "probe"))
			}
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot(),
			withConfig(SessionConfig{MaxSubagentDepth: 1, PluginDirs: []string{pluginDir}}))
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
			t.Fatal(err)
		}
		stop()
		if len(adapter.Requests()) != 2 {
			t.Fatalf("requests=%d", len(adapter.Requests()))
		}
		delivered := requireSingleEnvelope(t, adapter.Requests()[1], "plug:probe", "BODY_7f2a\n", source)
		if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "plug:probe" {
			t.Fatalf("activation events = %v, want [plug:probe]", names)
		}
		requireOrdinaryActivation(t, s, "plug:probe", source, "model_tool", false, delivered)
	})

	t.Run("ambiguous_suffix_reports_no_choice", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		first, second := t.TempDir(), t.TempDir()
		writePluginSkill(t, first, "alpha", "probe", "BODY_alpha\n")
		writePluginSkill(t, second, "beta", "probe", "BODY_beta\n")
		calls := 0
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			calls++
			if calls == 1 {
				return toolCallResponse(useSkillCall("skill-1", "probe"))
			}
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot(),
			withConfig(SessionConfig{MaxSubagentDepth: 1, PluginDirs: []string{first, second}}))
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
			t.Fatal(err)
		}
		stop()
		if names := skillActivatedEventNames(*evs); len(names) != 0 {
			t.Fatalf("ambiguous suffix chose a winner: %v", names)
		}
		if got := lifecycleInventory(s); len(got) != 0 {
			t.Fatalf("ambiguous suffix recorded inventory: %+v", got)
		}
		req := adapter.Requests()[1]
		errorSeen := false
		for _, msg := range req.Messages {
			for _, part := range msg.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == "skill-1" && part.ToolResult.IsError {
					errorSeen = true
				}
			}
		}
		if !errorSeen {
			t.Fatal("ambiguous suffix did not return an error result")
		}
	})

	t.Run("exact_name_wins_over_plugin_suffix", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		body := strings.Repeat("BODY_7f2a\n", 64)
		writeSkillMD(t, root, "probe", "---\nname: probe\ndescription: fixture\n---\n"+body)
		source := filepath.Join(root, "skills", "probe", "SKILL.md")
		pluginDir := t.TempDir()
		writePluginSkill(t, pluginDir, "plug", "probe", "BODY_other\n")
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot(),
			withConfig(SessionConfig{MaxSubagentDepth: 1, PluginDirs: []string{pluginDir}}))
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "/probe REQUEST_93d2", nil); err != nil {
			t.Fatal(err)
		}
		stop()
		if len(adapter.Requests()) != 1 {
			t.Fatalf("requests=%d", len(adapter.Requests()))
		}
		delivered := requireSingleEnvelope(t, adapter.Requests()[0], "probe", body, source)
		if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "probe" {
			t.Fatalf("activation events = %v, want [probe]", names)
		}
		requireOrdinaryActivation(t, s, "probe", source, "user_slash", true, delivered)
	})

	t.Run("command_name_collision_keeps_command_precedence", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		writeSkillMD(t, root, "review", "---\nname: review\ndescription: fixture\n---\nBODY_7f2a\n")
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
		s.pluginCommands = map[string]plugin.Command{
			"review": {Name: "review", Body: "command $ARGUMENTS", Source: "project"},
		}
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "/review diff", nil); err != nil {
			t.Fatal(err)
		}
		stop()
		if len(adapter.Requests()) != 1 {
			t.Fatalf("requests=%d", len(adapter.Requests()))
		}
		texts := userMessageTexts(adapter.Requests()[0])
		if len(texts) == 0 || texts[len(texts)-1] != "command diff" {
			t.Fatalf("command collision did not expand the command: %q", texts)
		}
		if names := skillActivatedEventNames(*evs); len(names) != 0 {
			t.Fatalf("command precedence emitted skill events: %v", names)
		}
		if states := skillTurnStates(s); len(states) != 0 {
			t.Fatalf("command expansion recorded typed skill state: %+v", states)
		}
	})

	t.Run("hidden_name_resolvable_by_user_through_full_catalog", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		body := strings.Repeat("BODY_7f2a\n", 64)
		writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\ndisable-model-invocation: true\n---\n"+body)
		source := filepath.Join(root, "skills", "opaque", "SKILL.md")
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
		for _, d := range s.skills.ModelEntries() {
			if d.CatalogName == "opaque" {
				t.Fatal("hidden skill leaked into the advertised model catalog")
			}
		}
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "/opaque REQUEST_93d2", nil); err != nil {
			t.Fatal(err)
		}
		stop()
		if len(adapter.Requests()) != 1 {
			t.Fatalf("requests=%d", len(adapter.Requests()))
		}
		delivered := requireSingleEnvelope(t, adapter.Requests()[0], "opaque", body, source)
		if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "opaque" {
			t.Fatalf("activation events = %v, want [opaque]", names)
		}
		requireOrdinaryActivation(t, s, "opaque", source, "user_slash", true, delivered)
	})

	t.Run("new_arguments_with_duplicate_bodies", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		body := strings.Repeat("BODY_7f2a\n", 64)
		writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
		adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
			return toolCallResponse(communicateCall("done-1", "ok"))
		}}
		s := newSession(t, withAdapter(adapter), withDir(root),
			withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "/opaque FIRST_ARGS", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ProcessInput(context.Background(), "/opaque SECOND_ARGS", nil); err != nil {
			t.Fatal(err)
		}
		stop()
		if len(adapter.Requests()) != 2 {
			t.Fatalf("requests=%d", len(adapter.Requests()))
		}
		// Both requests keep their own original prose and typed arguments; a
		// duplicate body can never discard the new request's arguments.
		second := adapter.Requests()[1]
		texts := userMessageTexts(second)
		foundOriginal := false
		for _, text := range texts {
			if text == "/opaque SECOND_ARGS" {
				foundOriginal = true
			}
		}
		if !foundOriginal {
			t.Fatalf("second request lost the original input: %q", texts)
		}
		var inputs []*schema.SkillInputRecord
		for _, state := range skillTurnStates(s) {
			if state.Input != nil {
				inputs = append(inputs, state.Input)
			}
		}
		if len(inputs) != 2 || inputs[0].Arguments != "FIRST_ARGS" || inputs[1].Arguments != "SECOND_ARGS" {
			t.Fatalf("typed inputs = %+v", inputs)
		}
		if inputs[0].AtomicGroupID == "" || inputs[0].AtomicGroupID == inputs[1].AtomicGroupID {
			t.Fatalf("typed inputs share an atomic group: %+v", inputs)
		}
		// One success event per genuinely new body: the second activation has
		// identical content, so no second new-body event may fire.
		if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "opaque" {
			t.Fatalf("activation events = %v, want exactly one new-body event", names)
		}
	})
}

func TestSkillActivation_RawRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	readFull, _ := json.Marshal(map[string]any{"file_path": source})
	readPartial, _ := json.Marshal(map[string]any{"file_path": source, "limit": 2})
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		switch calls {
		case 1:
			return toolCallResponse(llm.ToolCallData{ID: "read-1", Name: "read_file", Arguments: readFull, Type: "function"})
		case 2:
			return toolCallResponse(llm.ToolCallData{ID: "read-2", Name: "read_file", Arguments: readPartial, Type: "function"})
		default:
			return toolCallResponse(communicateCall("done-1", "ok"))
		}
	}}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	if len(adapter.Requests()) != 3 {
		t.Fatalf("requests=%d", len(adapter.Requests()))
	}
	// Both reads returned real file bytes to the model.
	foundFull, foundPartial := false, false
	for _, msg := range adapter.Requests()[2].Messages {
		for _, part := range msg.Content {
			if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
				continue
			}
			content, _ := part.ToolResult.Content.(string)
			switch part.ToolResult.ToolCallID {
			case "read-1":
				foundFull = strings.Contains(content, "BODY_7f2a")
			case "read-2":
				foundPartial = strings.Contains(content, "name: opaque")
			}
		}
	}
	if !foundFull || !foundPartial {
		t.Fatalf("raw reads did not return file bytes: full=%v partial=%v", foundFull, foundPartial)
	}
	// Inspecting SKILL.md is not activation: no success event, no inventory, no
	// obligations, and no typed skill state anywhere in the turn history.
	if names := skillActivatedEventNames(*evs); len(names) != 0 {
		t.Fatalf("raw file reads emitted activation events: %v", names)
	}
	if got := lifecycleInventory(s); len(got) != 0 {
		t.Fatalf("raw file reads recorded inventory: %+v", got)
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("raw file reads recorded obligations: %+v", got)
	}
	if states := skillTurnStates(s); len(states) != 0 {
		t.Fatalf("raw file reads recorded typed skill state: %+v", states)
	}
}

// TestMintSkillOperationID_SurvivesRestartWithoutInterveningSave: the minted
// counter must be durable before the identity can reach any carrier — a
// crash-restart with no intervening metadata save must never reissue an
// already-minted identity. The pre-Close LoadSessionMeta captures exactly the
// on-disk state a crash right after the mint would leave behind.
func TestMintSkillOperationID_SurvivesRestartWithoutInterveningSave(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	first, err := s.mintSkillOperationID()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := schema.LoadSessionMeta(stateDir, s.Meta().ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	restored, err := RestoreSessionFromMeta(s.Client(), s.Profile(), execenv.NewLocalExecutionEnvironment(root), saved, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	second, err := restored.mintSkillOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("post-restart mint reissued %q", first)
	}
}

// TestPrepareSelectedInput_DuplicateNamesInvokeOnce: normalization deliberately
// retains duplicate client selections, but one atomic group must not carry two
// invocations (and two obligations/carriers) under the SAME InvocationID —
// dedup preserves the request order, keeping the first occurrence.
func TestPrepareSelectedInput_DuplicateNamesInvokeOnce(t *testing.T) {
	root := t.TempDir()
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_dup1")
	writeSkillMD(t, root, "second", "---\nname: second\ndescription: fixture\n---\nBODY_dup2")
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: t.TempDir()}), withoutGitSnapshot())
	batch, err := s.prepareSelectedInput(context.Background(), queuedInput{
		ID:         "q-dup-1",
		SkillNames: []string{"opaque", "second", "opaque"},
	}, "user_selection")
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Items) != 2 {
		t.Fatalf("batch items = %+v, want exactly [opaque second]", batch)
	}
	seen := map[string]bool{}
	for _, item := range batch.Items {
		if seen[item.Invocation.InvocationID] {
			t.Fatalf("duplicate InvocationID %q in one atomic group", item.Invocation.InvocationID)
		}
		seen[item.Invocation.InvocationID] = true
	}
	if batch.Items[0].Invocation.Name != "opaque" || batch.Items[1].Invocation.Name != "second" {
		t.Fatalf("request order not preserved: %+v", batch.Items)
	}
}

// TestSkillActivation_AdmissionSaveFailureAdmitsNothing forces the metadata
// save inside admission to fail (the same breakSessionMetaPath injection the
// compaction save-failure tests use): the batch must admit NOTHING — no live
// obligation, no live or durable carrier — so a restart can never restore a
// durable carrier whose obligation was lost in the crash window between the
// transcript write and the metadata save. After the repair, a retry admits
// cleanly and the restored session sees the obligation.
func TestSkillActivation_AdmissionSaveFailureAdmitsNothing(t *testing.T) {
	root := t.TempDir()
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_admit_save")
	stateDir := t.TempDir()
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	batch, err := s.prepareSelectedInput(context.Background(), queuedInput{
		ID:         "q-admit-save",
		SkillNames: []string{"opaque"},
	}, "user_selection")
	if err != nil || batch == nil || len(batch.Items) != 1 {
		t.Fatalf("prepare: batch=%+v err=%v", batch, err)
	}

	repair := breakSessionMetaPath(t, s)
	if err := s.admitSkillActivationBatch(batch); err == nil {
		t.Fatal("a failed metadata save must not report admission success")
	}
	// Nothing half-admits: the obligation rolls back out of memory and no
	// carrier joined the live history.
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("failed admission left live obligations: %+v", got)
	}
	for _, state := range skillTurnStates(s) {
		if len(state.Outcomes) != 0 || len(state.Obligations) != 0 {
			t.Fatalf("failed admission recorded skill turn state: %+v", state)
		}
	}
	repair()

	// The next successful save persists the clean (obligation-free) state.
	if err := s.saveMeta(); err != nil {
		t.Fatalf("save after repair: %v", err)
	}
	restore := func() *Session {
		t.Helper()
		meta, err := schema.LoadSessionMeta(stateDir, s.id)
		if err != nil {
			t.Fatal(err)
		}
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})
		restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, stateDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { restored.Close() })
		return restored
	}

	// Crash-equivalent restore: the durable state carries neither the
	// obligation nor the carrier — the window cannot strand one without the
	// other.
	restored := restore()
	if got := lifecycleObligations(restored); len(got) != 0 {
		t.Fatalf("restore recovered an obligation whose admission failed: %+v", got)
	}
	for _, state := range skillTurnStates(restored) {
		if len(state.Outcomes) != 0 || len(state.Obligations) != 0 {
			t.Fatalf("restore recovered a carrier whose obligation was lost: %+v", state)
		}
	}

	// A retry after the repair admits cleanly, and the restored session sees
	// the obligation with its carrier.
	if err := s.admitSkillActivationBatch(batch); err != nil {
		t.Fatalf("retry after repair: %v", err)
	}
	if got := lifecycleObligations(s); len(got) != 1 {
		t.Fatalf("retry admission obligations = %+v, want exactly one", got)
	}
	restored = restore()
	if got := lifecycleObligations(restored); len(got) != 1 {
		t.Fatalf("restored obligations = %+v, want exactly one", got)
	}
	carrierSeen := false
	for _, state := range skillTurnStates(restored) {
		if len(state.Obligations) == 1 {
			carrierSeen = true
		}
	}
	if !carrierSeen {
		t.Fatal("restored history lost the admitted carrier turn")
	}
}
