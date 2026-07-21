# Wire honesty — design (v3, post two adversarial rounds; simplified to what has consumers)

Wave-4's web rewrite surfaced five places the live wire is less honest than the historical projection. v1 designed all five; round one (2 reviewers, 6 significant findings) fixed factual errors; round two (2 reviewers, simplification lens, 6 significant findings) established the cut that defines v3: **only one of the five fixes a bug any in-repo client actually has** — the rest existed so a separate branch could someday delete client workarounds that already ship and work. v3 builds the consumers' package and documents the rest as non-goals with revival conditions.

**What ships (one landing, branch `wire-honesty` off main):**

## A. Settled tool calls carry `ArgumentsJSON` and `ExitCode` (former items 3+4, merged — identical edit surface)

Two typed fields on the settled `commandExecution` item, both from data the producers already hold:
- `ArgumentsJSON`: the live projector resolves args at `internal/appprojector/appwire_projection.go:424-427` but omits them from the settled literal (:428-443) — add the field. (Historical already carries it: `apptranscript.go:284,312`.)
- `ExitCode *int64` (`json:"exitCode,omitempty"`): the shell exit code ALREADY rides the wire structurally inside `ToolState` (`agent/session_tools_shell.go:483` → `registry.go:543-547` → the item's `Raw`, live :420/:438 and reload `apptranscript.go:347`) — the round-one discovery that killed v1's plumbing survey. Promote: unmarshal `exit_code` from the `ToolState` the projector/transcript already hold onto the typed field, live AND reload. Non-shell tools: absent, honestly.

One commit-group: both fields on the same literal + the apptranscript mirror + settled-item assertions in both suites + catalog/doc drift gates. Client note (webui branch, later): its shell text-heuristic becomes the old-daemon fallback; its reducer consumes the typed fields.

## B. Escalation `resolved` broadcast (former item 5, redesigned by round two)

**The one genuine multi-client bug**: a card resolved (or cleared by interrupt/close) on one client stays stale on every other until re-snapshot.

- **Catalog:** one new notification `serf/sandbox/escalation/resolved`, typed payload `{threadId, ref, escalationId}`. No `reason`, no `approved` — round two verified no client consumes either (the sole consumer clears by id identically; the TUI settles off its own request's ACK, `hub_escalation.go:179-217`), and the producer cannot reliably distinguish close-cancel from interrupt anyway. Additive later if a "resolved elsewhere" toast ever wants more.
- **Producer:** ONE emit site — the convergence point. All three clearing paths (explicit resolve, turn-interrupt `ctx.Done()`, close-cancel) already funnel through the blocked goroutine in `escalateOnSandboxDenial` (`agent/session_escalation.go:186-206` — the select's two arms plus the defer that prunes the waiter on every exit; the `requested` event already emits from this exact function at :196). Emit the resolved event from that exit path. This is smaller than per-site emission AND correct where per-site was not: round two found the spec'd close-site emission racy (`cancelFunc()` runs before `cancelAllEscalations()` in `session_lifecycle.go:150/:180`, so the goroutine's own `ctx.Done()` arm frequently clears the waiter first, leaving the close-site emit an empty-map no-op). The convergence emit fires exactly once per escalation regardless of which arm wins.
- **Chain:** new `events.EventSandboxEscalationResolved` + payload struct + `eventdata.go` registration + a projector case emitting the notification (mirror the `requested` pattern, `appwire_projection.go:687` and its test at `appwire_projection_test.go:176`). The hub relay is method-agnostic (`app_relay.go:302`) — verified, no hub work.
- **Tolerance:** unknown-notification drop is the established norm in TUI and legacy web — covered by extending the existing projector test, NOT a blocking gate (round-two de-ceremony, accepted).
- Client note (webui branch, later): one reducer case calling its existing `resolvePendingEscalation` helper.

## Non-goals (analysis retained; each has a stated revival condition)

**Reasoning completion events + timestamps (v2 item 1) — DEFERRED.** No in-repo consumer gains: the TUI already tolerates reasoning `item/completed` AND multiple items per turn (verified `cmd/serf-tui/internal/transcript/reducer.go:229-242`) and reads no timing; the legacy web no-ops it (`beginReasoning` early-return); the new web observes timing client-side (shipped, reviewed). Shipping it would also add a producer behavior change (reset semantics → multiple reasoning items per turn) nothing asked for, and live durations reload can't match (schema has no per-part timing) — introducing an asymmetry, not removing one. **Revive when:** the daemon schema gains per-part reasoning timing (reload can then match), or a thin wire-only client needs server-authoritative durations. Revival notes from review: track the start with a single `reasoningStartAt time.Time` scalar, NOT a keyed map (exactly one reasoning item is ever open — `p.reasoningItem` is a scalar; a map adds a five-reset-site stale-entry leak hazard); prefer completing only at the existing reset points, preserving the single-item-per-turn model.

**Steering as a wire item (v2 item 2) — DROPPED.** The kept `serf/steering/injected` notification is proven sufficient (the TUI renders steering from it alone; the new web synthesizes from it in ~30 reviewed, working lines). The item's entire cost was self-created: main's embedded legacy web already renders `type:"steering"` items (`appwire.js:721-726` via the live fall-through :1046), so the item forces a coordinated two-path legacy guard, a cross-version double-render hazard, and a permanent live/reload turn-placement divergence — all to let a separate branch delete a working workaround. **Revive when:** a client exists that cannot synthesize steering from the notification.

## Process

One landing (A then B, or together — independent), TDD in the projector/transcript/agent-event suites, catalog + appwiredoc drift gates, `make test` + `make lint`. Merges to main independently of the webui branch; that branch absorbs and simplifies its client fallbacks afterward (tracked there). Round-two scoring for the record: 3-3 tie, split credit — one reviewer found the close-path emission race and the unconsumed payload fields; the other found the package restructure (drop/defer/merge) this version enacts.
