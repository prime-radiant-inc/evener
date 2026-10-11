# Delegate Messaging Implementation Plan

Status: Implemented. Approved by Jesse 2026-10-10. Merged PRs:

- #4244 journal records updates
- #4246 frames name their kind
- #4248 a delegate doesn't wake again for attention it saw (#3725)
- #4274 and #4284 attention stop/clear follow-ups
- #4252 delegate guidance
- #4268 `communicate(end_turn=false)` delivers updates (closes #4172)
- #4294 drain recovery
- #4295 compaction keeps pending attention turns
- #4299 `delegate_send` reaches only your own delegates
- #4305 steering provenance removed
- #4307 parent steers labelled from the parent

The live micro-test PR (PR 8) was skipped.

This plan is the historical record. Later rulings live in the merged PRs.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A delegate reaches its parent only through `communicate` (`end_turn=false` is an update the parent receives and may be woken by, `end_turn=true` the report), `delegate_send` reaches only your own delegates, and a parent's steer into a running child is labelled as from the parent.

**Architecture:** An update is a second kind of pending delivery in the delegate's own journal (`delegate_update_posted`), queued in the same per-delegate FIFO as the generation's report, so it rides the report's existing durable path unchanged: head-only dispatch, receiver-transcript attention, consumed markers, wake rules, the `ask_user` hold, the deferred-while-processing hold and the carry into a `delegate_send` wait's reply, and restart replay. The frame gains a `kind` attribute (`update` or `report`). `SteerCaller` and the `to="caller"` branch are deleted. A parent's steer gets steering source `parent`, an attribution line, and a "From parent" label in both clients.

**Tech Stack:** Go (agent and root modules), the delegate journal (`agent/internal/delegatestore`), TypeScript (`appwire-client/typescript`, the hub frontend, `mobile-native`), Bubble Tea TUI (`cmd/evener-tui`).

**Spec:** `docs/superpowers/specs/2026-10-10-delegate-messaging-design.md` (approved by Jesse 2026-10-10). Executors read the spec and this plan together.

## Global Constraints

- Work lands as a stack of tiny PRs, each opened with base `main`: 80–150 non-test lines, one mechanism each. A PR over ~400 lines needs Jesse's explicit OK. The PR table below says which tasks form which PR.
- Each PR is built in its own worktree (AGENTS.md worktree preference). A PR that depends on an unmerged predecessor branches from the predecessor's branch; once the predecessor squash-merges, `git rebase --onto origin/main <predecessor-branch>`, check your own lines are unchanged, and `git push --force-with-lease`. Open every PR with base `main` (a PR based on another branch gets no CI). Regular PRs, never drafts.
- TDD for every behavior change: watch each new test fail for the right reason before the code that makes it pass. Tests use real Evener code below a scripted provider at the LLM boundary. Default tests stay deterministic: no provider credentials, network, quota or model behavior.
- No tests that string-match source files. Prompt and tool-description text is pinned only by whole-output goldens (the `checkGolden` pattern in `agent/wire_fixture_test.go`, as `agent/testdata/memoryprompt` uses it). Frame markup, JSON keys, enum tokens and wire values are contract values and may be asserted exactly.
- Read `docs/product/README.md`, `docs/product/session-activity.md` and the S11/S12 rows of `docs/product/subsystems.md` before Task 1. AGENTS.md's session-activity rules apply: real producer, routing and client-lifetime coverage; the recorded producers in `agent/testdata/subagentwire` (and `notificationwire`/`systemeventwire` for steering items); any change to what the AppWire projector emits needs a `cmd/evener-tui` test case in the same PR.
- Evener prompts say "your human partner", never "the user". New prompt text for delegates says "your parent agent".
- No backward-compatibility code (spec decision 5). Old transcripts render as written; nothing is translated.
- Windows is unsupported: do not add or carry Windows-only checks or findings.
- Gates, per touched Go module (`agent` is its own module; the repo root module holds `appwire`, `cmd/*`, `server`, `internal`):
  - `go build ./...`, `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=linux go vet -tags evenerfuzz ./...`
  - toolchain gofmt: `"$(go env GOROOT)/bin/gofmt" -l <touched .go files>` prints nothing (never the Homebrew gofmt on PATH)
  - `golangci-lint run --allow-serial-runners ./...` in each touched module
  - targeted `go test -race -count=1 -run '<the PR's tests>'`
  - the full agent race run wrapped in the machine lock: `/Users/jesse/git/prime-radiant-inc/evener/.superpowers/bin/race-serial go test -race -count=1 ./...` from `agent/`
  - frontend changes: run Biome's autofix on every touched file first (`cd cmd/evener-hub/frontend && npx biome check --write <paths>`, which also covers files under `appwire-client/typescript`), then `make test-web` from the repo root; `appwire-client/typescript` changes also run `make test-api-package`; `mobile-native` changes run `make test-native`. Never run `npx biome` from the repo root, and never `npm ci` through a symlinked `node_modules`.
  - after touching `make/*.mk`: `go test -short -count=1 .` at the repo root (Makefile audits).
  - Set `-o pipefail` before gating anything on a piped command.
- Docs change in the same PR as the behavior they describe; product docs stay evergreen (present tense, no PR narrative).
- Merge gate per PR: every CI check green on the merged head; RoboRev reports nothing at Medium or higher unless refuted, and refutations go into code comments or tests (RoboRev does not read PR comments); `/simplify` run on the PR with its fixes pushed and re-reviewed, and the PR body states the `/simplify` outcome. Every RoboRev Low becomes a fast-follow PR after the parent merges. Use `/shepherd-pr` to drive each PR.
- Before every commit: `git diff --cached --stat`, and stage files by name.

## Rulings on spec ambiguities

Each ruling is what the tasks implement. Raise any you disagree with before Task 5.

1. **`delivered` and `queued`.** The spec glosses `queued` as "the parent is between turns". The implementation can only know where the update sits when the call returns: `delivered` means it is durably in the parent's transcript as attention; `queued` means it is durably in the delegate's journal but behind something (the parent is mid-turn and holds it for its next model boundary, or an earlier delivery of the same delegate is ahead of it); it reaches the parent without anyone acting. `not_delivered` (with `reason`) means the journal refused it: the delegate is stopping or its turn is ending, its run is stale, the parent route is closed, or the session has no parent route at all.
2. **Idle parent delegate.** The spec's table says an idle parent is woken; its Testing list says the update "queues for an idle parent delegate and arrives on its next turn". Both hold: the update lands in the idle parent's transcript and its attention starts the parent's next generation. Tests assert the wake.
3. **Frame body escaping.** The update body is the delegate's message passed through `escapeNotificationBody` (only `<` becomes `&lt;`, as `<job-notification>` bodies are escaped), so the text can neither close the frame nor open another, and the parent's model reads `&&` and quotes as written. Clients decode with `decodeNotificationEntities`; a literal `&lt;` the delegate typed comes back as `<`, the same known remainder job excerpts have.
4. **`kind` on every terminal packet.** Report frames, including `terminal_error` packets (a failure is still the generation's report), carry `kind="report"`. Quiet-watchdog frames keep their current form with no `kind`.
5. **Which parent messages get the label.** Only `delegate_send` into a *running* child produces a steering turn, so only that path gets source `parent`, the attribution line and the "From parent" label. A `delegate_send` that starts an idle child, like the initial `delegate` prompt, opens the child's generation as user input, exactly as the brief does; clients render it as the brief. No client renders a source on user-input items, and a delegate never has a human partner, so nothing is misattributed.
6. **Where "From parent" lives.** The shared table in `steeringLabels.ts` is keyed by steering kind. The parent label is a source rule in the same module: `steeringLabel(kind, source)` returns "From parent" for source `parent`, and both clients pass the item's source.
7. **Provenance.** The caller route carried the sender's watch provenance into the parent; reports never did. Updates ride the report path and carry none. Observer loops stay bounded by the watch delivery budget, as they are for reports today. Once the caller route is gone, the steering-claim provenance plumbing it fed is dead and PR 6 removes it.
8. **#3725 interaction (resolved by Jesse, 2026-10-10).** An update reaching a *running* parent delegate is presented at its next model boundary, as the spec says. Under today's machinery that attention also earns the parent one successor attention generation after its run (#3725's mechanism), and updates would make it more frequent. Jesse's ruling: a delegate that has already seen the attention that was supposed to wake it must not wake again, as the root already behaves; this reverses the 2026-08-29 no-action decision. PR 3a (Task 4a) implements it and lands before PR 4, which builds on it: Task 7's running-coordinator test asserts that the update the coordinator saw earns it no successor generation.
9. **Wire corpora.** The update frame is recorded in `agent/testdata/notificationwire` (where every delegate frame the clients parse is recorded) and the parent steer in `agent/testdata/systemeventwire` (where daemon steers are). `agent/testdata/subagentwire` gets a run that posts an update before reporting, pinning that `delegates/list` and its `reportPreview` do not change.
10. **Updates after the report.** A `communicate(end_turn=false)` once the delegate's generation is settling (its terminal communicate already prepared) is refused as `not_delivered`; nothing can land behind the report of the same generation.
11. **Root `to="caller"`.** The root gets the same pointer error as a delegate (it used to get "caller is only available from a delegate").
12. **Root text unchanged.** The root's Reporting section and `communicate` description stay byte for byte as they are; the delegate variants are separate and both are pinned by goldens.
13. **Prompt PR before delivery PR.** PR 3 (guidance) merges immediately before PR 4 (delivery). Between them, a delegate told that updates reach its parent sees today's behavior (they go nowhere, #4172), which is no worse than now; the reverse order would let every narrated `end_turn=false` wake parents.

## PR stack

| PR | Title | Tasks | Est. non-test lines |
|---|---|---|---|
| 1 | The delegate journal records updates in delivery order | 1 | ~60 |
| 2 | Delegate notification frames name their kind; clients render updates | 2, 3 | ~80 |
| 3 | Delegate guidance: updates reach your parent, only when it needs to know or act | 4 | ~50 |
| 3a | A delegate does not wake again for attention its run already showed the model | 4a | ~55, mostly renames |
| 4 | communicate(end_turn=false) delivers a delegate's update to its parent | 5, 6, 7, 8 | ~130 |
| 5 | delegate_send reaches only your own delegates | 9 | ~140, mostly deletions |
| 6 | Drop the steering provenance only the caller route set | 10 | ~45, mostly deletions |
| 7 | A parent's steer is labelled as from the parent | 11, 12 | ~45 |
| 8 (optional) | Live micro-test: a delegate's question gets its parent's answer | 13 | 0 (eval-tagged test code) |

PR 3a closes #3725; it is independent of PRs 1–3 and lands before PR 4, preferably before PR 3 so ruling 13's order holds. PR 4 closes #4172. After PR 5 merges, confirm PR #4197 is closed, and if not, close it with a comment linking the spec.

## File structure

- `agent/internal/delegatestore/event.go`: new event kind `delegate_update_posted` and its `UpdatePosted` payload.
- `agent/internal/delegatestore/record.go`: `PacketUpdate` packet kind.
- `agent/internal/delegatestore/fold.go`: `applyUpdatePosted`, `UpdateDeliveryID`, envelope validation.
- `agent/internal/delegatestore/store.go`: `cloneEvent` copies the new payload.
- `agent/delegate_delivery.go`: frame `kind` attribute and update body; inline waiters are never claimed by an update head; controller `PostUpdate` and `deliveryPending`.
- `agent/session_tools_jobs.go`: an update carried in a `delegate_send` reply renders as an update.
- `agent/session_tools_communicate.go`: `Session.postDelegateUpdate`, the update result, the tool wiring.
- `agent/session_tool_registry.go`: `toolDeps.reportsToParent`, `toolDeps.postDelegateUpdate`.
- `agent/session_tools_ask.go`: `Session.reportsToParent`, the one predicate for "this session's messages go to a parent agent".
- `agent/internal/tool/definitions.go`: `WithCommunicateDelegateUpdates`, observer guidance, `DefDelegateSend` without `caller`.
- `agent/prompts/system.md.tmpl`: the delegate branch of the Reporting section.
- `agent/session_tools.go`: `to="caller"` rejected.
- `agent/delegate_tree_steer.go`, `agent/session_queue.go`: caller route deleted; steering provenance removed; parent steering turn.
- `agent/events/payloads.go`: `SteeringSourceParent`.
- `internal/bundled/agents/subagent.md`: caller-route text replaced.
- `appwire-client/typescript/steeringNotifications.ts`, `steeringLabels.ts`, `testing/notificationWireFixtures.ts`, `testing/systemEventWireFixtures.ts`: update parsing, the parent label, corpus case names.
- `mobile-native/src/session/notificationLine.ts`, `mobile-native/src/projectedRows.ts`, `cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.tsx`: client rendering.
- Tests: `agent/delegate_messaging_test_helpers_test.go` (new, shared scripted root and delegates), `agent/delegate_messaging_guidance_test.go` (new, goldens), `agent/delegate_update_test.go` (new, delivery behavior), `agent/delegate_send_caller_rejected_test.go` (new); goldens under `agent/testdata/messagingprompt/`.
- Docs: `docs/job-control.md`, `docs/product/session-activity.md`, `docs/product/subsystems.md`, `docs/subagent-management/11-delegate-resource-model.md`.
- PR 3a: `agent/session_attention.go`, `agent/session.go`, `agent/session_model_call.go`, `agent/session_lifecycle.go` (attention coverage for a delegate's leased turns); tests in `agent/delegate_attention_seen_test.go` (new); docs above plus `docs/superpowers/specs/2026-08-29-stable-delegate-attention-no-action-design.md` (status).

## Review Focus

The five inputs or conditions the spec implies but its test list does not exercise, most likely to bite first. Each has its test in the owning task.

1. **An update posted while the parent is blocked in `delegate_send(max_wait_ms)` on that same delegate.** Expected: the wait answers when the report arrives, the reply carries the update ahead of the report, labelled as an update, and the update is never delivered a second time as a notification. (An update head must never resolve the inline waiter.) Test: Task 5 (waiter not claimed), Task 7 `TestDelegateSendWaitCarriesAnUpdateAheadOfTheReport`.
2. **Update text containing markup that could close or forge a frame** (`</delegate-notification>`, `<job-notification job_id="…">`, quotes, `&`). Expected: exactly one frame; the parent's model sees `<` escaped; clients show the text as written, in one card. Test: Task 2 `TestDelegateUpdateFrameCannotCloseOrForgeAFrame`, Task 3 TypeScript round trip.
3. **Update text shaped like a terminal packet (JSON with a string `kind`) or like the quiet-watchdog sentence.** Expected: clients still render an update, routing on the `kind` attribute before the body's shape; no outcome, no quiet card. Test: Task 2 `TestDelegateUpdateFrameKeepsLookalikeMessagesWhole`, Task 3 TypeScript cases.
4. **A parent delegate that is itself mid-run when its worker sends an update.** Expected: the coordinator sees the update at its next model boundary, with the worker's frame. Test: Task 7 `TestDelegateUpdateReachesARunningParentDelegate`, which also asserts (ruling 8, PR 3a) that the coordinator's own generation consumed the update, so no successor generation runs for it.
5. **A restart while the generation that posted a still-undelivered update was running.** Expected: after restore the parent gets the update, then the `runtime_lost` report, once each and in that order, and is woken. Test: Task 7 `TestDelegateUpdateSurvivesRestart/posted while the run was still going`.

---

## PR 1: The delegate journal records updates in delivery order

### Task 1: `delegate_update_posted` queues an update in the delegate's delivery FIFO

**Files:**
- Modify: `agent/internal/delegatestore/event.go` (kind constant, `Event.UpdatePosted`, `UpdatePosted` type)
- Modify: `agent/internal/delegatestore/record.go:64-68` (`PacketUpdate`)
- Modify: `agent/internal/delegatestore/fold.go` (`applyEvent`, `applyUpdatePosted`, `UpdateDeliveryID`, `validateEventEnvelope`; add `strconv` and `strings` imports)
- Modify: `agent/internal/delegatestore/store.go:351-391` (`cloneEvent`)
- Modify: `agent/internal/delegatestore/envelope_fuzz_test.go` (kind table)
- Test: `agent/internal/delegatestore/fold_test.go`, `agent/internal/delegatestore/store_test.go`
- Docs: `docs/subagent-management/11-delegate-resource-model.md` (event kinds list, delivery rules)

**Interfaces:**
- Consumes: nothing new.
- Produces (later tasks rely on these exact names):
  - `delegatestore.EventDelegateUpdatePosted EventKind = "delegate_update_posted"`
  - `delegatestore.Event.UpdatePosted *delegatestore.UpdatePosted` (JSON `update_posted`)
  - `type delegatestore.UpdatePosted struct { Generation uint64; Message string }`
  - `delegatestore.PacketUpdate PacketKind = "update"`
  - `func delegatestore.UpdateDeliveryID(delegateID string, seq uint64) string` returning `<delegateID>/update/<seq>`
  - Folding an `UpdatePosted` appends `PendingDelivery{DeliveryID: UpdateDeliveryID(id, event.Seq), Generation, OwnerDelegateID: Descriptor.ParentDelegateID, Packet: TerminalPacket{Kind: PacketUpdate, Message: <JSON string of the message>}}`.

- [ ] **Step 1: Create the worktree for PR 1**

```bash
git -C /Users/jesse/git/prime-radiant-inc/evener fetch origin main
git -C /Users/jesse/git/prime-radiant-inc/evener worktree add -b claude/delegate-update-journal \
  /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-update-journal origin/main
```

Work in that worktree for the rest of this task. Read the spec and this plan from branch `claude/delegate-messaging` (`.claude/worktrees/delegate-messaging`) until they land on `main`; do not copy them into the PR.

- [ ] **Step 2: Write the failing fold and store tests**

Append to `agent/internal/delegatestore/fold_test.go`:

```go
func updatePostedEvent(id string, generation uint64, message string) Event {
	return Event{
		Kind:         EventDelegateUpdatePosted,
		DelegateID:   id,
		UpdatePosted: &UpdatePosted{Generation: generation, Message: message},
	}
}

// A running generation's updates queue in the delegate's own delivery FIFO,
// in the order posted and ahead of the report the generation settles, each
// under an id its journal sequence makes unique.
func TestApplyUpdatePostedQueuesAheadOfTheGenerationReport(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_parent", ""),
		createdEvent("dlg_alpha", "dlg_parent"),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		updatePostedEvent("dlg_alpha", 1, "first question"),
		updatePostedEvent("dlg_alpha", 1, "second question"),
		preparedEvent("dlg_alpha", 1, reportedPacket("done")),
		finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", nil),
	)

	got := state["dlg_alpha"].PendingDeliveries
	want := []PendingDelivery{
		{DeliveryID: "dlg_alpha/update/4", Generation: 1, OwnerDelegateID: "dlg_parent", Packet: TerminalPacket{Kind: PacketUpdate, Message: json.RawMessage(`"first question"`)}},
		{DeliveryID: "dlg_alpha/update/5", Generation: 1, OwnerDelegateID: "dlg_parent", Packet: TerminalPacket{Kind: PacketUpdate, Message: json.RawMessage(`"second question"`)}},
	}
	if len(got) != 3 || !reflect.DeepEqual(got[:2], want) || got[2].DeliveryID != "dlg_alpha/delivery/1" {
		t.Fatalf("pending deliveries = %#v, want both updates in order, then the report", got)
	}
	if UpdateDeliveryID("dlg_alpha", 4) != "dlg_alpha/update/4" {
		t.Fatalf("UpdateDeliveryID = %q", UpdateDeliveryID("dlg_alpha", 4))
	}
	// An update is never the generation's outcome.
	if latest := state["dlg_alpha"].LatestPacket; latest == nil || latest.Kind != PacketReported {
		t.Fatalf("latest packet = %#v, want the report", latest)
	}
}

// An acknowledged update leaves the queue like any head.
func TestApplyUpdatePostedIsAcknowledgedAsTheHead(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		updatePostedEvent("dlg_alpha", 1, "question"),
		Event{Kind: EventDelegateDeliveryAcknowledged, DelegateID: "dlg_alpha", DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "dlg_alpha/update/3"}},
	)
	if got := state["dlg_alpha"].PendingDeliveries; len(got) != 0 {
		t.Fatalf("pending deliveries = %#v, want the acknowledged update gone", got)
	}
}

// Only the open, running generation posts updates: nothing can land behind
// its report, under a stop, or for a generation that is over.
func TestApplyUpdatePostedRefusesOutsideTheRunningGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix []Event
		event  Event
	}{
		{"no run", []Event{createdEvent("dlg_alpha", "")}, updatePostedEvent("dlg_alpha", 1, "q")},
		{"stale generation", []Event{
			createdEvent("dlg_alpha", ""),
			startedEvent("dlg_alpha", 1, TriggerOwnerInput),
			preparedEvent("dlg_alpha", 1, reportedPacket("done")),
			finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", nil),
			startedEvent("dlg_alpha", 2, TriggerOwnerInput),
		}, updatePostedEvent("dlg_alpha", 1, "q")},
		{"settling", []Event{
			createdEvent("dlg_alpha", ""),
			startedEvent("dlg_alpha", 1, TriggerOwnerInput),
			preparedEvent("dlg_alpha", 1, reportedPacket("done")),
		}, updatePostedEvent("dlg_alpha", 1, "q")},
		{"stopping", []Event{
			createdEvent("dlg_alpha", ""),
			startedEvent("dlg_alpha", 1, TriggerOwnerInput),
			stopRequestedEvent("dlg_alpha"),
		}, updatePostedEvent("dlg_alpha", 1, "q")},
		{"empty message", []Event{
			createdEvent("dlg_alpha", ""),
			startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		}, updatePostedEvent("dlg_alpha", 1, "  ")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := applyEvents(t, tc.prefix...)
			before := stateJSON(t, state)
			event := tc.event
			event.Seq = uint64(len(tc.prefix) + 1)
			if err := Apply(state, event); err == nil {
				t.Fatal("Apply accepted the update")
			}
			if got := stateJSON(t, state); got != before {
				t.Fatalf("refused update changed state:\n got %s\nwant %s", got, before)
			}
		})
	}
	t.Run("zero sequence", func(t *testing.T) {
		state := applyEvents(t, createdEvent("dlg_alpha", ""), startedEvent("dlg_alpha", 1, TriggerOwnerInput))
		if err := Apply(state, updatePostedEvent("dlg_alpha", 1, "q")); err == nil {
			t.Fatal("Apply accepted an update with no sequence to name it by")
		}
	})
}
```

Append to `agent/internal/delegatestore/store_test.go`:

```go
func TestCloneEventUpdatePostedIsolated(t *testing.T) {
	original := Event{
		Kind:         EventDelegateUpdatePosted,
		DelegateID:   "dlg_alpha",
		UpdatePosted: &UpdatePosted{Generation: 1, Message: "question"},
	}
	clone := cloneEvent(original)
	if clone.UpdatePosted == original.UpdatePosted {
		t.Fatal("clone aliased UpdatePosted payload")
	}
	clone.UpdatePosted.Message = "changed"
	if original.UpdatePosted.Message != "question" {
		t.Fatal("mutating clone changed original UpdatePosted payload")
	}
}

// An update survives the journal: written, reopened and folded, it is the
// same pending delivery under the same id.
func TestStoreRoundTripsUpdatePosted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	appended, _, err := store.AppendBatch(make(State), []Event{
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		updatePostedEvent("dlg_alpha", 1, "which table?"),
	})
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	events, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	state, err := Fold(events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	pending := state["dlg_alpha"].PendingDeliveries
	if len(pending) != 1 || pending[0].DeliveryID != UpdateDeliveryID("dlg_alpha", appended[2].Seq) || pending[0].Packet.Kind != PacketUpdate || string(pending[0].Packet.Message) != `"which table?"` {
		t.Fatalf("folded pending deliveries = %#v", pending)
	}
}
```

- [ ] **Step 3: Run the tests to see them fail**

Run: `cd agent && go test -count=1 -run 'UpdatePosted' ./internal/delegatestore/`
Expected: build failure, `undefined: EventDelegateUpdatePosted` (and `UpdatePosted`, `PacketUpdate`, `UpdateDeliveryID`).

- [ ] **Step 4: Implement the event, packet kind and fold**

In `agent/internal/delegatestore/event.go`, add after `EventDelegateAttentionChanged`:

```go
	EventDelegateUpdatePosted         EventKind = "delegate_update_posted"
```

add to `Event` after `AttentionChanged`:

```go
	UpdatePosted         *UpdatePosted             `json:"update_posted,omitempty"`
```

and after `DelegateAttentionChanged`:

```go
// UpdatePosted is a running generation's mid-work message to its owner, its
// communicate(end_turn=false). The fold queues it as a pending delivery
// behind the delegate's earlier deliveries and ahead of the report the same
// generation settles.
type UpdatePosted struct {
	Generation uint64 `json:"generation"`
	Message    string `json:"message"`
}
```

In `agent/internal/delegatestore/record.go`, extend the packet kinds:

```go
const (
	PacketReported      PacketKind = "reported"
	PacketTerminalError PacketKind = "terminal_error"
	// PacketUpdate is a running generation's mid-work update, queued by
	// delegate_update_posted. It never settles a run: validateTerminalPacket
	// refuses it on every terminal path.
	PacketUpdate PacketKind = "update"
)
```

In `agent/internal/delegatestore/fold.go`: add `"strconv"` and `"strings"` to the imports; add to `applyEvent`'s switch:

```go
	case EventDelegateUpdatePosted:
		return applyUpdatePosted(state, event)
```

add `event.UpdatePosted != nil,` to the `payloads` slice in `validateEventEnvelope` and to its switch:

```go
	case EventDelegateUpdatePosted:
		matching = event.UpdatePosted != nil
```

and add after `applyAttentionChanged`:

```go
func applyUpdatePosted(state State, event Event) error {
	payload := event.UpdatePosted
	if event.Seq == 0 {
		return fmt.Errorf("delegate %q update sequence is zero", event.DelegateID)
	}
	aggregate, err := requireExactOpenRun(state, event.DelegateID, payload.Generation)
	if err != nil {
		return err
	}
	// Settling means the generation's report is prepared and nothing may land
	// behind it; stopping means a stop owns the generation.
	if aggregate.Phase != PhaseRunning {
		return fmt.Errorf("delegate %q phase %q cannot post an update", event.DelegateID, aggregate.Phase)
	}
	if strings.TrimSpace(payload.Message) == "" {
		return fmt.Errorf("delegate %q update message is empty", event.DelegateID)
	}
	message, err := json.Marshal(payload.Message)
	if err != nil {
		return fmt.Errorf("delegate %q update message: %w", event.DelegateID, err)
	}
	aggregate.PendingDeliveries = append(aggregate.PendingDeliveries, PendingDelivery{
		DeliveryID:      UpdateDeliveryID(event.DelegateID, event.Seq),
		Generation:      payload.Generation,
		OwnerDelegateID: aggregate.Descriptor.ParentDelegateID,
		Packet:          TerminalPacket{Kind: PacketUpdate, Message: message},
	})
	return nil
}

// UpdateDeliveryID names the pending delivery a delegate_update_posted event
// queues. The event's journal sequence is unique for the life of the journal,
// so the receiver's attention id for an update never repeats.
func UpdateDeliveryID(delegateID string, seq uint64) string {
	return delegateID + "/update/" + strconv.FormatUint(seq, 10)
}
```

In `agent/internal/delegatestore/store.go`'s `cloneEvent`, before `return clone`:

```go
	if event.UpdatePosted != nil {
		payload := *event.UpdatePosted
		clone.UpdatePosted = &payload
	}
```

In `agent/internal/delegatestore/envelope_fuzz_test.go`, append to `envelopeKinds`:

```go
	{EventDelegateUpdatePosted, func(e *Event) { e.UpdatePosted = &UpdatePosted{} }},
```

change the comment's "Nine kinds against 512 payload combinations" to "Ten kinds against 1,024 payload combinations", and add a seed after the attention-changed one:

```go
	f.Add(9, uint16(512), "dlg_1") // update posted, matching payload
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `cd agent && go test -count=1 ./internal/delegatestore/ && go test -tags evenerfuzz -count=1 -run 'FuzzDelegateEventEnvelope' ./internal/delegatestore/`
Expected: PASS.

- [ ] **Step 6: Update the resource-model doc**

In `docs/subagent-management/11-delegate-resource-model.md`, in the "Required event kinds are:" list, insert `- delegate_update_posted;` immediately before `- delegate_delivery_acknowledged.`. In the delivery rules list, after the bullet that begins "Only the head pending delivery may be dispatched.", add:

```markdown
- A running generation's update (delegate_update_posted, its
  communicate(end_turn=false)) joins the same per-delegate queue as the
  pending delivery `<delegate_id>/update/<event seq>` with packet kind
  `update`. The generation's updates are therefore dispatched in the order
  posted and ahead of the report its finish appends. Only the open, running
  generation may post one.
```

- [ ] **Step 7: Run the agent-module gates**

```bash
set -o pipefail
cd agent
go build ./... && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=linux go vet -tags evenerfuzz ./...
"$(go env GOROOT)/bin/gofmt" -l internal/delegatestore
golangci-lint run --allow-serial-runners ./...
go test -race -count=1 ./internal/delegatestore/
/Users/jesse/git/prime-radiant-inc/evener/.superpowers/bin/race-serial go test -race -count=1 ./...
```

Expected: gofmt prints nothing; everything else passes. The full race run proves nothing outside the store folded the new kind wrong (the controller, activity reads and doctor all fold through `delegatestore.Fold`).

- [ ] **Step 8: Commit**

```bash
git add agent/internal/delegatestore/event.go agent/internal/delegatestore/record.go \
  agent/internal/delegatestore/fold.go agent/internal/delegatestore/store.go \
  agent/internal/delegatestore/envelope_fuzz_test.go agent/internal/delegatestore/fold_test.go \
  agent/internal/delegatestore/store_test.go docs/subagent-management/11-delegate-resource-model.md
git diff --cached --stat
git commit -m "feat(delegates): the delegate journal records updates in delivery order

A running generation's update is a delegate_update_posted event. The fold
queues it as a pending delivery of kind update, under an id its journal
sequence makes unique, behind the delegate's earlier deliveries and ahead
of the report its generation settles. Nothing posts one yet.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

- [ ] **Step 9: Open PR 1 and shepherd it**

Run `/simplify` on the branch, push its fixes, then open a regular PR with base `main` titled "feat(delegates): the delegate journal records updates in delivery order". The body names the spec, says the event has no producer until PR 4, states the `/simplify` outcome, and ends with `https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU`. Drive it with `/shepherd-pr` to the merge gate in Global Constraints.


## PR 2: Delegate notification frames name their kind; clients render updates

PR 2 needs PR 1's `PacketUpdate` and `UpdateDeliveryID`. Branch from `claude/delegate-update-journal` until PR 1 merges, then rebase onto `origin/main`.

### Task 2: The frame carries `kind`; an update's body is its message; a carried update renders as an update

**Files:**
- Modify: `agent/delegate_delivery.go:1141-1153` (`delegateNotificationContent`)
- Modify: `agent/session_tools_jobs.go:1817-1821` (`delegateSendResultFrom`)
- Modify: `agent/steering_notification_wire_fixture_test.go` (new `delegate-update` case; add `encoding/json` import)
- Regenerate: `agent/testdata/notificationwire/steering.json`
- Test: `agent/delegate_delivery_branches_test.go` (add `strings` import), `agent/delegate_send_deferred_head_test.go`
- Docs: `docs/job-control.md` (lines 81 and 1361-1367)

**Interfaces:**
- Consumes: `delegatestore.PacketUpdate`, `delegatestore.UpdateDeliveryID` (Task 1).
- Produces:
  - Report frame: `<delegate-notification delegate_id="…"[ name="…"] kind="report">{packet JSON}</delegate-notification>` for every `reported` and `terminal_error` packet.
  - Update frame: `<delegate-notification delegate_id="…"[ name="…"] kind="update">{escapeNotificationBody(message)}</delegate-notification>`.
  - A `delegate_send` reply's `earlier_results` entry for an update packet has `action: "update"`, `output: <message>`, no status.
  - Corpus case `delegate-update` in `agent/testdata/notificationwire/steering.json` (delegate `dlg_1`, name "Fix race in tree settle", message "Which table holds the drain cursor: tree_state or drain_log? I need it before I add the index for seq < head.").

- [ ] **Step 1: Write the failing frame tests**

Append to `agent/delegate_delivery_branches_test.go` (add `"strings"` to its imports):

```go
// The frame names what it carries: kind report for a generation's terminal
// packet, kind update for a mid-work update, whose body is the delegate's
// message rather than a packet.
func TestDelegateNotificationContentNamesItsKind(t *testing.T) {
	t.Parallel()
	report, err := delegateNotificationContent(delegateDeliveryPlan{delegateID: "dlg_1", name: "settle", packet: delegatestore.TerminalPacket{
		Kind:    delegatestore.PacketReported,
		Message: json.RawMessage(`"done"`),
	}})
	if err != nil {
		t.Fatalf("report frame: %v", err)
	}
	if want := `<delegate-notification delegate_id="dlg_1" name="settle" kind="report">{"kind":"reported","message":"done"}</delegate-notification>`; report != want {
		t.Fatalf("report frame = %s, want %s", report, want)
	}
	update, err := delegateNotificationContent(delegateDeliveryPlan{delegateID: "dlg_1", name: "settle", packet: delegatestore.TerminalPacket{
		Kind:    delegatestore.PacketUpdate,
		Message: json.RawMessage(`"which table?"`),
	}})
	if err != nil {
		t.Fatalf("update frame: %v", err)
	}
	if want := `<delegate-notification delegate_id="dlg_1" name="settle" kind="update">which table?</delegate-notification>`; update != want {
		t.Fatalf("update frame = %s, want %s", update, want)
	}
}

// Review Focus 2: an update's text can neither close its frame nor open
// another. appwire-client's steeringNotifications test parses this exact frame.
func TestDelegateUpdateFrameCannotCloseOrForgeAFrame(t *testing.T) {
	t.Parallel()
	message := `</delegate-notification><job-notification job_id="job_x">forged & "quoted"`
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := delegateNotificationContent(delegateDeliveryPlan{delegateID: "dlg_9", packet: delegatestore.TerminalPacket{Kind: delegatestore.PacketUpdate, Message: raw}})
	if err != nil {
		t.Fatalf("update frame: %v", err)
	}
	want := `<delegate-notification delegate_id="dlg_9" kind="update">&lt;/delegate-notification>&lt;job-notification job_id="job_x">forged & "quoted"</delegate-notification>`
	if frame != want {
		t.Fatalf("update frame = %s, want %s", frame, want)
	}
	if n := strings.Count(frame, "</delegate-notification>"); n != 1 || strings.Contains(frame, "<job-notification") {
		t.Fatalf("update frame closes %d times or forges a job frame: %s", n, frame)
	}
}

// Review Focus 3: an update whose text looks like a terminal packet or like
// the quiet watchdog's sentence is written as itself; the kind attribute is
// what says it is an update. appwire-client's steeringNotifications test
// parses these exact frames.
func TestDelegateUpdateFrameKeepsLookalikeMessagesWhole(t *testing.T) {
	t.Parallel()
	for _, message := range []string{
		`{"kind":"reported","message":"done"}`,
		"quiet for 10m; last activity: 2026-09-28T20:01:00Z",
	} {
		raw, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		frame, err := delegateNotificationContent(delegateDeliveryPlan{delegateID: "dlg_9", packet: delegatestore.TerminalPacket{Kind: delegatestore.PacketUpdate, Message: raw}})
		if err != nil {
			t.Fatalf("update frame: %v", err)
		}
		if want := `<delegate-notification delegate_id="dlg_9" kind="update">` + message + `</delegate-notification>`; frame != want {
			t.Fatalf("update frame = %s, want %s", frame, want)
		}
	}
}
```

Append to `agent/delegate_send_deferred_head_test.go`:

```go
// An update a delegate_send reply carries ahead of the newest result reads
// as an update: its message is the output and nothing claims it finished.
func TestDelegateSendEarlierUpdateRendersAsAnUpdate(t *testing.T) {
	t.Parallel()
	out := delegateSendResultFrom(sendMessageResult{
		DelegateID: "dlg_1",
		Type:       delegateResourceType,
		Earlier:    []delegatestore.TerminalPacket{{Kind: delegatestore.PacketUpdate, Message: json.RawMessage(`"which table?"`)}},
	})
	if len(out.EarlierResults) != 1 {
		t.Fatalf("earlier results = %#v, want one", out.EarlierResults)
	}
	earlier := out.EarlierResults[0]
	if earlier.Action != "update" || earlier.Output == nil || *earlier.Output != "which table?" || earlier.Status != "" || earlier.DelegateID != "dlg_1" {
		t.Fatalf("earlier update = %#v, want action update, the message as output and no status", earlier)
	}
}
```

(`delegate_send_deferred_head_test.go` needs `"primeradiant.com/evener/agent/internal/delegatestore"` in its imports; `encoding/json` is already there.)

- [ ] **Step 2: Run them to see them fail**

Run: `cd agent && go test -count=1 -run 'TestDelegateNotificationContentNamesItsKind|TestDelegateUpdateFrame|TestDelegateSendEarlierUpdateRendersAsAnUpdate' .`
Expected: FAIL: the report frame lacks `kind="report"`; the update frame is the packet JSON; the earlier update reads `action: completed`.

- [ ] **Step 3: Implement the frame and the reply rendering**

Replace `delegateNotificationContent` in `agent/delegate_delivery.go`:

```go
func delegateNotificationContent(plan delegateDeliveryPlan) (string, error) {
	// The name attribute is display-only: the frame stays addressable by
	// delegate_id alone, and an unnamed delegate renders no name attribute.
	attrs := []string{notificationAttr("delegate_id", plan.delegateID)}
	if plan.name != "" {
		attrs = append(attrs, notificationAttr("name", plan.name))
	}
	if plan.packet.Kind == delegatestore.PacketUpdate {
		var message string
		if err := json.Unmarshal(plan.packet.Message, &message); err != nil {
			return "", fmt.Errorf("decode delegate update message: %w", err)
		}
		// An update's body is the delegate's own words, with "<" escaped so
		// they can neither close this frame nor open another.
		attrs = append(attrs, notificationAttr("kind", "update"))
		return fmt.Sprintf("<delegate-notification %s>%s</delegate-notification>", strings.Join(attrs, " "), escapeNotificationBody(message)), nil
	}
	packet, err := json.Marshal(plan.packet)
	if err != nil {
		return "", fmt.Errorf("marshal delegate delivery packet: %w", err)
	}
	attrs = append(attrs, notificationAttr("kind", "report"))
	return fmt.Sprintf("<delegate-notification %s>%s</delegate-notification>", strings.Join(attrs, " "), packet), nil
}
```

In `agent/session_tools_jobs.go`, replace the `res.Earlier` loop in `delegateSendResultFrom`:

```go
	for _, packet := range res.Earlier {
		earlier := sendMessageResult{DelegateID: res.DelegateID, Type: res.Type, TranscriptRef: res.TranscriptRef}
		if packet.Kind == delegatestore.PacketUpdate {
			// A mid-work update the reply carries ahead of the newest result
			// settles nothing: it is the delegate's message and no more.
			earlier.Action = "update"
			_ = json.Unmarshal(packet.Message, &earlier.Output)
		} else {
			completeStableDelegateSendResult(&earlier, packet)
		}
		out.EarlierResults = append(out.EarlierResults, delegateSendResultFrom(earlier))
	}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `cd agent && go test -count=1 -run 'TestDelegateNotificationContent|TestDelegateUpdateFrame|TestDelegateSendEarlier|TestDelegateNameLabel' .`
Expected: PASS.

- [ ] **Step 5: Record the update frame in the notification corpus**

In `agent/steering_notification_wire_fixture_test.go` add `"encoding/json"` to the imports and insert this case immediately after the `delegate-quiet` case:

```go
		{
			name: "delegate-update",
			note: "A named subagent's mid-work update, communicate(end_turn=false): kind update, and the body is its message with \"<\" escaped.",
			turn: notificationWireAttention(
				delegateAttentionID(delegatestore.UpdateDeliveryID("dlg_1", 7)),
				notificationWireBarePacketFrame(t, "dlg_1", "Fix race in tree settle", delegatestore.TerminalPacket{
					Kind:    delegatestore.PacketUpdate,
					Message: json.RawMessage(`"Which table holds the drain cursor: tree_state or drain_log? I need it before I add the index for seq < head."`),
				}),
			),
		},
```

Run: `cd agent && go test -count=1 -run 'TestSteeringNotificationWireFixtures$' . -update-wire && git diff --stat testdata/notificationwire`
Expected: the corpus diff adds `kind=\"report\"` to every recorded `delegate-*` report frame (the six packet cases) and adds the `delegate-update` case; the quiet and job cases are unchanged. Read the diff; any other change is a bug.

- [ ] **Step 6: Update the job-control doc for the `kind` attribute**

In `docs/job-control.md`, line 81, change `` `<delegate-notification delegate_id="dlg_...">` `` to `` `<delegate-notification delegate_id="dlg_..." kind="report">` ``. Replace the frame example and the sentence after it (lines 1361-1367) with:

````markdown
Direct-delegate completion uses the stable identity and canonical bounded
terminal packet:

```xml
<delegate-notification delegate_id="dlg_..." name="..." kind="report">{packet JSON}</delegate-notification>
```

`kind="report"` marks the generation's terminal packet, whatever its outcome.
The `name` attribute appears only for a named delegate. The quiet watchdog's
frame carries neither `name` nor `kind`. A delegate frame never carries
`job_id` or `job_type="delegate"`. The full conversation stays available
through the delegate's `transcript_ref`.
````

- [ ] **Step 7: Commit**

```bash
git add agent/delegate_delivery.go agent/session_tools_jobs.go agent/delegate_delivery_branches_test.go \
  agent/delegate_send_deferred_head_test.go agent/steering_notification_wire_fixture_test.go \
  agent/testdata/notificationwire/steering.json docs/job-control.md
git diff --cached --stat
git commit -m "feat(delegates): delegate frames name their kind, and an update reads as its message

Report frames carry kind=\"report\". An update packet renders as
kind=\"update\" with the delegate's message, \"<\" escaped, as its body, and
a delegate_send reply that carries one ahead of its result shows it as an
update. The notification corpus records the update frame.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

### Task 3: Clients render a `kind="update"` frame as an update; the TUI keeps it visible

**Files:**
- Modify: `appwire-client/typescript/steeringNotifications.ts` (`ParsedNotification.update`, `parseDelegateNotification`, new `delegateUpdateNotification`)
- Modify: `appwire-client/typescript/testing/notificationWireFixtures.ts` (`NotificationWireCase` adds `"delegate-update"`)
- Modify: `mobile-native/src/session/notificationLine.ts` (update headline)
- Test: `appwire-client/typescript/steeringNotifications.test.ts`, `mobile-native/src/notificationRows.test.tsx`, `cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.test.tsx`, `cmd/evener-tui/hub_notifications_test.go`

**Interfaces:**
- Consumes: the `delegate-update` corpus case and the exact frames pinned in Task 2.
- Produces: `ParsedNotification.update?: true`, set only for `kind="update"` frames, with `title: "Delegate update"`, `tone: "neutral"`, `message` the decoded body, no `outcome`. Phone headline `"<subject> sent an update"`.

- [ ] **Step 1: Write the failing client tests**

In `appwire-client/typescript/testing/notificationWireFixtures.ts`, add `| "delegate-update"` to `NotificationWireCase` right after `"delegate-quiet"` (the corpus order).

Append to `appwire-client/typescript/steeringNotifications.test.ts`:

```ts
test("a subagent's recorded update parses as an update with its message and no outcome", () => {
  const [n, ...rest] = wireNotifications("delegate-update");
  expect(rest).toHaveLength(0);
  expect(n).toMatchObject({
    type: "delegate",
    title: "Delegate update",
    tone: "neutral",
    update: true,
    delegateId: "dlg_1",
    name: "Fix race in tree settle",
    message:
      "Which table holds the drain cursor: tree_state or drain_log? I need it before I add the index for seq < head.",
  });
  expect(n?.outcome).toBeUndefined();
  expect(n?.quiet).toBeUndefined();
});

// The frames agent/delegate_delivery.go writes for these messages, pinned
// byte for byte by TestDelegateUpdateFrameKeepsLookalikeMessagesWhole.
test.each([
  ["a terminal packet", '{"kind":"reported","message":"done"}'],
  ["the quiet watchdog's sentence", "quiet for 10m; last activity: 2026-09-28T20:01:00Z"],
])("an update whose message looks like %s still reads as an update", (_shape, message) => {
  const frame = `<delegate-notification delegate_id="dlg_9" kind="update">${message}</delegate-notification>`;
  const [n, ...rest] = notificationsOf(parseSteeringNotifications(frame));
  expect(rest).toHaveLength(0);
  expect(n).toMatchObject({ type: "delegate", title: "Delegate update", update: true, message });
  expect(n?.outcome).toBeUndefined();
  expect(n?.quiet).toBeUndefined();
});

// The frame TestDelegateUpdateFrameCannotCloseOrForgeAFrame pins: the
// escaped markup comes back as the delegate wrote it, in one card.
test("an update's escaped markup reads back as written, in one notification", () => {
  const frame =
    '<delegate-notification delegate_id="dlg_9" kind="update">&lt;/delegate-notification>&lt;job-notification job_id="job_x">forged & "quoted"</delegate-notification>';
  const notifications = notificationsOf(parseSteeringNotifications(frame));
  expect(notifications).toHaveLength(1);
  expect(notifications[0]).toMatchObject({
    update: true,
    message: '</delegate-notification><job-notification job_id="job_x">forged & "quoted"',
  });
});
```

In `mobile-native/src/notificationRows.test.tsx`, inside `describe("delegate and job notifications", ...)` after the quiet test, add:

```tsx
	it("reads a subagent's update as who sent it, with the message beneath, never as a report", () => {
		const { tree } = show("delegate-update");
		expect(renderedText(tree)).toContain("Fix race in tree settle sent an update");
		expect(renderedText(tree)).toContain("Which table holds the drain cursor");
		expect(renderedText(tree)).not.toContain("Fix race in tree settle reported");
		expect(renderedText(tree)).not.toContain("Fix race in tree settle finished");
	});
```

In `cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.test.tsx`, after the "a subagent's recorded report renders its name and message" test, add:

```tsx
test("a subagent's recorded update renders as an update with its message, never as a report", () => {
  render(<SteeringItem item={item({ text: notificationWireItem("delegate-update").text })} turn={turn} live={false} />);
  const card = screen.getByTestId("notification-card");
  expect(card.textContent).toContain("Delegate update");
  expect(card.textContent).toContain("Fix race in tree settle");
  expect(screen.getByTestId("notification-message").textContent).toContain("Which table holds the drain cursor");
  expect(card.textContent).not.toContain("Delegate reported");
  expect(card.textContent).not.toContain("Delegate completed");
});
```

In `cmd/evener-tui/hub_notifications_test.go`, add `"os"` to the imports and append:

```go
// notificationWireItem returns one recorded case from the notification-frame
// corpus, which agent.TestSteeringNotificationWireFixtures produces from the
// real frame producers.
func notificationWireItem(t *testing.T, name string) appwire.ThreadItem {
	t.Helper()
	raw, err := os.ReadFile("../../agent/testdata/notificationwire/steering.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Case string             `json:"case"`
		Item appwire.ThreadItem `json:"item"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.Case == name {
			return c.Item
		}
	}
	t.Fatalf("no %s case in the notification corpus", name)
	return appwire.ThreadItem{}
}

// A delegate's recorded update stays a visible steering row in the TUI, the
// way every delegate frame does.
func TestTUIDelegateUpdateNotificationStaysVisible(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUINotification(t, &m, appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		Ref:   "local:root",
		Items: []appwire.ThreadItem{notificationWireItem(t, "delegate-update")},
	})
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgSteering && strings.Contains(msgrender.RenderMessage(msg, 240, false), "Which table holds the drain cursor") {
			return
		}
	}
	t.Fatalf("messages = %+v, want the recorded update visible", m.session.messages)
}
```

- [ ] **Step 2: Run them to see the client ones fail**

Run: `make test-api-package` from the worktree root, then `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/messages/SteeringItem.test.tsx`
Expected: FAIL: the update parses through the legacy fallback as "Delegate notification" with `update` undefined (and the lookalike JSON parses as a reported packet). `update` is also not yet a `ParsedNotification` field, which `npm run typecheck` reports (vitest strips types, so a green vitest run never proves the typecheck).
Run: `go test -count=1 -run TestTUIDelegateUpdateNotificationStaysVisible ./cmd/evener-tui/`
Expected: PASS already (the TUI renders steering text as it arrives); it pins the wire case for the corpus change.
The phone test fails the same way as the parser; it runs under `make test-native` in Step 4.

- [ ] **Step 3: Implement update parsing and the phone headline**

In `appwire-client/typescript/steeringNotifications.ts`, add to `ParsedNotification` after `quiet?`:

```ts
  // A delegate's mid-work update (the frame's kind="update"): its message is
  // the delegate's own text, and it settles nothing, so it claims no outcome.
  update?: true;
```

In `parseDelegateNotification`, right after `const name = ...`:

```ts
  // An update names its kind; its body is the delegate's text whatever its
  // shape, so it routes before the packet and watchdog shapes are tried.
  if (attrs.kind === "update") return delegateUpdateNotification(block, name, delegateId, body);
```

and add after `parseDelegateNotification`:

```ts
// A delegate's mid-work update (agent/delegate_delivery.go's
// delegateNotificationContent, kind="update"): the body is the delegate's
// message with "<" escaped, decoded here once.
function delegateUpdateNotification(
  block: string,
  name: string,
  delegateId: string | undefined,
  body: string,
): ParsedNotification {
  const message = decodeNotificationEntities(body);
  return {
    type: "delegate",
    title: "Delegate update",
    tone: "neutral",
    secondary: name || delegateId || "",
    update: true,
    delegateId,
    name: name || undefined,
    excerpt: "",
    message: message || undefined,
    concerns: [],
    rawText: block,
  };
}
```

Update the comment above `parseDelegateNotification` ("A delegate frame's body is one of three shapes") to say a `kind="update"` frame's body is the delegate's message and is routed first.

In `mobile-native/src/session/notificationLine.ts`, in the `"delegate"` case, replace the `line` initializer:

```ts
			const line: NotificationLine = notification.quiet
				? { headline: `${subject} quiet · ${notification.quiet.window}`, failed }
				: notification.update
					? { headline: `${subject} sent an update`, failed }
					: // A report whose ending this client doesn't know still reported.
						{ headline: said(subject, notification.outcome, "reported"), failed };
```

The existing `detail` line already picks up `notification.message`.

- [ ] **Step 4: Format and run the client and TUI gates**

```bash
set -o pipefail
cd cmd/evener-hub/frontend && npx biome check --write \
  ../../../appwire-client/typescript/steeringNotifications.ts \
  ../../../appwire-client/typescript/steeringNotifications.test.ts \
  ../../../appwire-client/typescript/testing/notificationWireFixtures.ts \
  src/panes/session/transcript/messages/SteeringItem.test.tsx
cd ../../..   # back to the worktree root; the make targets live in the root Makefile
make test-web          # run the heavy gates one at a time, never in parallel
make test-api-package
make test-native
go build ./... && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=linux go vet -tags evenerfuzz ./...
golangci-lint run --allow-serial-runners ./cmd/evener-tui/...
go test -count=1 ./cmd/evener-tui/...
```

`mobile-native` files are formatted by the native tree's own Biome through the pre-commit hook (`make hooks`); `make test-native` checks them. Expected: everything passes.

- [ ] **Step 5: Run the agent-module gates for Task 2**

```bash
set -o pipefail
cd agent
go build ./... && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=linux go vet -tags evenerfuzz ./...
"$(go env GOROOT)/bin/gofmt" -l delegate_delivery.go session_tools_jobs.go delegate_delivery_branches_test.go delegate_send_deferred_head_test.go steering_notification_wire_fixture_test.go
golangci-lint run --allow-serial-runners ./...
go test -race -count=1 -run 'TestDelegateNotificationContent|TestDelegateUpdateFrame|TestDelegateSend|WireFixtures$' .
/Users/jesse/git/prime-radiant-inc/evener/.superpowers/bin/race-serial go test -race -count=1 ./...
```

- [ ] **Step 6: Commit and open PR 2**

```bash
git add appwire-client/typescript/steeringNotifications.ts appwire-client/typescript/steeringNotifications.test.ts \
  appwire-client/typescript/testing/notificationWireFixtures.ts mobile-native/src/session/notificationLine.ts \
  mobile-native/src/notificationRows.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.test.tsx \
  cmd/evener-tui/hub_notifications_test.go
git diff --cached --stat
git commit -m "feat(clients): a delegate's update renders as an update on the web, the phone and the TUI

The shared parser routes a kind=\"update\" frame before the packet and
watchdog shapes: \"Delegate update\" with the delegate's message and no
outcome. The phone says \"<name> sent an update\". The web card needs no
change of its own; tests pin all three against the recorded frame.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

Run `/simplify`, push its fixes, and open the PR (base `main`, regular) titled "feat(delegates): delegate frames name their kind; clients render updates". The body says no producer posts updates until PR 4, lists the corpus diff (six report frames gain `kind="report"`, one new case), states the `/simplify` outcome, and ends with `https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU`. Shepherd it to the merge gate.


## PR 3: Delegate guidance: updates reach your parent, only when it needs to know or act

PR 3 is independent of PRs 1 and 2 and merges immediately before PR 4 (ruling 13).

### Task 4: Pin the messaging guidance, then word it for delegates

**Files:**
- Create: `agent/delegate_messaging_test_helpers_test.go` (scripted root and delegates, shared with PR 4)
- Create: `agent/delegate_messaging_guidance_test.go`
- Create (generated): `agent/testdata/messagingprompt/root.md`, `agent/testdata/messagingprompt/delegate.md`
- Modify: `agent/internal/tool/definitions.go` (`WithCommunicateDelegateUpdates`; observer text in `DefDelegateWithSandbox` line 222, `DefJobWatch` line 398, `DefJobList` line 464)
- Modify: `agent/job_watch.go:807` and `:1008` (observer guidance in two `invalid_request` errors)
- Modify: `agent/prompts/system.md.tmpl:126` (delegate branch of the Reporting section)
- Modify: `agent/session_tools_ask.go` (`reportsToParent`)
- Modify: `agent/session_tool_registry.go` (`toolDeps.reportsToParent`, `newToolDeps`)
- Modify: `agent/session_tools_communicate.go:32-34` (apply the delegate wording)
- Modify: `agent/session_memory_test.go:2803-2804` (the `-update-prompt` flag's help names both golden directories)
- Modify: `make/fuzzing.mk:126-139` (`fuzz-goldens` regenerates the messaging goldens too)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `func tool.WithCommunicateDelegateUpdates(def llm.ToolDefinition) llm.ToolDefinition` (Task 6 extends its description).
  - `func (s *Session) reportsToParent() bool`: true for a delegate (`s.owningDelegateID != "" || s.isSubagentSession()`). Task 6 gates delivery on the same predicate.
  - `toolDeps.reportsToParent bool`.
  - Test helpers (Task 7 reuses and extends them): `type messagingAdapter`, `newMessagingAdapter(markers ...string) *messagingAdapter`, `(*messagingAdapter).script(key string, steps ...func(llm.Request) llm.Response)`, `(*messagingAdapter).requestsFor(key string) []llm.Request`, `(*messagingAdapter).waitForRequests(t *testing.T, key string, n int) []llm.Request`, `newMessagingRoot(t *testing.T, adapter *messagingAdapter) *Session`, `delegateToolResponse(id, prompt string) llm.Response`.
  - Goldens `agent/testdata/messagingprompt/{root,delegate}.md`, regenerated with `go test ./agent -run 'TestDelegateMessagingGuidanceGoldens$' -count=1 -update-prompt`.

- [ ] **Step 1: Create the worktree for PR 3**

```bash
git -C /Users/jesse/git/prime-radiant-inc/evener fetch origin main
git -C /Users/jesse/git/prime-radiant-inc/evener worktree add -b claude/delegate-update-guidance \
  /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-update-guidance origin/main
```

- [ ] **Step 2: Write the shared scripted-session helpers**

Create `agent/delegate_messaging_test_helpers_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// messagingAdapter scripts a root session and the delegates under it, each
// from its own step list, so sessions running at the same time never race
// for one script. The root is recognized by its session id. A delegate is
// recognized by the first marker one of its user messages carries (its brief
// names it) and keeps that script for the rest of its life. A step may block
// on a channel the test controls: steps run outside the adapter's lock. An
// unscripted request is answered with a plain final report.
type messagingAdapter struct {
	mu       sync.Mutex
	rootID   string
	markers  []string
	scripts  map[string][]func(llm.Request) llm.Response
	sessions map[string]string
	requests map[string][]llm.Request
	arrived  chan struct{}
}

func newMessagingAdapter(markers ...string) *messagingAdapter {
	return &messagingAdapter{
		markers:  markers,
		scripts:  make(map[string][]func(llm.Request) llm.Response),
		sessions: make(map[string]string),
		requests: make(map[string][]llm.Request),
		arrived:  make(chan struct{}, 1),
	}
}

func (*messagingAdapter) Name() string { return "openai" }

func (*messagingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// script appends steps to a script: "root", or one of the markers.
func (a *messagingAdapter) script(key string, steps ...func(llm.Request) llm.Response) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scripts[key] = append(a.scripts[key], steps...)
}

func (a *messagingAdapter) requestsFor(key string) []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.requests[key])
}

// waitForRequests waits until key's script has answered at least n requests.
func (a *messagingAdapter) waitForRequests(t *testing.T, key string, n int) []llm.Request {
	t.Helper()
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	deadline := time.After(30 * time.Second)
	for {
		if requests := a.requestsFor(key); len(requests) >= n {
			return requests
		}
		select {
		case <-a.arrived:
		case <-deadline:
			t.Fatalf("%s made %d requests, want %d", key, len(a.requestsFor(key)), n)
		}
	}
}

func (a *messagingAdapter) keyLocked(req llm.Request) string {
	if req.SessionID == a.rootID {
		return "root"
	}
	if key, ok := a.sessions[req.SessionID]; ok {
		return key
	}
	for _, msg := range req.Messages {
		if msg.Role != llm.RoleUser {
			continue
		}
		for _, marker := range a.markers {
			if strings.Contains(msg.Text(), marker) {
				a.sessions[req.SessionID] = marker
				return marker
			}
		}
	}
	return ""
}

func (a *messagingAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	key := a.keyLocked(req)
	a.requests[key] = append(a.requests[key], req)
	var step func(llm.Request) llm.Response
	if steps := a.scripts[key]; len(steps) > 0 {
		step, a.scripts[key] = steps[0], steps[1:]
	}
	a.mu.Unlock()
	select {
	case a.arrived <- struct{}{}:
	default:
	}
	response := finalResponse("done")
	if step != nil {
		response = step(req)
	}
	response.Provider = a.Name()
	response.Model = req.Model
	return response, nil
}

// newMessagingRoot starts a root session answered by adapter.
func newMessagingRoot(t *testing.T, adapter *messagingAdapter) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	adapter.mu.Lock()
	adapter.rootID = sess.ID()
	adapter.mu.Unlock()
	return sess
}

// delegateToolResponse is a model turn that starts one delegate with prompt.
func delegateToolResponse(id, prompt string) llm.Response {
	args, _ := json.Marshal(map[string]any{"prompt": prompt})
	return toolCallResponse(llm.ToolCallData{ID: id, Name: "delegate", Arguments: args, Type: "function"})
}
```

- [ ] **Step 3: Write the golden test and record today's text as the baseline**

Create `agent/delegate_messaging_guidance_test.go`:

```go
package agent

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// The Reporting section and the messaging tools' descriptions are prompt
// text, pinned whole per session kind in testdata/messagingprompt and never
// by substring. Root and delegate text come from the requests a real root and
// its real delegate send. The delegate and job tools' observer guidance comes
// from their definitions with fixed inputs, since the live delegate tool's
// sandbox wording depends on the host. Regenerate after an intended wording
// change with
//
//	go test ./agent -run 'TestDelegateMessagingGuidanceGoldens$' -count=1 -update-prompt
//
// and read the diff.
func TestDelegateMessagingGuidanceGoldens(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("GOLDEN-CHILD")
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "GOLDEN-CHILD: report the repository name.")
		},
		func(llm.Request) llm.Response { return finalResponse("root done") },
	)
	adapter.script("GOLDEN-CHILD", func(llm.Request) llm.Response { return finalResponse("evener") })
	root := newMessagingRoot(t, adapter)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	rootRequest := adapter.waitForRequests(t, "root", 1)[0]
	delegateRequest := adapter.waitForRequests(t, "GOLDEN-CHILD", 1)[0]

	observerTools := []llm.ToolDefinition{
		toolpkg.DefDelegate(nil),
		toolpkg.DefJobList(),
		toolpkg.DefJobWatch([]string{"assistant.tool", "communicate", "job.notification"}),
	}
	for _, tc := range []struct {
		name  string
		req   llm.Request
		extra []llm.ToolDefinition
	}{
		{"root", rootRequest, observerTools},
		{"delegate", delegateRequest, nil},
	} {
		checkGolden(t, filepath.Join("testdata", "messagingprompt", tc.name+".md"), messagingGuidanceGolden(t, tc.req, tc.extra), *updatePromptGoldens,
			"Regenerate with `go test ./agent -run 'TestDelegateMessagingGuidanceGoldens$' -count=1 -update-prompt` and read the diff.")
	}
}

// messagingGuidanceGolden renders the Reporting section of req's system
// prompt and the description of every messaging tool req offers, plus extra.
func messagingGuidanceGolden(t *testing.T, req llm.Request, extra []llm.ToolDefinition) []byte {
	t.Helper()
	var system strings.Builder
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleSystem {
			system.WriteString(msg.Text())
		}
	}
	_, section, ok := strings.Cut(system.String(), "\n## Reporting\n")
	if !ok {
		t.Fatal("system prompt has no Reporting section")
	}
	if next := strings.Index(section, "\n## "); next >= 0 {
		section = section[:next]
	}
	tools := slices.Clone(extra)
	for _, def := range req.Tools {
		if def.Name == "communicate" || def.Name == "delegate_send" {
			tools = append(tools, def)
		}
	}
	slices.SortFunc(tools, func(a, b llm.ToolDefinition) int { return strings.Compare(a.Name, b.Name) })
	var golden strings.Builder
	golden.WriteString("## Reporting\n" + section + "\n\n# Messaging tool descriptions\n")
	for _, def := range tools {
		fmt.Fprintf(&golden, "\n## %s\n\n%s\n\n", def.Name, def.Description)
		props, _ := def.Parameters["properties"].(map[string]any)
		for _, param := range slices.Sorted(maps.Keys(props)) {
			description, _ := props[param].(map[string]any)["description"].(string)
			fmt.Fprintf(&golden, "- `%s`: %s\n", param, description)
		}
	}
	return []byte(golden.String())
}
```

Change the `-update-prompt` flag's help in `agent/session_memory_test.go` to:

```go
var updatePromptGoldens = flag.Bool("update-prompt", false,
	"rewrite agent/testdata/memoryprompt and agent/testdata/messagingprompt from the current guidance and tool descriptions")
```

Run: `cd agent && go test -count=1 -run 'TestDelegateMessagingGuidanceGoldens$' .`
Expected: FAIL, `read testdata/messagingprompt/root.md: no such file or directory`.
Run: `cd agent && go test -count=1 -run 'TestDelegateMessagingGuidanceGoldens$' . -update-prompt && go test -count=1 -run 'TestDelegateMessagingGuidanceGoldens$' .`
Expected: the goldens are written, then PASS. Read both files: `root.md` and `delegate.md` hold today's identical communicate text, the root's checkpoint sentence, and the `caller` route in `delegate_send`.

Commit the baseline on its own so the next commit's diff is the wording change:

```bash
git add agent/delegate_messaging_test_helpers_test.go agent/delegate_messaging_guidance_test.go \
  agent/testdata/messagingprompt/root.md agent/testdata/messagingprompt/delegate.md agent/session_memory_test.go
git diff --cached --stat
git commit -m "test(prompts): pin the reporting guidance and messaging tool descriptions per session kind

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

- [ ] **Step 4: Write the failing wiring assertion**

Append to `TestDelegateMessagingGuidanceGoldens`, before the golden loop:

```go
	// The delegate gets communicate worded for its parent; the root keeps the
	// wording for its human partner.
	communicateDescription := func(req llm.Request) string {
		for _, def := range req.Tools {
			if def.Name == "communicate" {
				return def.Description
			}
		}
		t.Fatal("request offers no communicate tool")
		return ""
	}
	if got, want := communicateDescription(delegateRequest), toolpkg.WithCommunicateDelegateUpdates(toolpkg.DefCommunicate()).Description; got != want {
		t.Fatalf("delegate communicate is not worded for its parent:\n got %s\nwant %s", got, want)
	}
	if got, want := communicateDescription(rootRequest), toolpkg.DefCommunicate().Description; got != want {
		t.Fatalf("root communicate wording changed:\n got %s\nwant %s", got, want)
	}
```

Run: `cd agent && go test -count=1 -run 'TestDelegateMessagingGuidanceGoldens$' .`
Expected: build failure, `undefined: toolpkg.WithCommunicateDelegateUpdates`.

- [ ] **Step 5: Word communicate and the Reporting section for delegates**

In `agent/internal/tool/definitions.go`, after `WithCommunicateEndReason`, add:

```go
// communicateDelegateDescription is communicate worded for a delegate, whose
// messages go to its parent agent.
const communicateDelegateDescription = "Send a message to your parent agent; it is the only way your parent hears from you. A valid call has visible text in `message` or `output.message`. " +
	"With `end_turn=false` the message is an update: your parent receives it, and it wakes a parent that is waiting on you, so send one only when your parent needs to know something or act on it, never to narrate progress, and keep working in the following round. " +
	"With `end_turn=true` the message is your final report and ends your turn: put the complete result in it. A readiness marker you send before waiting for watch frames also uses `end_turn=true`. " +
	"Always include `output` as an object with exactly these top-level fields: `message`, `data`, and `artifacts`. For an update, use `output.message=\"\"`, `output.data={}`, and `output.artifacts=[]`. When handing back completed work or machine-readable results, populate `output` with the evidence and structured data your parent needs."

// WithCommunicateDelegateUpdates returns a copy of a communicate definition
// worded for a delegate: end_turn=false sends an update its parent receives,
// end_turn=true its final report. The output schema stays as the caller
// built it.
func WithCommunicateDelegateUpdates(def llm.ToolDefinition) llm.ToolDefinition {
	params := CloneSchemaMap(def.Parameters)
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		return def
	}
	if message, ok := props["message"].(map[string]any); ok {
		message["description"] = "The text your parent agent receives. When the task asks for concrete findings, put them here."
	}
	if endTurn, ok := props["end_turn"].(map[string]any); ok {
		endTurn["description"] = "false: send an update and keep working; your parent receives it, and it wakes a parent that is waiting on you. true: send your final report, or a readiness marker before you wait for watch frames, and end your turn."
	}
	def.Description = communicateDelegateDescription
	def.Parameters = params
	return def
}
```

In the same file, replace the observer guidance:
- in `DefDelegateWithSandbox`: `the child can call job_watch(source=\"parent\") and report findings with communicate(end_turn=true).` becomes `the child can call job_watch(source=\"parent\"), send updates with communicate(end_turn=false), and report findings with communicate(end_turn=true).`
- in `DefJobWatch`: `"Observers report findings with `communicate(end_turn=true)`. "` becomes `"Observers send updates with `communicate(end_turn=false)` and report findings with `communicate(end_turn=true)`. "`
- in `DefJobList`: `Observer sidecars report findings with `communicate(end_turn=true)`;` becomes `Observer sidecars send updates with `communicate(end_turn=false)` and findings with `communicate(end_turn=true)`;`

In `agent/job_watch.go`, in both `invalid_request` errors (lines 807 and 1008), replace `and report findings with communicate(end_turn=true)` with `send updates with communicate(end_turn=false), and report findings with communicate(end_turn=true)`.

In `agent/session_tools_ask.go`, after `hasHumanPartnerToAsk`:

```go
// reportsToParent reports whether this session is a delegate, whose
// communicate messages go to its parent agent rather than to a human
// partner. It gates both communicate's wording and update delivery.
func (s *Session) reportsToParent() bool {
	return s.owningDelegateID != "" || s.isSubagentSession()
}
```

In `agent/session_tool_registry.go`, add to `toolDeps` after `offersEndReason`:

```go
	// reportsToParent words communicate for a delegate, whose messages go to
	// its parent agent (Session.reportsToParent).
	reportsToParent bool
```

and in `newToolDeps` after `offersEndReason: s.hasHumanPartnerToAsk(),`:

```go
		reportsToParent:        s.reportsToParent(),
```

In `agent/session_tools_communicate.go`, after the `if deps.offersEndReason { ... }` block in `registerCommunicateTool`:

```go
	if deps.reportsToParent {
		resultToolDef = tool.WithCommunicateDelegateUpdates(resultToolDef)
	}
```

In `agent/prompts/system.md.tmpl`, replace the Reporting section's second paragraph:

```
Write when there is news, such as a finished phase, a blocker, or a result. When a long task moves from investigating to building, send one checkpoint with `end_turn=false`: what is done, the plan, and the next step. A tool result may carry new messages from the user or your caller; read them and act on them.
```

with (one line, so the root's text stays byte for byte):

```
Write when there is news, such as a finished phase, a blocker, or a result. {{ if .IsSubagent }}An update with `end_turn=false` reaches your parent agent and may wake it, so send one only when your parent needs to know something or act on it, and keep working after it. Your parent can send you messages while you work; read them and act on them.{{ else }}When a long task moves from investigating to building, send one checkpoint with `end_turn=false`: what is done, the plan, and the next step. A tool result may carry new messages from the user or your caller; read them and act on them.{{ end }}
```

In `make/fuzzing.mk`, change the last `fuzz-goldens` line to

```make
	@sh -c "cd agent && go test -run 'TestMemoryGuidanceFollowsCapabilities\$$|TestDelegateMessagingGuidanceGoldens\$$' . -update-prompt"
```

and update the comment above it: the last line "pins the agent's memory guidance and memory tool descriptions, per capability shape, and its reporting guidance and messaging tool descriptions, per session kind"; the `##` help lines say "and the memory and messaging prompt goldens".

- [ ] **Step 6: Regenerate the goldens and read the diff**

Run: `cd agent && go test -count=1 -run 'TestDelegateMessagingGuidanceGoldens$' . -update-prompt && go test -count=1 -run 'TestDelegateMessagingGuidanceGoldens$|TestMemoryGuidanceFollowsCapabilities$' . && git diff testdata/messagingprompt`
Expected: PASS. In `root.md` only the three observer sentences changed (delegate, job_list, job_watch); its Reporting section and communicate text are unchanged. In `delegate.md` the Reporting paragraph is the delegate line and communicate is the parent wording. Any other change is a bug.

- [ ] **Step 7: Run the gates**

```bash
set -o pipefail
cd agent
go build ./... && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=linux go vet -tags evenerfuzz ./...
"$(go env GOROOT)/bin/gofmt" -l internal/tool/definitions.go job_watch.go session_tools_ask.go session_tool_registry.go session_tools_communicate.go delegate_messaging_test_helpers_test.go delegate_messaging_guidance_test.go session_memory_test.go
golangci-lint run --allow-serial-runners ./...
go test -race -count=1 -run 'TestDelegateMessagingGuidanceGoldens$|TestMemoryGuidance|Communicate|Prompt' . ./internal/tool/
/Users/jesse/git/prime-radiant-inc/evener/.superpowers/bin/race-serial go test -race -count=1 ./...
cd .. && go test -short -count=1 .
```

The last line is the root Makefile audit, required because `make/fuzzing.mk` changed.

- [ ] **Step 8: Commit and open PR 3**

```bash
git add agent/internal/tool/definitions.go agent/job_watch.go agent/prompts/system.md.tmpl \
  agent/session_tools_ask.go agent/session_tool_registry.go agent/session_tools_communicate.go \
  agent/delegate_messaging_guidance_test.go agent/testdata/messagingprompt/root.md \
  agent/testdata/messagingprompt/delegate.md make/fuzzing.mk
git diff --cached --stat
git commit -m "feat(prompts): delegates send an update only when their parent needs to know or act

A delegate's communicate is worded for its parent: end_turn=false is an
update the parent receives and may be woken by, end_turn=true the report.
Its Reporting section says the same and drops the investigating-to-building
checkpoint, which would wake the parent as narration. Observer guidance
names both. The root's text is unchanged; goldens pin both.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

Run `/simplify`, push its fixes, and open the PR (base `main`, regular) titled "feat(prompts): delegate guidance: updates reach your parent, only when it needs to know or act". The body says the updates themselves land in PR 4, which merges right after this one (ruling 13), shows the golden diff, states the `/simplify` outcome, and ends with `https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU`. Shepherd it to the merge gate.


## PR 3a: A delegate does not wake again for attention its run already showed the model

PR 3a is independent of PRs 1–3: it touches only the session's attention coverage. It lands before PR 4 (ruling 8), and preferably before PR 3, so PR 3 can still merge immediately before PR 4 (ruling 13). Its body says "Closes #3725".

**Mechanism (origin/main 3fb139cd03).** The root already consumes attention its turn showed the model: `stageRootDelegateAttentionCoverage` (`agent/session_attention.go:969`, called from `agent/session_model_call.go:489`) stages the attention IDs a built request presents, `promoteStagedRootDelegateAttention` (`agent/session_lifecycle.go:2358`) credits them once the round's call settles, and the finish defer in `processOneInput` (`agent/session_lifecycle.go:1931-1946`) calls `finishRootDelegateAttentionTurn`, whose `unionCoveredRootDelegateAttention` (`agent/session_attention.go:1040`) adds the covered IDs still pending and resolves them consumed. Staging and its per-turn reset (`agent/session_lifecycle.go:2060`) are gated to the root receiver. A delegate's mid-run attention (`retainDelegateAttentionTurn`, `agent/session_attention.go:1426`) therefore joins its history, is presented at the next request, and stays pending; when the generation releases, `driveStableDelegateAttention` (`agent/delegate_runtime.go:660`) finds it in `pendingDelegateAttentionIDs`, reserves `ids[0]` and starts one `TriggerAttention` successor per item, and the model repeats its handoff. The fix lets a delegate turn that runs under its generation's lease use the same coverage. The lease is the delegate's only start claim: `ReserveAttention` (`agent/delegate_tree_start.go`) admits one reservation per delegate and `delegateAttentionWakeEligibleLocked` (`agent/delegate_tree_attention.go:572`) refuses one while a live binding exists, so the generation-less consumed marker written at the turn's end cannot conflict with a reserved generation (the objection recorded in the old staging comment). Unleased turns on a child session cover nothing.

### Task 4a: Coverage of presented attention extends to a delegate's leased generation

**Files:**
- Modify: `agent/session_attention.go` (`coversPresentedAttention`; rename and re-gate the coverage functions; failure guard in `finishRootDelegateAttentionTurn`)
- Modify: `agent/session.go:1037-1050` (coverage field names and comments)
- Modify: `agent/session_model_call.go:485-489` (pass `ctx` to staging)
- Modify: `agent/session_lifecycle.go:2058-2060`, `:2354-2358` (renamed calls)
- Modify: `agent/session_attention_test.go` (renamed calls; staging takes a context)
- Test: `agent/delegate_attention_seen_test.go` (new)
- Docs: `docs/superpowers/specs/2026-08-29-stable-delegate-attention-no-action-design.md` (status line), `docs/job-control.md` (attention bullets near line 1394), `docs/subagent-management/11-delegate-resource-model.md` (Attention drives, after line 1534), `docs/product/subsystems.md` (S12 row)

**Interfaces:**
- Consumes: nothing from other PRs.
- Produces:
  - `func (s *Session) coversPresentedAttention(ctx context.Context) bool`: true for the root receiver and for a turn whose `ctx` carries `delegateRunLeaseContextKey`.
  - Renames, with no other change in behavior for the root: `stageRootDelegateAttentionCoverage` → `stagePresentedAttentionCoverage(ctx context.Context, req llm.Request, historyTurns []schema.Turn)`, `promoteStagedRootDelegateAttention` → `promoteStagedAttentionCoverage`, `resetRootDelegateAttentionCoverage` → `resetAttentionCoverage`, `unionCoveredRootDelegateAttention` → `unionCoveredAttention`; fields `rootAttentionCoveredIDs` → `attentionCoveredIDs`, `rootAttentionStagedIDs` → `attentionStagedIDs`, `rootAttentionPreTurnArmIDs` → `attentionPreTurnIDs`.
  - Contract (PR 4's Task 7 relies on it): attention a settled request of a delegate's leased generation presented is resolved `consumed` with resume generation 0 before that generation finishes, and earns no attention generation; attention appended after the generation's last request was built stays pending and starts one.

- [ ] **Step 1: Create the worktree for PR 3a**

```bash
git -C /Users/jesse/git/prime-radiant-inc/evener fetch origin main
git -C /Users/jesse/git/prime-radiant-inc/evener worktree add -b claude/delegate-attention-seen \
  /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-attention-seen origin/main
```

Work in that worktree for the rest of this task. Read #3725 and its comments first (`gh issue view 3725 --repo prime-radiant-inc/evener --comments`); the root-cause comment is the source of the first test.

- [ ] **Step 2: Write the two behavior tests**

Create `agent/delegate_attention_seen_test.go`:

```go
package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

// #3725: attention that reaches a running delegate and is shown to its model
// in a settled request of that same generation is consumed with it. The
// delegate does not start another generation for it, so it never repeats
// its handoff, and its parent's next message still resumes it.
func TestDelegatePresentedAttentionDoesNotEarnSuccessorGeneration(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			sub := root.subagents.get(fixture.childID)
			armStableSupervisionAttention(t, sub, "attention:mid-run", "gate job finished: exit 0")
			return communicateResponse(false, "running gates")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "gate job finished") {
				t.Errorf("second request did not present the mid-run attention")
			}
			return finalResponse("handoff: committed, waiting for review")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "FOLLOW-UP: also check the migration") {
				t.Errorf("third request is not the parent's follow-up")
			}
			return finalResponse("migration checked")
		},
	}
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (a successor generation re-ran the delegate)", got)
	}
	if got := attentionRunStarts(t, fixture); got != 0 {
		t.Fatalf("attention generations = %d, want 0", got)
	}
	if resolution, generation := childAttentionResolution(t, sub, "attention:mid-run"); resolution != delegateAttentionConsumed || generation != 0 {
		t.Fatalf("mid-run attention resolution = %q for generation %d, want consumed by the generation that presented it (0)", resolution, generation)
	}
	// The delegate does not stay marked as needing attention it consumed.
	controller := root.delegateController
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the delegate's needs-attention mark to clear", func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		aggregate := controller.durable[fixture.delegateID]
		return aggregate != nil && !aggregate.NeedsAttention
	})

	// The parent's next message still resumes the same delegate.
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "FOLLOW-UP: also check the migration", 60_000)
	if outcome.result.Err != nil || outcome.commit == nil {
		t.Fatalf("follow-up send = %#v", outcome)
	}
	plans, err := outcome.commit.Complete(true)
	if err != nil {
		t.Fatalf("acknowledge follow-up result: %v", err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("execute follow-up acknowledgement: %v", err)
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := supervisionRequestCount(fixture.adapter); got != 3 {
		t.Fatalf("provider requests after the follow-up = %d, want 3", got)
	}
	if got := attentionRunStarts(t, fixture); got != 0 {
		t.Fatalf("attention generations after the follow-up = %d, want 0", got)
	}
}

// Attention appended after the generation's last request was built has not
// been shown to the model, so it still wakes the delegate: one attention
// generation, whose request presents it.
func TestDelegateAttentionAfterLastRequestStillWakesTheDelegate(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			// This request is already built: the attention lands after the
			// generation's last model boundary.
			sub := root.subagents.get(fixture.childID)
			armStableSupervisionAttention(t, sub, "attention:late", "late gate job finished: exit 1")
			return finalResponse("handoff: committed, waiting for review")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "late gate job finished") {
				t.Errorf("the attention generation's request did not present the late attention")
			}
			return finalResponse("the late gate run failed; looking into it")
		},
	}
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (the late attention must start one generation)", got)
	}
	if got := attentionRunStarts(t, fixture); got != 1 {
		t.Fatalf("attention generations = %d, want 1", got)
	}
	if resolution, generation := childAttentionResolution(t, sub, "attention:late"); resolution != delegateAttentionConsumed || generation == 0 {
		t.Fatalf("late attention resolution = %q for generation %d, want consumed by its own attention generation", resolution, generation)
	}
}

// attentionRunStarts counts the fixture delegate's generations its journal
// records as started by attention.
func attentionRunStarts(t *testing.T, fixture coldStableDelegateFixture) int {
	t.Helper()
	events, err := delegatestore.ReadEvents(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
	if err != nil {
		t.Fatalf("read delegate journal: %v", err)
	}
	starts := 0
	for _, event := range events {
		if event.Kind == delegatestore.EventDelegateRunStarted && event.DelegateID == fixture.delegateID &&
			event.RunStarted.Trigger == delegatestore.TriggerAttention {
			starts++
		}
	}
	return starts
}

// childAttentionResolution reads how the child transcript resolved
// attentionID: its disposition, and the generation it was consumed for, which
// is zero when the generation that presented it consumed it.
func childAttentionResolution(t *testing.T, sub *subagent, attentionID string) (delegateAttentionResolution, uint64) {
	t.Helper()
	sess := sub.sess
	sess.mu.Lock()
	path, sessionID := transcriptPath(sess.stateDir, sess.id), sess.id
	sess.mu.Unlock()
	fold, err := sess.readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("fold child attention: %v", err)
	}
	return fold.resolutions[attentionID], fold.resumeGenerations[attentionID]
}
```

- [ ] **Step 3: Watch the first test fail for the right reason**

```bash
cd /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-attention-seen/agent
go test -race -count=1 -run 'TestDelegatePresentedAttentionDoesNotEarnSuccessorGeneration|TestDelegateAttentionAfterLastRequestStillWakesTheDelegate' .
```

Expected: `TestDelegatePresentedAttentionDoesNotEarnSuccessorGeneration` fails with `provider requests = 3, want 2` (the successor attention generation answered by the third step, which here is the follow-up step, so its `third request is not the parent's follow-up` error may print too). `TestDelegateAttentionAfterLastRequestStillWakesTheDelegate` passes: it pins behavior the fix must keep, and Step 7 proves it is not vacuous. Any other failure (a hang, a helper error) is a broken test: fix the test before going on.

- [ ] **Step 4: Rename the coverage machinery for what it now serves**

The coverage functions and fields stop being root-only. Rename them mechanically:

```bash
A=/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-attention-seen/agent
perl -pi -e 's/\bstageRootDelegateAttentionCoverage\b/stagePresentedAttentionCoverage/g; s/\bpromoteStagedRootDelegateAttention\b/promoteStagedAttentionCoverage/g; s/\bresetRootDelegateAttentionCoverage\b/resetAttentionCoverage/g; s/\bunionCoveredRootDelegateAttention\b/unionCoveredAttention/g; s/\brootAttentionCoveredIDs\b/attentionCoveredIDs/g; s/\brootAttentionStagedIDs\b/attentionStagedIDs/g; s/\brootAttentionPreTurnArmIDs\b/attentionPreTurnIDs/g' \
  $A/session.go $A/session_attention.go $A/session_attention_test.go $A/session_lifecycle.go $A/session_model_call.go
git -C /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-attention-seen grep -n 'RootDelegateAttentionCoverage\|StagedRootDelegateAttention\|CoveredRootDelegateAttention\|rootAttentionCoveredIDs\|rootAttentionStagedIDs\|rootAttentionPreTurnArmIDs' -- agent
```

Expected: the grep prints nothing. Then fix the two field comments in `agent/session.go` that the rename left reading oddly; the block becomes:

```go
	// attentionCoveredIDs is the running turn's coverage set. Stage it per
	// round, promote it on settle, and read it at turn finish; the contract
	// lives on stagePresentedAttentionCoverage and unionCoveredAttention.
	// Reset at each turn start.
	attentionCoveredIDs map[string]struct{}
	// attentionStagedIDs is one round's candidate coverage: the attention
	// the round's built request presents. The round loop promotes it into
	// attentionCoveredIDs only when the round's call settles, so a failed
	// or filtered round never credits an item the model saw in no settled
	// call. Every request build re-stages it.
	attentionStagedIDs map[string]struct{}
	// attentionPreTurnIDs snapshots rootAttentionWakeIDs at turn start
	// (resetAttentionCoverage). Marking consults it to separate the root's
	// deliveries armed mid-turn from attention already owed a dedicated
	// notification turn. A delegate session arms no root wakes, so for it
	// the snapshot is empty.
	attentionPreTurnIDs map[string]struct{}
```

Run `"$(go env GOROOT)/bin/gofmt" -l` on the five files (prints nothing) and `go test -count=1 -run 'TestRootDelegateAttention_' .` from `agent/` (passes: the rename changes no behavior).

- [ ] **Step 5: Cover presented attention in a delegate's leased turns**

In `agent/session_attention.go`, replace `stagePresentedAttentionCoverage` (the renamed `stageRootDelegateAttentionCoverage`, with its doc comment) with:

```go
// coversPresentedAttention reports whether this turn consumes the attention
// its settled requests present. The root receiver's turns do. So does a
// stable delegate's turn running under its generation's lease: the lease is
// the delegate's only start claim, since a delegate holds one reservation at
// a time and no attention reservation is admitted while its generation holds
// a live binding (delegateAttentionWakeEligibleLocked). The generation-less
// consumed marker written at the turn's end therefore cannot conflict with a
// reserved attention generation. An unleased turn on a child session covers
// nothing.
func (s *Session) coversPresentedAttention(ctx context.Context) bool {
	if s.isRootDelegateAttentionReceiver() {
		return true
	}
	_, leased := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease)
	return leased
}

// stagePresentedAttentionCoverage records one built request's candidate
// coverage: the attention IDs the request presents that were not armed for
// the root's own notification turn when this turn began. Staging is
// candidacy, not credit — the round loop promotes the staged set into
// attentionCoveredIDs only when the round's call settles, because a request
// that never settled (a failed attempt, a content-filter retry whose
// compaction then folds the steering turn away) presented nothing the design
// may consume. Only a turn that coversPresentedAttention stages.
//
// A responses-continuation delta request carries only new items; any older
// steering turn lives in server-side state this session cannot verify. Nothing
// stages, and the items keep their wake for a full-history turn.
func (s *Session) stagePresentedAttentionCoverage(ctx context.Context, req llm.Request, historyTurns []schema.Turn) {
	if !s.coversPresentedAttention(ctx) {
		return
	}
	if req.HistoryMode == llm.HistoryModeResponsesDelta {
		return
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	var staged map[string]struct{}
	for _, turn := range historyTurns {
		if turn.AttentionID == "" {
			continue
		}
		if _, preTurn := s.attentionPreTurnIDs[turn.AttentionID]; preTurn {
			continue
		}
		if staged == nil {
			staged = make(map[string]struct{})
		}
		staged[turn.AttentionID] = struct{}{}
	}
	s.attentionStagedIDs = staged
}
```

Replace `resetAttentionCoverage` (with its doc comment) with the unguarded form. It must clear on every turn: a child's unleased turn that follows a leased one must not union the leased turn's coverage.

```go
// resetAttentionCoverage clears the per-turn coverage at turn start and
// snapshots the root's armed set that marking excludes, so the root credits
// only deliveries armed after this turn began. It runs for every turn, so a
// turn that covers nothing never inherits an earlier turn's coverage.
// maps.Clone(nil) is nil, and the field is lookup-only, so an empty armed set
// needs no special case.
func (s *Session) resetAttentionCoverage() {
	s.attentionMu.Lock()
	s.attentionCoveredIDs = nil
	s.attentionStagedIDs = nil
	s.attentionPreTurnIDs = maps.Clone(s.rootAttentionWakeIDs)
	s.attentionMu.Unlock()
}
```

In `finishRootDelegateAttentionTurn`, add as the first statement (before the existing comment block that opens the function body):

```go
	// A delegate's failed turn leaves what it presented pending for its next
	// attention generation; the paced retry and the paused notice below
	// belong to the root's rail.
	if turnErr != nil && !s.isRootDelegateAttentionReceiver() {
		return nil
	}
```

and extend its doc comment's first sentence to say it runs for every turn: "finishRootDelegateAttentionTurn consumes the exact selected IDs only after a successful model turn and durable resolution markers — plus any still-pending attention this turn's built requests already presented to the model, at the root or in a delegate's leased generation, which needs no wake of its own."

In `agent/session_model_call.go`, the staging call becomes `s.stagePresentedAttentionCoverage(ctx, req, historyTurns)`. In `agent/session_attention_test.go`, the four staging calls gain a context:

```bash
perl -pi -e 's/\.stagePresentedAttentionCoverage\(llm\.Request/.stagePresentedAttentionCoverage(context.Background(), llm.Request/g' \
  /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-attention-seen/agent/session_attention_test.go
```

- [ ] **Step 6: Watch both tests pass, then the coverage suites**

```bash
cd /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-attention-seen/agent
go test -race -count=1 -run 'TestDelegatePresentedAttentionDoesNotEarnSuccessorGeneration|TestDelegateAttentionAfterLastRequestStillWakesTheDelegate|TestRootDelegateAttention_' .
go test -race -count=3 -run 'TestDelegatePresentedAttentionDoesNotEarnSuccessorGeneration|TestDelegateAttentionAfterLastRequestStillWakesTheDelegate' .
```

Expected: PASS, including three repeated runs of the new tests. If the needs-attention wait times out, nothing re-drove the delegate after its generation released, so the projection kept `needs_attention: true` for attention already consumed. That is a regression of this change: debug it at the root (superpowers:systematic-debugging), starting from what wakes `driveStableDelegateAttention` after `releaseFinalizationLocked`, whose empty-pending branch calls `clearResolvedDelegateAttention`.

- [ ] **Step 7: Commit, then prove the late-attention test is not vacuous**

```bash
W=/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-attention-seen
git -C $W add agent/session.go agent/session_attention.go agent/session_attention_test.go agent/session_lifecycle.go agent/session_model_call.go agent/delegate_attention_seen_test.go
git -C $W diff --cached --stat
git -C $W commit -m "fix(agent): a delegate does not wake again for attention its run already showed the model

A delegate generation that presented mid-run attention to its model in a
settled request left it pending, so each item started one more attention
generation after the handoff and the model repeated its report (#3725).
A delegate turn running under its generation's lease now uses the root's
request-bound coverage: covered items still pending are consumed at the
turn's end, before the generation finishes. The lease is the delegate's
only start claim, so the generation-less marker cannot conflict with a
reserved attention generation. Attention appended after the last request
still starts its own generation.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

Now break the boundary on purpose: in `finishRootDelegateAttentionTurn`, change `ids = s.unionCoveredAttention(ids)` to `ids, _ = s.pendingDelegateAttentionIDs()` (consume everything pending at the turn's end, presented or not), and run:

```bash
cd /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-attention-seen/agent
go test -count=1 -run 'TestDelegateAttentionAfterLastRequestStillWakesTheDelegate' .
git -C /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-attention-seen checkout -- agent/session_attention.go
```

Expected: the test fails with `provider requests = 1, want 2` (the late attention was swallowed), and the checkout restores the committed fix. Confirm `git -C <worktree> status --short` is clean.

- [ ] **Step 8: Run the attention and supervision suites; settle changed expectations**

```bash
cd /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-attention-seen/agent
go test -race -count=1 -run 'Attention|Supervision|StableDelegate|DelegateResource|Delegate.*Drive' . 2>&1 | tail -40
```

A failure here falls in one of two kinds. A test that asserted a successor generation (or an extra provider request, an extra report, or a `consumed` marker with a resume generation) for attention that a settled request of the same leased generation had already presented pinned #3725's behavior: change its expectation to the new contract and list it in the PR body with one line each. A test whose attention arrived after the generation's last request, or reached an unleased turn, must keep its expectation: a failure there is a defect in this change; debug it at the root. Re-run until green.

- [ ] **Step 9: Docs**

In `docs/superpowers/specs/2026-08-29-stable-delegate-attention-no-action-design.md`, the status line becomes:

```markdown
- **Status:** Proposed after simplification and adversarial review. Partly reversed on 2026-10-10 by Jesse's ruling on #3725: attention a delegate generation already showed its model in a settled request is consumed with that generation, as the root's is, so it earns no successor generation. The "Consume attention in the ordinary generation" alternative and the transcript-visibility and no-skip non-goals no longer hold for that case; the root's settled-request coverage is the request-bound evidence they asked for.
```

In `docs/job-control.md`, after the bullet that begins "If the owner is mid-turn or awaiting an `ask_user` reply" (line 1394), add:

```markdown
- Attention a turn already showed the model is consumed with that turn. When a
  settled model request of a root turn, or of a delegate turn running in its
  generation, carried an attention entry, the turn's successful end appends the
  consumed marker, so the entry starts no further turn or attention generation.
  Attention appended after the turn's last request was built keeps its own wake.
```

In `docs/subagent-management/11-delegate-resource-model.md`, after step 8 of "Attention drives" (the list item ending "provider-excluded consumed resolution marker per bound ID.", line 1534), add the paragraph:

```markdown
Attention that reaches a delegate while one of its generations runs, whatever
started that generation, joins the generation's history and is presented at
its next model request. A settled request that presented it covers it: when the
turn ends successfully, still inside the generation's lease, the runtime appends
and fsyncs a generation-less consumed marker for each covered ID that is still
pending, so the delegate is not driven again for attention its model has
already seen. The lease is the delegate's only start claim (no attention
reservation is admitted while a generation holds a live binding), so the marker
cannot conflict with a reserved generation. A failed turn leaves its covered
IDs pending; attention appended after the generation's last request was built
stays pending and starts its own attention generation; a turn outside a lease
covers nothing.
```

In `docs/product/subsystems.md`, in the S12 row, change "Stable attention starts revalidate transcript work after claiming a retained child; accepted starts retain their exact generation through journal-commit retries." to "Stable attention starts revalidate transcript work after claiming a retained child; a delegate generation consumes the attention its settled requests already presented, so only attention that arrives after its last request starts another generation; accepted starts retain their exact generation through journal-commit retries."

```bash
W=/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/delegate-attention-seen
git -C $W add docs/superpowers/specs/2026-08-29-stable-delegate-attention-no-action-design.md docs/job-control.md docs/subagent-management/11-delegate-resource-model.md docs/product/subsystems.md
git -C $W diff --cached --stat
git -C $W commit -m "docs: a delegate consumes attention its generation already presented

Records Jesse's 2026-10-10 ruling on #3725 in the no-action design's
status, and states the coverage rule in job control, the delegate
resource model and the S12 row.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

- [ ] **Step 10: Gates**

Run the agent-module gates from Global Constraints (`go build`, the three `go vet` runs, toolchain gofmt on the touched `.go` files, `golangci-lint run --allow-serial-runners ./...`, the targeted race run from Step 6, then the full race run wrapped in `race-serial` from `agent/`). Set `-o pipefail` before gating on a piped command. Merge `origin/main` before the last gate run.

- [ ] **Step 11: Open the PR**

Run `/simplify`, push its fixes, and open the PR (base `main`, regular) titled "fix(agent): a delegate does not wake again for attention its run already showed the model". The body: "Closes #3725."; the mechanism in two sentences (the root's settled-request coverage now covers a delegate's leased turns; the lease is the only start claim, so the generation-less marker cannot meet a reservation); the two tests and what each pins (no successor generation plus the parent's follow-up still resumes; attention after the last request still wakes), with the red run from Step 3 and the mutation run from Step 7; the existing tests whose expectations changed in Step 8 and why; the 2026-08-29 design status change; the `/simplify` outcome; ending with `https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU`. Shepherd it to the merge gate.


## PR 4: communicate(end_turn=false) delivers a delegate's update to its parent

PR 4 needs PRs 1, 2, 3 and 3a. Branch from `origin/main` once they have merged (or from PR 3's branch, rebasing as each lands). Its body says "Closes #4172".

### Task 5: The controller posts an update into the delegate's delivery queue

**Files:**
- Modify: `agent/delegate_delivery.go` (`PostUpdate`; `newHeadDeliveryPlanLocked` lines 649-651)
- Test: `agent/delegate_update_test.go` (new)

**Interfaces:**
- Consumes: `delegatestore.EventDelegateUpdatePosted`, `delegatestore.UpdatePosted`, `delegatestore.UpdateDeliveryID`, `delegatestore.PacketUpdate` (Task 1).
- Produces: `func (c *delegateTreeController) PostUpdate(actor delegateActor, message string) (deliveryID string, plans delegateMutationPlans, err error)`. Errors: `errDelegateNotControllable` for an actor without a lease (the root), `errDelegateStaleLease` (wrapped) for a run that is not current, `errDelegateTargetBusy` for a stopping, settling or closing delegate or a fenced ancestor, or the journal's own error. On success the update is durable, and `plans.deliveries` holds its delivery plan when it is the delegate's head. A head update's plan never carries the generation's inline waiter.

- [ ] **Step 1: Write the failing controller tests**

Create `agent/delegate_update_test.go`:

```go
package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// A running delegate's update joins its own delivery queue behind whatever is
// already there. Only the head is planned, and its plan is the update's.
func TestDelegateControllerPostUpdateQueuesInDeliveryOrder(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	actor := delegateActor{lease: &delegateLease{delegateID: "dlg_target", generation: 1}}

	first, firstPlans, err := c.PostUpdate(actor, "first question")
	if err != nil {
		t.Fatalf("PostUpdate first: %v", err)
	}
	if !strings.HasPrefix(first, "dlg_target/update/") || len(firstPlans.deliveries) != 1 ||
		firstPlans.deliveries[0].deliveryID != first || firstPlans.deliveries[0].packet.Kind != delegatestore.PacketUpdate {
		t.Fatalf("first update %q planned %#v, want its own head plan", first, firstPlans.deliveries)
	}
	second, secondPlans, err := c.PostUpdate(actor, "second question")
	if err != nil {
		t.Fatalf("PostUpdate second: %v", err)
	}
	if len(secondPlans.deliveries) != 0 {
		t.Fatalf("second update planned behind the first: %#v", secondPlans.deliveries)
	}
	pending := delegateAggregateSnapshot(t, c, "dlg_target").PendingDeliveries
	if len(pending) != 2 || pending[0].DeliveryID != first || pending[1].DeliveryID != second {
		t.Fatalf("pending deliveries = %#v, want both updates in the order posted", pending)
	}
}

// Review Focus 1: an update at the head never takes the generation's inline
// waiter. The caller waiting in delegate_send wants the report, which will
// carry the update ahead of itself.
func TestDelegateControllerUpdateHeadLeavesTheInlineWaiter(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	if err := c.AttachRuntime(lease, &Session{}); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	if _, err := c.AdmitStartInput(lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}
	_, plans, err := c.PostUpdate(delegateActor{lease: &lease}, "which table?")
	if err != nil {
		t.Fatalf("PostUpdate: %v", err)
	}
	if len(plans.deliveries) != 1 || plans.deliveries[0].waiter != nil {
		t.Fatalf("update plans = %#v, want one head plan without the waiter", plans.deliveries)
	}
	c.mu.Lock()
	kept := c.live["dlg_target"].waiters[lease.generation]
	c.mu.Unlock()
	if kept != waiter {
		t.Fatalf("generation waiter = %p, want the registered %p still waiting for the report", kept, waiter)
	}
}

// An update the journal refuses is never queued: the root has no parent, a
// stale run cannot speak, and a stopping delegate's route is closed.
func TestDelegateControllerPostUpdateRefusals(t *testing.T) {
	t.Parallel()
	running := delegateLease{delegateID: "dlg_target", generation: 1}
	for _, tc := range []struct {
		name  string
		actor func(t *testing.T, c *delegateTreeController) delegateActor
		want  error
	}{
		{"root has no parent", func(*testing.T, *delegateTreeController) delegateActor {
			return rootDelegateActor("root-session")
		}, errDelegateNotControllable},
		{"stale run", func(*testing.T, *delegateTreeController) delegateActor {
			return delegateActor{lease: &delegateLease{delegateID: "dlg_target", generation: 2}}
		}, errDelegateStaleLease},
		{"stopping", func(t *testing.T, c *delegateTreeController) delegateActor {
			if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
				t.Fatalf("StopSubtree: %v", err)
			}
			return delegateActor{lease: &running}
		}, errDelegateTargetBusy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := newDelegateControllerTestHarness(t, 1, 1)
			seedDelegateControllerRunning(t, c, "dlg_target", "")
			_, plans, err := c.PostUpdate(tc.actor(t, c), "which table?")
			if !errors.Is(err, tc.want) || len(plans.deliveries) != 0 {
				t.Fatalf("PostUpdate = plans %#v err %v, want %v", plans.deliveries, err, tc.want)
			}
			if pending := delegateAggregateSnapshot(t, c, "dlg_target").PendingDeliveries; len(pending) != 0 {
				t.Fatalf("refused update queued: %#v", pending)
			}
		})
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd agent && go test -count=1 -run 'TestDelegateControllerPostUpdate|TestDelegateControllerUpdateHead' .`
Expected: build failure, `c.PostUpdate undefined`.

- [ ] **Step 3: Implement `PostUpdate` and the waiter guard**

In `agent/delegate_delivery.go`, in `newHeadDeliveryPlanLocked`, replace `waiter := c.claimDelegateWaiterLocked(delegateID, head.Generation)` with:

```go
	// An update never answers an inline wait: the waiting caller wants the
	// generation's report, and the report's reply carries any update still
	// ahead of it (carriableHeldClaimLocked).
	var waiter *delegateInlineWaiter
	if head.Packet.Kind != delegatestore.PacketUpdate {
		waiter = c.claimDelegateWaiterLocked(delegateID, head.Generation)
	}
```

and add after `ReplayDeliveries`:

```go
// PostUpdate records a running delegate's mid-work update in its journal,
// queued behind the delegate's earlier deliveries and ahead of the report its
// generation will settle, and plans the update's delivery when it is the
// delegate's head. It refuses a run that is no longer current, a delegate
// that is stopping or whose turn is ending, and a closed route to the
// parent; the update is then not delivered anywhere.
func (c *delegateTreeController) PostUpdate(actor delegateActor, message string) (string, delegateMutationPlans, error) {
	release, err := c.beginRetirementMutation()
	if err != nil {
		return "", delegateMutationPlans{}, err
	}
	defer release()
	if strings.TrimSpace(message) == "" {
		return "", delegateMutationPlans{}, errors.New("invalid_request: message is required")
	}
	if actor.lease == nil {
		return "", delegateMutationPlans{}, errDelegateNotControllable
	}
	lease := *actor.lease
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, _, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning); err != nil {
		return "", delegateMutationPlans{}, err
	}
	if c.hasSettlementClaimLocked(lease) {
		return "", delegateMutationPlans{}, errDelegateTargetBusy
	}
	appended, err := c.appendLocked(delegatestore.Event{
		Kind:         delegatestore.EventDelegateUpdatePosted,
		TS:           c.now(),
		DelegateID:   lease.delegateID,
		UpdatePosted: &delegatestore.UpdatePosted{Generation: lease.generation, Message: message},
	})
	if err != nil {
		return "", delegateMutationPlans{}, err
	}
	deliveryID := delegatestore.UpdateDeliveryID(lease.delegateID, appended[0].Seq)
	c.evidenceVersion++
	var plans delegateMutationPlans
	if pending := c.durable[lease.delegateID].PendingDeliveries; len(pending) != 0 && pending[0].DeliveryID == deliveryID {
		if plan := c.newHeadDeliveryPlanLocked(lease.delegateID, deliveryID); plan != nil {
			plans.deliveries = append(plans.deliveries, *plan)
		}
	}
	return deliveryID, plans, nil
}
```

- [ ] **Step 4: Run them to see them pass, and the delivery suites stay green**

Run: `cd agent && go test -count=1 -run 'TestDelegateControllerPostUpdate|TestDelegateControllerUpdateHead|TestDelegateController|TestDelegateSendWait' .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/delegate_delivery.go agent/delegate_update_test.go
git diff --cached --stat
git commit -m "feat(delegates): the controller posts a running delegate's update into its delivery queue

PostUpdate admits the poster's exact running lease, appends
delegate_update_posted, and plans the update when it is the delegate's
head. An update at the head never takes the generation's inline waiter;
the report's reply carries it instead.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

### Task 6: A delegate's `communicate(end_turn=false)` posts the update and reports where it stands

**Files:**
- Modify: `agent/session_tools_communicate.go` (`delegateUpdateDelivery`, `Session.postDelegateUpdate`, `notDeliveredUpdate`, the Exec wiring)
- Modify: `agent/delegate_delivery.go` (`deliveryPending`)
- Modify: `agent/session_tool_registry.go` (`toolDeps.postDelegateUpdate`, `newToolDeps`)
- Modify: `agent/internal/tool/definitions.go` (`communicateDelegateDescription` gains the result sentence)
- Modify: `agent/delegate_tree_controller_test.go` (lifecycle set and inventory gain `PostUpdate`)
- Modify: `agent/delegate_messaging_test_helpers_test.go` (`rootPendingAttention`, `rootAttentionFold`)
- Regenerate: `agent/testdata/messagingprompt/delegate.md`
- Test: `agent/delegate_update_test.go`

**Interfaces:**
- Consumes: `PostUpdate` (Task 5), `Session.reportsToParent` and `toolDeps.reportsToParent` (Task 4).
- Produces:
  - `type delegateUpdateDelivery struct { Status, Reason string }` with `Status` one of `delegateUpdateDelivered = "delivered"`, `delegateUpdateQueued = "queued"`, `delegateUpdateNotDelivered = "not_delivered"`.
  - `func (s *Session) postDelegateUpdate(ctx context.Context, message string) (update delegateUpdateDelivery, ok bool)`; `ok` is false for a root, whose `end_turn=false` stays a checkpoint for its human partner.
  - `toolDeps.postDelegateUpdate func(ctx context.Context, message string) (delegateUpdateDelivery, bool)`.
  - A delegate's `communicate(end_turn=false)` result JSON gains `"delivery": "<status>"` and, for `not_delivered`, `"reason": "<sentence>"`. A root's result is unchanged.
  - `func (c *delegateTreeController) deliveryPending(delegateID, deliveryID string) bool`.
  - Test helpers `rootPendingAttention(root *Session) []string` and `rootAttentionFold(t *testing.T, root *Session) delegateAttentionFold`.

- [ ] **Step 1: Add the attention-reading helpers**

Append to `agent/delegate_messaging_test_helpers_test.go`:

```go
// rootPendingAttention lists root's unresolved attention in transcript order,
// read from its transcript the way restore reads it, or nil when the read
// fails.
func rootPendingAttention(root *Session) []string {
	fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, root.ID()), root.ID())
	if err != nil {
		return nil
	}
	return fold.pendingIDs()
}

// rootAttentionFold is root's attention fold, read from its transcript.
func rootAttentionFold(t *testing.T, root *Session) delegateAttentionFold {
	t.Helper()
	fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("fold root attention: %v", err)
	}
	return fold
}
```

- [ ] **Step 2: Write the failing session tests**

Add to `agent/delegate_update_test.go` (extend its imports with `context`, `encoding/json`, `fmt`, `time`, `"github.com/spf13/afero"`, `toolpkg "primeradiant.com/evener/agent/internal/tool"`, `"primeradiant.com/evener/agent/schema"`, `"primeradiant.com/evener/llm"`):

```go
// A delegate's communicate(end_turn=false) is an update its parent receives.
// It stays in the delegate's own transcript, lands in an idle root's
// transcript as delegate attention with the update frame, and wakes the root.
// The root's next notification turn presents it and consumes it, and the
// delegate's tool result says it was delivered. (Reworked from the dropped
// caller-route WIP test 6f67c3f327.)
func TestDelegateUpdateWakesAnIdleRoot(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateResponse(false, "need the heading from you") },
		func(llm.Request) llm.Response { return finalResponse("summary written") },
		func(llm.Request) llm.Response { return finalResponse("noted") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	warmStableDelegateUnservedRoot(t, root, fixture)

	pending := rootPendingAttention(root)
	if len(pending) != 1 || !strings.HasPrefix(pending[0], delegateAttentionID(fixture.delegateID+"/update/")) {
		t.Fatalf("root attention = %v, want the delegate's update alone", pending)
	}
	frame := rootAttentionFold(t, root).content[pending[0]].Text()
	for _, attr := range []string{`delegate_id="` + fixture.delegateID + `"`, `kind="update"`, ">need the heading from you</delegate-notification>"} {
		if !strings.Contains(frame, attr) {
			t.Fatalf("update frame %q lacks %q", frame, attr)
		}
	}
	select {
	case <-wakes:
	default:
		t.Fatal("the update did not wake the root")
	}
	requests := fixture.adapter.Requests()
	if len(requests) < 2 || !strings.Contains(lastToolResultText(requests[1].Messages), `"delivery":"delivered"`) {
		t.Fatalf("the delegate's communicate result does not say delivered")
	}
	var checkpoints []string
	for _, entry := range decodeTranscriptEntries(t, afero.NewOsFs(), transcriptPath(root.stateDir, fixture.childID)) {
		if entry.Turn.Kind == schema.TurnCommunicate && entry.Turn.Communicate != nil && !entry.Turn.Communicate.EndTurn {
			checkpoints = append(checkpoints, entry.Turn.Communicate.Message)
		}
	}
	if len(checkpoints) != 1 || checkpoints[0] != "need the heading from you" {
		t.Fatalf("delegate transcript updates = %#v, want the update recorded", checkpoints)
	}

	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("root notification turn: %v", err)
	}
	requests = fixture.adapter.Requests()
	if !requestContainsText(requests[len(requests)-1], "need the heading from you") {
		t.Fatal("the root's notification turn did not present the update")
	}
	if got := rootAttentionFold(t, root).resolutions[pending[0]]; got != delegateAttentionConsumed {
		t.Fatalf("update attention = %q, want consumed", got)
	}
}

// An update the journal refuses still succeeds as a communicate call: the
// message is already in the delegate's transcript. The result says it was not
// delivered and why, and nothing is queued for the parent.
func TestDelegateUpdateNotDeliveredStillSucceeds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		refuse func(t *testing.T, c *delegateTreeController, lease delegateLease) delegateLease
	}{
		{"stopping delegate", func(t *testing.T, c *delegateTreeController, lease delegateLease) delegateLease {
			if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), lease.delegateID); err != nil {
				t.Fatalf("StopSubtree: %v", err)
			}
			return lease
		}},
		{"stale run", func(_ *testing.T, _ *delegateTreeController, lease delegateLease) delegateLease {
			return delegateLease{delegateID: lease.delegateID, generation: lease.generation + 1}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := newDelegateControllerTestHarness(t, 1, 1)
			seedDelegateControllerRunning(t, c, "dlg_target", "")
			runtime := attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
			runtime.profile = NewOpenAIProfile("gpt-5.2")
			runtime.delegateController = c
			runtime.owningDelegateID = "dlg_target"
			lease := tc.refuse(t, c, delegateLease{delegateID: "dlg_target", generation: 1})

			reg := toolpkg.NewRegistry()
			registerCommunicateTool(reg, newToolDeps(runtime))
			ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
			out, err := reg.Get("communicate").Exec(ctx, nil, map[string]any{"message": "need the schema", "end_turn": false})
			if err != nil {
				t.Fatalf("communicate: %v", err)
			}
			var result map[string]any
			if err := json.Unmarshal([]byte(fmt.Sprint(out)), &result); err != nil {
				t.Fatalf("decode result %v: %v", out, err)
			}
			if result["accepted"] != true || result["delivery"] != "not_delivered" {
				t.Fatalf("result = %v, want accepted and not_delivered", result)
			}
			if reason, _ := result["reason"].(string); reason == "" {
				t.Fatalf("result = %v, want a reason", result)
			}
			if pending := delegateAggregateSnapshot(t, c, "dlg_target").PendingDeliveries; len(pending) != 0 {
				t.Fatalf("refused update queued: %#v", pending)
			}
		})
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `cd agent && go test -count=1 -run 'TestDelegateUpdateWakesAnIdleRoot|TestDelegateUpdateNotDeliveredStillSucceeds' .`
Expected: FAIL: the root has no pending attention (the update went nowhere), and the result has no `delivery` key.

- [ ] **Step 4: Implement the session side and the tool wiring**

In `agent/delegate_delivery.go`, after `PostUpdate`:

```go
// deliveryPending reports whether delegateID's delivery queue still holds
// deliveryID unacknowledged.
func (c *delegateTreeController) deliveryPending(delegateID, deliveryID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	return aggregate != nil && slices.ContainsFunc(aggregate.PendingDeliveries, func(pending delegatestore.PendingDelivery) bool {
		return pending.DeliveryID == deliveryID
	})
}
```

In `agent/session_tools_communicate.go`, after `communicateEndState`:

```go
// delegateUpdateDelivery is where a delegate's update stands when its
// communicate(end_turn=false) call returns: the result's delivery and reason.
type delegateUpdateDelivery struct {
	Status string
	Reason string
}

const (
	// The update is in the parent's transcript as attention.
	delegateUpdateDelivered = "delivered"
	// The update is durable in the delegate's journal and reaches the parent
	// without anyone acting: the parent holds it for its next model boundary,
	// or an earlier delivery of the same delegate is ahead of it.
	delegateUpdateQueued = "queued"
	// The journal refused the update; the message stays in the delegate's
	// own transcript only.
	delegateUpdateNotDelivered = "not_delivered"
)

// postDelegateUpdate hands a delegate's communicate(end_turn=false) message
// to its parent as an update. ok is false for a root, whose end_turn=false
// message is a checkpoint for its human partner and goes nowhere else.
func (s *Session) postDelegateUpdate(ctx context.Context, message string) (delegateUpdateDelivery, bool) {
	if !s.reportsToParent() {
		return delegateUpdateDelivery{}, false
	}
	if s.delegateController == nil || s.owningDelegateID == "" {
		return delegateUpdateDelivery{Status: delegateUpdateNotDelivered, Reason: "this session has no route to a parent agent"}, true
	}
	actor, err := s.delegateActor(ctx)
	if err != nil {
		return notDeliveredUpdate(err), true
	}
	deliveryID, plans, err := s.delegateController.PostUpdate(actor, message)
	if err != nil {
		return notDeliveredUpdate(err), true
	}
	if err := s.executeDelegateMutationPlans(plans); err != nil {
		// The update is durable in the journal, and executeDelegateMutationPlans
		// has already requeued its delivery for retry.
		s.emit(events.EventWarning, warningDataFromError("deliver delegate update", err))
	}
	if s.delegateController.deliveryPending(s.owningDelegateID, deliveryID) {
		return delegateUpdateDelivery{Status: delegateUpdateQueued}, true
	}
	return delegateUpdateDelivery{Status: delegateUpdateDelivered}, true
}

// notDeliveredUpdate says, for the delegate's model, why its update was
// refused.
func notDeliveredUpdate(err error) delegateUpdateDelivery {
	reason := "it could not be recorded: " + err.Error()
	switch {
	case errors.Is(err, errDelegateStaleLease):
		reason = "this run of yours is no longer the current one"
	case errors.Is(err, errDelegateTargetBusy), errors.Is(err, errDelegateNotControllable):
		reason = "the route to your parent is closed: you are stopping or finishing, your parent is gone, or the session is shutting down"
	}
	return delegateUpdateDelivery{Status: delegateUpdateNotDelivered, Reason: reason}
}
```

In the communicate `Exec`, right after the `deps.deliverCommunicate(...)` error check:

```go
			// A delegate's end_turn=false message is an update for its parent.
			// The message is already in this transcript, so delivery never
			// fails the call; the result says where the update stands.
			var update delegateUpdateDelivery
			isUpdate := false
			if !endTurn && deps.postDelegateUpdate != nil {
				update, isUpdate = deps.postDelegateUpdate(ctx, message)
			}
```

and right after `resp := map[string]any{...}`:

```go
			if isUpdate {
				resp["delivery"] = update.Status
				if update.Reason != "" {
					resp["reason"] = update.Reason
				}
			}
```

In `agent/session_tool_registry.go`, add to `toolDeps` after `reportsToParent`:

```go
	// postDelegateUpdate hands a delegate's end_turn=false message to its
	// parent (Session.postDelegateUpdate); ok is false for a root.
	postDelegateUpdate func(ctx context.Context, message string) (update delegateUpdateDelivery, ok bool)
```

and to `newToolDeps` after `reportsToParent: s.reportsToParent(),`:

```go
		postDelegateUpdate:     s.postDelegateUpdate,
```

In `agent/delegate_tree_controller_test.go`, add `"PostUpdate": true,` to `delegateControllerLifecycleMethods` beside `"Steer": true,`, and add to `delegateControllerDormancyExpectedInventory`:

```go
	{filename: "session_tools_communicate.go", function: "(*Session).postDelegateUpdate", kind: "lifecycle method", symbol: "PostUpdate"}: 1,
```

In `agent/internal/tool/definitions.go`, in `communicateDelegateDescription`, insert after `"...never to narrate progress, and keep working in the following round. "`:

```go
	"The result's `delivery` says whether your parent has the update (`delivered`), will get it at its next step (`queued`), or cannot get it (`not_delivered`, with a `reason`); the call succeeds either way. " +
```

- [ ] **Step 5: Run the tests and regenerate the delegate golden**

Run: `cd agent && go test -count=1 -run 'TestDelegateUpdateWakesAnIdleRoot|TestDelegateUpdateNotDeliveredStillSucceeds|TestDelegateControllerProductionIntegrationMatchesInventory' .`
Expected: PASS.
Run: `cd agent && go test -count=1 -run 'TestDelegateMessagingGuidanceGoldens$' . -update-prompt && git diff testdata/messagingprompt`
Expected: only `delegate.md`'s communicate description gains the `delivery` sentence.

- [ ] **Step 6: Commit**

```bash
git add agent/session_tools_communicate.go agent/delegate_delivery.go agent/session_tool_registry.go \
  agent/internal/tool/definitions.go agent/delegate_tree_controller_test.go \
  agent/delegate_messaging_test_helpers_test.go agent/delegate_update_test.go agent/testdata/messagingprompt/delegate.md
git diff --cached --stat
git commit -m "feat(delegates): a delegate's communicate(end_turn=false) reaches its parent

The update is posted to the delegate's journal and delivered through the
report's attention path: an idle parent is woken, a busy one sees it at its
next model boundary. The result says delivered, queued or not_delivered
with the reason; the call never fails over delivery.

Closes #4172.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

### Task 7: The delivery matrix: running parents, waits, questions, order, nesting, restart

The behavior these tests pin is the report path's, inherited by updates in Tasks 5 and 6, so each is expected to pass on its first run. A failure is a real defect: debug it at the root (superpowers:systematic-debugging). Step 3 proves the tests are not vacuous.

**Files:**
- Modify: `agent/delegate_messaging_test_helpers_test.go` (`newMessagingRoot` takes `configure`; `updateResponse`, `delegateSendResponse`, `jobListResponse`, `awaitSignal`, `waitForRootAttention`; add `fmt` import)
- Modify: `agent/delegate_messaging_guidance_test.go` (call `newMessagingRoot(t, adapter, nil)`)
- Test: `agent/delegate_update_test.go`

**Interfaces:**
- Consumes: Tasks 4–6.
- Produces: `newMessagingRoot(t *testing.T, adapter *messagingAdapter, configure func(*SessionConfig)) *Session`; `updateResponse(id, message string) llm.Response`; `delegateSendResponse(id, delegateID, message string, maxWaitMS int) llm.Response`; `jobListResponse(id string) llm.Response`; `awaitSignal(t *testing.T, ch <-chan struct{}, what string)`; `waitForRootAttention(t *testing.T, root *Session, n int) []string`; `appendColdFixtureEvents(t *testing.T, fixture coldStableDelegateFixture, events ...delegatestore.Event) []delegatestore.Event`; `updateAttentionConsumedFor(t *testing.T, sess *Session) uint64` (in `agent/delegate_update_test.go`).

- [ ] **Step 1: Extend the helpers**

In `agent/delegate_messaging_test_helpers_test.go`, replace `newMessagingRoot` with:

```go
// newMessagingRoot starts a root session answered by adapter. configure, when
// set, adjusts the root's config first, for test-only hooks.
func newMessagingRoot(t *testing.T, adapter *messagingAdapter, configure func(*SessionConfig)) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(adapter)
	cfg := SessionConfig{StateDir: t.TempDir()}
	if configure != nil {
		configure(&cfg)
	}
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	adapter.mu.Lock()
	adapter.rootID = sess.ID()
	adapter.mu.Unlock()
	return sess
}
```

update the call in `agent/delegate_messaging_guidance_test.go` to `newMessagingRoot(t, adapter, nil)`, add `"fmt"` to the helper file's imports, and append:

```go
// updateResponse is a delegate's model turn that sends message to its parent
// as an update: communicate(end_turn=false).
func updateResponse(id, message string) llm.Response {
	return toolCallResponse(communicateCallArgs(id, map[string]any{"message": message, "end_turn": false}))
}

// delegateSendResponse is a model turn that sends message to delegateID and
// waits up to maxWaitMS for the generation it starts.
func delegateSendResponse(id, delegateID, message string, maxWaitMS int) llm.Response {
	args, _ := json.Marshal(map[string]any{"to": delegateID, "message": message, "max_wait_ms": maxWaitMS})
	return toolCallResponse(llm.ToolCallData{ID: id, Name: "delegate_send", Arguments: args, Type: "function"})
}

// jobListResponse is a model turn that calls job_list: a tool round that
// changes nothing, so the request after it is the turn's next model boundary.
func jobListResponse(id string) llm.Response {
	return toolCallResponse(llm.ToolCallData{ID: id, Name: "job_list", Arguments: json.RawMessage(`{}`), Type: "function"})
}

// awaitSignal waits for ch to close. Scripted steps call it on provider
// goroutines, so a hang is reported with Errorf, never Fatalf.
func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	select {
	case <-ch:
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	case <-time.After(30 * time.Second):
		t.Errorf("timed out waiting for %s", what)
	}
}

// waitForRootAttention waits until root holds at least n unresolved
// attention items and returns them in transcript order.
func waitForRootAttention(t *testing.T, root *Session, n int) []string {
	t.Helper()
	var pending []string
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, fmt.Sprintf("root to hold %d unresolved attention items", n), func() bool {
		pending = rootPendingAttention(root)
		return len(pending) >= n
	})
	return pending
}
```

- [ ] **Step 2: Write the matrix tests**

Add to `agent/delegate_update_test.go` (extend its imports with `slices` and `sync`):

```go
// A root mid-turn holds a delegate's update for its next model boundary: the
// request after the tool round presents it, and the turn that presented it
// consumes it, so it earns no wake of its own.
func TestDelegateUpdateReachesARunningRootAtItsNextModelBoundary(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("WORKER-BRIEF")
	release, deferred := make(chan struct{}), make(chan struct{})
	var deferredOnce sync.Once
	var root *Session
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "WORKER-BRIEF: ask your parent which table holds the drain cursor.")
		},
		func(llm.Request) llm.Response {
			close(release)
			awaitSignal(t, deferred, "the update deferred behind the root's turn")
			return jobListResponse("list")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, `kind="update"`) || !requestContainsText(req, "RUNNING_UPDATE") {
				t.Error("the root's next request does not present the update")
			}
			return finalResponse("root done")
		},
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, release, "the root's second request")
			return updateResponse("upd", "RUNNING_UPDATE: which table holds the drain cursor?")
		},
		func(llm.Request) llm.Response { return finalResponse("worker done") },
	)
	root = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.testOnly.delegateDeliveryClassified = func(s *Session, isDeferred bool) {
			if s == root && isDeferred {
				deferredOnce.Do(func() { close(deferred) })
			}
		}
	})
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	fold := rootAttentionFold(t, root)
	var updateID string
	for _, id := range fold.order {
		if strings.Contains(id, "/update/") {
			updateID = id
		}
	}
	if updateID == "" || fold.resolutions[updateID] != delegateAttentionConsumed {
		t.Fatalf("update attention %q = %q, want consumed by the turn that presented it", updateID, fold.resolutions[updateID])
	}
}

// Review Focus 1: an update posted while the parent waits in delegate_send on
// that delegate rides the wait's reply, after the earlier result the parent
// was holding and before the new report, labelled as an update, and never
// arrives again as a notification. The delegate is told it was queued.
func TestDelegateSendWaitCarriesAnUpdateAheadOfTheReport(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("WORKER-BRIEF")
	releaseFirst, firstDeferred := make(chan struct{}), make(chan struct{})
	var deferredOnce sync.Once
	sendReply, nextRequest, updateResult := make(chan string, 1), make(chan string, 1), make(chan string, 1)
	var (
		mu         sync.Mutex
		delegateID string
		root       *Session
	)
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "WORKER-BRIEF: answer when asked.")
		},
		func(req llm.Request) llm.Response {
			close(releaseFirst)
			awaitSignal(t, firstDeferred, "the first generation's report deferred behind the root's turn")
			id := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			mu.Lock()
			delegateID = id
			mu.Unlock()
			return delegateSendResponse("send", id, "Find the drain cursor table.", 60_000)
		},
		func(req llm.Request) llm.Response {
			sendReply <- lastToolResultText(req.Messages)
			nextRequest <- deferredHeadToolResults(req.Messages) + messagesText(req.Messages)
			return finalResponse("root done")
		},
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, releaseFirst, "the root's second request")
			return finalResponse("GEN1_READY")
		},
		func(llm.Request) llm.Response {
			return updateResponse("upd", "UPDATE_ONE: which table holds the drain cursor?")
		},
		func(req llm.Request) llm.Response {
			updateResult <- lastToolResultText(req.Messages)
			return finalResponse("GEN2_REPORT")
		},
	)
	root = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.testOnly.delegateDeliveryClassified = func(s *Session, isDeferred bool) {
			if s == root && isDeferred {
				deferredOnce.Do(func() { close(firstDeferred) })
			}
		}
	})
	// TRIPWIRE: half the send's wait; a send that sits out its wait fails here.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reply := <-sendReply
	first, update, report := strings.Index(reply, "GEN1_READY"), strings.Index(reply, "UPDATE_ONE"), strings.Index(reply, "GEN2_REPORT")
	if first < 0 || update < first || report < update || !strings.Contains(reply, "· update]") {
		t.Fatalf("delegate_send reply = %q, want the held report, then the update labelled as one, then the new report", reply)
	}
	if n := strings.Count(<-nextRequest, "UPDATE_ONE"); n != 1 {
		t.Fatalf("the root's next request carries the update %d times, want once, in the reply", n)
	}
	if got := <-updateResult; !strings.Contains(got, `"delivery":"queued"`) {
		t.Fatalf("the delegate's communicate result = %q, want queued behind the held report", got)
	}
	mu.Lock()
	id := delegateID
	mu.Unlock()
	if pending := delegateAggregateSnapshot(t, root.delegateController, id).PendingDeliveries; len(pending) != 0 {
		t.Fatalf("pending deliveries after the reply = %#v, want all acknowledged", pending)
	}
}

// A root awaiting its human partner's answer to ask_user holds a delegate's
// update and report: a wake runs no turn, and once the answer arrives the
// update reaches the model ahead of the report, and both are consumed.
func TestDelegateUpdateWaitsForTheAnswerToAPendingQuestion(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("WORKER-BRIEF")
	release := make(chan struct{})
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "WORKER-BRIEF: ask your parent which database to use.")
		},
		func(llm.Request) llm.Response { return toolCallResponse(askUserCall("ask1", askUserArgsValid())) },
		func(llm.Request) llm.Response { return finalResponse("going with Postgres") },
		func(llm.Request) llm.Response { return finalResponse("noted the worker") },
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, release, "the root awaiting its question")
			return updateResponse("upd", "ASK_UPDATE: Postgres or SQLite?")
		},
		func(llm.Request) llm.Response { return finalResponse("ASK_REPORT") },
	)
	root := newMessagingRoot(t, adapter, nil)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "pick a database", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := root.State(); got != SessionAwaiting {
		t.Fatalf("root state = %q, want awaiting its question", got)
	}
	close(release)
	pending := waitForRootAttention(t, root, 2)
	if !strings.Contains(pending[0], "/update/") {
		t.Fatalf("root attention = %v, want the update first", pending)
	}
	before := len(adapter.requestsFor("root"))
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("wake while awaiting: %v", err)
	}
	if got := len(adapter.requestsFor("root")); got != before {
		t.Fatalf("a wake while awaiting made %d root requests, want none", got-before)
	}
	if _, err := root.ProcessInput(ctx, "use Postgres", nil); err != nil {
		t.Fatalf("answer: %v", err)
	}
	presented := false
	for _, req := range adapter.requestsFor("root")[before:] {
		text := messagesText(req.Messages)
		u, r := strings.Index(text, "ASK_UPDATE"), strings.Index(text, "ASK_REPORT")
		if u >= 0 && r >= 0 {
			if r < u {
				t.Fatalf("the report reached the model before the update:\n%s", text)
			}
			presented = true
			break
		}
	}
	if !presented {
		t.Fatal("no request after the answer presented both the update and the report")
	}
	fold := rootAttentionFold(t, root)
	for _, id := range pending {
		if fold.resolutions[id] != delegateAttentionConsumed {
			t.Fatalf("attention %q = %q, want consumed after the answer", id, fold.resolutions[id])
		}
	}
}

// One delegate's updates reach its parent in the order sent and before its
// report, and an idle parent takes all three in one notification turn.
func TestDelegateUpdatesArriveInOrderBeforeTheReportInOneTurn(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm") },
		func(llm.Request) llm.Response { return communicateResponse(false, "FIRST_UPDATE") },
		func(llm.Request) llm.Response { return communicateResponse(false, "SECOND_UPDATE") },
		func(llm.Request) llm.Response { return finalResponse("THE_REPORT") },
		func(llm.Request) llm.Response { return finalResponse("noted all three") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	warmStableDelegateUnservedRoot(t, root, fixture)
	if outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "go on", 0); outcome.result.Err != nil {
		t.Fatalf("send: %v", outcome.result.Err)
	}
	pending := waitForRootAttention(t, root, 3)
	fold := rootAttentionFold(t, root)
	for i, want := range []string{"FIRST_UPDATE", "SECOND_UPDATE", "THE_REPORT"} {
		if frame := fold.content[pending[i]].Text(); !strings.Contains(frame, want) {
			t.Fatalf("attention %d = %q, want %s", i, frame, want)
		}
	}
	if !strings.Contains(fold.content[pending[2]].Text(), `kind="report"`) {
		t.Fatal("the last attention item is not the report")
	}
	before := len(fixture.adapter.Requests())
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("root notification turn: %v", err)
	}
	if got := len(fixture.adapter.Requests()) - before; got != 1 {
		t.Fatalf("the notification turn made %d requests, want one presenting all three", got)
	}
	fold = rootAttentionFold(t, root)
	for _, id := range pending {
		if fold.resolutions[id] != delegateAttentionConsumed {
			t.Fatalf("attention %q = %q, want consumed by the one turn", id, fold.resolutions[id])
		}
	}
}

// An idle parent delegate (a coordinator that ended its turn waiting on its
// worker) is woken by its worker's update: its next generation presents it.
func TestDelegateUpdateWakesAnIdleParentDelegate(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("COORD-BRIEF", "WORKER-BRIEF")
	coordinatorSettled := make(chan struct{})
	var settledOnce sync.Once
	presented := make(chan string, 1)
	var root *Session
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("coord", "COORD-BRIEF: start one worker delegate, then end your turn and wait for it.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on the coordinator") },
	)
	adapter.script("COORD-BRIEF",
		func(llm.Request) llm.Response {
			return delegateToolResponse("worker", "WORKER-BRIEF: ask your parent which table holds the drain cursor.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on the worker") },
		func(req llm.Request) llm.Response {
			presented <- messagesText(req.Messages)
			return finalResponse("told the worker")
		},
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, coordinatorSettled, "the coordinator's first generation to settle")
			return updateResponse("upd", "IDLE_PARENT_UPDATE: which table holds the drain cursor?")
		},
		func(llm.Request) llm.Response { return finalResponse("worker done") },
	)
	root = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		// The coordinator's report reaching the root marks its first
		// generation finished.
		cfg.testOnly.delegateDeliveryClassified = func(s *Session, _ bool) {
			if s == root {
				settledOnce.Do(func() { close(coordinatorSettled) })
			}
		}
	})
	serveSupervisionRootWakes(t, root)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	select {
	case text := <-presented:
		if !strings.Contains(text, `kind="update"`) || !strings.Contains(text, "IDLE_PARENT_UPDATE") {
			t.Fatalf("the coordinator's next generation does not present the update:\n%s", text)
		}
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	case <-time.After(30 * time.Second):
		t.Fatal("the idle coordinator was never woken by its worker's update")
	}
}

// Review Focus 4: a coordinator that is mid-run when its worker's update
// arrives sees it at its own next model boundary, and (ruling 8, PR 3a) its
// own generation consumes the update it saw, so no successor generation is
// started for it.
func TestDelegateUpdateReachesARunningParentDelegate(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("COORD-BRIEF", "WORKER-BRIEF")
	release, coordinatorDeferred := make(chan struct{}), make(chan struct{})
	var deferredOnce sync.Once
	presented := make(chan string, 1)
	var root, coordinator *Session
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("coord", "COORD-BRIEF: start one worker delegate and keep working.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on the coordinator") },
	)
	adapter.script("COORD-BRIEF",
		func(llm.Request) llm.Response {
			return delegateToolResponse("worker", "WORKER-BRIEF: ask your parent which table holds the drain cursor.")
		},
		func(llm.Request) llm.Response {
			close(release)
			awaitSignal(t, coordinatorDeferred, "the worker's update deferred behind the coordinator's turn")
			return jobListResponse("list")
		},
		func(req llm.Request) llm.Response {
			presented <- messagesText(req.Messages)
			return finalResponse("coordinator done")
		},
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, release, "the coordinator's second request")
			return updateResponse("upd", "RUNNING_PARENT_UPDATE: which table holds the drain cursor?")
		},
		func(llm.Request) llm.Response { return finalResponse("worker done") },
	)
	root = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		// Only the coordinator receives anything besides the root.
		cfg.testOnly.delegateDeliveryClassified = func(s *Session, isDeferred bool) {
			if s != root && isDeferred {
				deferredOnce.Do(func() {
					// Written before the close the coordinator's step waits
					// on, so it is visible once presented is received.
					coordinator = s
					close(coordinatorDeferred)
				})
			}
		}
	})
	serveSupervisionRootWakes(t, root)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	select {
	case text := <-presented:
		if !strings.Contains(text, `kind="update"`) || !strings.Contains(text, "RUNNING_PARENT_UPDATE") {
			t.Fatalf("the coordinator's next request does not present the update:\n%s", text)
		}
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	case <-time.After(30 * time.Second):
		t.Fatal("the running coordinator never saw its worker's update")
	}
	// The worker's later report may legitimately wake the coordinator again;
	// only the update's own resolution says whether it earned a generation.
	if generation := updateAttentionConsumedFor(t, coordinator); generation != 0 {
		t.Fatalf("the update the coordinator already saw was consumed by attention generation %d, want its presenting generation (0)", generation)
	}
}

// updateAttentionConsumedFor waits until sess's transcript resolves the update
// attention it received as consumed, and returns the generation that consumed
// it: zero when the generation that presented it consumed it at its turn's
// end, an attention generation's number when one was started for it.
func updateAttentionConsumedFor(t *testing.T, sess *Session) uint64 {
	t.Helper()
	sess.mu.Lock()
	path, sessionID := transcriptPath(sess.stateDir, sess.id), sess.id
	sess.mu.Unlock()
	var generation uint64
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the update's attention to be consumed", func() bool {
		fold, err := sess.readDelegateAttentionFold(path, sessionID)
		if err != nil {
			return false
		}
		for attentionID, resolution := range fold.resolutions {
			if strings.Contains(attentionID, "/update/") && resolution == delegateAttentionConsumed {
				generation = fold.resumeGenerations[attentionID]
				return true
			}
		}
		return false
	})
	return generation
}

// appendColdFixtureEvents appends events to the fixture's delegate journal
// before any root opens it, the way a process that crashed left it.
func appendColdFixtureEvents(t *testing.T, fixture coldStableDelegateFixture, events ...delegatestore.Event) []delegatestore.Event {
	t.Helper()
	path := delegateResourceStorePath(fixture.stateDir, fixture.meta.ID)
	stored, err := delegatestore.ReadEvents(path)
	if err != nil {
		t.Fatalf("read delegate journal: %v", err)
	}
	state, err := delegatestore.Fold(stored)
	if err != nil {
		t.Fatalf("fold delegate journal: %v", err)
	}
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("open delegate journal: %v", err)
	}
	appended, _, err := store.AppendBatch(state, events)
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("append delegate events: %v", err)
	}
	return appended
}

// A pending update survives a restart and wakes its parent, whether it was
// still in the delegate's journal or already in the parent's transcript.
func TestDelegateUpdateSurvivesRestart(t *testing.T) {
	t.Parallel()
	// Review Focus 5: the generation that posted the update was still
	// running at the crash. Restore settles it as runtime_lost, and the
	// parent gets the update, then that report, once each.
	t.Run("posted while the run was still going", func(t *testing.T) {
		t.Parallel()
		fixture := newColdStableDelegateFixture(t, "")
		startedAt := time.Unix(1_700_000_300, 0).UTC()
		appended := appendColdFixtureEvents(t, fixture,
			delegatestore.Event{Kind: delegatestore.EventDelegateRunStarted, TS: startedAt, DelegateID: fixture.delegateID,
				RunStarted: &delegatestore.RunStarted{Generation: 1, Trigger: delegatestore.TriggerOwnerInput, StartedAt: startedAt}},
			delegatestore.Event{Kind: delegatestore.EventDelegateUpdatePosted, TS: startedAt, DelegateID: fixture.delegateID,
				UpdatePosted: &delegatestore.UpdatePosted{Generation: 1, Message: "RESTART_UPDATE: which table holds the drain cursor?"}},
		)
		updateID := delegateAttentionID(delegatestore.UpdateDeliveryID(fixture.delegateID, appended[1].Seq))
		reportID := delegateAttentionID(delegateDeliveryID(fixture.delegateID, 1))
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("noted after the restart") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		pending := waitForRootAttention(t, root, 2)
		if !slices.Equal(pending, []string{updateID, reportID}) {
			t.Fatalf("root attention after restore = %v, want the update then the runtime_lost report", pending)
		}
		if !root.hasPendingRootDelegateAttention() {
			t.Fatal("restore did not arm the root's wake")
		}
		// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
			t.Fatalf("root notification turn: %v", err)
		}
		requests := fixture.adapter.Requests()
		text := messagesText(requests[len(requests)-1].Messages)
		if u, r := strings.Index(text, "RESTART_UPDATE"), strings.Index(text, `kind="report"`); u < 0 || r < u || strings.Count(text, "RESTART_UPDATE") != 1 {
			t.Fatalf("the root's turn after restore = %q, want the update once, then the report", text)
		}
	})
	t.Run("delivered but not yet read", func(t *testing.T) {
		t.Parallel()
		fixture := newColdStableDelegateFixture(t, "")
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return communicateResponse(false, "UNREAD_UPDATE: which table?") },
			func(llm.Request) llm.Response { return finalResponse("summary written") },
			func(llm.Request) llm.Response { return finalResponse("noted after the restart") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		warmStableDelegateUnservedRoot(t, root, fixture)
		pending := rootPendingAttention(root)
		if len(pending) != 1 {
			t.Fatalf("root attention before restart = %v, want the update", pending)
		}
		root.Close()
		restored := restoreSupervisionRoot(t, fixture, nil)
		if got := rootPendingAttention(restored); !slices.Equal(got, pending) {
			t.Fatalf("root attention after restart = %v, want %v", got, pending)
		}
		if !restored.hasPendingRootDelegateAttention() {
			t.Fatal("restore did not re-arm the unread update's wake")
		}
		// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := restored.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
			t.Fatalf("root notification turn: %v", err)
		}
		requests := fixture.adapter.Requests()
		if !requestContainsText(requests[len(requests)-1], "UNREAD_UPDATE") {
			t.Fatal("the restored root's turn did not present the update")
		}
		if got := rootAttentionFold(t, restored).resolutions[pending[0]]; got != delegateAttentionConsumed {
			t.Fatalf("update attention after restart = %q, want consumed", got)
		}
	})
}
```

- [ ] **Step 3: Run the matrix, then prove it can fail**

Run: `cd agent && go test -race -count=1 -run 'TestDelegateUpdate|TestDelegateSendWaitCarriesAnUpdateAheadOfTheReport' .`
Expected: PASS. If one fails, it is a real defect in the inherited path: stop and debug it at the root.

Then, to see the tests fail for the right reason, comment out the line `update, isUpdate = deps.postDelegateUpdate(ctx, message)` in `agent/session_tools_communicate.go`, rerun the same command, and confirm every matrix test fails on its update assertion (a missing frame, a missing `delivery`, or a wait that never finishes). Restore the line and confirm `git diff --stat agent/session_tools_communicate.go` shows no change against Task 6's commit. The running-coordinator test's no-successor assertion is PR 3a's contract, whose own tests (Task 4a Steps 3 and 7) already proved it can fail.

Run each new test ten times to catch interleaving flakes: `cd agent && go test -race -count=10 -run 'TestDelegateUpdate|TestDelegateSendWaitCarriesAnUpdateAheadOfTheReport' .` Expected: PASS every time. A flake gets a root-cause fix now (docs/developing-evener/testing.md), never a wider timeout.

- [ ] **Step 4: Commit**

```bash
git add agent/delegate_messaging_test_helpers_test.go agent/delegate_messaging_guidance_test.go agent/delegate_update_test.go
git diff --cached --stat
git commit -m "test(delegates): pin how updates reach running, waiting, asking, idle, nested and restarted parents

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

### Task 8: Existing suites, the outcome corpus pin, docs, and PR 4

**Files:**
- Modify (as the suite run finds them): delegate tests whose scripted delegates call `communicate(end_turn=false)`. Known: `agent/delegate_resource_supervision_test.go` (steps at lines 963, 980, 1012, 1081, 1320, 1400), `agent/delegate_resource_retention_stop_test.go:535`, `agent/notification_test.go:182`; also check `cmd/evener-hub/app_session_activity_relay_test.go`.
- Modify: `agent/subagent_outcome_wire_fixture_test.go` (the resumed run posts an update)
- Docs: `docs/job-control.md`, `docs/product/session-activity.md`, `docs/product/subsystems.md` (S11, S12)

- [ ] **Step 1: Run the suites that script delegates and fix what the new contract changes**

Run: `cd agent && go test -count=1 . 2>&1 | tail -80` (with `set -o pipefail`), then `go test -count=1 ./cmd/evener-hub/ ./cmd/evener/` from the repo root.

A delegate that calls `communicate(end_turn=false)` now sends its parent an update, so a test can see extra parent attention, an extra parent notification turn consuming a scripted step, or a different pending-attention count. For each failure:
- If the test's delegate used `end_turn=false` as narration that the test's subject does not care about, keep the step and update the expectation to include the update (the attention item, the extra parent request), with a comment that the delegate's `end_turn=false` is an update its parent receives.
- If the test asserts the parent received nothing from the delegate mid-turn, that assertion encoded #4172; replace it with the update's arrival.
- Never weaken an assertion about anything else, never delete a test, and never switch a delegate to `end_turn=true` to dodge the change.
- A failure unrelated to updates is a real bug: debug it at the root.

- [ ] **Step 2: Pin that an update leaves the subagent outcome corpus unchanged**

In `agent/subagent_outcome_wire_fixture_test.go`, in the resumed run's `c.appendLocked(...)`, insert an update between the run start and the run finish:

```go
	_, err = c.appendLocked(
		delegateControllerRunStartedEvent("dlg_reported", 2, delegatestore.TriggerOwnerInput, wireFixtureStart),
		// An update the resumed run posted before reporting: delegates/list
		// and its reportPreview read only the report.
		delegatestore.Event{
			Kind:         delegatestore.EventDelegateUpdatePosted,
			DelegateID:   "dlg_reported",
			UpdatePosted: &delegatestore.UpdatePosted{Generation: 2, Message: "Need the schema before I continue."},
		},
		delegateRunFinishedEvent(delegateLease{delegateID: "dlg_reported", generation: 2}, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", wireFixtureStart.Add(30*time.Second), delegateDeliveryID("dlg_reported", 2), &nextPacket),
	)
```

Run: `cd agent && go test -count=1 -run 'TestSubagentOutcomeWireFixtures$' .`
Expected: PASS without `-update-wire`: `agent/testdata/subagentwire/outcomes.json` does not change. A failure means an update leaked into Activity, which is a bug to fix, not a golden to regenerate.

- [ ] **Step 3: Update the docs**

In `docs/job-control.md`, after the paragraph that follows the report frame example (Task 2's text), add:

````markdown
A running delegate's `communicate(end_turn=false)` is an update for its owner.
Evener records it in the delegate's journal and delivers it through the same
ordered queue and receiver-transcript attention as the terminal packet:

```xml
<delegate-notification delegate_id="dlg_..." name="..." kind="update">the delegate's message, with "<" escaped</delegate-notification>
```

- An idle owner is woken, and its notification turn takes everything pending
  at once. A running owner sees the update at its next model boundary; an owner
  waiting on that delegate in `delegate_send(max_wait_ms)` gets it in the
  reply's `earlier_results`, ahead of the report. An owner awaiting an
  `ask_user` reply gets it after the reply.
- One delegate's updates arrive in the order sent, before that generation's
  report, and survive restart like any pending attention.
- The `communicate` result carries `delivery`: `delivered` (in the owner's
  transcript), `queued` (durable and waiting behind the owner's turn or an
  earlier delivery of the same delegate), or `not_delivered` with a `reason`
  (the delegate is stopping or finishing, its run is stale, or the route to the
  owner is closed). Delivery never fails the call: the message is already in
  the delegate's transcript.
- Updates never feed Activity's `reportPreview`.
- A root's `communicate(end_turn=false)` is unchanged: a checkpoint for its
  human partner.
````

In the "Model-facing guidance requirements" list, add after the `delegate_send` bullets:

```markdown
- A delegate's `communicate(end_turn=false)` is an update its parent receives
  and may be woken by: delegates send one only when the parent needs to know
  something or act, never as narration, and finish with
  `communicate(end_turn=true)`.
```

In the observer section, change step 4 of the observer pattern to "4. The child sends updates with communicate(end_turn=false) while it watches and reports its findings with communicate(end_turn=true).", and change the bullet "Report readiness or continuing status with `communicate(end_turn=false)` only when the observer will continue working in that same turn." to "Send an update with `communicate(end_turn=false)` only when the parent needs to know something or act: it reaches the parent and may wake it."

In `docs/product/session-activity.md`, in "Delegate reports and run identity", after the paragraph that ends "Opening the child transcript remains the path to the full conversation.", add:

```markdown
A delegate's mid-work updates (`communicate(end_turn=false)`) never contribute
to `reportPreview`. Each reaches its owner's transcript as its own
`<delegate-notification kind="update">` row, which the browser renders as a
"Delegate update" card and the phone as "<name> sent an update", with the
delegate's message beneath.
```

In `docs/product/subsystems.md`, in row S11's responsibilities cell, append this sentence after "...keeps its stricter fence and still refuses the fallback." (inside the same cell, before ` | [Stop/outbox ownership]`):

```markdown
A delegate's updates and reports reach its owner as typed attention in the owner's transcript, never as steering.
```

In row S12's cell, append this sentence after "...even when an active registration ends as `runtime_lost`." (before ` | [Session activity]`):

```markdown
A delegate's updates (`communicate(end_turn=false)`) ride its ordered delivery queue ahead of its generation's report and reach the owner as `kind="update"` attention under the report's wake, hold and restart rules; Activity's `reportPreview` reads only the report.
```

- [ ] **Step 4: Run the PR's gates**

```bash
set -o pipefail
"$(go env GOROOT)/bin/gofmt" -l $(git diff --name-only origin/main -- '*.go')   # from the worktree root; prints nothing
cd agent
go build ./... && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=linux go vet -tags evenerfuzz ./...
golangci-lint run --allow-serial-runners ./...
go test -race -count=1 -run 'TestDelegateUpdate|TestDelegateController|TestDelegateSend|TestDelegateMessagingGuidanceGoldens|WireFixtures$|Supervision' .
/Users/jesse/git/prime-radiant-inc/evener/.superpowers/bin/race-serial go test -race -count=1 ./...
cd .. && go build ./... && go vet ./... && go test -count=1 ./cmd/evener-hub/ ./cmd/evener/
```

- [ ] **Step 5: Commit and open PR 4**

```bash
git add agent/subagent_outcome_wire_fixture_test.go docs/job-control.md docs/product/session-activity.md docs/product/subsystems.md
git add <each test file Step 1 changed, by name>
git diff --cached --stat
git commit -m "docs(delegates): updates reach the owner as attention; suites expect them

Delegate tests whose scripted delegates narrate with end_turn=false now
expect the update their parent receives. The outcome corpus pins that an
update never changes delegates/list. Job control, session activity and the
S11/S12 rows describe updates.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

Run `/simplify`, push its fixes, and open the PR (base `main`, regular) titled "feat(delegates): communicate(end_turn=false) delivers a delegate's update to its parent". The body: "Closes #4172."; the delivery semantics (rulings 1, 2, 10); that a running coordinator's generation consumes the update it saw, so no successor generation runs for it (ruling 8, PR 3a), with a pointer to `TestDelegateUpdateReachesARunningParentDelegate`; the list of existing tests whose expectations changed and why; the `/simplify` outcome; ending with `https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU`. Shepherd it to the merge gate.


## PR 5: delegate_send reaches only your own delegates

PR 5 needs PR 4 (delegates must have an upward channel before the caller route goes). Branch from `origin/main` after PR 4 merges.

### Task 9: Reject `to="caller"` and delete the caller route

**Files:**
- Modify: `agent/session_tools.go:145-166` (the caller branch becomes the rejection)
- Modify: `agent/delegate_tree_steer.go` (delete `SteerCaller` lines 87-118, `beginCallerSteerPersistence` lines 134-160, `appendDelegateCallerSteeringDurably` lines 497-499; drop the `events` import if nothing else uses it)
- Modify: `agent/session_queue.go:310-347` (delete `enqueueDelegateCallerSteeringDurably`; drop imports it alone used)
- Modify: `agent/internal/tool/definitions.go:333-353` (`DefDelegateSend`)
- Modify: `internal/bundled/agents/subagent.md:25-33` (Reporting)
- Modify: `agent/delegate_tree_controller_test.go:460-461` (delete the two `SteerCaller` inventory lines)
- Modify: `agent/internal/tool/definitions_test.go` (delete `TestDefDelegateSendDescriptionDistinguishesCallerFromFinalReport` and `TestDefDelegateSendDescribesContextualCallerRoute`; both pin prose of a route that no longer exists, and the golden pins the new text)
- Delete: `agent/delegate_resource_caller_test.go`
- Create: `agent/delegate_send_caller_rejected_test.go`
- Regenerate: `agent/testdata/messagingprompt/root.md`, `agent/testdata/messagingprompt/delegate.md`
- Docs: `docs/job-control.md`, `docs/subagent-management/11-delegate-resource-model.md`

**Interfaces:**
- Consumes: Task 6's update channel (the error points delegates at it).
- Produces: `delegate_send(to="caller")` fails with an error beginning `invalid_request:` and nothing reaches the parent. `runtimeMessageAliasCaller` stays: `job_watch` uses it internally for session-source watches.

- [ ] **Step 1: Write the failing rejection test**

Create `agent/delegate_send_caller_rejected_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/afero"

	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// delegate_send reaches only a session's own delegates. A delegate that names
// its parent with to="caller" gets an invalid_request error pointing at
// communicate, and nothing reaches the parent.
func TestDelegateSendRejectsTheCallerRoute(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
	parentFS := afero.NewMemMapFs()
	attachDelegateSteerRuntime(t, c, "dlg_parent", parentFS)
	child := attachDelegateSteerRuntime(t, c, "dlg_child", afero.NewMemMapFs())
	child.id = "child-dlg_child"
	child.stateDir = t.TempDir()
	child.delegateController = c
	child.delegateRootSessionID = "root-session"
	child.owningDelegateID = "dlg_child"
	child.reg = toolpkg.NewRegistry()
	if err := registerStableDelegateTool(child.reg, child); err != nil {
		t.Fatalf("register stable delegate tool: %v", err)
	}
	args, err := json.Marshal(map[string]any{"to": "caller", "message": "need the schema"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, delegateLease{delegateID: "dlg_child", generation: 1})
	call := child.reg.ExecuteCall(ctx, child.env, llm.ToolCallData{ID: "caller-send", Name: "delegate_send", Arguments: args})
	if !call.IsError || !strings.Contains(call.Output, "invalid_request") {
		t.Fatalf("caller send = error:%v output:%q, want an invalid_request rejection", call.IsError, call.Output)
	}
	if entries := decodeTranscriptEntries(t, parentFS, "/dlg_parent.jsonl"); len(entries) != 0 {
		t.Fatalf("parent transcript = %#v, want nothing delivered", entries)
	}
	c.mu.Lock()
	steers := len(c.live["dlg_parent"].pendingSteers)
	c.mu.Unlock()
	if steers != 0 {
		t.Fatalf("parent pending steers = %d, want none", steers)
	}
}
```

Run: `cd agent && go test -count=1 -run TestDelegateSendRejectsTheCallerRoute .`
Expected: FAIL: the caller route delivers (`call.IsError` false and the parent transcript has the steer).

- [ ] **Step 2: Reject the route and delete it**

In `agent/session_tools.go`, replace the whole `if target == runtimeMessageAliasCaller { ... }` block in `stableDelegateSendTool` with:

```go
	if target == runtimeMessageAliasCaller {
		return nil, errors.New("invalid_request: delegate_send reaches only your own delegates; to message your parent, call communicate with end_turn=false")
	}
```

Delete `SteerCaller`, `beginCallerSteerPersistence` and `appendDelegateCallerSteeringDurably` from `agent/delegate_tree_steer.go`, and `enqueueDelegateCallerSteeringDurably` from `agent/session_queue.go`. Remove imports the compiler reports unused. Delete `agent/delegate_resource_caller_test.go` (`git rm`), the two `SteerCaller` lines from `delegateControllerDormancyExpectedInventory` in `agent/delegate_tree_controller_test.go`, and the two caller-route prose tests from `agent/internal/tool/definitions_test.go`.

Replace `DefDelegateSend` in `agent/internal/tool/definitions.go`:

```go
// DefDelegateSend defines the delegate_send tool, the single follow-up surface
// for your own durable delegates.
func DefDelegateSend() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "delegate_send",
		Description: "Sends a message to one of your own delegates, by the durable delegate_id you got when you created it. " +
			"To reach your own parent, use communicate instead: end_turn=false for an update, end_turn=true for your final report. " +
			"`to` accepts only a `dlg_...` delegate_id this session owns; it rejects `caller`, job/turn handles, and runtime aliases. " +
			"If the delegate is running or being driven, the message is steered and returns on delivery. " +
			"Idle delegates are started/resumed automatically through the existing restore path, so follow-up messages resume them without an explicit idle-mode flag.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"to":          map[string]any{"type": "string", "description": "The delegate_id (`dlg_...`) of a delegate this session created."},
				"message":     map[string]any{"type": "string", "description": "The message to deliver to that delegate."},
				"max_wait_ms": map[string]any{"type": "integer", "description": "0 (default): deliver/start without waiting. >0: for a newly started delegate generation, wait inline up to this many ms for its result; the reply also carries, under earlier_results, any earlier result of that delegate you had not yet received; delivery to a running delegate returns once delivered."},
			},
			"required": []string{"to", "message"},
		},
	}
}
```

(The text keeps "started/resumed automatically", which `TestDefDelegateSendShape` checks, and avoids the banned substrings `job_send_message`, `watched` and `main`, so mind words like "remain".)

In `internal/bundled/agents/subagent.md`, replace the paragraph under `## Reporting` that begins "The parent agent only sees the result you send back" and ends "it does not replace the final `communicate(end_turn=true)` result." with:

```markdown
The parent agent only sees what you send it with `communicate`, not your
intermediate tool calls or hidden reasoning. Make your final report complete and
actionable, and send it, like readiness markers and observer findings, with
`communicate(end_turn=true)`. When your parent needs to know something or act
before you finish, such as a question only it can answer, send an update with
`communicate(end_turn=false)` and keep working. It reaches your parent and may
wake it, so never send one as narration.
```

- [ ] **Step 3: Run the tests and regenerate the goldens**

Run: `cd agent && go test -count=1 -run 'TestDelegateSendRejectsTheCallerRoute|TestDefDelegateSend|TestDelegateControllerProductionIntegrationMatchesInventory|Steer' . ./internal/tool/`
Expected: PASS.
Run: `cd agent && go test -count=1 -run 'TestDelegateMessagingGuidanceGoldens$' . -update-prompt && git diff testdata/messagingprompt`
Expected: only the `delegate_send` sections change, in both files.

- [ ] **Step 4: Update the docs**

In `docs/job-control.md`:
- Replace the guidance bullet that begins "Use `delegate_send(to=<delegate_id>)` for follow-up on a durable child conversation. From within a delegate," with: "Use `delegate_send(to=<delegate_id>)` for follow-up on a durable child conversation; it reaches only your own delegates. A delegate reaches its parent only through `communicate`: `end_turn=false` for an update, `end_turn=true` for its report. `delegate_send` rejects `caller`, `job_id`, `transcript_ref`, and other runtime aliases."
- Replace the `delegate_send` section's opening paragraph with: "`delegate_send` sends a message to one of your durable child delegate conversations by `delegate_id`. It reaches only your own delegates: a delegate reaches its parent with `communicate` (`end_turn=false` for an update, `end_turn=true` for its report), and `to="caller"` is rejected with an error that says so."
- Delete the `caller` row of the target table and the semantics bullet that begins "If `to` is `caller`,".
- Replace the paragraph "Watch-origin observer completion uses `communicate(end_turn=true)`. A delegate may use the contextual `caller` target ..." with: "Watch-origin observers send updates with `communicate(end_turn=false)` and finish with `communicate(end_turn=true)`."
- In the observer section, replace the sentence "Use `delegate_send(to=<delegate_id>)` for parent-to-child follow-up and `delegate_send(to="caller")` for a non-terminal update to the controlling caller." with "Use `delegate_send(to=<delegate_id>)` for parent-to-child follow-up; the observer sends updates with `communicate(end_turn=false)`."

In `docs/subagent-management/11-delegate-resource-model.md`, replace the paragraph beginning "to=caller remains the explicit route from a delegate to its controlling caller." with:

```markdown
There is no caller route. A delegate reaches its owner only through
communicate: end_turn=false posts an update into the delegate's ordered
delivery queue, and end_turn=true settles its report. delegate_send(to="caller")
is rejected.
```

and change the Authorization bullet "delegate_send may target a directly owned delegate or caller." to "delegate_send may target only a directly owned delegate."

- [ ] **Step 5: Run the gates**

```bash
set -o pipefail
"$(go env GOROOT)/bin/gofmt" -l $(git diff --name-only origin/main -- '*.go')
cd agent
go build ./... && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=linux go vet -tags evenerfuzz ./...
golangci-lint run --allow-serial-runners ./...
go test -race -count=1 -run 'TestDelegateSend|Steer|TestDelegateController|TestDelegateMessagingGuidanceGoldens$' . ./internal/tool/
/Users/jesse/git/prime-radiant-inc/evener/.superpowers/bin/race-serial go test -race -count=1 ./...
cd .. && go build ./... && go vet ./...
```

`unused` in golangci-lint will name anything the deletion orphaned. The steering-claim provenance it leaves always nil is not unused to the linter; PR 6 removes it.

- [ ] **Step 6: Commit and open PR 5**

```bash
git add agent/session_tools.go agent/delegate_tree_steer.go agent/session_queue.go agent/internal/tool/definitions.go \
  agent/internal/tool/definitions_test.go internal/bundled/agents/subagent.md agent/delegate_tree_controller_test.go \
  agent/delegate_send_caller_rejected_test.go agent/testdata/messagingprompt/root.md agent/testdata/messagingprompt/delegate.md \
  docs/job-control.md docs/subagent-management/11-delegate-resource-model.md
git rm agent/delegate_resource_caller_test.go
git diff --cached --stat
git commit -m "feat(delegates): delegate_send reaches only your own delegates

to=\"caller\" is rejected with a pointer to communicate(end_turn=false), and
SteerCaller, its caller-steer admission and the root's caller queue entry
are deleted. delegate_send's description and the subagent role say that a
delegate reaches its parent through communicate alone.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

Run `/simplify`, push its fixes, and open the PR (base `main`, regular) titled "feat(delegates): delegate_send reaches only your own delegates". The body lists what was deleted, says PR 6 removes the steering provenance only this route set, states the `/simplify` outcome, and ends with `https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU`. Shepherd it to the merge gate. After it merges, confirm PR #4197 is closed; if not, close it with a comment linking the spec.

## PR 6: Drop the steering provenance only the caller route set

### Task 10: Remove the always-nil steering-claim provenance

After PR 5, `beginSteerPersistenceLocked` is only reached from `BeginSteerPersistence`, which passes `nil` provenance, so every steering claim, admission and model-claim provenance field is nil and `CompleteModelRequest`'s unlock-relock window around `unionActiveProvenance` binds nothing. This task has no behavior change; the existing steer, stop and model-request suites are its tests.

**Files:**
- Modify: `agent/delegate_tree_steer.go` (`delegateSteeringAdmission`, `delegateSteeringClaim`, `delegateModelRequestClaim`, `BeginSteerPersistence`, `beginSteerPersistenceLocked`, `CompleteSteerPersistence`, `BeginModelRequest`, `CompleteModelRequest`; drop the `provenance` import)

**Interfaces:**
- Consumes: PR 5's deletion.
- Produces: `beginSteerPersistenceLocked(delegateID string) (*delegateSteeringClaim, error)`; `delegateSteeringAdmission{entryID string; carriesAcrossGeneration bool}`; `delegateModelRequestClaim` without `steeringProvenance` or `testBeforeProvenance`.

- [ ] **Step 1: Confirm nothing else produces or reads the provenance**

Run: `grep -rn 'steeringProvenance\|testBeforeProvenance\|claim.provenance\|admission.provenance\|pending.provenance' agent --include='*.go'`
Expected: hits only in `agent/delegate_tree_steer.go`. Any other hit means the field is not dead: stop and report it instead of continuing.

- [ ] **Step 2: Remove the fields and the window**

In `agent/delegate_tree_steer.go`:
- Delete the `provenance` field from `delegateSteeringAdmission` and from `delegateSteeringClaim`, and `steeringProvenance` and `testBeforeProvenance` from `delegateModelRequestClaim`.
- `BeginSteerPersistence` returns `c.beginSteerPersistenceLocked(delegateID)`; `beginSteerPersistenceLocked` takes only `delegateID` and builds the claim without `provenance`.
- In `CompleteSteerPersistence`, build both admissions without `provenance`, and replace the stop-fenced comment's provenance rationale ("The admission is still recorded. The transcript replays the message, but only an admission carries the steer's causal provenance ...") with:

```go
		// The admission is still recorded: the successor's model claim binds
		// it like any steer its own generation took, which is what makes that
		// generation answer it (escalateCompletionRequirementLocked).
```

- In `BeginModelRequest`, the loop over `live.pendingSteers` only appends `pending.entryID` to `claim.steeringIDs`.
- Replace `CompleteModelRequest` with:

```go
func (c *delegateTreeController) CompleteModelRequest(claim *delegateModelRequestClaim, history []schema.Turn, scope replayScope) ([]llm.Message, error) {
	defer c.retirementChanged()
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim == nil || c.modelClaims[claim.token] != claim {
		return nil, errDelegateStaleLease
	}
	_, live, err := c.admitLeaseLocked(claim.lease, delegatestore.PhaseRunning)
	if err != nil || live.binding.runtime != claim.runtime {
		c.releaseModelClaimLocked(claim.token)
		c.evidenceVersion++
		if err != nil {
			return nil, err
		}
		return nil, errDelegateStaleLease
	}
	pending := make([]delegateSteeringAdmission, 0, len(claim.steeringIDs))
	claimedIDs := make(map[string]struct{}, len(claim.steeringIDs))
	for _, entryID := range claim.steeringIDs {
		pending = append(pending, delegateSteeringAdmission{entryID: entryID})
		claimedIDs[entryID] = struct{}{}
	}
	lateIDs := make(map[string]struct{})
	for _, admission := range live.pendingSteers {
		if _, claimed := claimedIDs[admission.entryID]; !claimed {
			lateIDs[admission.entryID] = struct{}{}
		}
	}
	for _, steeringClaim := range c.steeringClaims {
		if steeringClaim != nil && steeringClaim.lease == claim.lease {
			if _, claimed := claimedIDs[steeringClaim.entryID]; !claimed {
				lateIDs[steeringClaim.entryID] = struct{}{}
			}
		}
	}
	history, bound := projectDelegatePendingSteers(history, pending, lateIDs)
	if len(bound) != 0 {
		if err := c.escalateCompletionRequirementLocked(claim.lease); err != nil {
			c.releaseModelClaimLocked(claim.token)
			c.evidenceVersion++
			return nil, err
		}
	}
	kept := live.pendingSteers[:0]
	for _, pending := range live.pendingSteers {
		if _, claimed := bound[pending.entryID]; !claimed {
			kept = append(kept, pending)
		}
	}
	live.pendingSteers = kept
	c.releaseModelClaimLocked(claim.token)
	c.evidenceVersion++
	return expandHistory(history, scope), nil
}
```

Drop the `provenance` import.

- [ ] **Step 3: Run the steer, stop and model-request suites**

Run: `cd agent && go test -race -count=3 -run 'Steer|ModelRequest|Stop|Supervision|TestDelegateController' .`
Expected: PASS. Then `grep -n provenance agent/delegate_tree_steer.go` prints nothing.

- [ ] **Step 4: Run the gates, commit and open PR 6**

```bash
set -o pipefail
"$(go env GOROOT)/bin/gofmt" -l agent/delegate_tree_steer.go
cd agent
go build ./... && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=linux go vet -tags evenerfuzz ./...
golangci-lint run --allow-serial-runners ./...
/Users/jesse/git/prime-radiant-inc/evener/.superpowers/bin/race-serial go test -race -count=1 ./...
cd ..
git add agent/delegate_tree_steer.go
git diff --cached --stat
git commit -m "refactor(delegates): drop the steering provenance only the caller route set

Every steering claim has carried nil provenance since the caller route was
deleted, so the admission and model-claim fields and the unlock window that
bound them go.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

Run `/simplify`, push its fixes, open the PR (base `main`, regular) titled "refactor(delegates): drop the steering provenance only the caller route set" with the `/simplify` outcome and the session link, and shepherd it.

## PR 7: A parent's steer is labelled as from the parent

PR 7 needs PR 5 (it rewrites the steering append PR 5 left with one caller). Branch from `origin/main` after PR 5 merges.

### Task 11: A steer into a running delegate carries source `parent` and an attribution line

**Files:**
- Modify: `agent/events/payloads.go:341-346` (`SteeringSourceParent`)
- Modify: `agent/delegate_tree_steer.go` (`parentMessageAttribution`, `parentSteeringTurn`, `appendDelegateSteeringDurably`; delete `appendDelegateSteeringDurablyWithMetadata`; re-add the `events` import)
- Modify: `agent/system_event_wire_fixture_test.go` (new `steer-from-parent` case)
- Regenerate: `agent/testdata/systemeventwire/events.json`
- Test: `agent/delegate_tree_steer_test.go` (new test; the existing durability test's text expectation), `cmd/evener-tui/hub_notifications_test.go`
- Docs: `docs/job-control.md` (delegate_send semantics), `docs/product/subsystems.md` (S11)

**Interfaces:**
- Consumes: nothing new.
- Produces: `events.SteeringSourceParent = "parent"`; `const parentMessageAttribution = "Message from your parent agent:\n\n"`; `func parentSteeringTurn(message, stableTurnID string, now time.Time) schema.Turn`; corpus case `steer-from-parent` in `agent/testdata/systemeventwire/events.json` (message "Use the drain_log table; tree_state is being retired.").

- [ ] **Step 1: Write the failing tests**

Append to `agent/delegate_tree_steer_test.go` (it already imports `afero` and `context`; add `"primeradiant.com/evener/agent/events"` and `"primeradiant.com/evener/agent/schema"` if absent):

```go
// A parent's delegate_send into its running delegate is steering from the
// parent: source parent, no daemon kind, and the delegate's model reads the
// attribution line before the parent's words.
func TestDelegateSteerArrivesFromTheParent(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	fs := afero.NewMemMapFs()
	runtime := attachDelegateSteerRuntime(t, c, "dlg_target", fs)
	if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "use drain_log"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	entries := decodeTranscriptEntries(t, fs, "/dlg_target.jsonl")
	if len(entries) != 1 {
		t.Fatalf("delegate transcript = %#v, want one steer", entries)
	}
	turn := entries[0].Turn
	if turn.Kind != schema.TurnSteering || turn.SteeringSource != events.SteeringSourceParent || turn.SteeringKind != "" ||
		turn.Message.Text() != parentMessageAttribution+"use drain_log" {
		t.Fatalf("parent steer = %#v, want source parent with the attribution line first", turn)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.history) != 1 || runtime.history[0].StableTurnID != turn.StableTurnID {
		t.Fatalf("resident history = %#v, want the same steer", runtime.history)
	}
}
```

In the existing durability test (`runtime.history[0].Message.Text() != "persist me"` near line 53), change the expectation to `runtime.history[0].Message.Text() != parentMessageAttribution+"persist me"`.

Append to `cmd/evener-tui/hub_notifications_test.go`:

```go
// systemEventWireItem returns one recorded case from the system-event corpus,
// which agent.TestSystemEventWireFixtures produces from the real producers.
func systemEventWireItem(t *testing.T, name string) appwire.ThreadItem {
	t.Helper()
	raw, err := os.ReadFile("../../agent/testdata/systemeventwire/events.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Items []struct {
			Case string             `json:"case"`
			Item appwire.ThreadItem `json:"item"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Items {
		if c.Case == name {
			return c.Item
		}
	}
	t.Fatalf("no %s case in the system-event corpus", name)
	return appwire.ThreadItem{}
}

// A parent's recorded steer into its delegate stays a visible steering row.
func TestTUIParentSteerStaysVisible(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUINotification(t, &m, appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		Ref:   "local:root",
		Items: []appwire.ThreadItem{systemEventWireItem(t, "steer-from-parent")},
	})
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgSteering && strings.Contains(msgrender.RenderMessage(msg, 240, false), "Use the drain_log table") {
			return
		}
	}
	t.Fatalf("messages = %+v, want the parent's steer visible", m.session.messages)
}
```

Run: `cd agent && go test -count=1 -run 'TestDelegateSteerArrivesFromTheParent' .`
Expected: build failure, `undefined: events.SteeringSourceParent`.

- [ ] **Step 2: Implement the source, the attribution and the turn**

In `agent/events/payloads.go`, after `SteeringSourceUser`:

```go
// SteeringSourceParent marks steering a delegate's parent agent sent into its
// running turn with delegate_send. UIs label it "From parent" rather than
// rendering it as the human's words or as a daemon nudge.
const SteeringSourceParent = "parent"
```

In `agent/delegate_tree_steer.go`, replace `appendDelegateSteeringDurably` and delete `appendDelegateSteeringDurablyWithMetadata`:

```go
// parentMessageAttribution opens each message a delegate's parent steers into
// its running turn, so the delegate's model knows who is speaking.
const parentMessageAttribution = "Message from your parent agent:\n\n"

// parentSteeringTurn is the steering turn a delegate_send message to a running
// delegate appends as: from its parent, attribution first.
func parentSteeringTurn(message, stableTurnID string, now time.Time) schema.Turn {
	turn := schema.NewTurn(schema.TurnSteering, llm.User(parentMessageAttribution+message))
	turn.Timestamp = now.UTC()
	turn.SteeringSource = events.SteeringSourceParent
	turn.StableTurnID = stableTurnID
	return turn
}

func (s *Session) appendDelegateSteeringDurably(message, stableTurnID string) (delegateTranscriptEntry, error) {
	s.mu.Lock()
	ready := s.transcriptReady && s.transcript != nil
	s.mu.Unlock()
	if !ready {
		return delegateTranscriptEntry{}, errDelegateTranscriptUnavailable
	}
	turn := parentSteeringTurn(message, stableTurnID, s.sclock().Now())
	if err := s.appendTurnAfterTranscriptWrite(
		turn,
		func() error {
			// Delegate steering reaches this session from another session's
			// goroutine, like attention: the running execution's, or a
			// delivery turn of its own.
			_, err := s.recordTranscriptLocked(turn, transcript.DoorSynced, transcript.PlaceAsync)
			return err
		},
		func() { s.history = append(s.history, turn) },
	); err != nil {
		return delegateTranscriptEntry{}, err
	}
	return delegateTranscriptEntry{entryID: turn.StableTurnID, timestamp: turn.Timestamp}, nil
}
```

The call site in `Steer` and its audit entry are unchanged.

In `agent/system_event_wire_fixture_test.go`, add after the `steer-note-handoff` case:

```go
		{
			Case: "steer-from-parent",
			Note: "A delegate_send message steered into a running delegate (parentSteeringTurn): source parent, no kind, the attribution line first.",
			Item: projected(t, "steer-from-parent", parentSteeringTurn("Use the drain_log table; tree_state is being retired.", "q_fixture_parent", wireFixtureStart)),
		},
```

Run: `cd agent && go test -count=1 -run 'TestSystemEventWireFixtures$' . -update-wire && git diff --stat testdata/systemeventwire`
Expected: one case added, nothing else changed.

- [ ] **Step 3: Run the tests to see them pass**

Run: `cd agent && go test -count=1 -run 'TestDelegateSteer|TestDelegateController|WireFixtures$|Steer' . && cd .. && go test -count=1 -run TestTUIParentSteerStaysVisible ./cmd/evener-tui/`
Expected: PASS. Fix any other test the suite run shows asserting the old steer text or source `user` by comparing against `parentMessageAttribution` and `events.SteeringSourceParent`.

- [ ] **Step 4: Update the docs**

In `docs/job-control.md`, extend the `delegate_send` semantics bullet "If `to` identifies a running or currently-driven delegate, Evener injects the message into that exact generation. The return `action` is `steered`." with: "The injected steering turn has source `parent`, and the delegate's model reads it after the line `Message from your parent agent:`; clients label it "From parent". A message that starts an idle delegate opens its next generation as user input, as the original brief does."

In `docs/product/subsystems.md`, in row S11, append after Task 8's sentence:

```markdown
A parent's `delegate_send` into a running delegate is steering with source `parent`, which both clients label "From parent".
```

- [ ] **Step 5: Commit**

```bash
git add agent/events/payloads.go agent/delegate_tree_steer.go agent/delegate_tree_steer_test.go \
  agent/system_event_wire_fixture_test.go agent/testdata/systemeventwire/events.json \
  cmd/evener-tui/hub_notifications_test.go docs/job-control.md docs/product/subsystems.md
git add <any test Step 3 had to update, by name>
git diff --cached --stat
git commit -m "feat(delegates): a parent's steer reaches its delegate as from the parent

delegate_send into a running delegate appends steering with source parent
and an attribution line, so the delegate's model can tell its parent's
words from a human's. The system-event corpus records it.

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

### Task 12: Both clients label it "From parent"

**Files:**
- Modify: `appwire-client/typescript/steeringLabels.ts` (`steeringLabel(kind, source?)`)
- Modify: `appwire-client/typescript/testing/systemEventWireFixtures.ts` (`SystemEventWireCase` adds `"steer-from-parent"`)
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.tsx` (pass `item.source`)
- Modify: `mobile-native/src/projectedRows.ts:816` (pass `it.source`)
- Test: `appwire-client/typescript/steeringLabels.test.ts`, `cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.wire.test.tsx`, `mobile-native/src/systemEventRows.test.tsx`

**Interfaces:**
- Consumes: the `steer-from-parent` corpus case (Task 11).
- Produces: `steeringLabel(kind: string | undefined, source?: string): string`, returning `"From parent"` when `source === "parent"`.

- [ ] **Step 1: Write the failing client tests**

In `appwire-client/typescript/testing/systemEventWireFixtures.ts`, add `| "steer-from-parent"` after `"steer-note-handoff"`.

Append to `appwire-client/typescript/steeringLabels.test.ts`:

```ts
test("a steer from the session's parent agent says so, whatever its kind", () => {
  expect(steeringLabel(undefined, "parent")).toBe("From parent");
  expect(steeringLabel("agent-message", "parent")).toBe("From parent");
  expect(steeringLabel(undefined, undefined)).toBe("System steered");
});
```

Append to `cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.wire.test.tsx`:

```tsx
test("folds a parent's recorded steer to From parent, opening to what it said", () => {
  render(<SteeringItem item={steer("steer-from-parent")} turn={turn} live={false} />);
  expect(screen.getByText("From parent")).toBeTruthy();
  const body = screen.getByTestId("steering-item").querySelector("pre")?.textContent;
  expect(body).toBe(systemEventWireItem("steer-from-parent").text);
});
```

In `mobile-native/src/systemEventRows.test.tsx`, add `["steer-from-parent", "From parent"],` to `STEER_LABELS`.

Run: `make test-api-package`, then `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/messages/SteeringItem.wire.test.tsx`
Expected: FAIL: the label reads "System steered".

- [ ] **Step 2: Implement the label**

In `appwire-client/typescript/steeringLabels.ts`, replace `steeringLabel` and its comment:

```ts
// A steer the session's parent agent sent with delegate_send (the wire's
// source "parent", events.SteeringSourceParent) is the parent speaking, not
// the system, so it says so instead of "System steered".
const FROM_PARENT = "From parent";

/** The label a steer folds to in both clients: "From parent" for a steer
 * from the session's parent agent; otherwise "System steered: <what it did>",
 * or a bare "System steered" for a kind with no label, since a colon promises
 * a value. */
export function steeringLabel(kind: string | undefined, source?: string): string {
  if (source === "parent") return FROM_PARENT;
  const label = steeringKindLabel(kind);
  return label === undefined ? STEERED : `${STEERED}: ${label}`;
}
```

In `SteeringItem.tsx`, change `const label = steeringLabel(item.steeringKind);` to `const label = steeringLabel(item.steeringKind, item.source);`. In `mobile-native/src/projectedRows.ts`, change `label: steeringLabel(it.steeringKind)` to `label: steeringLabel(it.steeringKind, it.source)`.

- [ ] **Step 3: Format and run the client gates**

```bash
set -o pipefail
cd cmd/evener-hub/frontend && npx biome check --write \
  ../../../appwire-client/typescript/steeringLabels.ts ../../../appwire-client/typescript/steeringLabels.test.ts \
  ../../../appwire-client/typescript/testing/systemEventWireFixtures.ts \
  src/panes/session/transcript/messages/SteeringItem.tsx src/panes/session/transcript/messages/SteeringItem.wire.test.tsx
cd ../../..
make test-web
make test-api-package
make test-native
```

Expected: PASS.

- [ ] **Step 4: Run the Go gates for Task 11, commit and open PR 7**

```bash
set -o pipefail
"$(go env GOROOT)/bin/gofmt" -l $(git diff --name-only origin/main -- '*.go')
(cd agent && go build ./... && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=linux go vet -tags evenerfuzz ./... \
  && golangci-lint run --allow-serial-runners ./... \
  && /Users/jesse/git/prime-radiant-inc/evener/.superpowers/bin/race-serial go test -race -count=1 ./...)
go build ./... && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=linux go vet -tags evenerfuzz ./...
golangci-lint run --allow-serial-runners ./cmd/evener-tui/...
go test -count=1 ./cmd/evener-tui/...
git add appwire-client/typescript/steeringLabels.ts appwire-client/typescript/steeringLabels.test.ts \
  appwire-client/typescript/testing/systemEventWireFixtures.ts \
  cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.tsx \
  cmd/evener-hub/frontend/src/panes/session/transcript/messages/SteeringItem.wire.test.tsx \
  mobile-native/src/projectedRows.ts mobile-native/src/systemEventRows.test.tsx
git diff --cached --stat
git commit -m "feat(clients): a parent's steer is labelled From parent on the web and the phone

The shared steeringLabel takes the steer's source: a steer from the
session's parent agent folds to \"From parent\" instead of \"System
steered\".

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

Run `/simplify`, push its fixes, and open the PR (base `main`, regular) titled "feat(delegates): a parent's steer is labelled as from the parent". The body states ruling 5 (idle starts stay user input), the corpus case added, the `/simplify` outcome, and ends with `https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU`. Shepherd it to the merge gate.

## PR 8 (optional): Live micro-test

### Task 13: A delegate's question gets its parent's answer, live

Opt-in only (`//go:build eval` plus `EVENER_LIVE_TESTS=1`); it never runs in default tests. The model comes from a required flag, as `TestSkillsLive` takes `-skills-eval-model`; Jesse picks the cheap model when running it.

**Files:**
- Create: `agent/live_eval_registry_test.go` (`//go:build eval`; `liveEvalRegistry`, extracted from `agent/forced_note_live_test.go`)
- Modify: `agent/forced_note_live_test.go:40-61` (use `liveEvalRegistry`)
- Create: `agent/delegate_update_live_test.go` (`//go:build eval`)

**Interfaces:**
- Produces: `func liveEvalRegistry(t *testing.T) (*registry.Registry, string)` returning the registry and the state home, skipping the test when live evals are off or no provider layer is configured.

- [ ] **Step 1: Extract the live-eval registry setup**

Create `agent/live_eval_registry_test.go`:

```go
//go:build eval

package agent

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/internal/liveeval"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/llm/registry"
)

// liveEvalRegistry is the provider registry an explicitly opted-in live eval
// runs against, the developer's own provider layer and state home, with the state
// home it set. It skips the test when live evals are off or no provider layer
// is configured.
func liveEvalRegistry(t *testing.T) (*registry.Registry, string) {
	t.Helper()
	if !liveeval.Enabled(os.Getenv(liveeval.OptInEnv)) {
		t.Skipf("live eval disabled: set %s=1 to run provider-backed evals", liveeval.OptInEnv)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("live eval requires a resolvable user home: %v", err)
	}
	stateHome, providersConfig, noUserLayer := liveeval.Paths(envvars.XDGStateHome.Trimmed(), home)
	if noUserLayer {
		t.Skipf("%s is empty: no user provider layer to run a live eval against", envvars.EVENERProvidersConfig.Name)
	}
	t.Setenv(envvars.XDGStateHome.Name, stateHome)
	if _, err := os.Stat(providersConfig); err != nil {
		t.Skipf("providers.toml at %s: %v", providersConfig, err)
	}
	t.Setenv(envvars.EVENERProvidersConfig.Name, providersConfig)
	r, err := registry.Load()
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	return r, stateHome
}
```

In `agent/forced_note_live_test.go`, replace everything from the `liveeval.Enabled` check through `r, err := registry.Load()` and its error check with:

```go
	r, stateHome := liveEvalRegistry(t)
	if _, err := os.Stat(filepath.Join(stateHome, "evener", "auth", "openai.json")); err != nil {
		t.Skipf("no OAuth record at %s/evener/auth/openai.json: %v", stateHome, err)
	}
```

and drop imports it no longer uses.

- [ ] **Step 2: Write the micro-test**

Create `agent/delegate_update_live_test.go`:

```go
//go:build eval

package agent

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
)

var delegateUpdateEvalModel = flag.String("delegate-update-eval-model", "", "registry model selector for the explicitly opted-in delegate update micro-test")

// TestDelegateUpdateGetsItsParentsAnswerLive is the spec's optional live
// micro-test: a delegate that needs a fact only its parent has asks for it
// with communicate(end_turn=false); the update wakes the parent, which answers
// with delegate_send; and the delegate finishes with the answer.
//
// Run: EVENER_LIVE_TESTS=1 go test -tags eval ./agent/ -run TestDelegateUpdateGetsItsParentsAnswerLive -delegate-update-eval-model <selector> -v -timeout 15m
func TestDelegateUpdateGetsItsParentsAnswerLive(t *testing.T) {
	r, _ := liveEvalRegistry(t)
	if *delegateUpdateEvalModel == "" {
		t.Fatal("-delegate-update-eval-model is required")
	}
	client := llm.NewClient(llm.WithRegistry(r))
	prof, err := provider.Resolve(client.Registry(), *delegateUpdateEvalModel)
	if err != nil {
		t.Fatalf("resolve %s: %v", *delegateUpdateEvalModel, err)
	}
	dir := t.TempDir()
	root, err := NewSession(client, prof, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer root.Close()
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})

	const codename = "HALCYON-42"
	brief := "Start exactly one delegate with this prompt, word for word: \"Write a file named report.txt in the working directory whose only line is the codename your parent agent holds. " +
		"You do not know the codename: ask your parent for it with communicate(end_turn=false), then list the working directory with the shell while you wait, " +
		"and write the file once your parent's answer arrives. Finish with communicate(end_turn=true).\" " +
		"When the delegate asks you for the codename, answer it with delegate_send. The codename is " + codename + ". Do not write report.txt yourself."
	// TRIPWIRE: real provider round trips across two sessions; only a wedged
	// live call reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	if _, err := root.ProcessInput(ctx, brief, nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	report := filepath.Join(dir, "report.txt")
	for {
		if data, err := os.ReadFile(report); err == nil && strings.TrimSpace(string(data)) == codename {
			break
		}
		select {
		case <-wakes:
			if root.hasPendingRootDelegateAttention() {
				if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
					t.Fatalf("root notification turn: %v", err)
				}
			}
		case <-ctx.Done():
			t.Fatalf("report.txt never held the codename: %v", ctx.Err())
		}
	}
	sawUpdate := false
	for _, id := range rootAttentionFold(t, root).order {
		sawUpdate = sawUpdate || strings.Contains(id, "/update/")
	}
	if !sawUpdate {
		t.Fatal("the delegate's question did not reach the root as an update")
	}
}
```

- [ ] **Step 3: Compile it under the eval tag and run it once if Jesse wants**

Run: `cd agent && go vet -tags eval ./... && GOOS=linux go vet -tags eval ./... && go test -tags eval -count=1 -run 'TestDelegateUpdateGetsItsParentsAnswerLive|TestForcedNoteLive' .`
Expected: compiles; without `EVENER_LIVE_TESTS=1` both tests SKIP. `make lint-eval` covers the eval-tagged compile floor.

- [ ] **Step 4: Commit and open PR 8**

```bash
git add agent/live_eval_registry_test.go agent/forced_note_live_test.go agent/delegate_update_live_test.go
git diff --cached --stat
git commit -m "test(delegates): an opt-in live check that a delegate's question gets its parent's answer

Claude-Session: https://claude.ai/code/session_019Vi8aW6mmCJUtdi8j8fAvU"
```

Run `/simplify`, push, open the PR (base `main`, regular) with the run command and any live result Jesse asked for, and shepherd it.

## Self-review

**Spec coverage.**
- Decisions 1 and 4 (communicate is the only upward channel; an update wakes an idle parent): Tasks 5, 6, 7. Decision 2 (`delegate_send` own delegates only): Task 9. Decision 3 (updates only when the parent must know or act): Tasks 4 and 9 (prompt, tool and role text). Decision 5 (no compatibility code): no task adds any; old transcripts render as written.
- Tool contracts: communicate (Tasks 4, 6), delegate_send (Task 9), delegate observer guidance (Task 4), prompts (Tasks 4, 9), goldens (Tasks 4, 6, 9).
- Delivery: updates ride the report's attention path (Tasks 1, 5, 6); frame with `kind` (Task 2); when it arrives: idle (Tasks 6, 7), running (Task 7), `ask_user` (Task 7); order (Tasks 1, 7); parent to child (Tasks 11, 12); Activity and clients (Tasks 3, 8).
- Errors: undeliverable update (Task 6); `to="caller"` (Task 9).
- Testing list: every bullet maps to a named test in Tasks 2, 6, 7, 9 and 11; the live micro-test is Task 13.
- Docs: `docs/job-control.md` (Tasks 2, 8, 9, 11), `docs/product/session-activity.md` (Task 8), `docs/product/subsystems.md` S11/S12 (Tasks 8, 11), `docs/subagent-management/11-delegate-resource-model.md` (Tasks 1, 9).
- Closes #4172 (PR 4); PR #4197 confirmed closed (PR 5).
- #3725 (ruling 8, Jesse's 2026-10-10 ruling): PR 3a, Task 4a (no successor generation for presented attention; attention after the last request still wakes; the parent's follow-up still resumes); Task 7 asserts the running-coordinator case.

**Placeholder scan.** No TBD or "similar to". Task 8 Step 1 cannot list exact test edits in advance; it names the known files and gives the rule for each kind of failure.

**Type consistency.** `UpdateDeliveryID`, `PacketUpdate`, `EventDelegateUpdatePosted`, `UpdatePosted{Generation, Message}` (Task 1) are used with those names in Tasks 2, 5, 7 and 8. `PostUpdate(actor, message) (string, delegateMutationPlans, error)` (Task 5) matches Task 6. `delegateUpdateDelivery{Status, Reason}` and `postDelegateUpdate(ctx, message) (delegateUpdateDelivery, bool)` match between `toolDeps` and the Session. `newMessagingRoot` gains `configure` in Task 7, which updates its one earlier caller. `reportsToParent` (Task 4) gates both wording and delivery.

**Review Focus.** Each of the five lines has its test in the owning task: Task 5 and Task 7 (wait carry), Task 2 and Task 3 (markup, lookalikes), Task 7 (running coordinator, restart).
