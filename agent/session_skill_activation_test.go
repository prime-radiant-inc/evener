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
	"testing"

	"primeradiant.com/evener/agent/execenv"
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
	var pathError *os.PathError
	if !errors.As(err, &pathError) {
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
