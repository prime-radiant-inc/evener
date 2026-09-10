# web-notes-human-interrupts: a human note interrupts the turn, labels the divider, and survives rejoin

**What this covers**: the human whiteboard path end to end on the web
surface. `notes/human/set` (`appwire/types.go:810-824`,
`agent/session_notes_rpc.go:SetHumanNote`) stores the note, emits
`evener/notes/updated`, and injects a steer that interrupts the running turn;
the projector labels the steering divider `Human note`
(`SteeringItem.tsx:69`, kind `human-note` on the wire); the Notes panel's
Shared notes section renders it (`NotesPanel.tsx:165-261`); and a fresh
live read carries the note (the Task 7 I1 gap — fixed by the envelope
projection in `server/thread_envelope.go` + `server/appwire_runtime.go`,
pinned at unit level by
`server/thread_envelope_notes_test.go:TestThreadEnvelopeSeedUsesStructuredMetaNotes`
and at e2e level by step 6 below).

The Go layer covers the store, the steer, and the gate with unit tests
(`agent/session_notes_test.go:TestSetHumanNoteStoresAndSteers`,
`TestSetHumanNoteRetryOfOneOuterIDSteersOnce`,
`cmd/evener-hub/app_rpc_test.go:11332`
`TestHubRPCNotesHumanSetGatedByCapability`); this is the live-stack
counterpart that proves the interrupt, the label, and the rejoin actually
work. It exercises four things in one run:

- **notes/human/set while a turn runs** — the call lands on `setNotesHumanWithResume`
  (`cmd/evener-hub/app_session_resume.go:91-102`), which pre-flight gates on
  the `shared-notes` capability and resumes an exited session first, like
  `goal/set`. Same retry-safe-mutation shape as the goal precedent:
  `clientMutationId` + `expectedInstanceId` (`appwire/types.go:813-818`).
- **The human-note steer interrupts the model loop** — `executeAtomic`
  durably accepts the canonical note, one typed pending steering entry and
  the receipt together (`agent/session_notes_rpc.go:SetHumanNote`). One
  `clientMutationId` survives dispatch, retry, receipt reconciliation and
  ordinary pending-steering delivery; a retry cannot enqueue a second entry.
- **The divider label comes from the wire, not the prose** — the steering
  kind `events.SteeringKindHumanNote` is persisted at acceptance, and the
  transcript divider renders `System steered: Human note`
  (`SteeringItem.tsx:69,202-203`) without guessing from the note's prose.
- **Rejoin carries the note** — the live envelope seeds `HumanNote` from
  `SessionMeta()` and the root commit path patches it from
  `NotesUpdatedParams` (`server/thread_envelope.go`, `server/appwire_runtime.go`),
  so a fresh `thread/read` (what a rejoining client hydrates from) already
  carries the note before any push arrives.

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. The
`window.EvenerAppwire.request("notes/human/set", …)` /
`window.EvenerRenderer.sessionId` route this card used to drive died with the
vanilla frontend (`660376f78`), and its replacement is **not reachable from
`eval`**: `threadsStore` is a module import with nothing on `window`. So the
browser half must go through the real UI (the Notes editor), and the exact
assertions go to `/rpc` instead.

## Pre-state

- A freshly built hub on an isolated `$HOME` and a kernel-assigned port — see
  the Setup checklist in `docs/developing-evener/agentic-testing.md`. Token at
  `$HOME/.evener/auth-token` (that isolated one).
- **No provider credential needed.** This card drives the session against
  `test/e2e/fakellm` (a scripted OpenAI-compatible provider local to the
  repo), so a turn stays in flight for exactly as long as the run holds the
  model round open — no AGENTS.md pacing prompt, no sleeps, no network. The
  ready-made recipe is
  `scripts/e2e/e2e-webui-turn-controls.sh` (builds fakellm + evener + the
  SPA into a throwaway run dir, starts both on kernel-assigned ports, points
  the hub's `providers.toml` at fakellm): run it with `--hold 30 --rounds 40`
  so one turn stays "running" for ~20 minutes, then follow its printed
  browser auth URL and spawn recipe with working directory `$workspace` and
  model `fake/fake-test-model`. Manual equivalent: build
  `test/e2e/fakellm/cmd` and `cmd/evener`, point a `providers.toml` instance
  at the fake's `/v1` base URL, spawn with that `provider/model`.
- A real SPA bundle (`make build-web` — a checkout that has never run it serves a
  one-line `frontend/dist/PLACEHOLDER` and no app). The script above builds it
  unless passed `--skip-web`.
- A hermetic `$WORK` as the session's `working_dir` (the script's
  `$workspace` serves; `mktemp -d` otherwise).

## Steps

Spawn through the AppWire-backed UI: visit `$HUB/auth/$TOKEN`, navigate to
`/new`, use working directory `$WORK`, model `fake/fake-test-model`, and a
prompt that runs tool rounds (e.g. `read NOTES.md and keep working`). The
fake answers every round with a harmless `read_file` and ends the turn only
after `--rounds` of them, so the turn is genuinely in flight from the first
round on. Read the session ref off the resulting `/s/local:<SID>` path.

1. **[browser-free] Confirm the session is live with a turn in flight** (the
   precondition for the interrupt half of this card). Dial
   `ws://127.0.0.1:$PORT/rpc` with `Authorization: Bearer $TOKEN`,
   `initialize` (protocolVersion `evener-appwire-v2` — see "Driving AppWire
   directly" in `docs/developing-evener/agentic-testing.md`; frames carry no
   `jsonrpc` field), then call:
   ```json
   {"id":3,"method":"thread/read","params":{"ref":"local:<SID>","includeTurns":false}}
   ```
   **Expected:** `result.thread.status.type` is `active` with a non-empty
   `result.thread.evener.activeTurnId` (both must be present —
   `submitRouting.ts:48-50`'s `isTurnActive` requires the pair, and this card's
   browser steps key off the same condition), and
   `result.thread.evener.capabilities.sharedNotes` is `true`. Note it is the
   **appwire** `ThreadCapabilities` that carries the bit (which the hub's
   `shared-notes` pre-flight gate reads,
   `app_session_resume.go:97`); a `false` here means the gate will refuse
   step 2 and the negative path is covered by
   `TestHubRPCNotesHumanSetGatedByCapability` instead.

2. **[browser-free] Set the human note over the wire while the turn runs —
   the exact interrupt proof.** On the same socket, call:
   ```json
   {"id":4,"method":"notes/human/set","params":{"ref":"local:<SID>",
    "clientMutationId":"<uuid>","expectedInstanceId":"<SID>",
    "note":"prefer tabs over spaces in every example"}}
   ```
   Param and response shapes are `appwire.NotesHumanSetParams` /
   `NotesHumanSetResponse` (`appwire/types.go:813-824`); the response's
   `note` is the stored post-clamp value, which is what every downstream
   consumer converges on. **Expected:** no error (an `Unavailable` here means
   the `shared-notes` capability gate refused, or the source is gone). The
   turn does not end: it is interrupted and continues — steering injects into
   the running loop rather than terminating it (the same contract
   `web-steer-live-turn.md` pins for `turn/steer`).

3. **[browser-free] Prove the interrupt reached the model loop, not just the
   hub.** Watch `fakellm.log` (`tail -f $run/fakellm.log` in the script's
   run dir): every round's messages print to stderr with the session's name,
   so the round after the set carries the note text as a user message
   (`fakellm/cmd/main.go:22-24` — "a steer that arrived shows up as a user
   message in the following round"). **Expected:** a round line for this
   session whose messages contain `prefer tabs over spaces`. Falsify: the
   receipt in step 2 was `Applied` but no later round carries the text — the
   steer never reached the running loop.

4. **[browser] The same action through the real UI.** Authenticate and open
   the session:
   ```text
   navigate $HUB/auth/<TOKEN>?next=/s/local:<SID>
   await_element [data-testid="composer-input-card"]
   ```
   (Use the literal token, not the path. Note the ref form — a bare `/s/<SID>`
   renders "Page not found" by design.) Open **Notes** using its session
   chrome button/menu or `/notes` desktop pane. Find
   `[data-testid="shared-notes-section"]` and read the step-2 text from
   `[data-testid="shared-notes-editor"] textarea[aria-label="Human note"]`
   via `.value`; `shared-notes-human` is ended-session prose, not this live
   editor. There are no Edit/Save controls (`NotesPanel.tsx:165-187`).

   Edit to a distinct note, then blur. A dirty blur starts a **10,000 ms**
   timer only if no same-session editor remains focused. Observe the RPC
   timeline: no request before expiry; then await the save acknowledgment
   and `[data-testid="shared-notes-saved"]`. `shared-notes-saving` may appear
   while pending; failures use inline `shared-notes-error`. Saved proves
   durable acceptance, not consumption by the model (step 3 proves that).

   Refocus either same-session editor before expiry: the timer is cancelled.
   Blur again to start a fresh full 10,000 ms interval. Focus alone or an
   unchanged blur sends nothing. Two mounted editors share one draft/timer.
   A focused panel closed without blur retains its draft but invents no save;
   a timer from a real earlier blur survives unmount. Record actual blur
   events to distinguish these cases. Failed drafts survive close/reopen;
   newer edits survive older acknowledgments. Ambiguous submitted saves
   retain their original ID/payload in the existing persisted outbox for
   retry; unsent drafts are shared UI state, not browser-reload persistence.
   Compare the clean editor to the server's canonical acknowledgment, not a
   client-normalized copy. There is no `window` setter; drive the editor.
   ```javascript
   (() => {
     const section = document.querySelector('[data-testid="shared-notes-section"]');
     return {
       port: location.port,                        // page-identity check, always
       path: decodeURIComponent(location.pathname), // /s/local:<SID>; the literal colon breaks naive === compare
       section: !!section,
       human: section?.querySelector('[data-testid="shared-notes-editor"] textarea')?.value ?? null,
       editable: !!section?.querySelector('[data-testid="shared-notes-editor"] textarea'),
     };
   })()
   ```

5. **[browser] Read the labelled steering divider.** The daemon-originated
   human-note steer renders as the collapsible divider
   `[data-testid="steering-item"]` (`SteeringItem.tsx:111`) — NOT as a
   `user-message-item`: that shape is for steers the human typed themselves
   (see `web-steer-live-turn.md`'s Sharp edges; selecting on
   `user-message-item` here finds nothing and reads as a regression). Its
   summary reads `System steered: Human note` (`:202-203` with
   `KIND_LABELS["human-note"]`, `:69`).
   ```javascript
   (() => {
     const items = [...document.querySelectorAll('[data-testid="steering-item"]')];
     return {
       port: location.port,                        // page-identity check, always
       labels: items.map((el) => el.querySelector("summary")?.textContent ?? ""),
       bodies: items.map((el) => el.querySelector("pre")?.textContent?.slice(0, 80) ?? ""),
     };
   })()
   ```
   Cross-check against the daemon's authoritative record — the steering turns
   it actually queued:
   ```bash
   go run ./cmd/evener doctor transcript "$SID" --format outline --range last:40 | grep -i steering
   ```
   **Expected:** at least one divider whose summary contains `Human note` and
   whose body starts `human updated their whiteboard: prefer tabs over
   spaces`. The label must come from the wire kind, not the prose: a divider
   whose body carries the note text but whose summary is the bare `System
   steered` means the typed kind was lost and is a regression. The kind is
   persisted with the accepted entry, not annotated afterward; do not accept
   a missing label as a timing race. Falsify: no divider at all while step 3
   shows the text reaching the model (the projection dropped the steering turn).

6. **[browser-free] Rejoin with a fresh read — the Task 7 I1 pin.** Issue a
   new `thread/read` with `includeTurns:true` on a second socket (or the same
   one — what matters is that this is a fresh read, the exact call a
   rejoining client hydrates from, not a push the first socket already holds):
   ```json
   {"id":5,"method":"thread/read","params":{"ref":"local:<SID>","includeTurns":true}}
   ```
   **Expected:** `result.thread.evener.humanNote` is the step-4 edited text —
   present on the envelope snapshot itself, before any push arrives. This is
   the live-hydration path the envelope fix (`server/thread_envelope.go`
   seeding from `SessionMeta()`, `server/appwire_runtime.go` root commit
   from `NotesUpdatedParams`) closed: pre-fix, a live session hydrated notes
   as empty until the first push. Falsify: `humanNote` empty here while step
   4 shows it in the panel (the panel read it off a push; the snapshot
   dropped it — the I1 regression).

7. **[browser-free, history check] Assert bounded notes-context growth.**
   Still on the wire, count the per-round notes projections:
   ```bash
   go run ./cmd/evener doctor transcript "$SID" --format outline --range last:80 | grep -c NOTES_CONTEXT
   ```
   (or count `kind == "NOTES_CONTEXT"` turns in the transcript JSONL).
   **Expected:** at most one `NOTES_CONTEXT` turn per model round since the
   note was set — the projection is append-only per call
   (`agent/session_notes_rpc.go:161-167` `maybeAppendNotesContext`), the same
   growth shape as the existing per-round steering appends. It must grow at
   most linearly in rounds, and an empty store must append nothing
   (`notesContextBlock` returns `""`, pinned by
   `agent/session_notes_test.go:334`
   `TestNotesContextBlockContainsNotesAndURLs`). Falsify: the count exceeds
   the round count (a call site appending more than once per round), or
   `NOTES_CONTEXT` turns appear from before the note existed (empty state
   appended). If the bound ever needs revisiting, the shape to document is
   here: N rounds with notes set ⇒ N projections, each a fresh render of the
   current block (Task 5 M3 carried watch item).

## Expected

- **Step 1 (precondition, exact)**: `status.type` `active`, non-empty
  `activeTurnId`, `capabilities.sharedNotes` `true`. Falsify: `sharedNotes`
  `false` on a live evener session — the capability bit
  (`server/appwire_runtime.go`, beside `Goal`) regressed.
- **Step 2 (store, exact)**: `notes/human/set` returns the stored post-clamp
  note with no error. A whitespace-only or over-length note returns the
  normalized/clamped form (whitespace collapsed, 1000-rune clamp —
  `agent/session_notes.go:27-36`, pinned by `TestNormalizeNoteCollapsesWhitespaceAndClamps`);
  assert the *returned* value, never the request text (the store's
  clamp-then-compare is what every consumer converges on).
- **Step 3 (interrupt, exact)**: the next fakellm round after the set carries
  the note text. Falsify: `Applied` receipt, silent model — the steer was
  accepted and dropped.
- **Step 4 (panel, qualitative)**: Notes shows the live textarea; dirty blur
  saves after 10,000 ms and same-session refocus cancels it. Shared drafts
  survive panel close/reopen under the lifetime rules above. Falsify: missing
  body while `sharedNotes:true`, an inline save error while step 2 succeeds,
  duplicate requests, or an older acknowledgment overwriting a newer edit.
- **Step 5 (label, exact-in-browser)**: a `steering-item` divider labelled
  `System steered: Human note` whose body is the whiteboard text.
- **Step 6 (rejoin, exact)**: fresh `thread/read` carries the edited note on
  the envelope snapshot. Falsify: push has it, snapshot does not — I1 back.
- **Step 7 (growth, exact)**: `NOTES_CONTEXT` count ≤ rounds since the note
  was set, zero before it.

## Cleanup

```bash
scripts/e2e/e2e-webui-turn-controls.sh --stop "$run"   # kills fakellm + hub, removes the run dir
```

When the stack was hand-started instead of scripted: `thread/shutdown` the
session over `/rpc` (the old `/s/$SID/shutdown` form-POST shim is gone — it
404s silently and leaves the daemon up), then kill the hub by the PID you
captured and `rm -rf` your own run dir plus `$WORK`. Remove `$run` and
`$tmpdir` by name, never a `/tmp/evener-e2e-*` glob — a wildcard cleanup
deletes every other concurrent scenario's workdir too. Leave Jesse's real
`~/.evener` and `~/.local/state/evener` untouched; the isolated `$HOME`
above is what keeps this run's sessions out of them.

## Sharp edges

- **The web's note setter is not callable from `eval`.** `threadsStore` is a
  module-scoped zustand store; nothing is published on `window`. An `eval`
  that tries `window.EvenerAppwire.request("notes/human/set", …)` throws, and one that
  optional-chains it **fails open** — reporting "no note set" for what looks
  exactly like a real regression. Set the note through `/rpc` (step 2) for the
  exact assertion and through the Notes editor (step 4) for the UI one.
- **A human-note steer is `[data-testid="steering-item"]`, not
  `user-message-item`.** The divider is for *daemon*-originated steering
  (labelled `System steered: Human note`); a steer the human typed themselves
  reuses `UserMessageView` instead (`SteeringItem.tsx:148`). This is the
  mirror image of `web-steer-live-turn.md`'s Sharp edges — each card's
  selector is the other's falsification trap.
- **`expectedInstanceId` is the session id, not the ref.** It is the `local:`
  prefix stripped off (`e2e_control_without_turn_ids_test.go:19-25`
  `localInstanceIDForTestRef`); sending the full ref trips the fencing check
  and reads as a gate regression.
- **Frames carry no `jsonrpc` field.** `Message.UnmarshalJSON` rejects the
  frame outright (`appwire/jsonrpc.go:164-166`) and the socket drops — which
  reads as a network or auth problem, not a malformed request. Send bare
  `{id, method, params}` after `initialize`.
- **The `active` / `idle` vocabulary is canonical.** A running turn is
  `active`, never `processing` — `processing` is not a wire value at all, and
  `test/scenarios/scenario_docs_test.go` fails the build on any card that
  writes it. Poll `thread/read`'s `result.thread.status.type`.
- **Notes are whitespace-collapsed and 1000-rune-clamped, and the store
  dedupes equal saves.** Re-saving identical text is a no-op (no event, no
  steer — `TestSetHumanNoteNoOpOnEqualText`), so each step here uses fresh
  text; asserting a second steer for the same string measures the dedupe, not
  the path.
- **Retry converges; it does not duplicate.** Re-sending step 2 with the same
  `clientMutationId` and payload returns the recorded result, without another
  accepted notification or reverting a later note. Equal canonical text
  under a new ID is an applied no-op. A different payload under the same ID
  is rejected by the mutation contract; never mint a new ID to retry an
  ambiguous save.
- **Steering does not terminate the turn.** Like `turn/steer`, the note
  injects into the running loop — the turn keeps going and the model adapts
  on its next round. If the session reaches `ended`/`closed` right after step
  2, that is the failure, not the note.
- **The transcript is virtualized** (`VirtualList`, `panes/session/Session.tsx`),
  so a global divider count measures the viewport. Scope every query to the
  note's dividers (filter summaries on `Human note`), and use the doctor —
  not DOM counts — for the step-7 history bound.
- **The typed kind is part of atomic acceptance.** A bare `System steered`
  fallback for a consumed human-note entry is a regression, not tolerated
  label loss. The note and notification are accepted together; ordinary
  pending-steering delivery handles wake/recovery/consumption.
- **The pure store/steer logic is already unit-tested** —
  `TestSetHumanNoteStoresAndSteers` (`agent/session_notes_test.go:203`) pins
  the mutation id, the kind, and the text. If those pass and this card fails,
  the break is in the wiring (relay, gate, projection, or envelope), not the
  store.
