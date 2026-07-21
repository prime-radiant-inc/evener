# Wire honesty: five appwire gaps the web rewrite worked around — design

Five places where the live wire is less honest than the historical projection or than what clients need, discovered and root-caused during the Wave-4 web rewrite (each with client-side workarounds shipped on the webui branch; receipts inline). This branch (`wire-honesty`, off main `4128e762d`) fixes the PRODUCER side. Client consumption changes (simplifying the workarounds) belong to the webui branch and happen after it absorbs these — the wire changes are all additive, so no version skew breaks either side meanwhile.

**Shared constraints:** every change is additive (`omitempty` fields, existing notification kinds where possible, one new catalog entry total); the appwire catalog + appwiredoc drift gates must stay green; `internal/appprojector` and `internal/apptranscript` suites extend per item; TUI and legacy-web tolerance is verified for anything that puts a NEW item type or notification on the wire (items 2 and 5) BEFORE it ships.

## 1. Reasoning items complete on the wire (with timestamps)

**Today:** the live projector never emits any completing event for a reasoning item — no `item/completed` exists for reasoning in the wire vocabulary; the item only settles when its whole turn settles. Reasoning items also carry no `StartedAt`/`CompletedAt` on either path (`internal/appprojector/appwire_projection.go:257-282`; `internal/apptranscript/apptranscript.go:250-259`). Result: no client can show "Thought for Ns" from server truth; the new web stamps client-observed times at the turn-settle fold (live-only, lost on reload).

**Design:** the projector already tracks the open reasoning item (`p.reasoningItem`, `ensureReasoningItem`). Record its first-delta time (new `p.reasoningStartByItem` map, mirroring the existing `toolStartByKey` precedent). When reasoning ENDS — defined as the first subsequent assistant text, tool call, or turn completion after a reasoning item is open — emit `NotifyItemCompleted` for it with `StartedAt`/`CompletedAt`/`DurationMS` stamped from the recorded start and the ending event's timestamp (same honesty rule as tool timing, issue #37: anything not honestly recorded stays unset). No new catalog surface — `item/completed` already exists; reasoning items already flow through it structurally on the client side.

**Historical:** the transcript schema does not persist per-part reasoning timing, so reload remains duration-less — documented, matches legacy's own live-only behavior. (Persisting it is daemon-schema scope; out of this branch.)

**Client note (webui branch, later):** the reducer's `item/completed` path already merges reasoning items; wire timestamps will flow into ThinkBlock's preferred path automatically. The R2 observed-stamps fallback stays as the old-daemon degradation.

## 2. Steering becomes a real wire item

**Today:** `EventSteeringInjected` emits only the bare `serf/steering/injected` notification (`appwire_projection.go:573-593`); the historical path projects a real `"steering"` ThreadItem (`apptranscript.go:211-229`). Live clients must synthesize the transcript item themselves (the new web does, reducer case landed in Wave 4-R1).

**Design:** the projector ADDITIONALLY emits `NotifyItemCompleted` carrying a `"steering"` ThreadItem (id `p.nextItemID("steering")`, `TurnID: p.activeTurnID`, Text/Images/Source exactly as the notification's payload builds them). No active turn → follow `systemAnnouncementWithRaw`'s established branch: synthesize a completed single-item turn. The existing `serf/steering/injected` notification is KEPT unchanged — the TUI and legacy web consume it, and it serves non-transcript purposes (attention/liveness).

**Mandatory pre-ship checks:** (a) legacy web's `ITEM_COMPLETED` handling with an item type it has no renderer for — verify it falls back gracefully (it predates live `"steering"` items); (b) TUI likewise. If either renders garbage, gate the fix behind their tolerance fixes in the same branch.

**Client note:** the new web's synthesized-steering reducer case must be REMOVED when the webui branch absorbs this (one commit: drop synthesis, rely on the wire item) — otherwise steers double-render there. No skew in practice: that branch always runs its own embedded hub.

## 3. Settled tool calls carry `ArgumentsJSON`

**Today:** `EventToolCallEnd` resolves the args (`appwire_projection.go:424-427`) but uses them only to derive `Description` — the settled item literal (:428-443) omits `ArgumentsJSON`, so the live settle drops what `item/started` (:373) carried. Historical items carry it (`apptranscript.go:284,312`). The new web preserves client-side (Wave 4-R2 mergeArguments).

**Design:** one field in the settled-item literal: `ArgumentsJSON: argsJSON`. Nothing else. Tests: projector suite asserts the settled item's args equal the started item's.

## 4. Shell exit code as wire contract

**Today:** no structural exit code anywhere on the wire; the new web detects shell failure by parsing output text (`[… exit N]` / `exit_code=N` — with documented false-positive surface), and legacy did the same.

**Design (preferred, if plumbing is shallow):** surface the exit code structurally end-to-end — `ExitCode *int` on the tool-end event payload (agent module; additive field on the payload struct, module-compatible), projected as a new `ExitCode *int64` (`json:"exitCode,omitempty"`) on the settled commandExecution ThreadItem, and mirrored by apptranscript IF the persisted tool state carries it (investigate; if not persisted, live-only like timing, documented). **Fallback (if execenv's plumbing is deep):** centralize today's text parse in the projector so exactly one place owns the heuristic and every client gets the structural field — with a comment marking it heuristic pending real plumbing. The implementation task starts with a 30-minute plumbing survey and picks per this rule, reporting which branch it took.

**Client note:** the new web's `parseShellExitCode` heuristic becomes a fallback for old daemons once this lands.

## 5. Escalation `resolved` broadcast

**Today:** the catalog has only `serf/sandbox/escalation/requested` + the resolve METHOD. When one client resolves, no other client learns — cards go stale until re-snapshot (legacy had the identical staleness; verified during Wave 4-R3).

**Design:** one new catalog notification — `NotifySerfSandboxEscalationResolved` = `"serf/sandbox/escalation/resolved"`, payload `{threadId, ref, escalationId, approved bool}` (typed struct in the catalog, unlike requested's history — we get generated types for free). Emission point: wherever the daemon-side resolve handler accepts the decision and unblocks the waiting tool-exec goroutine — the same state change that removes the entry from the `PendingEscalations` snapshot source (verify it already prunes; if pruning is missing, that's a bug this item also fixes). The hub relays it like every thread-scoped notification. Unknown-notification tolerance in TUI/legacy is the established norm (verify once, cheaply).

**Client note:** the new web's reducer gains a one-line case calling its existing `resolvePendingEscalation` helper (built in R3 for exactly this), replacing own-client-only removal.

## Sequencing and testing

Independent items; recommended order 3 → 1 → 2 → 5 → 4 (ascending blast radius; 3 is one line + tests, 4 has the plumbing unknown). Each item: TDD in the projector/transcript suites; the catalog/doc drift gates; item 2 and 5 add the TUI/legacy tolerance verification. Full `make test` + `make lint` per landing. This branch merges to main independently of the webui branch; the webui branch absorbs via its main-monitor and then simplifies its client workarounds (tracked there, not here).
