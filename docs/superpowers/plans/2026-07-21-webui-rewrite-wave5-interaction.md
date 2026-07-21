# Web Rewrite Wave 5 — Interaction (M5) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Waves 1-4
> conventions apply verbatim (wave worktree + sub-streams with exclusive manifests, wave-local SDD
> artifacts, controller-owned chokepoints, honest exit-code gates — tsc BEFORE vitest with
> test-file-count verification — commits as separate invocations, Biome is the lint gate).

**Goal:** the session becomes fully drivable from the web — composer (send/steer/queue with the
legacy precedence rules), queue strip with edit/cancel/promote, drafts, attachments, ask_user
answering, and the session chrome (status row, model switch, session actions, tasks panel, goal).

**Parity floors:** `docs/web-ui/parity/parity-m5-composer.md` + `docs/web-ui/parity/contracts-composer-queue-pending.md`.
The session-chrome floor is thin by both docs' own admission: chrome tasks build from the design
doc's §Composer/§Session-chrome bullets (spec lines 89-97) with beyond-parity license; reviewers
get `cmd/serf-hub/assets/renderer.js` pointers for behavior spot-checks instead of a row checklist.

**Prereqs:** Wave 4 merged (integration `0f3bcaff2`+). Executes on wave worktree `webui-w5-interaction`
off integration; sub-streams branch off the wave branch after T1. Runs CONCURRENTLY with Wave 7
(settings) in its own worktree — W5 owns `panes/session/**`, `protocol/`, `stores/threads.ts`, and
the widgets `Textarea` + a new attachment drop-zone widget; W7 must not touch those (its own plan
carries the mirror constraint). Merges to integration are SERIAL: W5 first.

## Binding constraints (every task)

- Remount-safe: dockview unmounts inactive panes; durable state (drafts keyed per ref, queue,
  pending-optimistic entries, ask bookkeeping) lives in stores/localStorage, never component state
  that matters across a tab switch.
- Wire truth: ThreadModel from the reducer only; all wire calls through threads-store actions.
  Conflict (`expectedEntryId`/`expectedTurnId` mismatch) is ALWAYS a distinct outcome — never
  retried blind, never applied to the wrong entry (contracts §Queue: a shifted queue is a
  Conflict, not a wrong-message action).
- **Failure-feedback convention (decided this wave, T1):** user-initiated actions that fail
  surface via the existing `useToasts()` singleton — no new banner systems, no silent `.catch`.
- Widgets only; tokens-only CSS; sentence case; Biome clean; TDD with wire-true shapes (the
  wave-4 lesson: synthetic fixtures that the wire never produces are how bugs hide).
- Optimistic pending applies to ALL of send/steer/queue/drain (the legacy plain-send asymmetry is
  deliberately NOT carried — cross-cutting finding #2 in the parity doc, fixed per beyond-parity).

## Locked interfaces (T1 ships; streams import)

```ts
// stores/threads.ts — new/extended actions (all through the wire, Conflict-aware)
send/steer/queue(ref, text, attachments?: InputAttachment[])   // extended: base64 Data+MediaType+Name, not URL strings
drainAsSteer(ref): Promise<void>
promoteQueuedAsSteer(ref, index, expectedEntryId): Promise<void>
cancelQueued(ref, index, expectedEntryId): Promise<{removedText, removedImages}>
setModel(ref, modelProvider, model) / setReasoningEffort(ref, level) / setGoal(ref, objective)
rename(ref, name) / compact(ref) / clearThread(ref) / shutdown(ref) / forkFromTurn(ref, opts)  // fork #42 + aside #43 = same wire method

// protocol/model.ts — ThreadModel gains (hydrate + live where the wire pushes):
capabilities, goal, contextUsed/Window/Pressure, usage, workMillis, activeTurnStartedAt,
reasoningEffortLevels, supportsReasoning
// Capabilities have NO live push on the wire (verified): send/queue availability derives via the
// legacy precedence tiers (ended/closed → both false; active+queue-cap-false → both false;
// active → queue; else → send). Derivation is a pure exported helper, tested against the
// parity table verbatim (parity-m5-composer.md:64-71). Live capability push = wire-candidate,
// out of scope this wave.

// Session.tsx — T1 carves the slots; streams fill them, never edit Session.tsx again:
<Composer ref={ref} />         // below transcript (T2 fills; T3/T4 render inside Composer's own tree)
<SessionChrome ref={ref} />    // PaneScaffold header/footer surfaces (T5 fills)
```

## Tasks

### T1 (sequential): interaction chokepoint
Protocol-layer extension (ThreadModel fields above, hydrate + notifications where they exist —
wire-true fixtures); the full store-action set (Conflict typed as its own error class reusing
`WireError` machinery); attachment param shape (base64 InputItem, not URLs); the send/queue
derivation helper with the verbatim precedence table; the toast failure convention applied to the
existing silent `loadOlder` catch (`Session.tsx:134`) as the reference implementation; the two
Session.tsx slots (empty placeholder components streams replace); `Textarea` autogrow fixed to
scrollHeight-based with a max-height clamp (the newline-count version can't grow on wrapped
lines — widget-level fix, W5 owns Textarea this wave). Gate: full suite + live smoke sending a
real message through the new store path.

### T2 ∥ T3 ∥ T4 ∥ T5 (streams off the wave branch after T1):

- **T2 composer core** (`panes/session/composer/**` minus queue/ and askDock/): the input surface
  (Textarea), send-vs-steer-vs-queue routing via T1's derivation, Enter-to-send preference
  (localStorage per-device; `prefs.ts` store is M6 — a tiny local hook this wave, migration noted),
  drafts per-ref (localStorage, restore-on-mount — the legacy cross-session leak guard likely
  reduces to keying correctly under remount-per-pane; verify against dockview unmount semantics
  and document), attachments (paste/drag/picker → base64 InputItem; a new drop-zone widget owned
  by this stream; marker-in-textarea UX per contracts §Attachments), interrupt affordance.
  ~89 floor rows.
- **T3 queue strip + optimistic pending** (`panes/session/composer/queue/**`): queue rendering
  from `model.queue`, edit = restore-text-to-composer THEN cancelQueued (loser-safe order is a
  contract row), promote/cancel with expectedEntryId Conflict handling, drain-as-steer;
  optimistic pending as REDUCER/STORE-OWNED state (design decision made: declarative optimistic
  entries with a 10s timeout reaper, not a DOM-chip registry port), applied uniformly to
  send/steer/queue/drain. ~68 floor rows.
- **T4 ask_user answering dock** (`panes/session/composer/askDock/**`): the dock renders pending
  questions (shared parsing extracted from `transcript/tools/askUser.tsx` into a helper — T4 owns
  the extraction, the transcript card's rendering is untouched), answer composition to the
  `[answers]` format submitted via plain send (verified: NO dedicated wire method exists),
  multi-pending-set bookkeeping (late-arriving questions never swept into an in-flight
  settlement), Conflict drops composed text into the composer, never auto-retries. ~66 floor rows.
- **T5 session chrome** (`panes/session/chrome/**` + the SessionChrome slot): status row (state
  dot, model chip, reasoning effort, work-time clock from activeTurnStartedAt/workMillis, context
  gauge via Meter, cost), mid-session model switch (Combobox off reasoningEffortLevels/models),
  session actions (fork/aside via forkFromTurn, compact, clear, shutdown, rename) with
  destructive-action confirmation via Dialog, goal display/set (snapshot + optimistic local
  update; live push = wire-candidate), tasks panel — FIRST STEP: investigate `serf/tasks/list`'s
  real daemon response shape (the catalog types it `any`) and pin it with a wire-true fixture
  before building; steering classification (task-nudge/full-list suppression) lands here WITH the
  tasks panel since the panel owns that surface — NOTE: touches `transcript/messages/SteeringItem.tsx`
  (wave-4 file, sanctioned cross-wave edit, called out for the reviewer).

### T6: wave close
Parity sweep (both floors + the chrome bullets), live proof (real hub: send/steer/queue/edit/
promote under load, ask answer round-trip, model switch mid-session, fork+aside, goal set, tasks
panel live, attachment paste round-trip — the base64 PNG contract row), full gates, wave5-report,
merge to integration (SERIAL with W7 — W5 merges first).
