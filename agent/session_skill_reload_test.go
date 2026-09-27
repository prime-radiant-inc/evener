package agent

// Task 9: reload current sources after compaction and emit the complete
// fallback inventory. These tests drive the REAL machinery end to end — real
// fixture activation through the use_skill tool route, real fold publication
// through the actual compaction transaction (a scripted provider only at the
// external LLM boundary), real metadata persistence — and assert the typed
// lifecycle state plus the next ACTUAL provider request. Expected instruction
// bytes and digests come from the fixtures and captured wire bytes, never from
// the production renderer.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// newReloadSession builds a session whose main model answers through main on
// the "anthropic" boundary and whose compaction side calls (the fold's
// summarizer) answer through cheap on a separate instance, mirroring
// newScriptedSummaryCompactSession. window > 0 stamps a real small context
// window on the main profile; 0 keeps the constructor default. The returned
// adapters expose their captured provider requests.
func newReloadSession(t *testing.T, root string, window int, main, cheap func(llm.Request) llm.Response, opts ...sessionOpt) (*Session, *agenttest.ScriptedAdapter, *agenttest.ScriptedAdapter) {
	t.Helper()
	client := llm.NewClient()
	mainAdapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: main}
	cheapAdapter := &agenttest.ScriptedAdapter{Provider: "reload-cheap", Responder: cheap}
	client.Register(mainAdapter)
	client.Register(cheapAdapter)
	profile := WithCheapModel(newAnthropicProfile("claude-test"), "reload-cheap/sum")
	if window > 0 {
		profile = WithContextWindow(profile, window)
	}
	o := append([]sessionOpt{
		withClient(client),
		withProfile(profile),
		withDir(root),
		withoutGitSnapshot(),
		withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}),
	}, opts...)
	s := newSession(t, o...)
	disableSessionNaming(s)
	return s, mainAdapter, cheapAdapter
}

// reloadSummaryResponder scripts the fold's summarize layer with a fixed
// marker and counts its calls — the count that must not grow when reload
// admission must not trigger a second compaction/model-repair round.
func reloadSummaryResponder(marker string, calls *atomic.Int32) func(llm.Request) llm.Response {
	return func(llm.Request) llm.Response {
		if calls != nil {
			calls.Add(1)
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n" + marker + "\n[END SUMMARY]")}
	}
}

// compactContextCall builds a compact_context tool call requesting a
// reload selection.
func compactContextCall(id string, reloadSkills []string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{"note_to_self": "reload-note", "reload_skills": reloadSkills})
	return llm.ToolCallData{ID: id, Name: "compact_context", Arguments: args, Type: "function"}
}

// writeSkillMDRel writes a SKILL.md under <root>/<rel>/<name>/SKILL.md and
// returns its path — the variant of writeSkillMD used for non-`skills/`
// locations (e.g. the .agents/skills shadow copies these tests plant).
func writeSkillMDRel(t *testing.T, root, rel, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, rel, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	source := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(source, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return source
}

// plantOrdinaryRecord seeds a typed ordinary inventory record for a real
// fixture on disk, computing its identity through the real loader/renderer so
// digests and controls match the file's actual bytes. Preload-only and
// hidden-skill classes cannot be created through the model tool route, so the
// reminder fixtures plant typed state directly — the same seeding the
// publication tests use.
func plantOrdinaryRecord(t *testing.T, s *Session, root, name string, userAuthorized bool) schema.SkillContentIdentity {
	t.Helper()
	source := filepath.Join(root, "skills", name, "SKILL.md")
	descriptor := skill.Descriptor{CatalogName: name, Meta: skill.SkillMeta{Name: name, SkillFile: source, Dir: filepath.Dir(source)}}
	loaded, _, err := skill.Load(descriptor)
	if err != nil {
		t.Fatalf("plantOrdinaryRecord(%s): load fixture: %v", name, err)
	}
	rendered := skill.Render(loaded)
	identity := schema.SkillContentIdentity{
		Name:           name,
		DeclaredName:   loaded.Descriptor.Meta.Name,
		Source:         source,
		FileDigest:     loaded.Digest,
		RenderedDigest: rendered.Digest,
	}
	record := schema.OrdinarySkillActivation{
		Identity:       identity,
		Description:    loaded.Descriptor.Meta.Description,
		Controls:       schema.SkillInvocationControls{DisableModelInvocation: loaded.Descriptor.Controls.DisableModelInvocation, UserInvocable: loaded.Descriptor.Controls.UserInvocable},
		Route:          "model_tool",
		InvocationID:   "skill-op-planted-" + name,
		UserAuthorized: userAuthorized,
	}
	s.mu.Lock()
	entry := s.skillLifecycle.Inventory[name]
	entry.Ordinary = &record
	s.skillLifecycle.Inventory[name] = entry
	s.mu.Unlock()
	return identity
}

// plantPreloadRecord seeds a typed frozen-preload inventory record with
// independent provenance.
func plantPreloadRecord(t *testing.T, s *Session, name, description string) {
	t.Helper()
	record := schema.FrozenSkillPreload{Name: name, Description: description, Source: "preload://" + name, FileDigest: "opaque-preload-" + name, RenderedDigest: "opaque-preload-" + name}
	s.mu.Lock()
	entry := s.skillLifecycle.Inventory[name]
	entry.Preload = &record
	s.skillLifecycle.Inventory[name] = entry
	s.mu.Unlock()
}

// foldWithReloadSelection requests a real forced compaction cycle with the
// given selection and runs the actual round-tail fold, returning the winning
// publication's delivered handoff receipt.
func foldWithReloadSelection(t *testing.T, s *Session, selection schema.SkillReloadSelection) schema.SkillCompactionReceipt {
	t.Helper()
	if _, err := s.requestSkillCompaction(context.Background(), "reload-note", "", selection); err != nil {
		t.Fatalf("requestSkillCompaction: %v", err)
	}
	s.applyPendingForceCompact(context.Background())
	handoffs := pendingHandoffsSnapshot(s)
	if len(handoffs) != 1 || handoffs[0].Phase != skillCompactionReceiptDelivered {
		t.Fatalf("test setup: the forced fold must record exactly one delivered handoff, got %+v", handoffs)
	}
	return handoffs[0]
}

// requestInventoryDocument decodes the complete typed loaded-skill inventory
// document carried by a provider request, or nil when the request carries no
// reminder. The document is a machine contract (JSON entries between opaque
// tags), never natural-language prose.
func requestInventoryDocument(t *testing.T, req llm.Request) []schema.SkillInventorySummary {
	t.Helper()
	const open, closeTag = "<skill-inventory>\n", "\n</skill-inventory>"
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			if part.Kind != llm.ContentText {
				continue
			}
			i := strings.Index(part.Text, open)
			if i < 0 {
				continue
			}
			rest := part.Text[i+len(open):]
			payload, _, terminated := strings.Cut(rest, closeTag)
			if !terminated {
				t.Fatalf("unterminated skill-inventory document in %.120q", part.Text)
			}
			var entries []schema.SkillInventorySummary
			if err := json.Unmarshal([]byte(payload), &entries); err != nil {
				t.Fatalf("decoding skill-inventory document: %v", err)
			}
			return entries
		}
	}
	return nil
}

// reloadOutcomeByInvocation returns the typed outcome recorded for one reload
// invocation, failing the test when none exists.
func reloadOutcomeByInvocation(t *testing.T, s *Session, invocationID string) schema.SkillActivationOutcome {
	t.Helper()
	for _, state := range skillTurnStates(s) {
		for _, outcome := range state.Outcomes {
			if outcome.InvocationID == invocationID {
				return outcome
			}
		}
	}
	t.Fatalf("no typed outcome recorded for invocation %q", invocationID)
	return schema.SkillActivationOutcome{}
}

// reloadOutcomeForSkill returns the typed reload outcome recorded for one
// skill's compaction reload, failing the test when none exists.
func reloadOutcomeForSkill(t *testing.T, s *Session, name string) schema.SkillActivationOutcome {
	t.Helper()
	for _, state := range skillTurnStates(s) {
		for _, outcome := range state.Outcomes {
			if outcome.Identity.Name == name {
				return outcome
			}
		}
	}
	t.Fatalf("no typed reload outcome recorded for skill %q", name)
	return schema.SkillActivationOutcome{}
}

// hasReloadOutcome reports whether any typed turn state records an outcome
// with the given invocation id.
func hasReloadOutcome(s *Session, invocationID string) bool {
	for _, state := range skillTurnStates(s) {
		for _, outcome := range state.Outcomes {
			if outcome.InvocationID == invocationID {
				return true
			}
		}
	}
	return false
}

// requestEnvelopeNames lists the canonical names of every complete skill
// envelope a request carries, in order.
func requestEnvelopeNames(t *testing.T, req llm.Request) []string {
	t.Helper()
	var names []string
	for _, env := range requestSkillEnvelopes(t, req) {
		names = append(names, env.Doc.Name)
	}
	return names
}

// availabilityByName flattens an inventory document for assertions.
func availabilityByName(entries []schema.SkillInventorySummary) map[string]schema.SkillInventorySummary {
	out := make(map[string]schema.SkillInventorySummary, len(entries))
	for _, entry := range entries {
		out[entry.Name] = entry
	}
	return out
}

// TestSkillReload_CurrentSource_ReloadsNewBytes pins the current-source
// contract: after a real activation and a real published compaction whose
// selection names the skill, bytes and flags changed on disk are reloaded
// through the recorded source, and the next actual provider request carries
// the new complete opaque instructions — with the old/new digests and both
// flag values recorded in the typed outcome.
func TestSkillReload_CurrentSource_ReloadsNewBytes(t *testing.T) {
	t.Parallel()
	root := skillFixtureRoot(t)
	markGitRoot(t, root)
	oldBody := strings.Repeat("BODY_7f2a old source line\n", 64)
	newBody := strings.Repeat("BODY_9c1e new source line\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+oldBody)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	calls := 0
	s, main, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		calls++
		if calls == 1 {
			return toolCallResponse(useSkillCall("skill-1", "opaque"))
		}
		return toolCallResponse(communicateCall("done-1", "ok"))
	}, reloadSummaryResponder("SUMMARY_reload_1", nil))
	seedNumberedSessionHistory(t, s, 12)
	s.contextMgr.PreserveRecentTurns = 2
	evs, stop := captureEvents(s)

	// Turn 1 activates the real fixture through the model tool route.
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	before := lifecycleInventory(s)["opaque"].Ordinary
	if before == nil {
		t.Fatal("test setup: the real activation recorded no inventory")
	}
	if before.Controls != (schema.SkillInvocationControls{DisableModelInvocation: false, UserInvocable: true}) {
		t.Fatalf("test setup: fixture flags = %+v, want the defaults", before.Controls)
	}

	// A real published compaction selects the skill for reload and drops the
	// activation's carrier from the folded prefix.
	handoff := foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}})

	// The source changes on disk — new bytes and a new user-invocable flag —
	// between the compaction and the next request.
	newFile := "---\nname: opaque\ndescription: fixture\nuser-invocable: false\n---\n" + newBody
	if err := os.WriteFile(source, []byte(newFile), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	if _, err := s.ProcessInput(context.Background(), "REQUEST_again", nil); err != nil {
		t.Fatal(err)
	}
	stop()

	requests := main.Requests()
	if len(requests) != 3 {
		t.Fatalf("requests=%d, want 3 (activation round, commit round, reload round)", len(requests))
	}
	// The next actual provider request carries the NEW complete body, and no
	// stale envelope survived the fold.
	env := requireSingleEnvelope(t, requests[2], "opaque", newBody, source)
	newRenderedDigest := sha256.Sum256([]byte(env.Raw))
	fileBytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	newFileDigest := sha256.Sum256(fileBytes)

	invocationID := handoff.Operation.PublicationID + ":opaque"
	outcome := reloadOutcomeByInvocation(t, s, invocationID)
	if outcome.Status != "pending" {
		t.Fatalf("reload outcome status = %q, want pending (provisional until final admission)", outcome.Status)
	}
	if outcome.Identity.RenderedDigest != hex.EncodeToString(newRenderedDigest[:]) {
		t.Fatalf("outcome new rendered digest = %q, want the captured envelope's", outcome.Identity.RenderedDigest)
	}
	if outcome.Identity.FileDigest != hex.EncodeToString(newFileDigest[:]) {
		t.Fatalf("outcome new file digest = %q, want the fixture bytes' digest", outcome.Identity.FileDigest)
	}
	if outcome.PreviousIdentity == nil || outcome.PreviousIdentity.RenderedDigest != before.Identity.RenderedDigest ||
		outcome.PreviousIdentity.FileDigest != before.Identity.FileDigest {
		t.Fatalf("outcome previous identity = %+v, want the activation's recorded digests", outcome.PreviousIdentity)
	}
	if outcome.PreviousControls == nil || *outcome.PreviousControls != before.Controls {
		t.Fatalf("outcome previous controls = %+v, want the activation's recorded flags", outcome.PreviousControls)
	}
	if outcome.Activation == nil || outcome.Activation.Controls != (schema.SkillInvocationControls{DisableModelInvocation: false, UserInvocable: false}) {
		t.Fatalf("outcome new controls = %+v, want user-invocable false from the changed source", outcome.Activation)
	}
	if outcome.Activation != nil && outcome.Activation.Route != "compaction_reload" {
		t.Fatalf("reload activation route = %q, want compaction_reload", outcome.Activation.Route)
	}
	// The final admission advanced the inventory to the new content while
	// preserving the activation's unauthorized provenance.
	after := lifecycleInventory(s)["opaque"].Ordinary
	if after == nil || after.Identity.RenderedDigest != hex.EncodeToString(newRenderedDigest[:]) || after.UserAuthorized {
		t.Fatalf("inventory after reload = %+v, want the new content with unauthorized provenance", after)
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("delivered handoff must be consumed after durable admission, got %+v", handoffs)
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("obligations outstanding after admission: %+v", got)
	}
	if names := skillActivatedEventNames(*evs); len(names) != 2 {
		t.Fatalf("activation events = %v, want one for the original body and one for the genuinely new reload body", names)
	}
}

// TestSkillReload_CurrentSource_MissingReplacedSource pins the no-retargeting
// contract: a selected reload whose recorded source vanished — or was replaced
// by a file declaring a different identity — fails explicitly with a typed
// outcome, never silently retargets to another source, and never erases the
// earlier successful inventory record.
func TestSkillReload_CurrentSource_MissingReplacedSource(t *testing.T) {
	t.Run("deleted source fails without retargeting", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		body := strings.Repeat("BODY_7f2a original source\n", 64)
		writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
		source := filepath.Join(root, "skills", "opaque", "SKILL.md")
		calls := 0
		s, main, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
			calls++
			if calls == 1 {
				return toolCallResponse(useSkillCall("skill-1", "opaque"))
			}
			return toolCallResponse(communicateCall("done-1", "ok"))
		}, reloadSummaryResponder("SUMMARY_reload_2", nil))
		seedNumberedSessionHistory(t, s, 12)
		s.contextMgr.PreserveRecentTurns = 2
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
			t.Fatal(err)
		}
		before := lifecycleInventory(s)["opaque"].Ordinary
		if before == nil {
			t.Fatal("test setup: the real activation recorded no inventory")
		}
		handoff := foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}})
		// A replacement skill with the same name appears elsewhere while the
		// recorded source is deleted: the reload must fail on its own source,
		// never adopt the replacement.
		writeSkillMDRel(t, root, filepath.Join(".agents", "skills"), "opaque",
			"---\nname: opaque\ndescription: replacement\n---\n"+strings.Repeat("REPLACEMENT_a19f\n", 64))
		if err := os.Remove(source); err != nil {
			t.Fatalf("remove source: %v", err)
		}
		if _, err := s.ProcessInput(context.Background(), "REQUEST_again", nil); err != nil {
			t.Fatal(err)
		}
		stop()

		requests := main.Requests()
		last := requests[len(requests)-1]
		for _, env := range requestSkillEnvelopes(t, last) {
			if strings.Contains(env.Raw, "REPLACEMENT_a19f") {
				t.Fatal("the reload retargeted to the replacement source")
			}
		}
		if envs := requestSkillEnvelopes(t, last); len(envs) != 0 {
			t.Fatalf("deleted source delivered %d envelopes", len(envs))
		}
		outcome := reloadOutcomeByInvocation(t, s, handoff.Operation.PublicationID+":opaque")
		if outcome.Status != "failed" || outcome.ErrorCode != "source_missing" {
			t.Fatalf("outcome = status %q code %q, want failed source_missing", outcome.Status, outcome.ErrorCode)
		}
		if after := lifecycleInventory(s)["opaque"].Ordinary; after == nil || *after != *before {
			t.Fatalf("failed reload changed the earlier record: before=%+v after=%+v", before, after)
		}
		if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
			t.Fatalf("the failed reload's receipt must be consumed, got %+v", handoffs)
		}
		if got := lifecycleObligations(s); len(got) != 0 {
			t.Fatalf("failed reload left obligations: %+v", got)
		}
		if names := skillActivatedEventNames(*evs); len(names) != 1 {
			t.Fatalf("activation events = %v, want exactly the first delivery", names)
		}
	})

	t.Run("replaced identity fails without retargeting", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		markGitRoot(t, root)
		body := strings.Repeat("BODY_7f2a original source\n", 64)
		writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
		source := filepath.Join(root, "skills", "opaque", "SKILL.md")
		calls := 0
		s, main, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
			calls++
			if calls == 1 {
				return toolCallResponse(useSkillCall("skill-1", "opaque"))
			}
			return toolCallResponse(communicateCall("done-1", "ok"))
		}, reloadSummaryResponder("SUMMARY_reload_3", nil))
		seedNumberedSessionHistory(t, s, 12)
		s.contextMgr.PreserveRecentTurns = 2
		evs, stop := captureEvents(s)
		if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
			t.Fatal(err)
		}
		before := lifecycleInventory(s)["opaque"].Ordinary
		if before == nil {
			t.Fatal("test setup: the real activation recorded no inventory")
		}
		handoff := foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}})
		// The recorded source file is replaced by one declaring a different
		// skill identity at the same path.
		replaced := "---\nname: renamed_opaque\ndescription: fixture\n---\n" + body
		if err := os.WriteFile(source, []byte(replaced), 0o644); err != nil {
			t.Fatalf("rewrite source: %v", err)
		}
		if _, err := s.ProcessInput(context.Background(), "REQUEST_again", nil); err != nil {
			t.Fatal(err)
		}
		stop()

		last := main.Requests()[len(main.Requests())-1]
		if envs := requestSkillEnvelopes(t, last); len(envs) != 0 {
			t.Fatalf("replaced source delivered %d envelopes", len(envs))
		}
		outcome := reloadOutcomeByInvocation(t, s, handoff.Operation.PublicationID+":opaque")
		if outcome.Status != "failed" || outcome.ErrorCode != "source_changed" {
			t.Fatalf("outcome = status %q code %q, want failed source_changed", outcome.Status, outcome.ErrorCode)
		}
		if after := lifecycleInventory(s)["opaque"].Ordinary; after == nil || *after != *before {
			t.Fatalf("failed reload changed the earlier record: before=%+v after=%+v", before, after)
		}
		if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
			t.Fatalf("the failed reload's receipt must be consumed, got %+v", handoffs)
		}
		if names := skillActivatedEventNames(*evs); len(names) != 1 {
			t.Fatalf("activation events = %v, want exactly the first delivery", names)
		}
	})
}

// TestSkillReload_Reminder_AbsentSelection pins the complete fallback
// inventory: a compaction handoff with no selection — even when no accepted
// operation owns the fold — delivers one typed metadata notification listing
// EVERY loaded-skill inventory entry with its availability classification, and
// never enqueues any instruction bodies.
func TestSkillReload_Reminder_AbsentSelection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	const frontmatter = "---\nname: %s\ndescription: %s fixture\n%s---\n%s"
	writeSkillMD(t, root, "plain", fmt.Sprintf(frontmatter, "plain", "plain", "", strings.Repeat("PLAIN_5c31\n", 8)))
	writeSkillMD(t, root, "authorized", fmt.Sprintf(frontmatter, "authorized", "authorized", "disable-model-invocation: true\n", strings.Repeat("AUTHORIZED_5c31\n", 8)))
	writeSkillMD(t, root, "hidden", fmt.Sprintf(frontmatter, "hidden", "hidden", "disable-model-invocation: true\n", strings.Repeat("HIDDEN_5c31\n", 8)))
	writeSkillMD(t, root, "blocked", fmt.Sprintf(frontmatter, "blocked", "blocked", "disable-model-invocation: true\nuser-invocable: false\n", strings.Repeat("BLOCKED_5c31\n", 8)))
	writeSkillMD(t, root, "dual", fmt.Sprintf(frontmatter, "dual", "dual", "", strings.Repeat("DUAL_5c31\n", 8)))
	writeSkillMD(t, root, "gone", fmt.Sprintf(frontmatter, "gone", "gone", "", strings.Repeat("GONE_5c31\n", 8)))

	s, main, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return toolCallResponse(communicateCall("done-1", "ok"))
	}, reloadSummaryResponder("SUMMARY_reload_4", nil))
	seedNumberedSessionHistory(t, s, 12)
	plantOrdinaryRecord(t, s, root, "plain", false)
	plantOrdinaryRecord(t, s, root, "authorized", true) // authorized same-source continuation reloads regardless of flags
	plantOrdinaryRecord(t, s, root, "hidden", false)    // hidden but user-invocable
	plantOrdinaryRecord(t, s, root, "blocked", false)   // both flags block ordinary routes
	plantOrdinaryRecord(t, s, root, "dual", false)
	plantPreloadRecord(t, s, "dual", "dual preload provenance")
	plantPreloadRecord(t, s, "frozen", "permanent preload")
	plantOrdinaryRecord(t, s, root, "gone", false)
	if err := os.Remove(filepath.Join(root, "skills", "gone", "SKILL.md")); err != nil {
		t.Fatalf("remove gone source: %v", err)
	}
	evs, stop := captureEvents(s)

	foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "absent"})
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	stop()

	requests := main.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests=%d, want exactly the reload round", len(requests))
	}
	entries := requestInventoryDocument(t, requests[0])
	if entries == nil {
		t.Fatal("the next provider request carries no skill-inventory document")
	}
	byName := availabilityByName(entries)
	want := map[string]struct {
		availability string
		hasOrdinary  bool
		hasPreload   bool
	}{
		"plain":      {"reloadable", true, false},
		"authorized": {"reloadable", true, false},
		"hidden":     {"requires_user_activation", true, false},
		"blocked":    {"unavailable", true, false},
		"dual":       {"reloadable", true, true},
		"frozen":     {"permanent", false, true},
		"gone":       {"unavailable", true, false},
	}
	if len(entries) != len(want) {
		t.Fatalf("inventory document lists %d entries (%v), want every one of the %d inventory names", len(entries), availabilityNames(entries), len(want))
	}
	for name, w := range want {
		got, ok := byName[name]
		if !ok {
			t.Fatalf("inventory document omits loaded skill %q (have %v)", name, availabilityNames(entries))
		}
		if got.Availability != w.availability || got.HasOrdinary != w.hasOrdinary || got.HasPreload != w.hasPreload {
			t.Fatalf("inventory entry %q = availability %q (ordinary=%v preload=%v), want %q (ordinary=%v preload=%v)",
				name, got.Availability, got.HasOrdinary, got.HasPreload, w.availability, w.hasOrdinary, w.hasPreload)
		}
	}
	if envs := requestSkillEnvelopes(t, requests[0]); len(envs) != 0 {
		t.Fatalf("the reminder enqueued %d instruction bodies", len(envs))
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("the reminder's receipt must be consumed, got %+v", handoffs)
	}
	if names := skillActivatedEventNames(*evs); len(names) != 0 {
		t.Fatalf("the reminder emitted activation events: %v", names)
	}
	// The typed classification surface itself: the deleted source produces a
	// diagnostic, the intact ones do not.
	summary, diagnostics := s.skillInventorySummary(context.Background())
	if len(summary) != len(want) {
		t.Fatalf("skillInventorySummary lists %d entries, want %d", len(summary), len(want))
	}
	if len(diagnostics) != 1 || diagnostics[0].Name != "gone" {
		t.Fatalf("diagnostics = %+v, want exactly the deleted source's", diagnostics)
	}
}

func availabilityNames(entries []schema.SkillInventorySummary) []string {
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

// TestSkillReload_Reminder_InvalidSelection pins that an invalid selection —
// like an absent one — authorizes no reload and delivers the complete
// reminder instead.
func TestSkillReload_Reminder_InvalidSelection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "plain", "---\nname: plain\ndescription: plain fixture\n---\n"+strings.Repeat("PLAIN_5c31\n", 8))
	s, main, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return toolCallResponse(communicateCall("done-1", "ok"))
	}, reloadSummaryResponder("SUMMARY_reload_5", nil))
	seedNumberedSessionHistory(t, s, 12)
	plantOrdinaryRecord(t, s, root, "plain", false)
	evs, stop := captureEvents(s)

	foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "invalid", ErrorCode: "unknown_skill"})
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	stop()

	requests := main.Requests()
	entries := requestInventoryDocument(t, requests[len(requests)-1])
	if entries == nil || len(entries) != 1 || entries[0].Name != "plain" || entries[0].Availability != "reloadable" {
		t.Fatalf("invalid-selection reminder = %+v, want the complete single-entry inventory", entries)
	}
	if envs := requestSkillEnvelopes(t, requests[len(requests)-1]); len(envs) != 0 {
		t.Fatalf("an invalid selection authorized %d body loads", len(envs))
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("the reminder's receipt must be consumed, got %+v", handoffs)
	}
	if names := skillActivatedEventNames(*evs); len(names) != 0 {
		t.Fatalf("the reminder emitted activation events: %v", names)
	}
}

// TestSkillReload_Reminder_ExplicitEmptySelection pins that an explicit empty
// selection means "reload none" — no reminder, no bodies, receipt consumed.
func TestSkillReload_Reminder_ExplicitEmptySelection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "plain", "---\nname: plain\ndescription: plain fixture\n---\n"+strings.Repeat("PLAIN_5c31\n", 8))
	s, main, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return toolCallResponse(communicateCall("done-1", "ok"))
	}, reloadSummaryResponder("SUMMARY_reload_6", nil))
	seedNumberedSessionHistory(t, s, 12)
	plantOrdinaryRecord(t, s, root, "plain", false)

	foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "valid", Names: []string{}})
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}

	requests := main.Requests()
	last := requests[len(requests)-1]
	if entries := requestInventoryDocument(t, last); entries != nil {
		t.Fatalf("an explicit empty selection must not remind, got %+v", entries)
	}
	if envs := requestSkillEnvelopes(t, last); len(envs) != 0 {
		t.Fatalf("an explicit empty selection enqueued %d bodies", len(envs))
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("the empty selection's receipt must be consumed, got %+v", handoffs)
	}
}

// TestSkillReload_Budget_OrderedPriority pins the budget contract with a real
// small-window profile and the actual token estimator: a new explicit
// activation is admitted first, selected reload A fits the remaining budget,
// reload B exceeds it and fails individually with a typed context_budget
// outcome — and no second compaction/model-repair round runs.
func TestSkillReload_Budget_OrderedPriority(t *testing.T) {
	t.Parallel()
	root := skillFixtureRoot(t)
	markGitRoot(t, root)
	newactBody := strings.Repeat("NEWACT_5c31 explicit activation line\n", 50)
	reloadABody := strings.Repeat("RELOAD_A_5c31 fits line\n", 120)
	reloadBBody := strings.Repeat("RELOAD_B_5c31 exceeds line\n", 6000)
	writeSkillMD(t, root, "newact", "---\nname: newact\ndescription: new activation\n---\n"+newactBody)
	writeSkillMD(t, root, "skA", "---\nname: skA\ndescription: reload A\n---\n"+reloadABody)
	writeSkillMD(t, root, "skB", "---\nname: skB\ndescription: reload B\n---\n"+reloadBBody)
	newactSource := filepath.Join(root, "skills", "newact", "SKILL.md")
	skASource := filepath.Join(root, "skills", "skA", "SKILL.md")

	var summarizeCalls atomic.Int32
	calls := 0
	// A real window with measured headroom: the session's mandatory content
	// (tool definitions + system prompt ≈ 22K tokens by the actual estimator)
	// plus the new activation fit, reload A fits the remainder, reload B
	// (≈21K tokens) does not. Pressure stays below the elicitation threshold.
	s, main, _ := newReloadSession(t, root, 40000, func(llm.Request) llm.Response {
		calls++
		switch calls {
		case 1:
			return toolCallResponse(useSkillCall("skill-1", "newact"))
		case 2:
			return toolCallResponse(compactContextCall("compact-1", []string{"skA", "skB"}))
		default:
			return toolCallResponse(communicateCall("done-1", "ok"))
		}
	}, reloadSummaryResponder("SUMMARY_reload_7", &summarizeCalls))
	seedNumberedSessionHistory(t, s, 12)
	plantOrdinaryRecord(t, s, root, "skA", false)
	plantOrdinaryRecord(t, s, root, "skB", false)
	evs, stop := captureEvents(s)

	// One turn: activate newact, request the compaction with the selection
	// (the real round-tail fold publishes it), then finish.
	_, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil)
	stop()
	if err != nil {
		t.Fatalf("the budgeted reload turn failed: %v", err)
	}
	if n := summarizeCalls.Load(); n != 1 {
		t.Fatalf("the summarizer ran %d time(s), want exactly the compaction's own call — an over-budget reload must fail individually, never start a second compaction/model-repair round", n)
	}

	requests := main.Requests()
	if len(requests) != 3 {
		t.Fatalf("requests=%d, want 3 (activation, compaction request, reload dispatch)", len(requests))
	}
	final := requests[2]
	envs := requestSkillEnvelopes(t, final)
	if len(envs) != 2 {
		t.Fatalf("final dispatch carries %d skill envelopes, want the new activation's and reload A's bodies", len(envs))
	}
	var sawNewact, sawA bool
	for _, env := range envs {
		switch env.Doc.Name {
		case "newact":
			sawNewact = true
			if env.Doc.Instructions != newactBody || env.Doc.Source != newactSource {
				t.Fatalf("newact envelope source = %q, want the complete body from %q", env.Doc.Source, newactSource)
			}
		case "skA":
			sawA = true
			if env.Doc.Instructions != reloadABody || env.Doc.Source != skASource {
				t.Fatalf("reload A envelope is not the complete body from %q", skASource)
			}
		case "skB":
			t.Fatal("over-budget reload B was delivered as a body")
		}
	}
	if !sawNewact || !sawA {
		t.Fatalf("final dispatch envelopes = %v, want newact and skA", requestEnvelopeNames(t, final))
	}

	// The receipt was consumed by its durable reload admission within the turn.
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("delivered handoffs after the turn = %+v, want the consumed receipt", handoffs)
	}
	// The reload invocations carry their publication identity: fold-<rev>:<name>.
	outcomeA := reloadOutcomeForSkill(t, s, "skA")
	if outcomeA.Status != "pending" {
		t.Fatalf("reload A outcome status = %q, want pending", outcomeA.Status)
	}
	if !strings.HasPrefix(outcomeA.InvocationID, "fold-") || !strings.HasSuffix(outcomeA.InvocationID, ":skA") {
		t.Fatalf("reload A invocation id = %q, want the publication identity plus name", outcomeA.InvocationID)
	}
	outcomeB := reloadOutcomeForSkill(t, s, "skB")
	if outcomeB.Status != "failed" || outcomeB.ErrorCode != "context_budget" {
		t.Fatalf("reload B outcome = status %q code %q, want failed context_budget", outcomeB.Status, outcomeB.ErrorCode)
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("obligations outstanding after admission: %+v", got)
	}
	// skA's reloaded content is identical to its recorded inventory identity, so
	// its admission is not a new-body event; only the explicit activation fired.
	if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "newact" {
		t.Fatalf("activation events = %v, want exactly the new explicit activation's", names)
	}
}

// TestSkillReload_Budget_MandatoryMetadataExceeds pins the metadata-budget
// contract: when the current input plus the COMPLETE post-compaction inventory
// metadata cannot fit the window, the dispatch fails visibly with the full
// list kept — never a trimmed reminder — and no recovery compaction runs.
func TestSkillReload_Budget_MandatoryMetadataExceeds(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	var summarizeCalls atomic.Int32
	// The window admits the current input alone (≈23K tokens mandatory) but no
	// possible version of the complete reminder metadata below.
	s, main, _ := newReloadSession(t, root, 40000, func(llm.Request) llm.Response {
		return toolCallResponse(communicateCall("done-1", "ok"))
	}, reloadSummaryResponder("SUMMARY_reload_8", &summarizeCalls))
	seedNumberedSessionHistory(t, s, 12)
	// A large preload-only inventory: the complete reminder metadata alone is
	// far larger than the window's remaining headroom.
	for i := range 2000 {
		plantPreloadRecord(t, s, fmt.Sprintf("bulk%04d", i), "bulk fixture description")
	}
	evs, stop := captureEvents(s)

	foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "absent"})
	_, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil)
	stop()
	if err == nil && !hasEventKind(*evs, events.EventError) {
		t.Fatal("metadata-budget exhaustion must fail the dispatch visibly")
	}
	// No second compaction/model-repair round: only the fold's own call ran.
	if n := summarizeCalls.Load(); n != 1 {
		t.Fatalf("the summarizer ran %d time(s), want exactly the fold's own call", n)
	}
	// The receipt is retained — nothing was consumed or trimmed to fit.
	handoffs := pendingHandoffsSnapshot(s)
	if len(handoffs) != 1 || handoffs[0].Phase != skillCompactionReceiptDelivered {
		t.Fatalf("handoffs after the failed dispatch = %+v, want the unconsumed delivered receipt", handoffs)
	}
	for _, req := range main.Requests() {
		if envs := requestSkillEnvelopes(t, req); len(envs) != 0 {
			t.Fatalf("a failed metadata dispatch delivered %d bodies", len(envs))
		}
	}
}

// TestSkillReload_Budget_PreparedReminderCountsAgainstAdmission pins the first
// missing term in the admission budget: the tokens of the turns preparation
// already appended. A reminder receipt and a reload receipt are staged in the
// same round; a budget that admits the body when the reminder's own tokens are
// ignored must reject it once they count, with the existing typed
// context_budget outcome and its visible explanation — not an admission that
// overflows the rebuilt request after the receipts were already consumed. The
// control run proves the rejection is attributable to the reminder: the same
// body, prepared alone, fits and is admitted.
func TestSkillReload_Budget_PreparedReminderCountsAgainstAdmission(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("TIPPED_5c31 reload body line\n", 80)
	writeSkillMD(t, root, "tipped", "---\nname: tipped\ndescription: fixture\n---\n"+body)
	// prepare builds a fresh session whose pending handoffs are the given
	// reminder receipt (optionally) plus one valid selection of the fixture,
	// prepares them through the real machinery, and reports what preparation
	// staged.
	prepare := func(t *testing.T, withReminder bool) (*Session, *skillActivationBatch, []schema.SkillActivationOutcome, int) {
		t.Helper()
		s, _, _ := newReloadSession(t, root, 100000, func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("unused")}
		}, reloadSummaryResponder("SUMMARY_staged_reminder", nil))
		seedNumberedSessionHistory(t, s, 12)
		plantOrdinaryRecord(t, s, root, "tipped", true)
		// A large preload-only inventory makes the reminder's own tokens
		// unambiguous against the body's headroom.
		for i := range 40 {
			plantPreloadRecord(t, s, fmt.Sprintf("bulk%02d", i), "bulk fixture description")
		}
		var receipts []schema.SkillCompactionReceipt
		if withReminder {
			receipts = append(receipts, schema.SkillCompactionReceipt{
				Phase: skillCompactionReceiptDelivered,
				Operation: schema.SkillCompactionOperation{
					Generation:    1,
					Selection:     schema.SkillReloadSelection{State: "absent"},
					PublicationID: "pub-staged-reminder",
				},
			})
		}
		receipts = append(receipts, schema.SkillCompactionReceipt{
			Phase: skillCompactionReceiptDelivered,
			Operation: schema.SkillCompactionOperation{
				Generation:    2,
				Selection:     schema.SkillReloadSelection{State: "valid", Names: []string{"tipped"}},
				PublicationID: "pub-staged-body",
			},
		})
		s.mu.Lock()
		s.skillLifecycle.PendingHandoffs = receipts
		s.mu.Unlock()
		batch, outcomes, staged, err := s.prepareCompactedSkillReloads(context.Background())
		if err != nil {
			t.Fatalf("prepareCompactedSkillReloads (reminder=%v): %v", withReminder, err)
		}
		return s, batch, outcomes, staged
	}
	bodyTokens := func(t *testing.T, batch *skillActivationBatch) int {
		t.Helper()
		if batch == nil || len(batch.Items) != 1 {
			t.Fatalf("prepared batch = %+v, want the one selected reload", batch)
		}
		return llm.EstimateMessagesInputTokens([]llm.Message{llm.User(batch.Items[0].Rendered.Content)}).Tokens
	}
	s, batch, outcomes, staged := prepare(t, true)
	if staged <= 0 {
		t.Fatalf("the reminder turn was appended but preparation reported %d staged input tokens", staged)
	}
	window := s.profile.ContextWindowSize()
	// One token short of fitting the body on its own, so the reminder's staged
	// tokens are the only thing that can cross the window.
	budget := &llm.TokenBudget{InputTokens: window - bodyTokens(t, batch) - 2}
	if err := s.admitCompactedSkillReloads(context.Background(), s.profile, budget, staged, batch, outcomes); err != nil {
		t.Fatalf("admitCompactedSkillReloads: %v", err)
	}
	rejected := reloadOutcomeForSkill(t, s, "tipped")
	if rejected.Status != "failed" || rejected.ErrorCode != "context_budget" {
		t.Fatalf("reload outcome = status %q code %q, want failed context_budget: the staged reminder's tokens must count against the body's admission", rejected.Status, rejected.ErrorCode)
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("handoffs after admission = %+v, want both consumed", handoffs)
	}
	control, controlBatch, controlOutcomes, controlStaged := prepare(t, false)
	if controlStaged != 0 {
		t.Fatalf("a valid-selection preparation staged %d tokens, want none", controlStaged)
	}
	controlWindow := control.profile.ContextWindowSize()
	controlBudget := &llm.TokenBudget{InputTokens: controlWindow - bodyTokens(t, controlBatch) - 2}
	if err := control.admitCompactedSkillReloads(context.Background(), control.profile, controlBudget, controlStaged, controlBatch, controlOutcomes); err != nil {
		t.Fatalf("admitCompactedSkillReloads (control): %v", err)
	}
	admitted := reloadOutcomeForSkill(t, control, "tipped")
	if admitted.Status != "pending" {
		t.Fatalf("control reload outcome = status %q code %q, want pending: the same body must fit when no reminder precedes it", admitted.Status, admitted.ErrorCode)
	}
}

// TestSkillReload_Budget_RejectionExplanationCountsAgainstLaterBodies pins the
// second missing term: the tokens of the notification admission appends for a
// body it rejects. The first body is far larger than the window and is rejected
// with a visible context_budget explanation; that explanation's own tokens must
// enter the running total, so the second body — which fits when the explanation
// is ignored — is rejected too, instead of being admitted and then failing the
// whole turn after the receipts were consumed.
func TestSkillReload_Budget_RejectionExplanationCountsAgainstLaterBodies(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	hugeBody := strings.Repeat("HUGE_5c31 body line\n", 6000)
	laterBody := strings.Repeat("LATER_5c31 body line\n", 60)
	writeSkillMD(t, root, "huge", "---\nname: huge\ndescription: fixture\n---\n"+hugeBody)
	writeSkillMD(t, root, "later", "---\nname: later\ndescription: fixture\n---\n"+laterBody)
	s, _, _ := newReloadSession(t, root, 100000, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_explanation", nil))
	seedNumberedSessionHistory(t, s, 12)
	plantOrdinaryRecord(t, s, root, "huge", true)
	plantOrdinaryRecord(t, s, root, "later", true)
	s.mu.Lock()
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{{
		Phase: skillCompactionReceiptDelivered,
		Operation: schema.SkillCompactionOperation{
			Generation:    1,
			Selection:     schema.SkillReloadSelection{State: "valid", Names: []string{"huge", "later"}},
			PublicationID: "pub-explanation",
		},
	}}
	s.mu.Unlock()
	batch, outcomes, staged, err := s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads: %v", err)
	}
	if staged != 0 {
		t.Fatalf("a valid-selection preparation staged %d tokens, want none", staged)
	}
	if batch == nil || len(batch.Items) != 2 {
		t.Fatalf("prepared batch = %+v, want both selected reloads", batch)
	}
	laterTokens := 0
	for _, item := range batch.Items {
		if skillContentIdentity(item).Name == "later" {
			laterTokens = llm.EstimateMessagesInputTokens([]llm.Message{llm.User(item.Rendered.Content)}).Tokens
		}
	}
	if laterTokens <= 0 {
		t.Fatal("test setup: no later body prepared")
	}
	window := s.profile.ContextWindowSize()
	// One token short of fitting the later body on its own: only the huge
	// body's rejection explanation can push it over.
	budget := &llm.TokenBudget{InputTokens: window - laterTokens - 2}
	if err := s.admitCompactedSkillReloads(context.Background(), s.profile, budget, staged, batch, outcomes); err != nil {
		t.Fatalf("admitCompactedSkillReloads: %v", err)
	}
	if got := reloadOutcomeForSkill(t, s, "huge"); got.Status != "failed" || got.ErrorCode != "context_budget" {
		t.Fatalf("huge reload outcome = status %q code %q, want failed context_budget", got.Status, got.ErrorCode)
	}
	if got := reloadOutcomeForSkill(t, s, "later"); got.Status != "failed" || got.ErrorCode != "context_budget" {
		t.Fatalf("later reload outcome = status %q code %q, want failed context_budget: the earlier rejection's explanation tokens must count against the later body's admission", got.Status, got.ErrorCode)
	}
}

// TestSkillReload_Budget_StagedNotificationsStayWithinWindow pins the combined
// invariant: after an admission that appends notifications, the running total
// (preparation's staged turns + every body and notification admission recorded)
// still leaves the complete staged request inside the window, so the rebuild
// after admission cannot fail the whole turn. The budget is one token short of
// the second body, so the first body's rejection explanation must both count
// against the second body and still leave the total under the window.
func TestSkillReload_Budget_StagedNotificationsStayWithinWindow(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	hugeBody := strings.Repeat("HUGE_5c31 body line\n", 6000)
	smallBody := strings.Repeat("SMALL_5c31 body line\n", 60)
	writeSkillMD(t, root, "huge", "---\nname: huge\ndescription: fixture\n---\n"+hugeBody)
	writeSkillMD(t, root, "small", "---\nname: small\ndescription: fixture\n---\n"+smallBody)
	s, _, _ := newReloadSession(t, root, 100000, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_within_window", nil))
	seedNumberedSessionHistory(t, s, 12)
	plantOrdinaryRecord(t, s, root, "huge", true)
	plantOrdinaryRecord(t, s, root, "small", true)
	for i := range 40 {
		plantPreloadRecord(t, s, fmt.Sprintf("bulk%02d", i), "bulk fixture description")
	}
	s.mu.Lock()
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{
		{
			Phase: skillCompactionReceiptDelivered,
			Operation: schema.SkillCompactionOperation{
				Generation:    1,
				Selection:     schema.SkillReloadSelection{State: "absent"},
				PublicationID: "pub-within-reminder",
			},
		},
		{
			Phase: skillCompactionReceiptDelivered,
			Operation: schema.SkillCompactionOperation{
				Generation:    2,
				Selection:     schema.SkillReloadSelection{State: "valid", Names: []string{"huge", "small"}},
				PublicationID: "pub-within-body",
			},
		},
	}
	s.mu.Unlock()
	batch, outcomes, staged, err := s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads: %v", err)
	}
	if staged <= 0 {
		t.Fatalf("the reminder turn was appended but preparation reported %d staged input tokens", staged)
	}
	if batch == nil || len(batch.Items) != 2 {
		t.Fatalf("prepared batch = %+v, want both selected reloads", batch)
	}
	smallTokens := 0
	for _, item := range batch.Items {
		if skillContentIdentity(item).Name == "small" {
			smallTokens = llm.EstimateMessagesInputTokens([]llm.Message{llm.User(item.Rendered.Content)}).Tokens
		}
	}
	if smallTokens <= 0 {
		t.Fatal("test setup: no small body prepared")
	}
	window := s.profile.ContextWindowSize()
	base := window - staged - smallTokens - 200
	budget := &llm.TokenBudget{InputTokens: base}
	if err := s.admitCompactedSkillReloads(context.Background(), s.profile, budget, staged, batch, outcomes); err != nil {
		t.Fatalf("admitCompactedSkillReloads: %v", err)
	}
	if got := reloadOutcomeForSkill(t, s, "huge"); got.Status != "failed" || got.ErrorCode != "context_budget" {
		t.Fatalf("huge reload outcome = status %q code %q, want failed context_budget", got.Status, got.ErrorCode)
	}
	if got := reloadOutcomeForSkill(t, s, "small"); got.Status != "pending" {
		t.Fatalf("small reload outcome = status %q code %q, want pending: the second body fits under the reserved window", got.Status, got.ErrorCode)
	}
	if want := base + staged + smallTokens; budget.InputTokens <= want {
		t.Fatalf("running total after admission = %d, want more than %d: the rejection explanation's tokens must count against the same budget", budget.InputTokens, want)
	}
	if budget.InputTokens+1 >= window {
		t.Fatalf("running total after admission = %d with rounding at window %d, want the complete staged request inside the window", budget.InputTokens, window)
	}
	carriers := 0
	for _, state := range skillTurnStates(s) {
		if len(state.Obligations) != 0 {
			carriers++
		}
	}
	if carriers != 1 {
		t.Fatalf("admitted %d reload carriers, want exactly the small body's", carriers)
	}
}

// TestSkillReload_Budget_LaterExplanationReservedBeforeEarlierBody pins the
// reservation half of the fix: before a body is admitted, the space owed to the
// notifications admission has not appended yet must already be reserved. Two
// bodies are selected in one receipt in order, so the body that fits alone is
// processed first: "big" fits the window on its own by a single token, while
// "titanic" is far larger than the window and is therefore always rejected with
// a context_budget explanation. Measuring only each body against its own budget
// would admit "big", and its carrier would leave no room for titanic's visible
// explanation — the rebuild after admission would overflow and fail the whole
// turn. The ordering is the point: reserving titanic's explanation up front
// rejects "big" instead, and the complete staged request still fits. Without
// that reservation "big" is admitted, so this test is the one that would catch
// its removal.
func TestSkillReload_Budget_LaterExplanationReservedBeforeEarlierBody(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	bigBody := strings.Repeat("BIG_5c31 reserve line\n", 60)
	titanicBody := strings.Repeat("TITANIC_5c31 line\n", 6000)
	writeSkillMD(t, root, "big", "---\nname: big\ndescription: fixture\n---\n"+bigBody)
	writeSkillMD(t, root, "titanic", "---\nname: titanic\ndescription: fixture\n---\n"+titanicBody)
	s, _, _ := newReloadSession(t, root, 100000, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_reserve", nil))
	seedNumberedSessionHistory(t, s, 12)
	plantOrdinaryRecord(t, s, root, "big", true)
	plantOrdinaryRecord(t, s, root, "titanic", true)
	// The reservation the production check measures is the cost of titanic's
	// notice; it must be a positive number of tokens for the discrimination.
	if reserve := skillReloadNotificationReserve("titanic"); reserve <= 0 {
		t.Fatalf("test setup: titanic's notification reserve = %d, want a positive cost", reserve)
	}
	s.mu.Lock()
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{{
		Phase: skillCompactionReceiptDelivered,
		Operation: schema.SkillCompactionOperation{
			Generation:    1,
			Selection:     schema.SkillReloadSelection{State: "valid", Names: []string{"big", "titanic"}},
			PublicationID: "pub-reserve",
		},
	}}
	s.mu.Unlock()

	batch, outcomes, staged, err := s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads: %v", err)
	}
	if staged != 0 {
		t.Fatalf("a valid-selection preparation staged %d tokens, want none", staged)
	}
	if batch == nil || len(batch.Items) != 2 || skillContentIdentity(batch.Items[0]).Name != "big" {
		t.Fatalf("prepared batch = %+v, want big first and titanic second", batch)
	}
	bigTokens := llm.EstimateMessagesInputTokens([]llm.Message{llm.User(batch.Items[0].Rendered.Content)}).Tokens
	window := s.profile.ContextWindowSize()
	// big fits the window on its own by exactly one token: only the space
	// reserved for titanic's later context_budget explanation can reject it.
	budget := &llm.TokenBudget{InputTokens: window - bigTokens - 2}
	if err := s.admitCompactedSkillReloads(context.Background(), s.profile, budget, staged, batch, outcomes); err != nil {
		t.Fatalf("admitCompactedSkillReloads: %v", err)
	}
	if got := reloadOutcomeForSkill(t, s, "big"); got.Status != "failed" || got.ErrorCode != "context_budget" {
		t.Fatalf("big reload outcome = status %q code %q, want failed context_budget: the earlier body must not consume headroom the later explanation needs", got.Status, got.ErrorCode)
	}
	if got := reloadOutcomeForSkill(t, s, "titanic"); got.Status != "failed" || got.ErrorCode != "context_budget" {
		t.Fatalf("titanic reload outcome = status %q code %q, want failed context_budget", got.Status, got.ErrorCode)
	}
	if budget.InputTokens+1 >= window {
		t.Fatalf("running total after admission = %d with rounding at window %d, want the complete staged request inside the window", budget.InputTokens, window)
	}
}

// TestSkillReload_Preload_OnlySelectionIsNoOp pins the preload rule: a
// selection naming a preload-only inventory entry needs no disk load, admits
// no body, records no outcome, and still consumes its receipt.
func TestSkillReload_Preload_OnlySelectionIsNoOp(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	s, main, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return toolCallResponse(communicateCall("done-1", "ok"))
	}, reloadSummaryResponder("SUMMARY_reload_9", nil))
	seedNumberedSessionHistory(t, s, 12)
	plantPreloadRecord(t, s, "frozen", "permanent preload")

	handoff := foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "valid", Names: []string{"frozen"}})
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}

	requests := main.Requests()
	last := requests[len(requests)-1]
	if entries := requestInventoryDocument(t, last); entries != nil {
		t.Fatalf("a valid selection must not remind, got %+v", entries)
	}
	if envs := requestSkillEnvelopes(t, last); len(envs) != 0 {
		t.Fatalf("a preload-only selection enqueued %d bodies", len(envs))
	}
	if hasReloadOutcome(s, handoff.Operation.PublicationID+":frozen") {
		t.Fatal("a preload-only selection must record no reload outcome")
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("the no-op selection's receipt must be consumed, got %+v", handoffs)
	}
}

// TestSkillReload_Preload_DualProvenanceReloadsOrdinary pins that an exact
// selection targets the ordinary record when a name holds both a frozen
// preload and ordinary state: the reload delivers the ordinary source's body
// and both provenances survive in the inventory entry.
func TestSkillReload_Preload_DualProvenanceReloadsOrdinary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("DUAL_5c31 ordinary body line\n", 64)
	writeSkillMD(t, root, "dual", "---\nname: dual\ndescription: dual fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "dual", "SKILL.md")
	s, main, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return toolCallResponse(communicateCall("done-1", "ok"))
	}, reloadSummaryResponder("SUMMARY_reload_10", nil))
	seedNumberedSessionHistory(t, s, 12)
	s.contextMgr.PreserveRecentTurns = 2
	identity := plantOrdinaryRecord(t, s, root, "dual", false)
	plantPreloadRecord(t, s, "dual", "dual preload provenance")

	handoff := foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "valid", Names: []string{"dual"}})
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}

	requests := main.Requests()
	requireSingleEnvelope(t, requests[len(requests)-1], "dual", body, source)
	outcome := reloadOutcomeByInvocation(t, s, handoff.Operation.PublicationID+":dual")
	if outcome.Status != "pending" || outcome.Identity != identity {
		t.Fatalf("dual-provenance reload outcome = %+v, want pending with the ordinary identity", outcome)
	}
	entry := lifecycleInventory(s)["dual"]
	if entry.Preload == nil || entry.Ordinary == nil {
		t.Fatalf("dual-provenance entry after reload = %+v, want both provenances", entry)
	}
	if entry.Preload.Name != "dual" || entry.Ordinary.Identity != identity {
		t.Fatalf("dual-provenance entry = %+v, want the preload intact and the ordinary identity advanced", entry)
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("the reload's receipt must be consumed, got %+v", handoffs)
	}
}

// TestSkillReload_Repeated_RetainedContentNotDuplicated pins the reuse rule:
// a second compaction cycle selecting the same skill whose complete reloaded
// content is still present in the retained tail reuses it — the request keeps
// exactly one envelope, the outcome is already_present, and no duplicate body
// or new activation event appears.
func TestSkillReload_Repeated_RetainedContentNotDuplicated(t *testing.T) {
	t.Parallel()
	root := skillFixtureRoot(t)
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a repeated source\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	calls := 0
	s, main, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		calls++
		if calls == 1 {
			return toolCallResponse(useSkillCall("skill-1", "opaque"))
		}
		return toolCallResponse(communicateCall("done-1", "ok"))
	}, reloadSummaryResponder("SUMMARY_reload_11", nil))
	seedNumberedSessionHistory(t, s, 12)
	// Default retention (PreserveRecentTurns=6) keeps the geometry honest:
	// the extra turns below push the original activation carrier past the
	// retained window before the first fold, while the first fold's reload
	// carrier stays inside it for the second — so cycle 1 must restore the
	// body and cycle 2 must reuse it.
	evs, stop := captureEvents(s)

	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	seedNumberedSessionHistory(t, s, 8)
	before := lifecycleInventory(s)["opaque"].Ordinary
	if before == nil {
		t.Fatal("test setup: the real activation recorded no inventory")
	}
	// Cycle 1: the fold drops the activation carrier; the reload restores it.
	foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}})
	if _, err := s.ProcessInput(context.Background(), "REQUEST_cycle1", nil); err != nil {
		t.Fatal(err)
	}
	first := main.Requests()[len(main.Requests())-1]
	requireSingleEnvelope(t, first, "opaque", body, source)
	// The restored content is present in the retained tail, so the typed
	// summary classifies it as already present.
	summary, diagnostics := s.skillInventorySummary(context.Background())
	if len(diagnostics) != 0 {
		t.Fatalf("summary diagnostics = %+v, want none", diagnostics)
	}
	var classified string
	for _, entry := range summary {
		if entry.Name == "opaque" {
			classified = entry.Availability
		}
	}
	if classified != "already_present" {
		t.Fatalf("retained complete content classified %q, want already_present", classified)
	}

	// Cycle 2 selects the same skill again: its complete content is already
	// in the retained tail, so it is reused — never duplicated.
	second := foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}})
	if _, err := s.ProcessInput(context.Background(), "REQUEST_cycle2", nil); err != nil {
		t.Fatal(err)
	}
	stop()

	last := main.Requests()[len(main.Requests())-1]
	requireSingleEnvelope(t, last, "opaque", body, source)
	outcome := reloadOutcomeByInvocation(t, s, second.Operation.PublicationID+":opaque")
	if outcome.Status != "already_present" {
		t.Fatalf("repeated reload outcome status = %q, want already_present", outcome.Status)
	}
	if after := lifecycleInventory(s)["opaque"].Ordinary; after == nil || *after != *before {
		t.Fatalf("the repeated reload changed the inventory: before=%+v after=%+v", before, after)
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("the repeated reload's receipt must be consumed, got %+v", handoffs)
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("obligations outstanding after admission: %+v", got)
	}
	// One activation event for the original body; the unchanged reloads never
	// duplicated it.
	if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "opaque" {
		t.Fatalf("activation events = %v, want exactly the original delivery", names)
	}
}

// TestSkillReloadReminder_CarriesDiscoveryDiagnostics: an inventory entry
// whose recorded source can no longer be read classifies "unavailable" — the
// typed reminder must also carry the computed diagnostic that says WHY, not
// discard it (the old comment claimed diagnostics "ride the typed turn"; they
// never did).
func TestSkillReloadReminder_CarriesDiscoveryDiagnostics(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	missing := filepath.Join(t.TempDir(), "gone", "SKILL.md")
	s.skillLifecycle.Inventory["opaque"] = schema.SkillInventoryEntry{Ordinary: &schema.OrdinarySkillActivation{
		Identity:       schema.SkillContentIdentity{Name: "opaque", DeclaredName: "opaque", Source: missing, FileDigest: "f", RenderedDigest: "r"},
		Controls:       schema.SkillInvocationControls{UserInvocable: true},
		Route:          "user_slash",
		InvocationID:   "inv-1",
		UserAuthorized: true,
	}}
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{{
		Phase: skillCompactionReceiptDelivered,
		Operation: schema.SkillCompactionOperation{
			Generation:    1,
			Selection:     schema.SkillReloadSelection{State: "absent"},
			PublicationID: "pub-1",
		},
	}}

	if _, _, _, err := s.prepareCompactedSkillReloads(context.Background()); err != nil {
		t.Fatal(err)
	}
	var reminder *schema.SkillReloadReminder
	for _, state := range skillTurnStates(s) {
		if state.ReloadReminder != nil {
			reminder = state.ReloadReminder
		}
	}
	if reminder == nil {
		t.Fatal("no reload reminder recorded")
	}
	if len(reminder.Inventory) != 1 || reminder.Inventory[0].Availability != skillAvailabilityUnavailable {
		t.Fatalf("reminder inventory = %+v, want the unreadable entry unavailable", reminder.Inventory)
	}
	if len(reminder.Diagnostics) == 0 {
		t.Fatal("the reminder discarded the discovery diagnostics: an unreadable source shows unavailable with no reason")
	}
	found := false
	for _, diagnostic := range reminder.Diagnostics {
		if diagnostic.Category == "unreadable_source" && diagnostic.Source == missing {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %+v, want unreadable_source for %s", reminder.Diagnostics, missing)
	}
}

// TestSkillReloadReminder_ConsumesReceiptOnlyAfterDurableAdmission pins the
// failure side of the reminder's admission contract. The reminder turn IS the
// receipt's durable admission, so a transcript write failure must surface the
// failure and leave the receipt UNCONSUMED: consuming it while nothing was
// recorded would lose the only post-compaction reminder forever, with no retry
// on the next attempt or after a restart.
func TestSkillReloadReminder_ConsumesReceiptOnlyAfterDurableAdmission(t *testing.T) {
	s := newTestSession(t)
	s.skillLifecycle.Inventory["opaque"] = schema.SkillInventoryEntry{Ordinary: &schema.OrdinarySkillActivation{
		Identity: schema.SkillContentIdentity{
			Name:           "opaque",
			DeclaredName:   "opaque",
			Source:         filepath.Join(t.TempDir(), "opaque", "SKILL.md"),
			FileDigest:     "f",
			RenderedDigest: "r",
		},
		Controls: schema.SkillInvocationControls{UserInvocable: true},
		Route:    "user_slash",
	}}
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{{
		Phase: skillCompactionReceiptDelivered,
		Operation: schema.SkillCompactionOperation{
			Generation:    1,
			Selection:     schema.SkillReloadSelection{State: "absent"},
			PublicationID: "pub-1",
		},
	}}

	// A genuinely failing transcript, wired the way this package's other
	// transcript-failure tests wire it.
	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	writer, err := transcript.NewWriterWithFS(fs, "/session.jsonl", transcript.Header{SessionID: s.id})
	if err != nil {
		t.Fatalf("create failing transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	fs.fail = true
	s.mu.Lock()
	if s.transcript != nil {
		t.Cleanup(func() { _ = s.transcript.Close() })
	}
	s.transcript = writer
	s.transcriptReady = true
	s.mu.Unlock()

	_, _, _, err = s.prepareCompactedSkillReloads(context.Background())
	if err == nil {
		t.Fatal("a reminder whose transcript write failed reported success")
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 1 {
		t.Fatalf("the receipt must stay unconsumed when its reminder was not recorded, got %+v", handoffs)
	}
	for _, state := range skillTurnStates(s) {
		if state.ReloadReminder != nil {
			t.Fatal("a reminder turn joined the history despite the failed transcript write")
		}
	}
}

// countSkillReloadReminderTurns returns how many turns in the session's history
// carry a typed post-compaction reload reminder.
func countSkillReloadReminderTurns(s *Session) int {
	n := 0
	for _, state := range skillTurnStates(s) {
		if state.ReloadReminder != nil {
			n++
		}
	}
	return n
}

// TestSkillReloadReminder_DurableReminderConsumedAtRestore pins the restart
// half of the reminder's admission contract: the reminder turn IS its handoff's
// durable admission, so a snapshot that still holds the handoff (a crash or
// failed save between the turn's transcript write and the consumption save)
// must be reconciled from that durable turn at restore — never repeat the
// reminder. Without the reconciliation the handoff survives the restart and the
// same inventory notification is delivered a second time.
func TestSkillReloadReminder_DurableReminderConsumedAtRestore(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.mu.Lock()
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{{
		Phase: skillCompactionReceiptDelivered,
		Operation: schema.SkillCompactionOperation{
			Generation:    1,
			Selection:     schema.SkillReloadSelection{State: "absent"},
			PublicationID: "pub-durable-reminder",
		},
	}}
	s.mu.Unlock()

	reminder := &schema.SkillReloadReminder{
		Revision:      9,
		PublicationID: "pub-durable-reminder",
		Selection:     schema.SkillReloadSelection{State: "absent"},
	}
	turn := schema.NewTurn(schema.TurnSystem, llm.User("the complete skill inventory reminder"))
	turn.SkillState = &schema.SkillTurnState{ReloadReminder: reminder}
	entries := []transcript.Entry{{Turn: turn}}

	if got := countSkillReloadReminderTurns(s); got != 0 {
		t.Fatalf("test setup: the reminder turn already joined history %d time(s)", got)
	}
	reconcileSkillCompactionReceipts(entries, &s.skillLifecycle, s.id)

	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("the durable reminder left its handoff pending: %+v", handoffs)
	}
	batch, outcomes, _, err := s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads after reconciliation: %v", err)
	}
	if batch != nil || len(outcomes) != 0 {
		t.Fatalf("a consumed handoff still prepared delivery: batch=%+v outcomes=%+v", batch, outcomes)
	}
	if got := countSkillReloadReminderTurns(s); got != 0 {
		t.Fatalf("the consumed handoff delivered the reminder again (%d turns)", got)
	}
}

// TestSkillReloadReminder_FitFailureConsumesEarlierReminders pins the failure
// half. When a later receipt's reminder cannot fit, the reminders already
// durably admitted in this same call must be consumed before the error returns:
// otherwise every retry re-appends them, and since the fit check measures the
// history that now contains the duplicates, each attempt consumes more of the
// very window it is checking and the cycle can never converge.
func TestSkillReloadReminder_FitFailureConsumesEarlierReminders(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	// A construction window keeps the context manager's estimator live (a
	// profile without a window reports zero usage for every history), then the
	// session profile is narrowed to admit exactly one reminder candidate.
	s, _, _ := newReloadSession(t, root, 1_000_000, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_fit", nil))
	plantPreloadRecord(t, s, "bulk", strings.Repeat("bulk fixture description ", 200))

	// Mirror the production reminder content exactly so the window can be sized
	// to admit exactly one of the two candidates.
	summary, diagnostics := s.skillInventorySummary(context.Background())
	if len(summary) == 0 {
		t.Fatal("test setup: the planted inventory is empty")
	}
	s.mu.Lock()
	revision := s.skillLifecycle.Revision
	sys := s.cachedSystemPrompt
	history := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()
	if s.contextMgr == nil || s.profile == nil {
		t.Fatal("test setup: the session has no context manager or profile")
	}
	reminder := schema.SkillReloadReminder{
		Revision:      revision,
		PublicationID: "pub-fit-1",
		Selection:     schema.SkillReloadSelection{State: "absent"},
		Inventory:     summary,
		Diagnostics:   skillInventoryDiagnostics(diagnostics),
	}
	content := renderSkillReloadReminder(reminder, s.canInstructTool("use_skill"))
	used := s.contextMgr.EstimateUsage(history, len(sys)).Used
	tokens := llm.EstimateMessagesInputTokens([]llm.Message{llm.User(content)}).Tokens
	window := used + 1024 + tokens + 1
	if window >= 102400 {
		t.Fatalf("test setup: window %d does not keep the 1024-token reserve", window)
	}
	if got := skillReloadSafetyReserve(window); got != 1024 {
		t.Fatalf("test setup: reserve at window %d = %d, want 1024", window, got)
	}
	s.profile = WithContextWindow(s.profile, window)
	if !s.skillReloadReminderFits(content) {
		t.Fatalf("test setup: the first reminder must fit window %d (used=%d tokens=%d)", window, used, tokens)
	}

	s.mu.Lock()
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{
		{
			Phase: skillCompactionReceiptDelivered,
			Operation: schema.SkillCompactionOperation{
				Generation:    1,
				Selection:     schema.SkillReloadSelection{State: "absent"},
				PublicationID: "pub-fit-1",
			},
		},
		{
			Phase: skillCompactionReceiptDelivered,
			Operation: schema.SkillCompactionOperation{
				Generation:    2,
				Selection:     schema.SkillReloadSelection{State: "absent"},
				PublicationID: "pub-fit-2",
			},
		},
	}
	s.mu.Unlock()

	if _, _, _, err := s.prepareCompactedSkillReloads(context.Background()); err == nil {
		t.Fatal("the second reminder cannot fit the window; the preparation must fail visibly")
	}
	if got := countSkillReloadReminderTurns(s); got != 1 {
		t.Fatalf("reminder turns after the fit failure = %d, want exactly the one admitted before the failure", got)
	}
	handoffs := pendingHandoffsSnapshot(s)
	if len(handoffs) != 1 || handoffs[0].Operation.PublicationID != "pub-fit-2" {
		t.Fatalf("handoffs after the fit failure = %+v, want only the undelivered pub-fit-2", handoffs)
	}

	// The retry must not re-append the reminder that was already admitted.
	if _, _, _, err := s.prepareCompactedSkillReloads(context.Background()); err == nil {
		t.Fatal("the retained reminder still cannot fit the window")
	}
	if got := countSkillReloadReminderTurns(s); got != 1 {
		t.Fatalf("reminder turns after the retry = %d, want no re-append", got)
	}
}

// TestSkillReload_AdmittedCarrierWaitsForItsDurableObligation pins the same
// ordering for the compaction-reload route that the tool round and the
// selection route already hold: a reload carrier embeds the COMPLETE skill
// body, so it must not be recorded before the obligation that keeps that body
// alive at dispatch is durable. A failed admission save must therefore publish
// no carrier at all — the code recorded it first and then claimed, in a
// comment, that the carriers were durable "in the same save".
func TestSkillReload_AdmittedCarrierWaitsForItsDurableObligation(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_reload_carrier")
	s, _, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_carrier", nil))
	plantOrdinaryRecord(t, s, root, "opaque", true)
	s.mu.Lock()
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{{
		Phase: skillCompactionReceiptDelivered,
		Operation: schema.SkillCompactionOperation{
			Generation:    1,
			Selection:     schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}},
			PublicationID: "pub-reload-carrier",
		},
	}}
	s.mu.Unlock()

	batch, outcomes, _, err := s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads: %v", err)
	}
	if batch == nil || len(batch.Items) != 1 {
		t.Fatalf("test setup: prepared batch = %+v, want the one selected reload", batch)
	}

	repair := breakSessionMetaPath(t, s)
	err = s.admitCompactedSkillReloads(context.Background(), nil, &llm.TokenBudget{}, 0, batch, outcomes)
	if err == nil {
		t.Fatal("a failed admission save must surface an error")
	}
	repair()

	for _, state := range skillTurnStates(s) {
		if len(state.Obligations) != 0 {
			t.Fatalf("a failed admission save published a carrier turn carrying %+v", state.Obligations)
		}
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("a failed admission save left live obligations: %+v", got)
	}
}

// failSessionTranscript installs a transcript writer whose writes fail, wired
// the way this package's other transcript-failure tests wire one (a
// transcriptWriteFailFS over a mem filesystem), and marks the session's
// transcript ready so appends reach it. Used to pin that a lost carrier write
// is never mistaken for a recorded body.
func failSessionTranscript(t *testing.T, s *Session) {
	t.Helper()
	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	writer, err := transcript.NewWriterWithFS(fs, "/session.jsonl", transcript.Header{SessionID: s.id})
	if err != nil {
		t.Fatalf("create failing transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	fs.fail = true
	s.mu.Lock()
	if s.transcript != nil {
		t.Cleanup(func() { _ = s.transcript.Close() })
	}
	s.transcript = writer
	s.transcriptReady = true
	s.mu.Unlock()
}

// TestSkillReload_CarrierWriteFailureIsVisible pins the durability door for a
// reload carrier. The carrier embeds the COMPLETE reloaded body, so a failed
// transcript write must surface as an error and leave the carrier out of the
// live history; recordTurn's non-durable, error-swallowing append recorded the
// turn anyway and reported success, so a crash before the next sync interval
// lost the only copy of the body while the lifecycle advanced as if it were
// delivered. The already-persisted obligation keeps the body recoverable.
func TestSkillReload_CarrierWriteFailureIsVisible(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_reload_write_fail")
	s, _, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_write_fail", nil))
	plantOrdinaryRecord(t, s, root, "opaque", true)
	s.mu.Lock()
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{{
		Phase: skillCompactionReceiptDelivered,
		Operation: schema.SkillCompactionOperation{
			Generation:    1,
			Selection:     schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}},
			PublicationID: "pub-reload-write-fail",
		},
	}}
	s.mu.Unlock()

	batch, outcomes, _, err := s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads: %v", err)
	}
	if batch == nil || len(batch.Items) != 1 {
		t.Fatalf("test setup: prepared batch = %+v, want the one selected reload", batch)
	}
	failSessionTranscript(t, s)

	if err := s.admitCompactedSkillReloads(context.Background(), nil, &llm.TokenBudget{}, 0, batch, outcomes); err == nil {
		t.Fatal("a failed carrier write must surface an error, not report a recorded body")
	}
	for _, state := range skillTurnStates(s) {
		if len(state.Obligations) != 0 {
			t.Fatalf("a failed carrier write published a carrier turn carrying %+v", state.Obligations)
		}
	}
}

// TestSkillReload_FailedReloadNotificationWriteFailureIsVisible pins the same
// door for the typed failure notification: when the selected source cannot be
// reloaded, the explanation the model is owed must be durably recorded, and a
// failed write must fail the preparation instead of consuming the receipt
// without ever telling the model why the reload did not happen.
func TestSkillReload_FailedReloadNotificationWriteFailureIsVisible(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	source := writeSkillMDRel(t, root, "skills", "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_reload_notify_fail")
	s, _, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_notify_fail", nil))
	plantOrdinaryRecord(t, s, root, "opaque", true)
	if err := os.Remove(source); err != nil {
		t.Fatalf("remove the planted source: %v", err)
	}
	s.mu.Lock()
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{{
		Phase: skillCompactionReceiptDelivered,
		Operation: schema.SkillCompactionOperation{
			Generation:    1,
			Selection:     schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}},
			PublicationID: "pub-reload-notify-fail",
		},
	}}
	s.mu.Unlock()
	failSessionTranscript(t, s)

	if _, _, _, err := s.prepareCompactedSkillReloads(context.Background()); err == nil {
		t.Fatal("a failed reload-failure notification write must fail the preparation visibly")
	}
}

// TestSkillReload_DuplicateSelectionsAdmitOneBody pins within-batch reuse: two
// pending handoffs that select the same skill must admit ONE instruction body
// and one activation, not two. The reuse check reads the live history, so
// staging the carriers until after the obligation save (which the durability
// finding required) must not lose the dedup the immediate-record path got for
// free — otherwise the model receives duplicate instruction bodies and the
// lifecycle publishes a duplicate activation per extra handoff.
func TestSkillReload_DuplicateSelectionsAdmitOneBody(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_reload_duplicate")
	s, _, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_duplicate", nil))
	plantOrdinaryRecord(t, s, root, "opaque", true)
	s.mu.Lock()
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{
		{
			Phase: skillCompactionReceiptDelivered,
			Operation: schema.SkillCompactionOperation{
				Generation:    1,
				Selection:     schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}},
				PublicationID: "pub-dup-1",
			},
		},
		{
			Phase: skillCompactionReceiptDelivered,
			Operation: schema.SkillCompactionOperation{
				Generation:    2,
				Selection:     schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}},
				PublicationID: "pub-dup-2",
			},
		},
	}
	s.mu.Unlock()

	batch, outcomes, _, err := s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads: %v", err)
	}
	if batch == nil || len(batch.Items) != 2 {
		t.Fatalf("test setup: prepared batch = %+v, want both selections", batch)
	}
	if err := s.admitCompactedSkillReloads(context.Background(), nil, &llm.TokenBudget{}, 0, batch, outcomes); err != nil {
		t.Fatalf("admitCompactedSkillReloads: %v", err)
	}

	bodies := 0
	notifications := 0
	for _, state := range skillTurnStates(s) {
		switch {
		case len(state.Obligations) != 0:
			bodies++
		case len(state.Outcomes) != 0:
			notifications++
		}
	}
	if bodies != 1 {
		t.Fatalf("admitted %d instruction bodies for one skill, want exactly one (the second selection must reuse it)", bodies)
	}
	if notifications != 1 {
		t.Fatalf("recorded %d reuse notifications, want exactly one", notifications)
	}
}

// TestSkillReload_IncompleteIdentitySelectionRetiresReceipt pins that a valid
// selection naming a skill whose recorded identity is incomplete is REPORTED
// (a typed invalid_metadata failure the model can see) and that its handoff is
// retired with the rest of the selection. Leaving the name a silent no-op left
// the publication unconsumed forever, so every later request re-processed the
// same selection: the reloadable name alongside it was re-announced as
// already_present and collected another delivery obligation each time, with no
// bound. The legacy record here is the shape a pre-identity activation leaves.
func TestSkillReload_IncompleteIdentitySelectionRetiresReceipt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_reload_legacy")
	s, _, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_legacy", nil))
	plantOrdinaryRecord(t, s, root, "opaque", true)
	s.mu.Lock()
	// A legacy activation: an ordinary record with no recorded identity at all.
	s.skillLifecycle.Inventory["scope:probe"] = schema.SkillInventoryEntry{
		Ordinary: &schema.OrdinarySkillActivation{Description: "legacy-probe"},
	}
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{{
		Phase: skillCompactionReceiptDelivered,
		Operation: schema.SkillCompactionOperation{
			Generation:    1,
			Selection:     schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe", "opaque"}},
			PublicationID: "pub-legacy-identity",
		},
	}}
	s.mu.Unlock()

	notices := func() int {
		n := 0
		for _, state := range skillTurnStates(s) {
			for _, outcome := range state.Outcomes {
				if outcome.ErrorCode == "invalid_metadata" {
					n++
				}
			}
		}
		return n
	}
	obligations := func() int { return len(lifecycleObligations(s)) }

	batch, outcomes, _, err := s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads: %v", err)
	}
	if notices() != 1 {
		t.Fatalf("visible invalid_metadata notices after the first preparation = %d, want exactly one", notices())
	}
	if batch != nil {
		if err := s.admitCompactedSkillReloads(context.Background(), nil, &llm.TokenBudget{}, 0, batch, outcomes); err != nil {
			t.Fatalf("admitCompactedSkillReloads: %v", err)
		}
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("the reported selection left its handoff pending: %+v", handoffs)
	}
	after1, obligations1 := notices(), obligations()

	// A later request must not re-process the retired selection.
	if _, _, _, err := s.prepareCompactedSkillReloads(context.Background()); err != nil {
		t.Fatalf("second prepareCompactedSkillReloads: %v", err)
	}
	if got := notices(); got != after1 {
		t.Fatalf("notices grew from %d to %d on re-processing a retired receipt", after1, got)
	}
	if got := obligations(); got != obligations1 {
		t.Fatalf("obligations grew from %d to %d on re-processing a retired receipt", obligations1, got)
	}
}

// countReloadOutcomes returns how many recorded turns carry a typed outcome for
// one reload invocation — the notice count a retry must not increase.
func countReloadOutcomes(s *Session, invocationID string) int {
	n := 0
	for _, state := range skillTurnStates(s) {
		for _, outcome := range state.Outcomes {
			if outcome.InvocationID == invocationID {
				n++
			}
		}
	}
	return n
}

// plantReloadReceipt seeds one delivered handoff carrying selection under the
// given publication identity — for a valid selection, the deterministic
// invocation identity a retry re-derives is publicationID + ":" + name.
func plantReloadReceipt(s *Session, publicationID string, selection schema.SkillReloadSelection) {
	s.mu.Lock()
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{{
		Phase: skillCompactionReceiptDelivered,
		Operation: schema.SkillCompactionOperation{
			Generation:    1,
			Selection:     selection,
			PublicationID: publicationID,
		},
	}}
	s.mu.Unlock()
}

// opaqueReloadSelection selects the fixture skill "opaque" for reload.
var opaqueReloadSelection = schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}}

// TestSkillReload_FailedNoticeNotReappendedAfterSaveFailure pins the
// idempotence of a reload-failure notice across a transient admission-save
// failure. The explanation is durably recorded in preparation, BEFORE the
// admission save consumes the receipt; when that save fails the receipt is
// restored, so the retry re-prepares the SAME deterministic invocation
// (publication:name) and must recognize the notice already recorded for it
// instead of appending a second copy. Without that, every transient disk-save
// failure adds another duplicate notification turn and the count grows with
// each retry.
func TestSkillReload_FailedNoticeNotReappendedAfterSaveFailure(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	source := writeSkillMDRel(t, root, "skills", "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_notice_retry\n")
	s, _, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_notice_retry", nil))
	plantOrdinaryRecord(t, s, root, "opaque", true)
	if err := os.Remove(source); err != nil {
		t.Fatalf("remove the fixture source: %v", err)
	}
	const publication = "pub-notice-retry"
	plantReloadReceipt(s, publication, opaqueReloadSelection)
	invocationID := publication + ":opaque"

	batch, outcomes, staged, err := s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads: %v", err)
	}
	if got := countReloadOutcomes(s, invocationID); got != 1 {
		t.Fatalf("notices after the first preparation = %d, want exactly one", got)
	}

	repair := breakSessionMetaPath(t, s)
	if err := s.admitCompactedSkillReloads(context.Background(), nil, &llm.TokenBudget{}, staged, batch, outcomes); err == nil {
		t.Fatal("a failed admission save must surface an error")
	}
	repair()
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 1 {
		t.Fatalf("the failed save must leave the receipt pending, got %+v", handoffs)
	}

	batch, outcomes, staged, err = s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads (retry): %v", err)
	}
	if batch != nil {
		if err := s.admitCompactedSkillReloads(context.Background(), nil, &llm.TokenBudget{}, staged, batch, outcomes); err != nil {
			t.Fatalf("admitCompactedSkillReloads (retry): %v", err)
		}
	}
	if got := countReloadOutcomes(s, invocationID); got != 1 {
		t.Fatalf("notices after the retry = %d, want 1: the durable notice must not be re-appended", got)
	}
}

// TestSkillReload_ReuseNoticeNotReappendedAfterSaveFailure pins the same
// idempotence for the reuse notice admission appends. The notice is recorded
// before the admission save; a failed save restores the receipt, and the retry
// re-runs the reuse decision. It must recognize the already-durable notice for
// the deterministic invocation instead of appending it again.
func TestSkillReload_ReuseNoticeNotReappendedAfterSaveFailure(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_reuse_retry\n")
	s, _, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_reuse_retry", nil))
	plantOrdinaryRecord(t, s, root, "opaque", true)
	const publication = "pub-reuse-retry"
	plantReloadReceipt(s, publication, opaqueReloadSelection)
	invocationID := publication + ":opaque"

	batch, outcomes, staged, err := s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads: %v", err)
	}
	if batch == nil || len(batch.Items) != 1 {
		t.Fatalf("test setup: prepared batch = %+v, want the one selected reload", batch)
	}
	// Retain the complete body in the live history so the admission reuses it
	// and records the reuse notice.
	s.mu.Lock()
	s.history = append(s.history, schema.NewTurn(schema.TurnSystem, llm.User(batch.Items[0].Rendered.Content)))
	s.mu.Unlock()

	repair := breakSessionMetaPath(t, s)
	if err := s.admitCompactedSkillReloads(context.Background(), nil, &llm.TokenBudget{}, staged, batch, outcomes); err == nil {
		t.Fatal("a failed admission save must surface an error")
	}
	repair()
	if got := countReloadOutcomes(s, invocationID); got != 1 {
		t.Fatalf("reuse notices after the failed save = %d, want exactly one", got)
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 1 {
		t.Fatalf("the failed save must leave the receipt pending, got %+v", handoffs)
	}

	batch, outcomes, staged, err = s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads (retry): %v", err)
	}
	if batch == nil || len(batch.Items) != 1 {
		t.Fatalf("test setup: retried batch = %+v, want the one selected reload", batch)
	}
	if err := s.admitCompactedSkillReloads(context.Background(), nil, &llm.TokenBudget{}, staged, batch, outcomes); err != nil {
		t.Fatalf("admitCompactedSkillReloads (retry): %v", err)
	}
	if got := countReloadOutcomes(s, invocationID); got != 1 {
		t.Fatalf("reuse notices after the retry = %d, want 1: the durable notice must not be re-appended", got)
	}
}

// plantReminderReceipt stages one delivered handoff whose selection authorized
// no reload, the shape preparation answers with a complete inventory reminder.
func plantReminderReceipt(s *Session, publicationID string) {
	plantReloadReceipt(s, publicationID, schema.SkillReloadSelection{State: "absent"})
}

// TestSkillReloadReminder_RetryAfterAFailedConsumptionSaveAppendsNoSecondTurn:
// the reminder turn IS the admission, so its receipt leaves with the turn's
// durable commit whatever the metadata save then does. A retry after a failed
// save therefore finds no receipt and appends no second turn saying the same
// thing, which would deliver the same inventory twice for one handoff.
func TestSkillReloadReminder_RetryAfterAFailedConsumptionSaveAppendsNoSecondTurn(t *testing.T) {
	t.Parallel()
	metaFS := &notesRenameFailureFS{Fs: afero.NewMemMapFs(), fail: true, err: errParkedNotesSave}
	s := newSession(t,
		withConfig(SessionConfig{StateDir: t.TempDir(), testOnly: testConfig{metaFS: metaFS}}),
		withoutGitSnapshot(),
	)
	drainSessionEvents(s)
	plantPreloadRecord(t, s, "opaque", "fixture description")
	const publication = "pub-reminder-retry"
	plantReminderReceipt(s, publication)

	if _, _, _, err := s.prepareCompactedSkillReloads(context.Background()); err == nil {
		t.Fatal("a failed consumption save must surface an error")
	}
	if got := countSkillReloadReminderTurns(s); got != 1 {
		t.Fatalf("reminders after the failed save = %d, want exactly one", got)
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("the recorded reminder must consume its receipt whatever the save did, got %+v", handoffs)
	}

	// A fold between the failed save and the retry compacts the reminder out
	// of the live history. Recognizing an already-recorded reminder by scanning
	// that history would re-append it here; the consumed receipt is what keeps
	// the retry from delivering the inventory a second time.
	s.mu.Lock()
	s.history = slices.DeleteFunc(s.history, func(turn schema.Turn) bool {
		return turn.SkillState != nil && turn.SkillState.ReloadReminder != nil
	})
	s.mu.Unlock()
	if got := countSkillReloadReminderTurns(s); got != 0 {
		t.Fatalf("test setup: %d reminder turn(s) survived the fold", got)
	}

	metaFS.fail = false
	if _, _, staged, err := s.prepareCompactedSkillReloads(context.Background()); err != nil {
		t.Fatalf("prepareCompactedSkillReloads (retry): %v", err)
	} else if staged != 0 {
		t.Fatalf("the retry staged %d input tokens for a reminder it appended nothing for, want 0", staged)
	}
	if got := countSkillReloadReminderTurns(s); got != 0 {
		t.Fatalf("the retry appended %d reminder(s): the transcript already holds the only reminder this handoff is owed", got)
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("handoffs after the retry = %+v, want the receipt consumed", handoffs)
	}
}

// TestSkillReloadReminder_ConsumingAnEmptyPublicationRetiresNoCancellation: a
// terminal cancellation receipt carries no publication identity. Consuming a
// reminder whose receipt also names none must not sweep those cancellations
// away with it — they are retired on their own schedule.
func TestSkillReloadReminder_ConsumingAnEmptyPublicationRetiresNoCancellation(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot())
	s.mu.Lock()
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{
		{Phase: skillCompactionReceiptCancelled, Operation: schema.SkillCompactionOperation{Generation: 1}},
		{Phase: skillCompactionReceiptDelivered, Operation: schema.SkillCompactionOperation{Generation: 2, Selection: schema.SkillReloadSelection{State: "absent"}}},
	}
	revision := s.skillLifecycle.Revision
	s.consumeSkillReloadReminderLocked("")
	kept := len(s.skillLifecycle.PendingHandoffs)
	bumped := s.skillLifecycle.Revision != revision
	s.mu.Unlock()
	if kept != 2 || bumped {
		t.Fatalf("consuming an identity-less reminder kept %d of 2 receipts (revision bumped: %v), want all of them untouched", kept, bumped)
	}
}

// TestSkillReload_FailedAdmissionSaveKeepsAConcurrentHandoff: the admission
// save runs without s.mu, so a fold publishing in that window records a handoff
// of its own. A rollback that restores a pre-admission snapshot discards it,
// and one that skips the restore because the lifecycle moved loses the receipt
// the failed save was supposed to keep. Only what this admission changed comes
// back, and everything else stays.
func TestSkillReload_FailedAdmissionSaveKeepsAConcurrentHandoff(t *testing.T) {
	t.Parallel()
	const consumed = "pub-consumed-reload"
	const arrived = "pub-arrived-mid-save"
	metaFS := &notesRenameFailureFS{Fs: afero.NewMemMapFs(), fail: true, err: errParkedNotesSave}
	s := newSession(t,
		withConfig(SessionConfig{StateDir: t.TempDir(), testOnly: testConfig{metaFS: metaFS}}),
		withoutGitSnapshot(),
	)
	drainSessionEvents(s)
	// A selection naming only a preload-only skill is consumed with no outcome
	// at all, so an empty batch is a complete admission for it.
	plantPreloadRecord(t, s, "opaque", "fixture description")
	plantReloadReceipt(s, consumed, opaqueReloadSelection)
	metaFS.before = func() {
		s.mu.Lock()
		s.recordSkillCompactionHandoffLocked(schema.SkillCompactionReceipt{
			Phase: skillCompactionReceiptDelivered,
			Operation: schema.SkillCompactionOperation{
				Generation:    2,
				Selection:     opaqueReloadSelection,
				PublicationID: arrived,
			},
		})
		s.skillLifecycle.Revision++
		s.mu.Unlock()
	}

	budget := &llm.TokenBudget{}
	if err := s.admitCompactedSkillReloads(context.Background(), nil, budget, 0, &skillActivationBatch{}, nil); err == nil {
		t.Fatal("a failed admission save must surface an error")
	}
	pending := map[string]bool{}
	for _, handoff := range pendingHandoffsSnapshot(s) {
		pending[handoff.Operation.PublicationID] = true
	}
	if !pending[arrived] {
		t.Fatalf("handoffs after the failed save = %v, want the handoff recorded while the save ran kept", pending)
	}
	if !pending[consumed] {
		t.Fatalf("handoffs after the failed save = %v, want the unconsumed reload receipt back", pending)
	}
}

// TestSkillReload_FailedAdmissionSaveRestoresTheReceiptAheadOfLaterHandoffs:
// pending handoffs are processed in slice order, so the slice order is the
// delivery order. A receipt the failed save puts back must return to the
// position it held, ahead of a handoff a fold published while the save ran;
// appended after it, the retry would deliver the newer publication's reload
// before the older one's.
func TestSkillReload_FailedAdmissionSaveRestoresTheReceiptAheadOfLaterHandoffs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "older", "---\nname: older\ndescription: fixture\n---\nBODY_order_older\n")
	writeSkillMD(t, root, "newer", "---\nname: newer\ndescription: fixture\n---\nBODY_order_newer\n")
	metaFS := &notesRenameFailureFS{Fs: afero.NewMemMapFs(), fail: true, err: errParkedNotesSave}
	s, _, _ := newReloadSession(t, root, 0, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("unused")}
	}, reloadSummaryResponder("SUMMARY_order", nil),
		withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir(), testOnly: testConfig{metaFS: metaFS}}),
	)
	drainSessionEvents(s)
	plantOrdinaryRecord(t, s, root, "older", true)
	plantOrdinaryRecord(t, s, root, "newer", true)
	const olderPublication = "pub-order-older"
	const newerPublication = "pub-order-newer"
	plantReloadReceipt(s, olderPublication, schema.SkillReloadSelection{State: "valid", Names: []string{"older"}})
	metaFS.before = func() {
		s.mu.Lock()
		s.recordSkillCompactionHandoffLocked(schema.SkillCompactionReceipt{
			Phase: skillCompactionReceiptDelivered,
			Operation: schema.SkillCompactionOperation{
				Generation:    2,
				Selection:     schema.SkillReloadSelection{State: "valid", Names: []string{"newer"}},
				PublicationID: newerPublication,
			},
		})
		s.skillLifecycle.Revision++
		s.mu.Unlock()
	}

	batch, outcomes, staged, err := s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads: %v", err)
	}
	if err := s.admitCompactedSkillReloads(context.Background(), nil, &llm.TokenBudget{}, staged, batch, outcomes); err == nil {
		t.Fatal("a failed admission save must surface an error")
	}
	wantPending := []string{olderPublication, newerPublication}
	if got := pendingPublicationIDs(s); !slices.Equal(got, wantPending) {
		t.Fatalf("handoffs after the failed save = %v, want the restored receipt back at its position: %v", got, wantPending)
	}

	// The retry delivers the publications in the order they were published.
	metaFS.before = nil
	metaFS.fail = false
	batch, outcomes, staged, err = s.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads (retry): %v", err)
	}
	wantInvocations := []string{olderPublication + ":older", newerPublication + ":newer"}
	var gotInvocations []string
	for _, outcome := range outcomes {
		gotInvocations = append(gotInvocations, outcome.InvocationID)
	}
	if !slices.Equal(gotInvocations, wantInvocations) {
		t.Fatalf("retry prepared %v, want the older publication's reload first: %v", gotInvocations, wantInvocations)
	}
	if err := s.admitCompactedSkillReloads(context.Background(), nil, &llm.TokenBudget{}, staged, batch, outcomes); err != nil {
		t.Fatalf("admitCompactedSkillReloads (retry): %v", err)
	}
	var gotCarriers []string
	for _, state := range skillTurnStates(s) {
		for _, obligation := range state.Obligations {
			gotCarriers = append(gotCarriers, obligation.InvocationID)
		}
	}
	if !slices.Equal(gotCarriers, wantInvocations) {
		t.Fatalf("retry recorded carriers %v, want the older publication's body first: %v", gotCarriers, wantInvocations)
	}
}

// pendingPublicationIDs reports the pending handoffs' publication identities
// in slice order, the order preparation processes them.
func pendingPublicationIDs(s *Session) []string {
	var ids []string
	for _, handoff := range pendingHandoffsSnapshot(s) {
		ids = append(ids, handoff.Operation.PublicationID)
	}
	return ids
}
