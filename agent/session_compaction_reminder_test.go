package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all" // register the real chatcompletions protocol for the live end-to-end session
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
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
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

// TestCheckpointReminder_StartOnlyUpdateIsNotACompletionBoundary pins the
// completion: it must produce no reminder and must not disturb the rate
// window. The script starts task 1 explicitly before completing anything,
// so if the start-only mutation reached the completion hook it would open
// the rate window one round early, shift the first reminder onto the wrong
// request, and over-close the window count.
func TestCheckpointReminder_StartOnlyUpdateIsNotACompletionBoundary(t *testing.T) {
	t.Parallel()
	u := ckptReminderUsage(1_000, 1_000)
	sess, adapter := ckptReminderSession(t, true, ckptReminderCost(), []func(req llm.Request) llm.Response{
		ckptReminderTaskStep("call_add", ckptReminderAddArgs(3), u),                            // r1: add 3 tasks
		ckptReminderTaskStep("call_start1", `{"update":[{"id":1,"status":"in_progress"}]}`, u), // r2: START-ONLY — not a boundary
		ckptReminderTaskStep("call_done1", ckptReminderDoneArgs(1), u),                         // r3: boundary 1 (window opens)
		ckptReminderTaskStep("call_done2", ckptReminderDoneArgs(2), u),                         // r4: boundary 2 → reminder rides r5
		ckptReminderTaskStep("call_done3", ckptReminderDoneArgs(3), u),                         // r5: boundary 3 (0 remaining)
		func(llm.Request) llm.Response { return finalResponse("done") },                        // r6
	})
	ckptReminderRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 6 {
		t.Fatalf("scripted adapter saw %d requests, want 6", len(reqs))
	}
	// With the start-only mutation treated as a boundary, the first
	// reminder would fire one round early and ride r4.
	if got := ckptReminderOccurrences(reqs[3]); got != 0 {
		t.Fatalf("request r4 carries %d reminder turns, want 0: a start-only update is not a step completion", got)
	}
	if got := ckptReminderOccurrences(reqs[4]); got != 1 {
		t.Fatalf("request r5 carries %d reminder turns, want 1 (the second completion's reminder)", got)
	}
	if got := ckptReminderOccurrences(reqs[5]); got != 1 {
		t.Fatalf("the final request carries %d reminder turns, want exactly 1", got)
	}
	// The rate window observed exactly three completion rounds: one open
	// (r3) and two closes (r4, r5). The start-only r2 contributed nothing.
	sess.mu.Lock()
	windowOpen, rateWindows, issued := sess.ckptWindowOpen, sess.ckptRateWindows, sess.ckptRemindersIssued
	sess.mu.Unlock()
	if !windowOpen || rateWindows != 2 || issued != 1 {
		t.Fatalf("mechanism state = window %v / %d closed windows / %d issued, want open / 2 / 1 (the start-only round must not touch the window)", windowOpen, rateWindows, issued)
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

// TestCheckpointReminder_BatchCompletionIsOneWindowClose pins the rate
// window's batch semantics: one task_list update completing several steps
// is ONE step-completion boundary — one rate-window open or close per
// completing call, not per completed step. The batch call below finishes
// two tasks at once, so the window count after three completing calls must
// be two closes plus one open, and the projected rate stays per-boundary.
func TestCheckpointReminder_BatchCompletionIsOneWindowClose(t *testing.T) {
	t.Parallel()
	u := ckptReminderUsage(1_000, 1_000)
	sess, adapter := ckptReminderSession(t, true, ckptReminderCost(), []func(req llm.Request) llm.Response{
		ckptReminderTaskStep("call_add", ckptReminderAddArgs(4), u),                                             // r1: add 4 tasks
		ckptReminderTaskStep("call_batch", `{"update":[{"id":1,"status":"done"},{"id":2,"status":"done"}]}`, u), // r2: boundary 1 — ONE window open for two finished steps
		ckptReminderTaskStep("call_done3", ckptReminderDoneArgs(3), u),                                          // r3: boundary 2 → gate: rate 1, 1 remaining → reminder rides r4
		ckptReminderTaskStep("call_done4", ckptReminderDoneArgs(4), u),                                          // r4: boundary 3 (0 remaining)
		func(llm.Request) llm.Response { return finalResponse("done") },                                         // r5
	})
	ckptReminderRun(t, sess)

	reqs := adapter.Requests()
	if len(reqs) != 5 {
		t.Fatalf("scripted adapter saw %d requests, want 5", len(reqs))
	}
	if got := ckptReminderOccurrences(reqs[3]); got != 1 {
		t.Fatalf("request r4 carries %d reminder turns, want 1 (the second boundary's reminder)", got)
	}
	if got := ckptReminderOccurrences(reqs[4]); got != 1 {
		t.Fatalf("the final request carries %d reminder turns, want exactly 1", got)
	}
	// Three completing calls → one open + two closes. A per-step close
	// would count the batch's two finishes as two windows: 3, not 2.
	sess.mu.Lock()
	windowOpen, rateWindows := sess.ckptWindowOpen, sess.ckptRateWindows
	sess.mu.Unlock()
	if !windowOpen || rateWindows != 2 {
		t.Fatalf("rate window = open %v / %d closed windows, want open / 2 (one close per completing CALL)", windowOpen, rateWindows)
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

// --- End-to-end over the real OpenAI chat protocol ---

// The tests above drive the scripted fake adapter, so their usage never
// crosses a provider parser. These two drive a live session over the REAL
// chatcompletions protocol at an httptest SSE server, pinning the whole
// path the free models ride: provider usage payload → ParseChatUsage →
// recorded cache-write accounting → the reminder gate.

// ckptChatUsage builds the per-request chat usage payload: 2000 prompt
// tokens of which 1000 were cache hits; with miss, the non-cached 1000 are
// reported as DeepSeek's prompt_cache_miss_tokens — the automatic cache's
// newly written suffix. Recorded totals per request either way: input 1000,
// cache read 1000, cache write 1000 (with the field) — gate ratio 2.4 at the
// second boundary with 1 remaining step.
func ckptChatUsage(miss bool) map[string]any {
	usage := map[string]any{
		"prompt_tokens":         2000,
		"completion_tokens":     100,
		"total_tokens":          2100,
		"prompt_tokens_details": map[string]any{"cached_tokens": 1000},
	}
	if miss {
		usage["prompt_cache_miss_tokens"] = 1000
	}
	return usage
}

// ckptChatSSEStep renders one scripted assistant response as an SSE body:
// a single tool-call delta (full arguments in one fragment), a
// finish_reason chunk, a usage chunk, and the DONE terminator.
func ckptChatSSEStep(t *testing.T, callID, tool string, args map[string]any, usage map[string]any) string {
	t.Helper()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal tool args: %v", err)
	}
	chunk := func(payload map[string]any) string {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal SSE chunk: %v", err)
		}
		return "data: " + string(raw) + "\n\n"
	}
	var b strings.Builder
	b.WriteString(chunk(map[string]any{
		"id": "c1", "model": "gpt-5.2",
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"index": 0, "id": callID, "type": "function",
					"function": map[string]any{"name": tool, "arguments": string(argsJSON)},
				}},
			},
		}},
	}))
	b.WriteString(chunk(map[string]any{
		"id":      "c1",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
	}))
	b.WriteString(chunk(map[string]any{"id": "c1", "choices": []any{}, "usage": usage}))
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// ckptChatSteps scripts the five responses of the standard 3-task flow.
func ckptChatSteps(t *testing.T, usage map[string]any) []string {
	t.Helper()
	adds := []any{}
	for range 3 {
		adds = append(adds, map[string]any{"type": "implement", "description": "step", "prompt": "do it"})
	}
	done := func(id int) map[string]any {
		return map[string]any{"update": []any{map[string]any{"id": id, "status": "done"}}}
	}
	return []string{
		ckptChatSSEStep(t, "call_add", "task_list", map[string]any{"add": adds}, usage),
		ckptChatSSEStep(t, "call_done1", "task_list", done(1), usage),
		ckptChatSSEStep(t, "call_done2", "task_list", done(2), usage),
		ckptChatSSEStep(t, "call_done3", "task_list", done(3), usage),
		ckptChatSSEStep(t, "call_final", "communicate", map[string]any{
			"message": "done", "end_turn": true,
			"output": map[string]any{"message": "", "data": map[string]any{}, "artifacts": []any{}},
		}, usage),
	}
}

// ckptChatServer is the scripted chat endpoint plus a request-body capture.
type ckptChatServer struct {
	mu     sync.Mutex
	bodies [][]byte
	i      int
	steps  []string
}

// requests returns the captured request bodies in arrival order.
func (s *ckptChatServer) requests() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte{}, s.bodies...)
}

// bodyText flattens one captured request body.
func ckptChatBodyText(body []byte) string {
	return string(body)
}

// ckptChatSession builds the live end-to-end fixture: a chat-protocol
// instance over an httptest SSE server, the loop's pricing injected through
// WithResolved, and the namer's cheap route pinned off the main transport.
func ckptChatSession(t *testing.T, miss bool) (*Session, *ckptChatServer) {
	t.Helper()
	cs := &ckptChatServer{steps: ckptChatSteps(t, ckptChatUsage(miss))}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Session start lists the instance's models with a bodyless GET.
		// Leave that listing unavailable: a successful listing makes
		// NewSession's live-model fill re-resolve the profile from the
		// registry, which would discard the injected pricing row the gate
		// needs (the fail-open path keeps the caller's profile), and a
		// served list would consume one of the scripted chat steps. A 404
		// is a permanent, non-retried error.
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		cs.mu.Lock()
		cs.bodies = append(cs.bodies, body)
		step := cs.steps[min(cs.i, len(cs.steps)-1)]
		cs.i++
		cs.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, step)
	}))
	t.Cleanup(srv.Close)

	clientDir := t.TempDir()
	client := registryClientAt(t, clientDir, map[string]registry.Provider{
		"ckptchat": {
			Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
			APIKey: "test", Transport: registry.Transport{BaseURL: srv.URL + "/v1"},
		},
	}, []string{"ckptchat"})
	profile := resolveClientProfile(t, client, "ckptchat/gpt-5.2")
	res := profile.Resolved()
	res.Caps.Cost = ckptReminderCost()
	profile = withTestSessionNamer(client, profile.WithResolved(res))

	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir:           newBucket(t),
		CheckpointReminder: true,
		MaxSubagentDepth:   1,
		NoProjectPrompts:   true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	drainSessionEvents(sess)
	return sess, cs
}

// TestCheckpointReminder_ChatProtocolCacheMissPassesGate proves the free
// models' whole path end-to-end: an OpenAI-chat session whose provider
// reports DeepSeek's prompt_cache_miss_tokens gets the recorded cache-write
// accounting the gate needs, passes it, and sees the reminder ride the next
// request — the request bodies, not a scripted adapter.
func TestCheckpointReminder_ChatProtocolCacheMissPassesGate(t *testing.T) {
	t.Parallel()
	sess, cs := ckptChatSession(t, true)
	ckptReminderRun(t, sess)

	bodies := cs.requests()
	if len(bodies) != 5 {
		t.Fatalf("chat server saw %d requests, want 5", len(bodies))
	}
	if strings.Contains(ckptChatBodyText(bodies[2]), ckptReminderMarker) {
		t.Fatal("reminder rode the request after the FIRST completion; the observed rate has no basis yet")
	}
	third := ckptChatBodyText(bodies[3])
	if !strings.Contains(third, ckptReminderMarker) {
		t.Fatal("the request after the gate-passing completion carries no reminder: the chat parser did not surface prompt_cache_miss_tokens as cache-write accounting")
	}
	// The wire body is JSON, so the envelope's < > arrive HTML-escaped.
	if !strings.Contains(third, "compact_context") || !strings.Contains(third, `\u003cSYSTEM-REMINDER`) {
		t.Fatalf("reminder on the wire lacks its envelope or the compact_context name:\n%s", third)
	}
	if got := strings.Count(ckptChatBodyText(bodies[4]), ckptReminderMarker); got != 1 {
		t.Fatalf("the final request carries %d reminder turns, want exactly 1 (no second injection)", got)
	}
}

// TestCheckpointReminder_ChatProtocolWithoutCacheMissStaysSilent pins the
// same end-to-end path without the field: a provider that reports only
// cached_tokens (no prompt_cache_miss_tokens) leaves the rewrite cost
// uncomputable, so the gate stays silent even with pricing present.
func TestCheckpointReminder_ChatProtocolWithoutCacheMissStaysSilent(t *testing.T) {
	t.Parallel()
	sess, cs := ckptChatSession(t, false)
	ckptReminderRun(t, sess)

	bodies := cs.requests()
	if len(bodies) != 5 {
		t.Fatalf("chat server saw %d requests, want 5", len(bodies))
	}
	for i, body := range bodies {
		if strings.Contains(ckptChatBodyText(body), ckptReminderMarker) {
			t.Fatalf("request %d carries a reminder with no prompt_cache_miss_tokens in the provider usage, want silence", i)
		}
	}
	sess.mu.Lock()
	issued := sess.ckptRemindersIssued
	sess.mu.Unlock()
	if issued != 0 {
		t.Fatalf("remindersIssued = %d with no cache-write accounting on the wire, want 0", issued)
	}
}
