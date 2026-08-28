package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
)

func TestAppThreadReadColdDelegatesMatchReconnectedDetailedStatus(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	sessionID := strings.TrimPrefix(params.Ref, "local:")
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry missing")
	}
	path := filepath.Join(entry.StateDir, "sessions", sessionID, "delegates.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	descriptor := map[string]any{
		"child_session_id": "child-cold", "transcript_ref": "local:child-cold", "owner_session_id": sessionID,
		"task": "cold task", "description": "cold description", "agent_type": "explorer", "resolved_profile_id": "openai", "resolved_model": "gpt-5",
		"tool_name_ceiling": []string{"communicate"}, "delegation_allowance": 2, "parent_watch_granted": true, "resumable": true, "config": map[string]any{},
	}
	childWriter, err := transcript.NewWriter(filepath.Join(entry.StateDir, "sessions", "child-cold.transcript.jsonl"), transcript.Header{
		SessionID:       "child-cold",
		ParentSessionID: sessionID,
		ProfileID:       "openai",
		Model:           "gpt-5",
	})
	if err != nil {
		t.Fatalf("create child transcript: %v", err)
	}
	if err := childWriter.Close(); err != nil {
		t.Fatalf("close child transcript: %v", err)
	}
	batch, err := json.Marshal(map[string]any{"events": []any{map[string]any{
		"kind": "delegate_created", "seq": 1, "ts": time.Unix(10, 0).UTC(), "delegate_id": "dlg_cold", "created": map[string]any{"descriptor": descriptor},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	rawJournal := append([]byte("{\"version\":1}\n"), append(batch, '\n')...)
	if err := os.WriteFile(path, rawJournal, 0o600); err != nil {
		t.Fatal(err)
	}

	thread, found := requirePastThreadForRead(t, cfg, params)
	if !found {
		t.Fatal("past thread not found")
	}
	if thread.Evener.Diagnostics == nil || len(thread.Evener.Diagnostics.Delegates) != 1 {
		t.Fatalf("cold thread delegates = %+v", thread.Evener.Diagnostics)
	}
	got := thread.Evener.Diagnostics.Delegates[0]
	if got.DelegateID != "dlg_cold" || got.ChildSessionID != "child-cold" || got.TranscriptRef != "local:child-cold" ||
		got.OwnerSessionID != sessionID || got.Type != "delegate" || got.Lifecycle != "idle" || got.Phase != "idle" || got.ProjectionRevision != 1 ||
		got.Task != "cold task" || got.Description != "cold description" || got.DelegationAllowance != 2 || !got.ParentWatchGranted {
		t.Fatalf("cold stable delegate = %+v", got)
	}
}

func TestPastThreadReadCarriesSkillCatalog(t *testing.T) {
	cfg, entry := seedPastSessionWithSkillFixtures(t)
	thread, ok, err := pastThreadForRead(context.Background(), cfg, appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID})
	if err != nil || !ok {
		t.Fatalf("pastThreadForRead = %v, %v", err, ok)
	}
	if thread.Evener.Diagnostics == nil {
		t.Fatal("past thread has no diagnostics")
	}
	for _, want := range []string{"doctoring-evener", "project-skill", "extra-skill", "fixture-plugin:plugin-skill"} {
		if !hasSkill(thread.Evener.Diagnostics.Skills, want) {
			t.Fatalf("skills = %+v, missing %q", thread.Evener.Diagnostics.Skills, want)
		}
	}
	wantNames := []string{"doctoring-evener", "extra-skill", "fixture-plugin:plugin-skill", "project-skill"}
	if len(thread.Evener.Diagnostics.Skills) != len(wantNames) {
		t.Fatalf("skill catalog = %+v, want exactly %d entries", thread.Evener.Diagnostics.Skills, len(wantNames))
	}
	for i, want := range wantNames {
		if got := thread.Evener.Diagnostics.Skills[i].Name; got != want {
			t.Fatalf("skill catalog order = %+v, want %v", thread.Evener.Diagnostics.Skills, wantNames)
		}
	}
	descriptions := map[string]string{
		"extra-skill":                 "extra description",
		"fixture-plugin:plugin-skill": "plugin description",
		"project-skill":               "project description",
		"doctoring-evener":            "extra override",
	}
	for _, got := range thread.Evener.Diagnostics.Skills {
		if want, ok := descriptions[got.Name]; ok && got.Description != want {
			t.Fatalf("%s description = %q, want %q", got.Name, got.Description, want)
		}
	}
	for _, got := range thread.Evener.Diagnostics.Skills {
		if strings.Contains(got.Name, string(filepath.Separator)) {
			t.Fatalf("skill metadata contains a path: %+v", got)
		}
	}
}

func TestPastThreadReadResponseCarriesSkillCatalog(t *testing.T) {
	cfg, entry := seedPastSessionWithSkillFixtures(t)
	response, ok, err := pastThreadReadResponse(context.Background(), cfg, appwire.ThreadReadParams{
		Ref: "local:" + entry.Meta.ID, IncludeTurns: true, TurnLimit: 1,
	})
	if err != nil || !ok {
		t.Fatalf("pastThreadReadResponse = %v, %v", err, ok)
	}
	if response.Thread.Evener.Diagnostics == nil || !hasSkill(response.Thread.Evener.Diagnostics.Skills, "fixture-plugin:plugin-skill") {
		t.Fatalf("bounded response diagnostics = %+v", response.Thread.Evener.Diagnostics)
	}
}

func TestPastThreadSkillCatalogLayerPrecedence(t *testing.T) {
	t.Run("project overrides embedded", func(t *testing.T) {
		workingDir := t.TempDir()
		writeSkillFixture(t, filepath.Join(workingDir, "skills"), "doctoring-evener", "project wins")
		got := discoverPastThreadSkills(hubcore.PastEntry{Meta: schema.SessionMeta{EnvInfo: schema.EnvironmentInfo{WorkingDir: workingDir}}})
		if description := skillDescription(got, "doctoring-evener"); description != "project wins" {
			t.Fatalf("project-over-embedded description = %q", description)
		}
	})

	t.Run("skills dirs override project", func(t *testing.T) {
		workingDir, extraDir := t.TempDir(), t.TempDir()
		writeSkillFixture(t, filepath.Join(workingDir, "skills"), "layered-skill", "project value")
		writeSkillFixture(t, extraDir, "layered-skill", "extra value")
		got := discoverPastThreadSkills(hubcore.PastEntry{Meta: schema.SessionMeta{
			EnvInfo: schema.EnvironmentInfo{WorkingDir: workingDir}, Config: schema.ConfigSnapshot{SkillsDirs: []string{extraDir}},
		}})
		if description := skillDescription(got, "layered-skill"); description != "extra value" {
			t.Fatalf("SkillsDirs-over-project description = %q", description)
		}
	})

	t.Run("plugin metadata is canonical", func(t *testing.T) {
		pluginDir := writeThreadSkillPlugin(t, t.TempDir(), "metadata-plugin", "plugin-skill", "plugin value")
		got := discoverPastThreadSkills(hubcore.PastEntry{Meta: schema.SessionMeta{Config: schema.ConfigSnapshot{PluginDirs: []string{pluginDir}}}})
		if description := skillDescription(got, "metadata-plugin:plugin-skill"); description != "plugin value" {
			t.Fatalf("plugin description = %q", description)
		}
	})
}

func TestPastThreadSkillCatalogIncludesAutomaticUserSkills(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	workingDir := t.TempDir()
	writeSkillFixture(t, filepath.Join(xdg, "evener", "skills"), "cold-user-skill", "user description")
	writeSkillFixture(t, filepath.Join(xdg, "evener", "skills"), "doctoring-evener", "user override")

	got := discoverPastThreadSkills(hubcore.PastEntry{Meta: schema.SessionMeta{
		EnvInfo: schema.EnvironmentInfo{WorkingDir: workingDir},
	}})
	if description := skillDescription(got, "cold-user-skill"); description != "user description" {
		t.Fatalf("automatic user skill description = %q, want %q", description, "user description")
	}
	if description := skillDescription(got, "doctoring-evener"); description != "user override" {
		t.Fatalf("automatic user override description = %q, want %q", description, "user override")
	}
}

func TestPastThreadSkillCatalogUsesFirstDuplicatePlugin(t *testing.T) {
	root := t.TempDir()
	first := writeThreadSkillPlugin(t, root, "duplicate-plugin", "same-skill", "first value")
	second := writeThreadSkillPlugin(t, root, "duplicate-plugin", "same-skill", "second value")
	got := discoverPastThreadSkills(hubcore.PastEntry{Meta: schema.SessionMeta{Config: schema.ConfigSnapshot{PluginDirs: []string{first, second}}}})
	if description := skillDescription(got, "duplicate-plugin:same-skill"); description != "first value" {
		t.Fatalf("duplicate plugin description = %q, want first plugin value", description)
	}
}

func TestPastThreadSkillCatalogUsesFirstManifestDuplicatePlugin(t *testing.T) {
	root := t.TempDir()
	first := writeThreadSkillPlugin(t, root, "successful-duplicate", "same-skill", "broken first")
	writeSkillFile(t, filepath.Join(first, "commands", "broken.md"), "---\ndescription: [\n---\nbody")
	writeSkillFile(t, filepath.Join(first, "agents", "broken.md"), "---\ndescription: [\n---\nbody")
	writeSkillFile(t, filepath.Join(first, "hooks", "hooks.json"), "{not json")
	second := writeThreadSkillPlugin(t, root, "successful-duplicate", "same-skill", "valid later")
	if _, err := plugin.Load(first); err == nil {
		t.Fatal("broken first duplicate unexpectedly loaded")
	}
	if _, err := plugin.Load(second); err != nil {
		t.Fatalf("valid later duplicate failed to load: %v", err)
	}
	loaded, skipped := plugin.LoadAllFailSoft([]string{first, second})
	if len(loaded) != 0 || len(skipped) != 2 || skipped[1].Name != "successful-duplicate" {
		t.Fatalf("startup duplicate selection = loaded=%+v skipped=%+v, want first manifest selected then skipped", loaded, skipped)
	}

	got := discoverPastThreadSkills(hubcore.PastEntry{Meta: schema.SessionMeta{Config: schema.ConfigSnapshot{
		PluginDirs: []string{first, second},
	}}})
	if description := skillDescription(got, "successful-duplicate:same-skill"); description != "broken first" {
		t.Fatalf("first-manifest duplicate description = %q, want broken first", description)
	}
}

func TestPastThreadSkillLoaderIgnoresMalformedPluginComponents(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{name: "commands", setup: func(t *testing.T, dir string) {
			writeSkillFile(t, filepath.Join(dir, "commands", "broken.md"), "---\ndescription: [\n---\nbody")
		}},
		{name: "agents", setup: func(t *testing.T, dir string) {
			writeSkillFile(t, filepath.Join(dir, "agents", "broken.md"), "---\ndescription: [\n---\nbody")
		}},
		{name: "hooks", setup: func(t *testing.T, dir string) {
			writeSkillFile(t, filepath.Join(dir, "hooks", "hooks.json"), "{not json")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeThreadSkillPlugin(t, t.TempDir(), "malformed-plugin", "valid-skill", "valid value")
			tc.setup(t, dir)
			if _, err := plugin.Load(dir); err == nil {
				t.Fatal("full plugin loader unexpectedly accepted malformed component")
			}
			got := discoverPastThreadSkills(hubcore.PastEntry{Meta: schema.SessionMeta{Config: schema.ConfigSnapshot{PluginDirs: []string{dir}}}})
			if description := skillDescription(got, "malformed-plugin:valid-skill"); description != "valid value" {
				t.Fatalf("skill-only loader lost valid skill: description=%q catalog=%+v", description, got)
			}
		})
	}
}

func TestPastThreadReadCarriesExistingDelegateDiagnosticAlongsideSkills(t *testing.T) {
	cfg, entry := seedPastSessionWithSkillFixtures(t)
	delegateDir := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID)
	if err := os.MkdirAll(delegateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	descriptor := map[string]any{
		"child_session_id": entry.Meta.ID, "transcript_ref": "local:" + entry.Meta.ID,
		"owner_session_id": entry.Meta.ID, "task": "fixture task", "description": "fixture description",
		"agent_type": "explorer", "resumable": true,
		"tool_name_ceiling": []string{"communicate"},
		"config":            map[string]any{},
	}
	batch, err := json.Marshal(map[string]any{"events": []any{map[string]any{
		"kind": "delegate_created", "seq": 1, "ts": time.Unix(10, 0).UTC(),
		"delegate_id": "dlg_fixture", "created": map[string]any{"descriptor": descriptor},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	journal := append([]byte("{\"version\":1}\n"), append(batch, '\n')...)
	if err := os.WriteFile(filepath.Join(delegateDir, "delegates.jsonl"), journal, 0o600); err != nil {
		t.Fatal(err)
	}

	thread, ok, err := pastThreadForRead(context.Background(), cfg, appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID})
	if err != nil || !ok {
		t.Fatalf("pastThreadForRead = %v, %v", err, ok)
	}
	if thread.Evener.Diagnostics == nil || len(thread.Evener.Diagnostics.Delegates) != 1 ||
		!hasSkill(thread.Evener.Diagnostics.Skills, "project-skill") {
		t.Fatalf("diagnostics = %+v", thread.Evener.Diagnostics)
	}
}

// TestPastThreadReadCarriesDelegateDiagnosticEvenWithZeroDelegates is the
// wire-level test proving pastEntryThread's containment behavior actually
// reaches API consumers: degrading a corrupt shared delegates.jsonl to zero
// delegates plus a diagnostic string, rather than hard-failing (see agent's
// TestLoadSessionDelegateStatus_OversizedDelegateJournalLineDegradesWithDiagnosticInsteadOfFailing,
// which proves the AGENT-side half of this), must still surface that
// diagnostic on the WIRE even though there is no delegate left to attach it
// to. This drives that exact containment case (an unterminated trailing
// batch line, decoding to zero delegates with a delegate_journal_torn_tail
// diagnostic -- cheaper to construct here than the oversized-line case, but
// goes through the identical scanRootDelegateState degrade-to-diagnostic
// path already proven above) all the way through pastThreadForRead, and
// asserts the diagnostic lands on the actual appwire.Thread payload's
// EvenerDiagnostics.DelegateDiagnostics field -- the wire itself, not an
// internal agent.LoadSessionDelegateStatus return value.
func TestPastThreadReadCarriesDelegateDiagnosticEvenWithZeroDelegates(t *testing.T) {
	cfg, entry := seedPastSessionWithSkillFixtures(t)
	delegateDir := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID)
	if err := os.MkdirAll(delegateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A valid, terminated version header followed by an unterminated
	// trailing batch line (no closing bracket, no newline): delegatestore's
	// ScanEventsFrom treats ANY unterminated final line as a torn tail
	// (discarded whole, never decoded, regardless of its partial content),
	// so this decodes to zero events -- zero delegates -- with a
	// delegate_journal_torn_tail diagnostic, exactly the "corrupt shared
	// journal, no delegates to attach a per-delegate diagnostic to" shape
	// the oversized-line case also produces, without needing a real 128 MiB
	// fixture line.
	journal := []byte("{\"version\":1}\n{\"events\":[{\"seq\":1")
	if err := os.WriteFile(filepath.Join(delegateDir, "delegates.jsonl"), journal, 0o600); err != nil {
		t.Fatal(err)
	}

	thread, ok, err := pastThreadForRead(context.Background(), cfg, appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID})
	if err != nil || !ok {
		t.Fatalf("pastThreadForRead = %v, %v", err, ok)
	}
	if thread.Evener.Diagnostics == nil {
		t.Fatal("Diagnostics is nil, want it populated with a delegate-subsystem diagnostic")
	}
	if len(thread.Evener.Diagnostics.Delegates) != 0 {
		t.Fatalf("Delegates = %+v, want empty (nothing survives a torn tail with no complete batch before it)", thread.Evener.Diagnostics.Delegates)
	}
	found := false
	for _, d := range thread.Evener.Diagnostics.DelegateDiagnostics {
		if strings.Contains(d, "torn_tail") {
			found = true
		}
	}
	if !found {
		t.Fatalf("DelegateDiagnostics = %v, want one naming the torn tail -- the wire response must carry this even though delegates is empty", thread.Evener.Diagnostics.DelegateDiagnostics)
	}
}

func hasSkill(skills []appwire.EvenerSkillInfo, name string) bool {
	for _, skill := range skills {
		if skill.Name == name {
			return true
		}
	}
	return false
}

func seedPastSessionWithSkillFixtures(t *testing.T) (hubcore.WebConfig, hubcore.PastEntry) {
	t.Helper()
	root := t.TempDir()
	workingDir := filepath.Join(root, "project")
	extraDir := filepath.Join(root, "extra-skills")
	pluginDir := filepath.Join(root, "plugin")
	writeSkillFixture(t, filepath.Join(workingDir, "skills"), "project-skill", "project description")
	writeSkillFixture(t, filepath.Join(workingDir, "skills"), "doctoring-evener", "project override")
	writeSkillFixture(t, extraDir, "extra-skill", "extra description")
	writeSkillFixture(t, extraDir, "doctoring-evener", "extra override")
	if err := os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"name":"fixture-plugin","mcpServers":123}`)
	if err := os.WriteFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	writeSkillFixture(t, filepath.Join(pluginDir, "skills"), "plugin-skill", "plugin description")

	stateDir := filepath.Join(root, "state", "projects", "project-repo-0000000000")
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: workingDir},
		Config:    schema.ConfigSnapshot{SkillsDirs: []string{extraDir}, PluginDirs: []string{pluginDir}},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "state", "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	entry, ok := idx.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	return hubcore.WebConfig{Past: idx}, entry
}

func writeSkillFixture(t *testing.T, parent, name, description string) {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\nfixture body\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeThreadSkillPlugin(t *testing.T, root, pluginName, skillName, description string) string {
	t.Helper()
	dir := filepath.Join(root, pluginName+"-"+strings.ReplaceAll(description, " ", "-"))
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(`{"name":"`+pluginName+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSkillFixture(t, filepath.Join(dir, "skills"), skillName, description)
	return dir
}

func writeSkillFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func skillDescription(skills []appwire.EvenerSkillInfo, name string) string {
	for _, skill := range skills {
		if skill.Name == name {
			return skill.Description
		}
	}
	return ""
}

func TestHubThreadReadStableDelegateDoesNotExtractActivationID(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"job_retired_activation","delegate_id":"dlg_stable","status":"running"}`)
	item := appwire.ThreadItem{Type: "commandExecution", ToolName: "delegate", Raw: bytes.Clone(raw), Status: appwire.TurnStatusInProgress}
	got := reconcileDelegateThreadItemForTest(item, agent.HistoricalJobRecord{
		JobID: "job_retired_activation", DelegateID: "dlg_stable", Type: "delegate", Status: "completed", OutputBytes: 99,
	})
	if !bytes.Equal(got.Raw, raw) || got.Status != item.Status {
		t.Fatalf("thread read extracted/reconciled retired activation id: got raw=%s status=%s", got.Raw, got.Status)
	}
}

func TestHubThreadReadStableDelegateIsReadOnly(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	sessionID := strings.TrimPrefix(params.Ref, "local:")
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry missing")
	}
	jobsPath := filepath.Join(entry.StateDir, "sessions", sessionID, "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(jobsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"kind":"job_started","job_id":"job_readonly","type":"shell","owner_session_id":"` + sessionID + `","started_at":"2026-08-15T00:00:00Z"}` + "\n")
	if err := os.WriteFile(jobsPath, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(1234, 0).UTC()
	if err := os.Chtimes(jobsPath, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := requirePastThreadForRead(t, cfg, params); !found {
		t.Fatal("past thread not found")
	}
	afterRaw, err := os.ReadFile(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterRaw, raw) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("thread read mutated jobs journal: before=%v/%v/%v after=%v/%v/%v", before.Size(), before.Mode(), before.ModTime(), after.Size(), after.Mode(), after.ModTime())
	}
}

func requirePastThreadForRead(t testing.TB, cfg hubcore.WebConfig, params appwire.ThreadReadParams) (appwire.Thread, bool) {
	t.Helper()
	thread, found, err := pastThreadForRead(context.Background(), cfg, params)
	if err != nil {
		t.Fatalf("pastThreadForRead: %v", err)
	}
	return thread, found
}

func requirePastThreadReadResponse(t testing.TB, cfg hubcore.WebConfig, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, bool) {
	t.Helper()
	resp, found, err := pastThreadReadResponse(context.Background(), cfg, params)
	if err != nil {
		t.Fatalf("pastThreadReadResponse: %v", err)
	}
	return resp, found
}

func requirePastThreadTurnsList(t testing.TB, cfg hubcore.WebConfig, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, bool) {
	t.Helper()
	resp, found, err := pastThreadTurnsList(context.Background(), cfg, params)
	if err != nil {
		t.Fatalf("pastThreadTurnsList: %v", err)
	}
	return resp, found
}

func requirePastEntryTurns(t testing.TB, cfg hubcore.WebConfig, entry hubcore.PastEntry) []appwire.Turn {
	t.Helper()
	turns, err := pastEntryTurns(cfg, entry)
	if err != nil {
		t.Fatalf("pastEntryTurns: %v", err)
	}
	return turns
}

// pricingRegistry is the hermetic registry a past thread's dollar figures
// resolve through: the curated rows for one anthropic instance, offline,
// uncached, with no user layer and nothing from the developer's own state.
func pricingRegistry(tb testing.TB) *hubcore.ProviderRegistry {
	tb.Helper()
	stateRoot := tb.TempDir()
	holder := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		r, err := registry.Load(append([]registry.Option{
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(stateRoot),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(map[string]registry.Provider{"anthropic": {APIKey: "test"}}),
		}, extra...)...)
		return r, nil, err
	})
	if err := holder.Reload(); err != nil {
		tb.Fatalf("registry: %v", err)
	}
	return holder
}

func requirePastEntryThread(t testing.TB, cfg hubcore.WebConfig, entry hubcore.PastEntry, includeTurns bool) appwire.Thread {
	t.Helper()
	thread, err := pastEntryThread(context.Background(), cfg, entry, includeTurns)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	return thread
}

func TestThreadReadDoesNotReconcileStableDelegateFromActivationJob(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`)
	item := appwire.ThreadItem{Type: "commandExecution", ID: "item_delegate", CallID: "call_delegate", ToolName: "delegate", Raw: raw, Status: appwire.TurnStatusCompleted}
	rec := agent.HistoricalJobRecord{JobID: "job_A", DelegateID: "dlg_A", Type: "delegate", Status: "completed", Task: "inspect billing", TranscriptRef: "local:child", OriginToolCallID: "call_delegate", OutputBytes: 42}

	got := reconcileDelegateThreadItemForTest(item, rec)
	if got.Status != item.Status || !bytes.Equal(got.Raw, raw) {
		t.Fatalf("activation job mutated stable delegate transcript item: status=%q raw=%s", got.Status, got.Raw)
	}
}

func TestHistoricalDelegateActivationExhaustionDoesNotRewriteStableTranscript(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"job_exhausted","delegate_id":"dlg_exhausted","status":"running","task":"bounded work","transcript_ref":"local:child-exhausted"}`)
	item := appwire.ThreadItem{
		Type:     "commandExecution",
		ID:       "item_delegate",
		CallID:   "call_delegate",
		ToolName: "delegate",
		Raw:      raw,
		Status:   appwire.TurnStatusInProgress,
	}
	rec := agent.HistoricalJobRecord{
		JobID:         "job_exhausted",
		DelegateID:    "dlg_exhausted",
		Type:          "delegate",
		Status:        "exhausted",
		Reason:        "tool_round_budget_exhausted",
		Task:          "bounded work",
		TranscriptRef: "local:child-exhausted",
	}

	got := reconcileDelegateThreadItemForTest(item, rec)
	if got.Status != item.Status || !bytes.Equal(got.Raw, raw) {
		t.Fatalf("activation exhaustion mutated stable delegate transcript item: status=%q raw=%s", got.Status, got.Raw)
	}
}

func TestThreadReadLeavesDelegateRawUnchangedWithoutJobstoreRecord(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"job_missing","delegate_id":"dlg_A","status":"running"}`)
	item := appwire.ThreadItem{Type: "commandExecution", ID: "item_delegate", CallID: "call_delegate", ToolName: "delegate", Raw: raw, Status: appwire.TurnStatusInProgress}
	thread := appwire.Thread{Turns: []appwire.Turn{{ID: "turn_1", Items: []appwire.ThreadItem{item}}}}

	got := reconcileDelegateThreadItems(thread, map[string]agent.HistoricalJobRecord{})
	gotItem := got.Turns[0].Items[0]
	if gotItem.Status != appwire.TurnStatusInProgress || string(gotItem.Raw) != string(raw) {
		t.Fatalf("item changed without jobstore record: status=%q raw=%s", gotItem.Status, gotItem.Raw)
	}
}

func TestThreadReadReconciliationIgnoresMismatchedJobID(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"job_A","status":"running"}`)
	item := appwire.ThreadItem{Type: "commandExecution", ID: "item_delegate", CallID: "call_delegate", ToolName: "delegate", Raw: raw, Status: appwire.TurnStatusInProgress}
	rec := agent.HistoricalJobRecord{JobID: "job_B", Type: "delegate", Status: "completed"}

	got := reconcileDelegateThreadItemForTest(item, rec)
	if got.Status != appwire.TurnStatusInProgress || string(got.Raw) != string(raw) {
		t.Fatalf("mismatched job id reconciled: status=%q raw=%s", got.Status, got.Raw)
	}
}

func TestPastThreadReadDoesNotReconcileStableDelegateFromActivationJob(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-repo-0000000000")
	parentID := "02wMz5Txv1C3Hut0M8GCeB"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions", parentID), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:             parentID,
		ProfileID:      "openai",
		Model:          "gpt-5",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt:      now,
		UpdatedAt:      now,
		TurnCount:      1,
		OriginalPrompt: "inspect billing",
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", parentID+".transcript.jsonl"), transcript.Header{
		SessionID: parentID,
		CreatedAt: now,
		ProfileID: "openai",
		Model:     "gpt-5",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	runningRaw := json.RawMessage(`{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`)
	if err := w.Append(schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_delegate", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_delegate",
				Name:       "delegate",
				Content:    string(runningRaw),
				ToolState:  runningRaw,
			},
		}}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	writeHistoricalJobLog(t, filepath.Join(stateDir, "sessions", parentID, "jobs.jsonl"), now, "job_A")

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread, ok := requirePastThreadForRead(t, hubcore.WebConfig{Past: idx}, appwire.ThreadReadParams{Ref: "local:" + parentID, IncludeTurns: true})
	if !ok {
		t.Fatal("past thread not found")
	}
	if len(thread.Turns) != 1 || len(thread.Turns[0].Items) != 1 {
		t.Fatalf("turns=%+v", thread.Turns)
	}
	item := thread.Turns[0].Items[0]
	if item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("status=%q, want transcript-projected completed", item.Status)
	}
	if !bytes.Equal(item.Raw, runningRaw) {
		t.Fatalf("activation job mutated transcript raw: got %s want %s", item.Raw, runningRaw)
	}
}

func TestPastThreadReadProjectsThinkingFromTranscript(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-repo-0000000000")
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions", sessionID), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "kimi",
		Model:     "kimi-for-coding",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt: now,
		UpdatedAt: now,
		TurnCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID,
		CreatedAt: now,
		ProfileID: "kimi",
		Model:     "kimi-for-coding",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "Let me reason about this."}},
			{Kind: llm.ContentText, Text: "Here is the answer."},
		}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread, ok := requirePastThreadForRead(t, hubcore.WebConfig{Past: idx}, appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true})
	if !ok {
		t.Fatal("past thread not found")
	}
	if len(thread.Turns) != 1 {
		t.Fatalf("turns=%+v", thread.Turns)
	}
	items := thread.Turns[0].Items
	if len(items) != 2 {
		t.Fatalf("expected reasoning + agentMessage, got %+v", items)
	}
	if items[0].Type != "reasoning" || items[0].Text != "Let me reason about this." {
		t.Fatalf("reasoning item=%+v", items[0])
	}
	if items[1].Type != "agentMessage" || items[1].Text != "Here is the answer." {
		t.Fatalf("agent message item=%+v", items[1])
	}
}

func TestPastThreadReadProjectsToolResultOutputImages(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-repo-0000000000")
	sessionID := "02wMz5Txv733WHFsVy66SR"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID,
		CreatedAt: now,
		ProfileID: "openai",
		Model:     "gpt-5",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := w.Append(schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_img", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID:     "call_img",
				Name:           "screenshot",
				Content:        "captured",
				ImageData:      png,
				ImageMediaType: "image/png",
			},
		}}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread, ok := requirePastThreadForRead(t, hubcore.WebConfig{Past: idx}, appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true})
	if !ok {
		t.Fatal("past thread not found")
	}
	if len(thread.Turns) != 1 || len(thread.Turns[0].Items) != 1 {
		t.Fatalf("turns=%+v", thread.Turns)
	}
	item := thread.Turns[0].Items[0]
	wantSHA := imageSha(png)
	if len(item.OutputImages) != 1 {
		t.Fatalf("OutputImages=%+v, want one", item.OutputImages)
	}
	img := item.OutputImages[0]
	if img.Source != "tool-result" || img.Name != "screenshot" || img.MediaType != "image/png" || img.Size != int64(len(png)) || img.SHA != wantSHA || img.URL != "/s/"+sessionID+"/images/"+wantSHA {
		t.Fatalf("OutputImages[0]=%+v", img)
	}
}

// TestPastEntryTurns_StampsCostFromSessionModel verifies pastEntryTurns
// estimates each turn's Cost from the row the session's own recorded
// instance and model resolve to — usage alone (from the transcript) isn't
// enough to price a turn, since the cost lives on the registry row.
func TestPastEntryTurns_StampsCostFromSessionModel(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-repo-0000000000")
	sessionID := "01COST"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID,
		CreatedAt: now,
		ProfileID: "anthropic",
		Model:     "claude-opus-4-5",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(schema.Turn{
		Kind:    schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "Here is the answer."}}},
		Usage:   llm.Usage{InputTokens: 100, OutputTokens: 50},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entry := hubcore.PastEntry{
		ID:       sessionID,
		Meta:     schema.SessionMeta{ID: sessionID, ProfileID: "anthropic", Model: "claude-opus-4-5"},
		StateDir: stateDir,
	}
	cfg := hubcore.WebConfig{Registry: pricingRegistry(t)}
	turns := requirePastEntryTurns(t, cfg, entry)
	var found bool
	for _, turn := range turns {
		if turn.Usage == nil {
			continue
		}
		found = true
		if !strings.HasPrefix(turn.Cost, "~$") {
			t.Fatalf("turn.Cost=%q, want ~$ prefix", turn.Cost)
		}
	}
	if !found {
		t.Fatalf("no turn with usage found: %+v", turns)
	}

	// Flag day (spec §14.1): with no registry to resolve against there is no
	// fallback pricing table, so the same turns carry no cost at all.
	for _, turn := range requirePastEntryTurns(t, hubcore.WebConfig{}, entry) {
		if turn.Cost != "" {
			t.Fatalf("turn.Cost=%q with no registry, want empty", turn.Cost)
		}
	}
}

func TestPastEntryThread_CarriesWorkMetrics(t *testing.T) {
	entry := hubcore.PastEntry{
		Meta: schema.SessionMeta{
			WorkMillis: 5000,
			CumulativeUsage: schema.CumulativeUsage{
				InputTokens:  100,
				OutputTokens: 50,
				TotalTokens:  150,
			},
		},
	}

	thread := requirePastEntryThread(t, hubcore.WebConfig{}, entry, false)

	if thread.Evener.WorkMillis != 5000 {
		t.Fatalf("thread.Evener.WorkMillis = %d, want 5000", thread.Evener.WorkMillis)
	}
	if thread.Evener.Usage == nil {
		t.Fatalf("thread.Evener.Usage = nil, want non-nil")
	}
	if thread.Evener.Usage.InputTokens != 100 || thread.Evener.Usage.OutputTokens != 50 || thread.Evener.Usage.TotalTokens != 150 {
		t.Fatalf("thread.Evener.Usage = %+v, want InputTokens=100 OutputTokens=50 TotalTokens=150", thread.Evener.Usage)
	}
	if thread.Evener.ActiveTurnStartedAt != 0 {
		t.Fatalf("thread.Evener.ActiveTurnStartedAt = %d, want 0 (ended session)", thread.Evener.ActiveTurnStartedAt)
	}
}

// TestPastEntryThread_CarriesCostTotal proves the past-entry hydrate stamps
// the session-level dollar total on EvenerThread from the cumulative usage at
// the cost the session's own ProfileID/Model resolve to on the hub's registry
// (spec §7.5) — the honest full-session figure, never a page of loaded turns
// — and honestly omits it when there is no usage, no registry, or no cost on
// the row (the absent-vs-zero distinction).
func TestPastEntryThread_CarriesCostTotal(t *testing.T) {
	cfg := hubcore.WebConfig{Registry: pricingRegistry(t)}
	usage := schema.CumulativeUsage{InputTokens: 100_000, OutputTokens: 20_000, TotalTokens: 120_000}

	priced := hubcore.PastEntry{
		Meta: schema.SessionMeta{ProfileID: "anthropic", Model: "claude-opus-4-5", CumulativeUsage: usage},
	}
	thread := requirePastEntryThread(t, cfg, priced, false)
	want := appwire.EstimateCost(costFor(cfg.Registry, "anthropic", "claude-opus-4-5"), thread.Evener.Usage)
	if want == "" {
		t.Fatal("fixture registry has no cost for anthropic/claude-opus-4-5")
	}
	if thread.Evener.Cost != want {
		t.Fatalf("thread.Evener.Cost = %q, want %q", thread.Evener.Cost, want)
	}
	if !strings.HasPrefix(thread.Evener.Cost, "~$") {
		t.Fatalf("thread.Evener.Cost = %q, want ~$ prefix", thread.Evener.Cost)
	}

	noUsage := hubcore.PastEntry{Meta: schema.SessionMeta{ProfileID: "anthropic", Model: "claude-opus-4-5"}}
	if got := requirePastEntryThread(t, cfg, noUsage, false); got.Evener.Cost != "" {
		t.Fatalf("no-usage thread.Evener.Cost = %q, want \"\" (absent)", got.Evener.Cost)
	}

	unknownInstance := hubcore.PastEntry{
		Meta: schema.SessionMeta{ProfileID: "no-such-instance", Model: "claude-opus-4-5", CumulativeUsage: usage},
	}
	if got := requirePastEntryThread(t, cfg, unknownInstance, false); got.Evener.Cost != "" {
		t.Fatalf("unresolvable-reference thread.Evener.Cost = %q, want \"\" (absent, not ~$0.00)", got.Evener.Cost)
	}

	// Flag day (spec §14.1): a hub with no registry has nothing to price
	// against and says so, rather than reaching for a bundled catalog.
	if got := requirePastEntryThread(t, hubcore.WebConfig{}, priced, false); got.Evener.Cost != "" {
		t.Fatalf("no-registry thread.Evener.Cost = %q, want \"\" (absent)", got.Evener.Cost)
	}
}

// TestPastEntryThread_UnnamedSessionKeepsTheShortForm proves the wire Name
// field agrees with the rail row (kata kspb / hubcore.nodeTitle) rather than
// the bare 22-char ID SessionDisplayName falls back to when a session has
// neither a generated name nor a prompt. Name feeds the pane header
// (model.name || ref) and the browser tab title (threadName(ref) ?? ref) on
// the frontend, and the TUI's tree title (thread.Name) directly — all three
// showed the raw ID until this fix (kata b309); the rail alone was fixed by
// kspb, in a different function (nodeTitle) that never touches this wire
// object.
//
// The short-circuit is scoped to Name only, not Preview: Preview is this
// wire object's own full-text field (unaffected, still the raw-ID fallback,
// matching the live-thread path in appwire_runtime.go and the TUI's own
// Name-then-Preview-then-SessionID chain), and SessionDisplayName itself
// stays untouched because eight other callers want its bare-ID last resort
// (see nodeTitle's doc comment).
func TestPastEntryThread_UnnamedSessionKeepsTheShortForm(t *testing.T) {
	const id = "033vq9Kif27AzZgnbjr55t" // a real 22-char UUIDv7 base62 payload

	unnamed := hubcore.PastEntry{Meta: schema.SessionMeta{ID: id}}
	thread := requirePastEntryThread(t, hubcore.WebConfig{}, unnamed, false)
	if want := hubcore.ShortID(id); thread.Name != want {
		t.Fatalf("thread.Name = %q, want %q", thread.Name, want)
	}
	if thread.Name == id {
		t.Fatalf("thread.Name = %q — the raw payload, which is what ShortID exists to avoid", thread.Name)
	}
	if thread.Preview != id {
		t.Fatalf("thread.Preview = %q, want the raw ID %q (Preview keeps the full-text fallback)", thread.Preview, id)
	}

	// A prompted-but-unnamed session has something better than the ID to
	// show, so it keeps the prompt verbatim rather than being shortened.
	prompted := hubcore.PastEntry{Meta: schema.SessionMeta{ID: id, OriginalPrompt: "fix the login bug"}}
	if got := requirePastEntryThread(t, hubcore.WebConfig{}, prompted, false).Name; got != "fix the login bug" {
		t.Fatalf("thread.Name (prompted) = %q, want the prompt verbatim", got)
	}

	// A named session is untouched.
	named := hubcore.PastEntry{Meta: schema.SessionMeta{ID: id, Name: "Login bug fix"}}
	if got := requirePastEntryThread(t, hubcore.WebConfig{}, named, false).Name; got != "Login bug fix" {
		t.Fatalf("thread.Name (named) = %q, want %q", got, "Login bug fix")
	}
}

func runningSubagentProjectionConfig(t *testing.T) (hubcore.WebConfig, string) {
	t.Helper()
	return runningSubagentProjectionConfigWithState(t, "")
}

// childState, when non-empty, is the status the parent's daemon carries for
// the child ("" models an old daemon that carries no per-descendant states).
func runningSubagentProjectionConfigWithState(t *testing.T, childState string) (hubcore.WebConfig, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-running-subagent-0000000000")
	now := time.Date(2026, 7, 13, 20, 0, 0, 0, time.UTC)
	parentID := "02wMz5Txv1C3Hut0M8GCeB"
	childID := "02wMz5Txv2enqVTitaig6F"
	for _, meta := range []schema.SessionMeta{
		{ID: parentID, CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: childID, CreatedAt: now, UpdatedAt: now, ParentSessionID: parentID, IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	} {
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	var states map[string]string
	if childState != "" {
		states = map[string]string{childID: childState}
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:                 rendezvous.Entry{PID: 1, SessionID: parentID},
		SessionID:             parentID,
		Status:                appwire.ThreadStatusIdle,
		RunningSubagentIDs:    []string{childID},
		RunningSubagentStates: states,
	})
	return hubcore.WebConfig{Past: past, Roster: roster}, childID
}

func TestPastThreadReadProjectsRunningSubagentActive(t *testing.T) {
	cfg, childID := runningSubagentProjectionConfig(t)
	thread, ok := requirePastThreadForRead(t, cfg, appwire.ThreadReadParams{Ref: "local:" + childID})
	if !ok {
		t.Fatal("running subagent not found in past index")
	}
	if thread.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("running subagent status = %q, want %q", thread.Status.Type, appwire.ThreadStatusActive)
	}
}

// A live (non-closed, resumable) in-process subagent whose daemon reports it
// settled must read as idle, not working: liveness is not activity.
func TestPastThreadReadProjectsIdleSubagentIdle(t *testing.T) {
	cfg, childID := runningSubagentProjectionConfigWithState(t, appwire.ThreadStatusIdle)
	thread, ok := requirePastThreadForRead(t, cfg, appwire.ThreadReadParams{Ref: "local:" + childID})
	if !ok {
		t.Fatal("idle subagent not found in past index")
	}
	if thread.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("idle subagent status = %q, want %q", thread.Status.Type, appwire.ThreadStatusIdle)
	}
}

func seedBoundedPastThread(t *testing.T) (hubcore.WebConfig, appwire.ThreadReadParams) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-bounded-0000000000")
	sessionID := "02wMz5Txv47YP64RR3B9YJ"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5", TurnCount: 200,
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/project"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Input fixture, not a durability test: batch the writes so seeding the
	// 200-turn transcript does not pay one fsync per Append. Close still
	// flushes, so the transcript read back is byte-identical.
	w.SyncInterval = time.Hour
	for range 199 {
		if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("saved turn"))); err != nil {
			t.Fatal(err)
		}
	}
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := w.Append(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_img", Name: "screenshot", Content: "captured", ImageData: png, ImageMediaType: "image/png"},
	}}}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return hubcore.WebConfig{Past: idx}, appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, TurnLimit: 40}
}

// TestPastEntryThreadAdvertisesResumableCapabilities asserts a past/exited
// local thread advertises exactly the capabilities that actually succeed once
// qp94's auto-resume is in place (kata xr4x). The resume-and-retry mutations
// (compact, clear, change model, shutdown) plus the always-available ones
// (send, fork, goal, rename) are true; the turn-in-flight controls (steer,
// interrupt, queue) are false because a cold exited session has no active turn
// for them to act on.
func TestPastEntryThreadAdvertisesResumableCapabilities(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-repo-0000000000")
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt: now,
		UpdatedAt: now,
		TurnCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	entry, ok := idx.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	thread := requirePastEntryThread(t, hubcore.WebConfig{Past: idx}, entry, false)
	caps := thread.Evener.Capabilities

	want := appwire.ThreadCapabilities{
		Send:              true,
		ForkFromTurn:      true,
		Compact:           true,
		Clear:             true,
		ChangeModel:       true,
		ChangeVisionModel: true,
		Shutdown:          true,
		Goal:              true,
		Rename:            true,
		// Steer, Interrupt, Queue stay false: turn-in-flight controls with no
		// active turn on a cold exited session.
	}
	if caps != want {
		t.Fatalf("past thread capabilities:\n got  %+v\n want %+v", caps, want)
	}
}

func TestMergePastThreadForReadDoesNotReadSavedTurnsWhenLiveWindowPresent(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	entry, ok := pastEntryForRead(cfg, params)
	if !ok {
		t.Fatal("past thread not found")
	}
	path := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	liveTurns := []appwire.Turn{{ID: "turn_live", ItemsView: "full"}}

	got, err := mergePastThreadForRead(context.Background(), cfg, params, appwire.Thread{
		ID:        entry.Meta.ID,
		SessionID: entry.Meta.ID,
		Turns:     liveTurns,
	})
	if err != nil {
		t.Fatalf("merge live window with unreadable saved transcript: %v", err)
	}
	if !reflect.DeepEqual(got.Turns, liveTurns) {
		t.Fatalf("merged turns = %+v, want live window %+v", got.Turns, liveTurns)
	}
	if got.ModelProvider != entry.Meta.Model || got.CWD != entry.Meta.EnvInfo.WorkingDir {
		t.Fatalf("merged metadata = model %q cwd %q, want %q %q", got.ModelProvider, got.CWD, entry.Meta.Model, entry.Meta.EnvInfo.WorkingDir)
	}
}

func TestMergePastThreadForReadUsesSavedTurnsWhenLiveResponseHasNone(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	entry, ok := pastEntryForRead(cfg, params)
	if !ok {
		t.Fatal("past thread not found")
	}

	got, err := mergePastThreadForRead(context.Background(), cfg, params, appwire.Thread{ID: entry.Meta.ID, SessionID: entry.Meta.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != entry.Meta.TurnCount {
		t.Fatalf("merged turns = %d, want saved fallback %d", len(got.Turns), entry.Meta.TurnCount)
	}
}

func TestPastThreadReadUsesBoundedSavedTranscript(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	full, ok := requirePastThreadForRead(t, cfg, params)
	if !ok || len(full.Turns) != 200 {
		t.Fatalf("full saved thread found=%v turns=%d, want true/200", ok, len(full.Turns))
	}
	wantTurns, wantCursor := appwire.WindowTurns(full.Turns, params.TurnLimit)

	var projected []int
	restore := apptranscript.InstallReadObserverForTesting(func(stats apptranscript.ReadStats) { projected = append(projected, stats.ProjectedTurns) })
	t.Cleanup(restore)
	got, ok := requirePastThreadReadResponse(t, cfg, params)
	if !ok {
		t.Fatal("past thread not found")
	}
	if !reflect.DeepEqual(got.Thread.Turns, wantTurns) || got.OlderCursor != wantCursor {
		t.Fatal("bounded saved read differs from full reference")
	}
	if !reflect.DeepEqual(projected, []int{40}) {
		t.Fatalf("saved read used legacy full projection of 200 turns; bounded projection reports = %v, want [40]", projected)
	}
	last := got.Thread.Turns[len(got.Thread.Turns)-1].Items[0]
	if len(last.OutputImages) != 1 || last.OutputImages[0].Name != "screenshot" {
		t.Fatalf("bounded saved projection lost embedded output image: %+v", last)
	}
}

func TestPastThreadTranscriptReadersPropagateUnsupportedFormat(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	entry, ok := pastEntryForRead(cfg, params)
	if !ok {
		t.Fatal("past thread not found")
	}
	path := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, found, err := pastThreadReadResponse(context.Background(), cfg, params)
	if !found || !errors.Is(err, transcript.ErrUnsupportedFormat) || resp.Thread.Turns != nil {
		t.Fatalf("past thread/read = (%+v, %v, %v), want found empty ErrUnsupportedFormat", resp, found, err)
	}
	page, found, err := pastThreadTurnsList(context.Background(), cfg, appwire.ThreadTurnsListParams{Ref: params.Ref, Limit: 1})
	if !found || !errors.Is(err, transcript.ErrUnsupportedFormat) || page.Data != nil {
		t.Fatalf("past thread/turns/list = (%+v, %v, %v), want found empty ErrUnsupportedFormat", page, found, err)
	}
}

// TestPastThreadForRead_PastGateMisses pins every way the past gate declines
// a read: a hub with no past index, params naming no thread at all, a ref
// belonging to another source, and a thread id the index does not hold. All
// four must report "no past thread" with an empty thread and no error, since
// the callers (thread/read, thread/turns/list, the live-thread merge) treat
// found=false as "nothing to add" and an error as a failed read.
//
// The foreign-ref case seeds the index with a session whose id IS the codex
// ref's thread id: dropping the local-source check would answer another
// source's caller out of local session state.
func TestPastThreadForRead_PastGateMisses(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
	emptyIndex := hubcore.NewPastIndex(filepath.Join(t.TempDir(), "projects", "*"))
	if _, err := emptyIndex.Rebuild(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		cfg    hubcore.WebConfig
		params appwire.ThreadReadParams
	}{
		{"no past index", hubcore.WebConfig{}, appwire.ThreadReadParams{ThreadID: sessionID}},
		{"no ref and no thread id", cfg, appwire.ThreadReadParams{}},
		{"another source's ref", cfg, appwire.ThreadReadParams{Ref: "codex:" + sessionID}},
		{"thread id absent from the index", hubcore.WebConfig{Past: emptyIndex}, appwire.ThreadReadParams{ThreadID: sessionID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			thread, found, err := pastThreadForRead(context.Background(), tc.cfg, tc.params)
			if found || err != nil || !reflect.DeepEqual(thread, appwire.Thread{}) {
				t.Fatalf("pastThreadForRead = (%+v, %v, %v), want the empty not-found miss", thread, found, err)
			}
		})
	}
}

func TestPastThreadTurnsListUsesBoundedSavedTranscript(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	full, ok := requirePastThreadForRead(t, cfg, params)
	if !ok {
		t.Fatal("past thread not found")
	}
	_, cursor := appwire.WindowTurns(full.Turns, params.TurnLimit)
	want := appwire.PageTurns(full.Turns, cursor, 30)

	var projected []int
	restore := apptranscript.InstallReadObserverForTesting(func(stats apptranscript.ReadStats) { projected = append(projected, stats.ProjectedTurns) })
	t.Cleanup(restore)
	got, ok := requirePastThreadTurnsList(t, cfg, appwire.ThreadTurnsListParams{Ref: params.Ref, Cursor: cursor, Limit: 30})
	if !ok {
		t.Fatal("past thread not found")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("bounded saved page differs from full reference")
	}
	if !reflect.DeepEqual(projected, []int{30}) {
		t.Fatalf("saved page used legacy full projection of 200 turns; bounded projection reports = %v, want [30]", projected)
	}
}

func writeHistoricalJobLog(t *testing.T, path string, ts time.Time, jobID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	startedAt := ts.Format(time.RFC3339Nano)
	endedAt := ts.Add(time.Second).Format(time.RFC3339Nano)
	lines := []string{
		`{"kind":"job_started","seq":1,"ts":"` + startedAt + `","job_id":"` + jobID + `","type":"delegate","task":"inspect billing","owner_session_id":"02wMz5Txv1C3Hut0M8GCeB","visible_to_session_id":"02wMz5Txv1C3Hut0M8GCeB","delegate_id":"dlg_A","origin_tool_call_id":"call_delegate","started_at":"` + startedAt + `"}`,
		`{"kind":"job_session_assigned","seq":2,"ts":"` + startedAt + `","job_id":"` + jobID + `","transcript_ref":"local:child"}`,
		`{"kind":"job_finished","seq":3,"ts":"` + endedAt + `","job_id":"` + jobID + `","status":"completed","reason":"exit_zero","ended_at":"` + endedAt + `","output_bytes":42}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStampSessionImageURLsIsTheOnlyAuthorityForTheSHARoute pins the three
// rules the sha-addressed route depends on. Producers (the agent live, the
// transcript projector on reload) mint sha-only descriptors and never a URL;
// this is where the route is decided, once, for both.
func TestStampSessionImageURLsIsTheOnlyAuthorityForTheSHARoute(t *testing.T) {
	sha := strings.Repeat("a", 64)
	turns := []appwire.Turn{{Items: []appwire.ThreadItem{{OutputImages: []appwire.OutputImage{
		{Source: "tool-result", SHA: sha},
		{Source: "read-file", SHA: sha, URL: "/doc/image?session=s&path=shot.png"},
		{Source: "shell-path", Path: "shot.png"},
	}}}}}
	stampSessionImageURLs("proj/one", turns)
	got := turns[0].Items[0].OutputImages
	if got[0].URL != "/s/proj%2Fone/images/"+sha {
		t.Errorf("sha-only descriptor URL=%q, want the escaped sha route", got[0].URL)
	}
	if got[1].URL != "/doc/image?session=s&path=shot.png" {
		t.Errorf("already-routed descriptor URL=%q, want it left alone", got[1].URL)
	}
	if got[2].URL != "" {
		t.Errorf("sha-less descriptor URL=%q, want no route invented for it", got[2].URL)
	}
}

func TestStampSessionImageURLsLeavesDescriptorsAloneWithoutASession(t *testing.T) {
	sha := strings.Repeat("b", 64)
	turns := []appwire.Turn{{Items: []appwire.ThreadItem{{OutputImages: []appwire.OutputImage{{SHA: sha}}}}}}
	stampSessionImageURLs("", turns)
	if url := turns[0].Items[0].OutputImages[0].URL; url != "" {
		t.Fatalf("URL=%q, want no route stamped when the session is unknown", url)
	}
}

// TestStampSessionImageURLsCoversReplayedInputImages pins kata ck8z: a
// replayed user-attached image reaches the wire with only metadata sha/size
// (projectReplayInputImage strips the bytes), and handleSessionImage serves
// exactly that sha back — so the stamping pass must put the fetchable route
// on the item, not leave the client to reconstruct it from metadata.
func TestStampSessionImageURLsCoversReplayedInputImages(t *testing.T) {
	sha := strings.Repeat("d", 64)
	turns := []appwire.Turn{{Items: []appwire.ThreadItem{{Images: []appwire.InputItem{
		{Type: "image", Metadata: map[string]string{"sha": sha, "size": "78"}},
		{Type: "image", URL: "/doc/image?session=s&path=shot.png", Metadata: map[string]string{"sha": sha}},
		{Type: "image", Name: "inline.png"},
	}}}}}
	stampSessionImageURLs("proj/one", turns)
	got := turns[0].Items[0].Images
	if got[0].URL != "/s/proj%2Fone/images/"+sha {
		t.Errorf("sha-metadata image URL=%q, want the escaped sha route", got[0].URL)
	}
	if got[1].URL != "/doc/image?session=s&path=shot.png" {
		t.Errorf("already-routed image URL=%q, want it left alone", got[1].URL)
	}
	if got[2].URL != "" {
		t.Errorf("sha-less image URL=%q, want no route invented for it", got[2].URL)
	}
}

// TestStampThreadImageURLsFallsBackToThreadID mirrors how the file-backed
// enrichment resolves a thread's session: SessionID first, thread ID second.
func TestStampThreadImageURLsFallsBackToThreadID(t *testing.T) {
	sha := strings.Repeat("c", 64)
	thread := stampThreadImageURLs(appwire.Thread{
		ID:    "02wMz5Txv733WHFsVy66SR",
		Turns: []appwire.Turn{{Items: []appwire.ThreadItem{{OutputImages: []appwire.OutputImage{{SHA: sha}}}}}},
	})
	if url := thread.Turns[0].Items[0].OutputImages[0].URL; url != "/s/02wMz5Txv733WHFsVy66SR/images/"+sha {
		t.Fatalf("URL=%q, want the sha route built from the thread id", url)
	}
}
