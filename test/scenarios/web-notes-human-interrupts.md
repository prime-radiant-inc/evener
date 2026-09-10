# web-notes-human-interrupts: atomic human-note saves interrupt, label the divider, and survive rejoin

**What this covers**: the shared human whiteboard through real AppWire, a
running session, the Notes panel, and fresh reads. `notes/human/set` durably
accepts the canonical note, one typed pending steering entry, and its receipt
in one `executeAtomic` effect (`agent/session_notes_rpc.go:SetHumanNote`). One
`clientMutationId` is used throughout acceptance, retry, receipt reconciliation,
and ordinary pending-steering delivery. The kind is persisted at acceptance,
not added after queuing. `Saved` means durable acceptance, not model consumption.

**Surface and contract**: `docs/developing-evener/agentic-testing.md`,
`docs/superpowers/specs/2026-09-10-shared-notes-simplify-design.md`,
`frontend/src/panes/session/chrome/NotesPanel.tsx`, and
`frontend/src/stores/humanNoteDrafts.ts` (frontend paths relative to
`cmd/evener-hub`). Use the Notes panel, not Details. Stores are module-scoped:
there is no `window` note setter. Drive the UI for editor proofs and `/rpc`
for exact wire assertions. This card is manual guidance, not a recorded run.

## Pre-state

- Build a real SPA and hub on an isolated HOME and kernel-assigned port using
  the setup checklist in `docs/developing-evener/agentic-testing.md`.
- No provider credential is needed for this plumbing scenario. Use
  `scripts/e2e/e2e-webui-turn-controls.sh --hold 30 --rounds 40`, follow its
  printed auth URL and spawn recipe, and use its workspace and model
  `fake/fake-test-model`. The script builds the SPA unless `--skip-web` is
  supplied. The standalone fake emits fixed harmless `read_file` rounds;
  it does not follow natural-language instructions to choose tools.
- Record the isolated run directory, HOME, hub port, token, workspace and SID.
  Never inspect or mutate personal session files. Observe provider request
  logs and wire events rather than guessing readiness from a fixed sleep.

## Steps

1. **[browser-free] Confirm capability and an active turn.** Spawn through
   `/new` using the scripted provider above. Read the ref from `/s/local:<SID>`.
   Connect to `ws://127.0.0.1:$PORT/rpc` with `Authorization: Bearer $TOKEN`;
   initialize with protocolVersion `evener-appwire-v2` (frames have no
   `jsonrpc` field). Call:
   ```json
   {"id":3,"method":"thread/read","params":{"ref":"local:<SID>","includeTurns":false}}
   ```
   Require `result.thread.status.type == "active"`, non-empty
   `result.thread.evener.activeTurnId`, and `capabilities.sharedNotes == true`.
   A missing capability is a prerequisite failure; the hub's `shared-notes`
   pre-flight gate must still refuse unsupported sessions.

2. **[browser-free] Save during the turn and prove retry identity.** Choose
   a unique opaque note sentinel and a fresh UUID, then call:
   ```json
   {"id":4,"method":"notes/human/set","params":{"ref":"local:<SID>",
    "clientMutationId":"<uuid>","expectedInstanceId":"<SID>","note":"opaque-note-A"}}
   ```
   Require an applied receipt and canonical `result.note`. Retain the exact
   request payload and ID. Replay them and require the recorded result with
   no second accepted notification. A replay must not revert a later save.
   A distinct ID with equal canonical text is an applied no-op without a new
   notes event or steer. Count typed steering entries in the authoritative
   transcript/journal, not the virtualized browser DOM. The mutation snapshot
   is note authority; metadata is only a projection. An active interrupt
   fence rejects a changed save before either effect commits; a held Stop
   gate parks accepted steering and is not released by saving.

3. **[browser-free] Prove delivery to the model boundary.** In the isolated
   run's `fakellm.log`, find this SID's next request carrying the opaque note
   sentinel. The real session loop must continue after the interrupt rather
   than terminate. A successful receipt alone is not this proof: it establishes
   acceptance, while a subsequent provider request establishes consumption.
   Match task data, not the wording of the surrounding generated prompt.

4. **[browser] Exercise shared draft and delayed blur saving.** Navigate to
   `$HUB/auth/<TOKEN>?next=/s/local:<SID>` and await
   `[data-testid="composer-input-card"]`. Open **Notes** (the session chrome
   button/menu, or `/notes` for the desktop workspace pane). Require
   `[data-testid="shared-notes-section"]`. The live editor is
   `[data-testid="shared-notes-editor"] textarea[aria-label="Human note"]`;
   read its `.value`, not `shared-notes-human` (that is ended-session prose).
   Record `location.port` and decoded `location.pathname` with DOM evidence.

   - Focus alone and unchanged blur must issue no request. Edit to a new
     opaque sentinel: there are no Edit/Save buttons. A dirty blur starts a
     **10,000 ms** timer only when no same-session editor remains focused.
     Observe the WebSocket request timeline: no request before the deadline;
     after the timer fires, await the request and acknowledgment, not a sleep.
   - Refocus any editor for the same session before expiry. Require cancellation
     (no save at the old deadline); blur again and require a new full 10,000 ms
     interval. Two mounted editors for this session share one text record and
     one timer; edits in one must appear in the other, without duplicate saves.
   - Close a focused editor without a blur: its unsaved draft survives reopen
     and closing must not invent a save. Distinguish this from a real blur
     before closing: an already-started timer survives panel unmount and saves
     normally. Record actual focus/blur events so the cases are unambiguous.
   - Await `[data-testid="shared-notes-saved"]`; saving may show
     `shared-notes-saving`, and a failure shows `shared-notes-error` inline.
     Compare the editor value with the server's canonical acknowledgment.
     Failed text survives panel close/reopen. An ambiguous request remains in
     the existing persisted mutation outbox with its original ID and payload
     for retry; an unsent draft is session-owned UI state, not a promise of
     browser-reload durability. If exercising a failure/retry, capture the
     actual error and original/retried IDs rather than assuming a new write.
   - A newer edit during an in-flight save must survive its older response or
     push. Blur that edit to schedule it normally. Clean drafts accept fresh
     authoritative notes even while focused; dirty drafts retain user text.
     If idle, `shared-notes-idle-wake` warns that saving wakes the agent.

5. **[browser + wire] Verify the typed divider.** After normal consumption,
   require `[data-testid="steering-item"]` with summary
   `System steered: Human note`. It is not a `user-message-item`.
   Cross-check the corresponding authoritative steering turn's kind
   `human-note` and note sentinel through `thread/read` with `includeTurns:true`
   or the isolated session's doctor transcript. A missing kind or fallback
   label is a regression, not an acceptable timing race. Do not infer kind
   from generated prose, and do not use a global DOM count for history.

6. **[browser-free] Rejoin and compare canonical authority.** Initialize a
   fresh socket and call `thread/read` with `includeTurns:true`. Require
   `result.thread.evener.humanNote` to equal the latest acknowledged note
   byte-for-byte before relying on a push. Reopen Notes and compare the clean
   editor as well. Include normal input, whitespace collapse, Unicode, a
   1000-rune boundary that ends in a space, and an explicit clear after a
   non-empty save. Compute the fixture expectations independently: 999 `x`
   characters followed by ` y` stores 999 `x` characters plus one space;
   do not trim the read or derive an oracle by re-normalizing the response.
   The permanent real-stack regression is
   `cmd/evener/serve_notes_projection_test.go:TestNotesCanonicalEnvelopeProjection`.

7. **[browser-free] Bound context growth.** Inspect the isolated session's
   transcript and provider-round records. At most one `NOTES_CONTEXT` turn
   may be appended per model round while notes/URLs are set, and none while
   the whole store is empty. Correlate round identity and opaque task data;
   do not pin generated prompt copy. Historical blocks remain history after
   a clear; only later context must reflect the current canonical state.

## Expected

Capability gates remain enforced; one changed save durably accepts one typed
notification under one mutation ID; the provider request proves consumption.
Notes uses shared drafts with 10,000 ms dirty-blur saving and refocus
cancellation. Canonical acknowledgments, fresh snapshots and clean editors
agree, including trailing space, Unicode and clear. Rejoin preserves state,
failed/newer drafts are not overwritten, typed labels are not race-tolerant,
and per-round context growth remains bounded.

## Cleanup

For the scripted stack, run:
```bash
scripts/e2e/e2e-webui-turn-controls.sh --stop "$run"
```
For a hand-started stack, shut down the session through `thread/shutdown` on
`/rpc`, stop only the processes recorded for this run, and reclaim only the
fixture-owned directories using the repository's scratch cleanup helpers.
Leave personal HOME/state and other scenarios untouched. Record checks not
performed (including any failure/retry variant) as unrun, not passed.
