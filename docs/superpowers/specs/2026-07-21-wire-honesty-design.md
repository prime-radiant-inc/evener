# Wire honesty: five appwire gaps the web rewrite worked around — design (v2, post-adversarial-review)

Five places where the live wire is less honest than the historical projection or than what clients need, discovered and root-caused during the Wave-4 web rewrite. This branch (`wire-honesty`, off main `4128e762d`) fixes the PRODUCER side. **v2 incorporates a two-reviewer adversarial review** (6 significant + 5 minor findings, all verified against source and folded below — the largest: item 4's original premise was factually wrong, item 2 is not producer-only for main's own web, and item 1 needed reset semantics specified).

**Shared constraints:** additive wire surface (one new catalog entry total) — with ONE stated exception: item 2 requires a coordinated one-guard legacy-web change on this same branch (below). The appwire catalog + appwiredoc drift gates stay green; `internal/appprojector` and `internal/apptranscript` suites extend per item; TUI and legacy-web tolerance is VERIFIED (not assumed) for items 1, 2, and 5 — the review already established several answers, cited inline.

## 1. Reasoning items complete on the wire (with timestamps and specified reset semantics)

**Today:** the live projector never emits a completing event for reasoning items and they carry no timestamps on either path (`internal/appprojector/appwire_projection.go:257-282`; `internal/apptranscript/apptranscript.go:250-259`). `p.reasoningItem` is reset only at UserInput (:138), GoalContinuation (:190), AssistantTextStart (:234), and SessionEnd (:822) — NOT at tool-call start — so today a reasoning→tool→reasoning turn collapses all bursts into ONE item, and the legacy renderer documents "single reasoning item per turn" (`renderer.js:2867`).

**Design:** reasoning ends at the first subsequent assistant text, tool-call start, or turn completion after a reasoning item is open. At that point the projector emits `NotifyItemCompleted` for the reasoning item with `StartedAt`/`CompletedAt`/`DurationMS` from a recorded first-delta time (new `p.reasoningStartByItem`, mirroring `toolStartByKey`) **AND resets `p.reasoningItem`** — completion-without-reset would let the next burst's deltas target an already-completed item id, the exact wire dishonesty this branch removes. **Consequence, stated deliberately: multiple reasoning items per turn become possible** (each burst its own item — the honest model). Verified impact: the new web renders items independently (fine); the legacy web maps a reasoning `item/completed` to `REASONING_START`, whose `beginReasoning` early-returns while a block is open — a benign no-op (verified `appwire.js:1045→:757-760`, `renderer.js:2869`), and its one-block-per-turn rendering visually coalesces multiple bursts (acceptable). **Mandatory check:** the TUI's reasoning handling under (a) a reasoning `item/completed` and (b) multiple reasoning items per turn — verify, don't assume.

**Historical:** the schema does not persist per-part reasoning timing; reload remains duration-less (documented; daemon-schema scope, out).

## 2. Steering becomes a real wire item (with the coordinated legacy guard)

**Today:** `EventSteeringInjected` emits only the bare notification (`appwire_projection.go:573-593`); historical projects a real `"steering"` item (`apptranscript.go:211-229`); live clients synthesize.

**Design:** the projector additionally emits `NotifyItemCompleted` with a `"steering"` ThreadItem (id `p.nextItemID("steering")`, `TurnID: p.activeTurnID`; no-active-turn → the `systemAnnouncementWithRaw` synthesized-turn branch). The `serf/steering/injected` notification is KEPT (TUI consumes it; attention/liveness semantics).

**NOT additive — the review's core finding:** the legacy web ALREADY renders `type:"steering"` items (`appwire.js:721-726` `eventsFromItem` → `STEERING_INJECTED`), reachable from the live `item/completed` fall-through (`:1045`) and the completed-turn inline path (`:805-808`/`:1071`), while the kept notification handler (`:1092-1097`) ALSO renders — so main's own embedded web (no UI flag on main) would render every steer twice the moment this lands. **Coordinated fix in this same branch:** one guard in the legacy LIVE `item/completed` path (and the completed-turn inline path) skipping `type === "steering"` — live steers keep rendering via the notification exactly as today; the reload/hydration path (which needs `eventsFromItem`'s steering mapping) is untouched. Skew check: new daemon + old (unguarded) web = the known double-render (accepted only across versions, never on one branch); old daemon + guarded web = notification renders once, nothing lost. The TUI is verified tolerant (no `steering` case in its reducer switch — silent drop; `cmd/serf-tui/internal/transcript/reducer.go:203-306`).

**Accepted divergence, stated:** the live item lands INSIDE the active turn; reload projects the steer as its own standalone turn (`apptranscript.go:211`, one entry = one turn). Live and post-reload grouping differ — same accepted asymmetry as mid-turn systemMessages (the `systemAnnouncementWithRaw` precedent). Clients must not assume turn-membership stability across reload for these.

**Client note (webui branch, later):** the new web drops its reducer synthesis when absorbing this (one commit) — its embedded hub always matches its own client, so no skew there.

## 3. Settled tool calls carry `ArgumentsJSON`

Unchanged from v1 (no review findings): one field in `EventToolCallEnd`'s settled-item literal (`appwire_projection.go:428-443`, args already resolved at :424-427). Tests: settled item's args equal the started item's.

## 4. Shell exit code: promote the field that is ALREADY on the wire

**v1's premise was false (both reviewers, independently):** the shell exit code already rides the wire structurally, live AND historical — `shellToolResult.ExitCode *int` (`agent/session_tools_shell.go:483`, set :337/:366) → `StateResult.State` (:346) → JSON-marshaled `ToolCallEndData.ToolState` (`agent/internal/tool/registry.go:543-547`) → the settled item's `Raw` (`appwire_projection.go:420,438`) and the reload path (`apptranscript.go:347`). The `[… exit N]` text clients parse is DERIVED from this same field (:456,:547). The gap is only that `Raw` is untyped and the new web's model discards it.

**Design (replaces v1's plumbing survey + fallback entirely):** promote — the projector unmarshals `exit_code` from `data.ToolState` (which it already holds) onto a new typed `ExitCode *int64` (`json:"exitCode,omitempty"`) on the settled commandExecution ThreadItem; apptranscript mirrors from `part.ToolResult.ToolState`. Zero execenv/event changes, zero text parsing, works for reload too. Non-shell tools without the field: absent, honestly. **Client note:** the new web's `parseShellExitCode` heuristic becomes the old-daemon fallback; its reducer starts consuming the typed field.

## 5. Escalation `resolved` broadcast (full footprint, all clearing paths)

**Catalog:** one new notification `NotifySerfSandboxEscalationResolved` = `"serf/sandbox/escalation/resolved"`, typed payload `{threadId, ref, escalationId, approved bool, reason string}` — `reason` distinguishes `"resolved"` (a human answered) from `"cancelled"` (cleared without an answer).

**Producer footprint (the review's correction — v1's "emission point" could not emit):** `Session.ResolveSandboxEscalation` (`agent/session_escalation.go:213-225`) only deletes the map entry and signals a channel; it has no wire access. The real chain, mirroring how `requested` works (`session_escalation.go:196` → projector case `appwire_projection.go:687`): a new `events.EventSandboxEscalationResolved` kind + `SandboxEscalationResolvedData` payload + `eventdata.go` sealed-interface registration + `s.emit(...)` + a projector case emitting the notification. **Emit on ALL THREE clearing paths** (the review's staleness finding): explicit resolve (:213), the turn-interrupt `ctx.Done()` arm (:204), and `cancelAllEscalations` on Close (:265-273) — otherwise interrupt/close-cleared cards stay stale on other clients, the exact defect this item exists to fix. Pruning from `PendingEscalations` already exists (defer at :186-190) — verified, no fix needed. The hub relay is method-agnostic (`cmd/serf-hub/app_relay.go:302`) — verified, old hubs relay fine.

**Tolerance:** unknown-notification drop is the established norm in TUI and legacy web — verify once, cheaply (the requested-side precedent and tests at `appwire_projection_test.go:176` are the pattern to extend).

**Client note:** the new web adds a one-line reducer case calling its existing `resolvePendingEscalation` helper.

## Sequencing and testing

**Revised order (the review inverted v1's risk calibration): 3 → 4 → 1 → 5 → 2** — ascending true blast radius: 3 is one line; 4 is now a small typed promotion of existing data; 1 adds the reset-semantics behavior change (TUI check); 5 adds the event-chain footprint; 2 carries the coordinated legacy-web guard and lands last, fully checked. Each item: TDD in projector/transcript suites (+ agent-module event tests for 5, + the legacy-web guard's jstest for 2); catalog/doc drift gates; the named tolerance verifications are BLOCKING gates, not notes. Full `make test` + `make lint` per landing. This branch merges to main independently; the webui branch absorbs and simplifies its client workarounds afterward (tracked there).
