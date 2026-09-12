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
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
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
	root := t.TempDir()
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
	root := t.TempDir()
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
	root := t.TempDir()
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
