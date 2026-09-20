package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Checkpoint compaction reminder (SoL-Pi auto-research design, mechanism 2):
// at task-list step completion boundaries the harness MAY inject a steering
// reminder that compaction is available and cheap right now, provided the
// projected input savings (observed request rate between completed steps ×
// remaining step count × typical request input size) beat the prompt-cache
// rewrite cost (recent cache-write tokens × the cache-write rate from
// llm/pricing.go data) by the escalating margin. The agent elects compaction
// through its existing compact_context tool; the mechanism never compacts
// itself. These tests drive the whole thing through the scripted provider at
// the LLM boundary: offline, deterministic, no live requests.

// ckptReminderUsage builds the per-request usage every scripted response
// carries: inputTokens fresh input plus cacheWriteTokens reported written to
// the prompt cache. Cache-write presence is what makes the rewrite cost
// computable; cache reads are omitted to keep the token arithmetic legible.
func ckptReminderUsage(inputTokens, cacheWriteTokens int) llm.Usage {
	w := cacheWriteTokens
	return llm.Usage{InputTokens: inputTokens, CacheWriteTokens: &w}
}

// ckptReminderStep scripts one assistant tool call carrying the given usage.
func ckptReminderStep(callID, tool, args string, u llm.Usage) func(llm.Request) llm.Response {
	return func(llm.Request) llm.Response {
		return llm.Response{
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: callID, Name: tool, Arguments: []byte(args)}},
				},
			},
			Usage: u,
		}
	}
}

// ckptReminderTaskStep scripts one task_list call with usage.
func ckptReminderTaskStep(callID, args string, u llm.Usage) func(llm.Request) llm.Response {
	return ckptReminderStep(callID, "task_list", args, u)
}

// ckptReminderAddArgs renders a task_list add payload of n tasks.
func ckptReminderAddArgs(n int) string {
	type addEntry struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	adds := make([]addEntry, n)
	for i := range adds {
		adds[i] = addEntry{Type: "implement", Description: "step", Prompt: "do it"}
	}
	raw, err := json.Marshal(map[string]any{"add": adds})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func ckptReminderDoneArgs(id int) string {
	return `{"update":[{"id":` + strconv.Itoa(id) + `,"status":"done"}]}`
}

// ckptReminderPricedProfile returns a profile whose pricing row is injected
// fake cost data (never the live registry's). Unpriced models are exercised
// by passing nil.
func ckptReminderPricedProfile(cost *registry.Cost) *provider.Profile {
	base := NewOpenAIProfile("gpt-5.2")
	res := base.Resolved()
	res.Caps.Cost = cost
	return base.WithResolved(res)
}

// ckptReminderCost is the injected pricing row the gate reads: $3/M input,
// $15/M output, $0.30/M cache read, $3.75/M cache write.
func ckptReminderCost() *registry.Cost {
	return &registry.Cost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
}

// ckptReminderSession builds a session with the checkpoint reminder flag on
// (or off), the injected pricing row, a StateDir so the transcript can be
// inspected, and the scripted openai adapter. The cheap-model route is pinned
// to the dedicated namer adapter so compaction side channels never consume
// the main script's steps.
func ckptReminderSession(t *testing.T, flag bool, cost *registry.Cost, steps []func(req llm.Request) llm.Response) (*Session, *fakeAdapter) {
	t.Helper()
	adapter := &fakeAdapter{name: "openai", steps: steps}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, ckptReminderPricedProfile(cost))
	sess := newSession(t,
		withClient(client),
		withProfile(profile),
		withConfig(SessionConfig{
			StateDir:           newBucket(t),
			CheckpointReminder: flag,
			MaxSubagentDepth:   1,
			NoProjectPrompts:   true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}),
	)
	drainSessionEvents(sess)
	return sess, adapter
}

// ckptReminderRun drives one input to completion with the standard tripwire.
func ckptReminderRun(t *testing.T, sess *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "work", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if out != "done" {
		t.Fatalf("ProcessInput output = %q, want %q", out, "done")
	}
}

// ckptReminderMarker is the reminder text's distinctive opener.
const ckptReminderMarker = "You just completed a step on your task list"

// ckptReminderRequestText flattens a request's messages to their text.
func ckptReminderRequestText(req llm.Request) string {
	var b strings.Builder
	for _, msg := range req.Messages {
		b.WriteString(msg.Text())
		b.WriteByte('\n')
	}
	return b.String()
}

// ckptReminderRequestHasReminder reports whether any message in the request
// carries the checkpoint reminder (identified by its distinctive opener).
func ckptReminderRequestHasReminder(req llm.Request) bool {
	return strings.Contains(ckptReminderRequestText(req), ckptReminderMarker)
}

// ckptReminderCountInRequests counts requests carrying the reminder.
func ckptReminderCountInRequests(reqs []llm.Request) int {
	n := 0
	for _, req := range reqs {
		if ckptReminderRequestHasReminder(req) {
			n++
		}
	}
	return n
}

// ckptReminderOccurrences counts how many reminder turns a request carries:
// an injected reminder becomes a steering turn in history, so its text rides
// every subsequent request — the occurrence count in a later request is the
// number of distinct reminders ever injected before it.
func ckptReminderOccurrences(req llm.Request) int {
	return strings.Count(ckptReminderRequestText(req), ckptReminderMarker)
}

// ckptReminderTranscript reads the session's transcript entries.
func ckptReminderTranscript(t *testing.T, sess *Session) []transcript.Entry {
	t.Helper()
	_, entries, _, err := readTranscript(sess.TranscriptPath())
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	return entries
}

// ckptReminderReminderTurns returns the transcript's TurnSteering entries
// recorded with the checkpoint-reminder steering kind.
func ckptReminderReminderTurns(t *testing.T, sess *Session) []transcript.Entry {
	t.Helper()
	var out []transcript.Entry
	for _, e := range ckptReminderTranscript(t, sess) {
		if e.Turn.Kind == schema.TurnSteering && e.Turn.SteeringKind == events.SteeringKindCheckpointReminder {
			out = append(out, e)
		}
	}
	return out
}

// ckptReminderBoundaryTurns counts compaction boundary entries
// (TurnCheckpoint or TurnSummary) in the transcript.
func ckptReminderBoundaryTurns(t *testing.T, sess *Session) int {
	t.Helper()
	n := 0
	for _, e := range ckptReminderTranscript(t, sess) {
		if e.Turn.Kind == schema.TurnCheckpoint || e.Turn.Kind == schema.TurnSummary {
			n++
		}
	}
	return n
}

// TestCheckpointReminder_StepCompletionCarriesReminderOnNextRequest is the
// headline contract: with the flag on, fake pricing injected, and the cost
// gate passing, a step completion injects the steering reminder into the NEXT
// model request; the reminder names compact_context; the transcript records
// it as a steering injection of the checkpoint-reminder kind — and, the
// agent ignoring it, no compaction ever happens (the mechanism never forces
// one).
func TestCheckpointReminder_StepCompletionCarriesReminderOnNextRequest(t *testing.T) {
	t.Parallel()
	// Typical request input = 1k fresh + 1k cache-write = 2k tokens (kept
	// far below the window so recorded pressure never reaches the automatic
	// compaction thresholds — the ratio math is scale-invariant). Gate at
	// the second boundary: rate 1 request/step (closed window r2→r3), 1
	// remaining step, savings = 1·1·2k·$3/M = $0.006 vs rewrite
	// 1k·$3.75/M = $0.00375 → ratio 1.6 > margin 1.0 → reminder.
	u := ckptReminderUsage(1_000, 1_000)
	sess, adapter := ckptReminderSession(t, true, ckptReminderCost(), []func(req llm.Request) llm.Response{
		ckptReminderTaskStep("call_add", ckptReminderAddArgs(3), u),     // r1: add 3 tasks (no boundary)
		ckptReminderTaskStep("call_done1", ckptReminderDoneArgs(1), u),  // r2: boundary 1 — opens the rate window, no rate yet
		ckptReminderTaskStep("call_done2", ckptReminderDoneArgs(2), u),  // r3: boundary 2 — gate passes → reminder
		ckptReminderTaskStep("call_done3", ckptReminderDoneArgs(3), u),  // r4: carries the reminder; boundary 3 (0 remaining)
		func(llm.Request) llm.Response { return finalResponse("done") }, // r5
	})
	ckptReminderRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 5 {
		t.Fatalf("scripted adapter saw %d requests, want 5", len(reqs))
	}
	// The first boundary opens the observation window: no rate has been
	// observed between completed steps yet, so no reminder may fire.
	if ckptReminderRequestHasReminder(reqs[2]) {
		t.Fatal("reminder rode the request after the FIRST completion; the observed rate has no basis yet")
	}
	if !ckptReminderRequestHasReminder(reqs[3]) {
		t.Fatal("the request after the gate-passing completion carries no checkpoint reminder")
	}
	text := ckptReminderRequestText(reqs[3])
	for _, want := range []string{"<SYSTEM-REMINDER>", "compact_context", "optional"} {
		if !strings.Contains(text, want) {
			t.Fatalf("reminder text lacks %q:\n%s", want, text)
		}
	}
	// Exactly one reminder was injected (the third boundary has 0 remaining
	// steps, so its projected savings are zero and the gate fails there).
	if got := ckptReminderOccurrences(reqs[4]); got != 1 {
		t.Fatalf("the final request carries %d checkpoint reminder turns, want exactly 1 (no second injection)", got)
	}

	// The transcript records the injection as a steering turn of the
	// checkpoint-reminder kind — never as a compaction boundary — and the
	// agent ignored it, so no compaction boundary exists at all.
	turns := ckptReminderReminderTurns(t, sess)
	if len(turns) != 1 {
		t.Fatalf("transcript holds %d checkpoint-reminder steering turns, want 1", len(turns))
	}
	if !strings.Contains(turns[0].Turn.Message.Text(), "compact_context") {
		t.Fatalf("recorded reminder does not name compact_context:\n%s", turns[0].Turn.Message.Text())
	}
	if n := ckptReminderBoundaryTurns(t, sess); n != 0 {
		t.Fatalf("transcript holds %d compaction boundary turns, want 0: the mechanism must never force compaction", n)
	}
	sess.mu.Lock()
	issued := sess.ckptRemindersIssued
	windowOpen := sess.ckptWindowOpen
	rateWindows := sess.ckptRateWindows
	sess.mu.Unlock()
	if issued != 1 {
		t.Fatalf("remindersIssued = %d, want 1", issued)
	}
	if !windowOpen || rateWindows != 2 {
		t.Fatalf("rate window state = open %v / %d closed windows, want open / 2", windowOpen, rateWindows)
	}
}

// TestCheckpointReminder_GateFailsInjectsNothing pins the gate-fail
// contract: when projected savings do not beat the margin-adjusted rewrite
// cost, no reminder is injected anywhere, and every request is byte-identical
// to a session running with the mechanism off.
func TestCheckpointReminder_GateFailsInjectsNothingByteIdenticalToFlagOff(t *testing.T) {
	t.Parallel()
	// 100 fresh + 1k cache-write: savings 1·1·1.1k·$3/M = $0.0033 vs
	// rewrite 1k·$3.75/M = $0.00375 → ratio 0.88, below the 1.0 margin at
	// every boundary.
	u := ckptReminderUsage(100, 1_000)
	steps := func() []func(req llm.Request) llm.Response {
		return []func(req llm.Request) llm.Response{
			ckptReminderTaskStep("call_add", ckptReminderAddArgs(3), u),
			ckptReminderTaskStep("call_done1", ckptReminderDoneArgs(1), u),
			ckptReminderTaskStep("call_done2", ckptReminderDoneArgs(2), u),
			ckptReminderTaskStep("call_done3", ckptReminderDoneArgs(3), u),
			func(llm.Request) llm.Response { return finalResponse("done") },
		}
	}
	on, onAdapter := ckptReminderSession(t, true, ckptReminderCost(), steps())
	ckptReminderRun(t, on)
	off, offAdapter := ckptReminderSession(t, false, ckptReminderCost(), steps())
	ckptReminderRun(t, off)

	onReqs, offReqs := onAdapter.Requests(), offAdapter.Requests()
	if len(onReqs) != len(offReqs) {
		t.Fatalf("flag-on run made %d requests, flag-off made %d", len(onReqs), len(offReqs))
	}
	if got := ckptReminderCountInRequests(onReqs); got != 0 {
		t.Fatalf("%d requests carry a reminder with the gate failing, want 0", got)
	}
	for i := range onReqs {
		a, err := json.Marshal(onReqs[i].Messages)
		if err != nil {
			t.Fatalf("marshal on-request %d: %v", i, err)
		}
		b, err := json.Marshal(offReqs[i].Messages)
		if err != nil {
			t.Fatalf("marshal off-request %d: %v", i, err)
		}
		// Wall-clock noise is the only legitimate difference between two runs
		// taken at different instants: task-store timestamps and sub-
		// millisecond tool durations. Everything else must be byte-identical.
		noise := regexp.MustCompile(`(?:"(?:created_at|updated_at|completed_at)":"[^"]*",?|,"duration_ms":\d+)`)
		if got, want := noise.ReplaceAllString(string(a), ""), noise.ReplaceAllString(string(b), ""); got != want {
			t.Fatalf("request %d differs between gate-failing flag-on and flag-off runs:\n%s\n---\n%s", i, a, b)
		}
	}
	// The gate-failing run still observes the rate (its state may move) but
	// never issues a reminder.
	on.mu.Lock()
	issued := on.ckptRemindersIssued
	on.mu.Unlock()
	if issued != 0 {
		t.Fatalf("remindersIssued = %d with the gate failing, want 0", issued)
	}
	// The flag-off run leaves the mechanism state untouched: byte-identical
	// behavior means not even internal observation ran.
	off.mu.Lock()
	windowOpen, rateWindows := off.ckptWindowOpen, off.ckptRateWindows
	off.mu.Unlock()
	if windowOpen || rateWindows != 0 {
		t.Fatalf("flag-off run opened the rate window (%v/%d): the mechanism must be fully inert", windowOpen, rateWindows)
	}
}

// TestCheckpointReminder_SecondReminderDemandsLargerMargin pins the
// escalating-margin schedule behaviorally: a projected-savings ratio that
// beat the first reminder's 1.0× margin but lands under the second attempt's
// 1.5× margin produces a first reminder and no second one.
func TestCheckpointReminder_SecondReminderDemandsLargerMargin(t *testing.T) {
	t.Parallel()
	// 500 fresh + 1k cache-write → typical input 1.5k tokens. Rate 1
	// request/step. Four tasks:
	//   boundary 2: 2 remaining → savings 1·2·1.5k·$3/M = $0.009 vs
	//               rewrite 1k·$3.75/M = $0.00375 → ratio 2.4 > 1.0 → reminder #1.
	//   boundary 3: 1 remaining → savings $0.0045 → ratio 1.2: above the
	//               first margin, under the second → no reminder.
	u := ckptReminderUsage(500, 1_000)
	sess, adapter := ckptReminderSession(t, true, ckptReminderCost(), []func(req llm.Request) llm.Response{
		ckptReminderTaskStep("call_add", ckptReminderAddArgs(4), u),     // r1
		ckptReminderTaskStep("call_done1", ckptReminderDoneArgs(1), u),  // r2: boundary 1 (window opens)
		ckptReminderTaskStep("call_done2", ckptReminderDoneArgs(2), u),  // r3: boundary 2 → reminder #1 rides r4
		ckptReminderTaskStep("call_done3", ckptReminderDoneArgs(3), u),  // r4: carries reminder; boundary 3 → gate fails 1.5×
		ckptReminderTaskStep("call_done4", ckptReminderDoneArgs(4), u),  // r5: boundary 4 (0 remaining)
		func(llm.Request) llm.Response { return finalResponse("done") }, // r6
	})
	ckptReminderRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 6 {
		t.Fatalf("scripted adapter saw %d requests, want 6", len(reqs))
	}
	if got := ckptReminderOccurrences(reqs[3]); got != 1 {
		t.Fatalf("the request after the first gate pass carries %d reminder turns, want 1", got)
	}
	if got := ckptReminderOccurrences(reqs[4]); got != 1 {
		t.Fatalf("the request after the second attempt carries %d reminder turns, want 1", got)
	}
	if got := ckptReminderOccurrences(reqs[5]); got != 1 {
		t.Fatalf("the final request carries %d reminder turns, want 1: the second attempt failed the 1.5x margin and must not have injected another", got)
	}
	sess.mu.Lock()
	issued := sess.ckptRemindersIssued
	sess.mu.Unlock()
	if issued != 1 {
		t.Fatalf("remindersIssued = %d, want 1 (the second attempt must not escalate the count)", issued)
	}
}

// TestCheckpointReminder_AgentElectsCompactionThroughCompactTool pins the
// election path: after a reminder, the agent calling its existing
// compact_context tool compacts exactly as it does today — the mechanism
// adds no compaction machinery of its own — and the published compaction
// re-arms the margin ladder.
func TestCheckpointReminder_AgentElectsCompactionThroughCompactTool(t *testing.T) {
	t.Parallel()
	u := ckptReminderUsage(1_000, 1_000)
	sess, adapter := ckptReminderSession(t, true, ckptReminderCost(), []func(req llm.Request) llm.Response{
		ckptReminderTaskStep("call_add", ckptReminderAddArgs(3), u),                                // r1
		ckptReminderTaskStep("call_done1", ckptReminderDoneArgs(1), u),                             // r2: boundary 1
		ckptReminderTaskStep("call_done2", ckptReminderDoneArgs(2), u),                             // r3: boundary 2 → reminder rides r4
		ckptReminderStep("call_compact", "compact_context", `{"note_to_self":"keep the plan"}`, u), // r4: agent elects compaction
		ckptReminderTaskStep("call_done3", ckptReminderDoneArgs(3), u),                             // r5: after compaction; boundary 3 (0 remaining)
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	ckptReminderRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 6 {
		t.Fatalf("scripted adapter saw %d requests, want 6", len(reqs))
	}
	if !ckptReminderRequestHasReminder(reqs[3]) {
		t.Fatal("the request preceding the election carries no reminder")
	}
	// The compaction happened through the existing tool's machinery: a real
	// compaction boundary now exists in the transcript.
	if n := ckptReminderBoundaryTurns(t, sess); n == 0 {
		t.Fatal("the agent's compact_context election produced no compaction boundary")
	}
	// The published compaction re-arms the escalating-margin ladder.
	sess.mu.Lock()
	issued := sess.ckptRemindersIssued
	sess.mu.Unlock()
	if issued != 0 {
		t.Fatalf("remindersIssued = %d after a published compaction, want 0 (margin ladder must re-arm)", issued)
	}
}

// TestCheckpointReminder_FlagOffNeverInjects pins the off-by-default
// contract: with the flag unset no step boundary injects a reminder, the
// transcript holds no checkpoint-reminder steering turn, and the mechanism's
// observation state never opens.
func TestCheckpointReminder_FlagOffNeverInjects(t *testing.T) {
	t.Parallel()
	u := ckptReminderUsage(1_000, 1_000)
	sess, adapter := ckptReminderSession(t, false, ckptReminderCost(), []func(req llm.Request) llm.Response{
		ckptReminderTaskStep("call_add", ckptReminderAddArgs(3), u),
		ckptReminderTaskStep("call_done1", ckptReminderDoneArgs(1), u),
		ckptReminderTaskStep("call_done2", ckptReminderDoneArgs(2), u),
		ckptReminderTaskStep("call_done3", ckptReminderDoneArgs(3), u),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	ckptReminderRun(t, sess)

	reqs := adapter.Requests()
	if got := ckptReminderCountInRequests(reqs); got != 0 {
		t.Fatalf("%d requests carry a checkpoint reminder with the flag off, want 0", got)
	}
	if turns := ckptReminderReminderTurns(t, sess); len(turns) != 0 {
		t.Fatalf("transcript holds %d checkpoint-reminder steering turns with the flag off, want 0", len(turns))
	}
	if n := ckptReminderBoundaryTurns(t, sess); n != 0 {
		t.Fatalf("transcript holds %d compaction boundaries with the flag off, want 0", n)
	}
	sess.mu.Lock()
	windowOpen, rateWindows, issued := sess.ckptWindowOpen, sess.ckptRateWindows, sess.ckptRemindersIssued
	sess.mu.Unlock()
	if windowOpen || rateWindows != 0 || issued != 0 {
		t.Fatalf("flag-off mechanism state = window %v / %d windows / %d issued, want untouched zeros", windowOpen, rateWindows, issued)
	}
}

// TestCheckpointReminder_NoPricingNoReminder pins the degrade path: with no
// pricing row for the session's model the cost gate cannot be computed, so
// the mechanism injects nothing and computes nothing — no panic, no reminder.
func TestCheckpointReminder_NoPricingNoReminder(t *testing.T) {
	t.Parallel()
	u := ckptReminderUsage(1_000, 1_000)
	sess, adapter := ckptReminderSession(t, true, nil, []func(req llm.Request) llm.Response{
		ckptReminderTaskStep("call_add", ckptReminderAddArgs(3), u),
		ckptReminderTaskStep("call_done1", ckptReminderDoneArgs(1), u),
		ckptReminderTaskStep("call_done2", ckptReminderDoneArgs(2), u),
		ckptReminderTaskStep("call_done3", ckptReminderDoneArgs(3), u),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	ckptReminderRun(t, sess)

	reqs := adapter.Requests()
	if got := ckptReminderCountInRequests(reqs); got != 0 {
		t.Fatalf("%d requests carry a checkpoint reminder with no pricing data, want 0", got)
	}
	sess.mu.Lock()
	issued := sess.ckptRemindersIssued
	sess.mu.Unlock()
	if issued != 0 {
		t.Fatalf("remindersIssued = %d with no pricing data, want 0", issued)
	}
}

// TestCheckpointReminder_NoCacheWriteAccountingNoReminder pins the
// accounting leg of the degrade rule: the rewrite cost is computed from the
// cache-write token accounting, so a session whose recent requests reported
// no cache-write tokens (the OpenAI-protocol adapters report none today)
// cannot compute the gate and gets no reminder — even with pricing present
// and savings that would otherwise pass.
func TestCheckpointReminder_NoCacheWriteAccountingNoReminder(t *testing.T) {
	t.Parallel()
	u := llm.Usage{InputTokens: 1_000} // no CacheWriteTokens reported
	sess, adapter := ckptReminderSession(t, true, ckptReminderCost(), []func(req llm.Request) llm.Response{
		ckptReminderTaskStep("call_add", ckptReminderAddArgs(3), u),
		ckptReminderTaskStep("call_done1", ckptReminderDoneArgs(1), u),
		ckptReminderTaskStep("call_done2", ckptReminderDoneArgs(2), u),
		ckptReminderTaskStep("call_done3", ckptReminderDoneArgs(3), u),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	ckptReminderRun(t, sess)

	reqs := adapter.Requests()
	if got := ckptReminderCountInRequests(reqs); got != 0 {
		t.Fatalf("%d requests carry a checkpoint reminder with no cache-write accounting, want 0", got)
	}
	sess.mu.Lock()
	issued := sess.ckptRemindersIssued
	sess.mu.Unlock()
	if issued != 0 {
		t.Fatalf("remindersIssued = %d with no cache-write accounting, want 0", issued)
	}
}

// TestCheckpointReminder_NoCacheWriteRateNoReminder pins the third
// uncomputable-gate leg: a pricing row without a cache-write rate cannot
// price the rewrite, so no reminder — the savings side alone is never
// enough.
func TestCheckpointReminder_NoCacheWriteRateNoReminder(t *testing.T) {
	t.Parallel()
	u := ckptReminderUsage(1_000, 1_000)
	sess, adapter := ckptReminderSession(t, true, &registry.Cost{Input: 3, Output: 15}, []func(req llm.Request) llm.Response{
		ckptReminderTaskStep("call_add", ckptReminderAddArgs(3), u),
		ckptReminderTaskStep("call_done1", ckptReminderDoneArgs(1), u),
		ckptReminderTaskStep("call_done2", ckptReminderDoneArgs(2), u),
		ckptReminderTaskStep("call_done3", ckptReminderDoneArgs(3), u),
		func(llm.Request) llm.Response { return finalResponse("done") },
	})
	ckptReminderRun(t, sess)

	reqs := adapter.Requests()
	if got := ckptReminderCountInRequests(reqs); got != 0 {
		t.Fatalf("%d requests carry a checkpoint reminder with no cache-write rate, want 0", got)
	}
}

// TestCheckpointReminder_SnapshotRoundTrip pins that CheckpointReminder
// persists through the toSnapshot/configFromSnapshot converter pair and
// defaults to off.
func TestCheckpointReminder_SnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	in := SessionConfig{CheckpointReminder: true}
	out := configFromSnapshot(in.toSnapshot().Clone())
	if !out.CheckpointReminder {
		t.Fatal("CheckpointReminder did not survive the snapshot round trip: delegates and restored sessions would silently run without the reminder")
	}
	off := SessionConfig{}
	if configFromSnapshot(off.toSnapshot()).CheckpointReminder {
		t.Fatal("default-off flag turned on by the snapshot round trip")
	}
}

// TestCheckpointReminderMarginSchedule pins the escalating margin schedule
// exactly: the first reminder needs savings above 1.0× the rewrite cost,
// the second above 1.5×, the third and every later one above 2.0× — capped.
func TestCheckpointReminderMarginSchedule(t *testing.T) {
	t.Parallel()
	for _, w := range []struct {
		issued int
		margin float64
	}{
		{0, 1.0},
		{1, 1.5},
		{2, 2.0},
		{3, 2.0},
		{9, 2.0},
	} {
		if got := checkpointReminderMargin(w.issued); got != w.margin {
			t.Errorf("checkpointReminderMargin(%d) = %v, want %v", w.issued, got, w.margin)
		}
	}
}

// TestCheckpointReminderGateFormula pins the projection formula numerically:
// savings = rate × remaining × typical input tokens priced at the input rate
// (through llm.EstimateCost); rewrite = recent cache-write tokens at the
// cache-write rate; the gate is savings > margin × rewrite.
func TestCheckpointReminderGateFormula(t *testing.T) {
	t.Parallel()
	price, ok := llm.PriceFromCost(ckptReminderCost())
	if !ok || price.CacheCreation5mPerM == nil {
		t.Fatalf("injected cost row did not resolve to a cache-write rate: %+v", price)
	}
	in := checkpointReminderGateInput{
		rate:           2,
		remaining:      3,
		typicalInput:   1000,
		cacheWrite:     500,
		cacheWriteSeen: true,
		price:          price,
	}
	// savings = 2·3·1000·$3/M = $0.018; rewrite = 500·$3.75/M = $0.001875.
	pass, savings, rewrite := in.gatePass()
	if want := 0.018; absF(savings-want) > 1e-12 {
		t.Fatalf("savings = %v, want %v", savings, want)
	}
	if want := 0.001875; absF(rewrite-want) > 1e-12 {
		t.Fatalf("rewrite = %v, want %v", rewrite, want)
	}
	if !pass {
		t.Fatal("gate failed with savings 9.6x the rewrite cost and margin 1.0")
	}
	// Same numbers at margin 1.5 (one reminder already issued): ratio 9.6
	// still passes; shrink the projected savings below the margin and it
	// must fail.
	in.remindersIssued = 1
	if pass, _, _ := in.gatePass(); !pass {
		t.Fatal("gate failed at ratio 9.6 under the 1.5x margin")
	}
	in.typicalInput = 100 // savings = 2·3·100·$3/M = $0.0018 → ratio 0.96
	if pass, _, _ := in.gatePass(); pass {
		t.Fatal("gate passed with savings 0.96x the rewrite cost under the 1.5x margin")
	}
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
