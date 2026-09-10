# Shared Notes Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver atomic human-note save-and-notify with a shared editor that saves after a cancellable 10-second blur delay.

**Architecture:** Extend the existing client-mutation snapshot and outbox. One atomic effect owns the note, typed notification, and receipt; one session draft controller owns editing and delayed submission. Keep agent notes and URLs in metadata with correct transactional locking.

**Tech Stack:** Go, afero filesystem seams, AppWire, React, Zustand, IndexedDB mutation outbox, Vitest fake clocks.

**Spec:** `docs/superpowers/specs/2026-09-10-shared-notes-simplify-design.md`

## Global Constraints

- Dirty blur starts a 10,000 ms timer if no editor for the session remains focused.
- Any editor for the same session gaining focus cancels that timer.
- The reservation snapshot must not change the note or enqueue notification.
- An active interrupt fence rejects a changed save before either effect commits.
- Stop's existing steering-held gate remains authoritative: accepted notification stays parked, and saving does not release Stop.
- Acknowledgment clears only the submitted generation. Older responses and pushes cannot overwrite newer or failed text.
- Failed drafts survive panel close/reopen. Ambiguous requests retain their original identity for retry.
- Jesse approved a clean cutover: no migration or backward-compatibility layer. Preserve new-format notes across restore, including explicit clears.
- Normalize once on the server using the existing whitespace and Unicode limits.
- Read `docs/developing-evener/testing.md` before changing tests. Use real product code with external filesystem, clock, RPC, or provider seams; no internal behavior mocks.
- Scope is PR #1070. Commit named paths and push normally to `origin/shared-notes`; do not merge into main.

---

### Task 1: Atomic human-note acceptance and read authority

**Files:**
- Modify: `agent/session_notes_rpc.go`, `agent/session_notes.go`, `agent/session_client_mutation.go`, `agent/session_client_mutation_queue.go`, `agent/session_init.go`, `agent/session_state.go`, `agent/session.go`.
- Modify corresponding session metadata definitions/readers under `agent/schema/`, server notes handlers under `server/`, and past-session reader `cmd/evener-hub/app_threadread.go`.
- Modify callback types and serving wiring in `server/server.go`, `server/appwire_runtime.go`, and `cmd/evener/serve.go`; return the full response rather than reconstructing Note alone.
- Modify: `appwire/types.go`, relevant receipt/method classifiers under `appwire/` and `internal/appprojector/`.
- Generate: `docs/appwire-protocol.md`, `cmd/evener-hub/frontend/src/protocol/types.gen.ts` with `go generate ./appwire/...`.
- Test: create `agent/session_notes_atomic_test.go`; adapt existing notes, restore, mutation-journal, server, projector, and hub tests whose contracts this task changes. Do not remove unrelated assertions.

**Interfaces:**
- Consume `clientMutationStore.executeAtomic(request, prepare, effect)`, `addPendingSteering`, `reflectDurableClientSteering`, `wakeForPendingSteering`.
- Produce the existing `notes/human/set` method with the unchanged request fields and a response containing `Note string` (`note`) and `Receipt appwire.MutationReceipt` (`receipt`).
- Change `Session.SetHumanNote(clientMutationID, note string)` to return `(appwire.NotesHumanSetResponse, error)` and update its actual callers; do not add a wrapper solely to preserve tests.
- Canonical human-note state lives in `clientMutationSnapshot`. Ensure schema cloning/validation and read-only projection distinguish absent authority from an explicitly empty note. Jesse confirmed no running instance has notes and approved a clean cutover without migration.
- Existing metadata fields are in `agent/schema/snapshot.go`; the mutation loader in `agent/session_client_mutation_persist.go` rejects unknown fields. Remove obsolete notes delivery fields under the approved cutover, retaining strict decoding and unrelated journal fields. Do not reset or alter real journals.

- [x] **Write and run the first behavioral red test.** This catches storage committed through a refused notification. The existing fixture creates real session code; the store mutation seeds a durable fence.

```go
func TestHumanNoteFenceRejectsWholeSave(t *testing.T) {
    s := newNotesToolSession(t)
    s.stateDir = t.TempDir()
    if err := s.ensureClientMutationStore(); err != nil { t.Fatal(err) }
    if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
        snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "stop"}
        return nil
    }); err != nil { t.Fatal(err) }
    if _, err := s.SetHumanNote("save", "sentinel"); err == nil {
        t.Fatal("save crossed the interrupt fence")
    }
    human, _ := s.notesSnapshot()
    if human != "" { t.Fatalf("refused save changed note to %q", human) }
    if n := len(s.clientMutations.snapshot().SteeringOrder); n != 0 {
        t.Fatalf("refused save queued %d notifications", n)
    }
}
```

Run `GOMAXPROCS=4 go test -p 4 ./agent -run '^TestHumanNoteFenceRejectsWholeSave$' -count=1`. Expect an assertion failure because the old implementation stores the refused note.

- [x] **Replace the split operation with the existing atomic transition.** Build a request from the raw note so same-ID/different-payload detection remains exact. In the effect, normalize, check current canonical value and fence, reserve normal steering identity only for a changed accepted note, set `SteeringKind` before `addPendingSteering`, and marshal/apply one response. No nested `clientMutationSteer` call. Applied no-ops have no pending execution. On replay return the recorded Note and refresh receipt disposition/projection from the record, reflect/wake accepted pending work normally. Return normalized structured mutation errors.

```go
type NotesHumanSetResponse struct {
    Note    string          `json:"note"`
    Receipt MutationReceipt `json:"receipt"`
}
```

- [x] **Add red/green durability tests one boundary at a time.** Use `clientMutationFaults.AfterReservation`, `BeforeEffectSnapshotRename`, and `AfterEffectSnapshotRename` with the real filesystem snapshot loader. Assert old note/no steer before effect, and new note/one typed steer after effect even when the call reports uncertainty. Retry with the same ID after restore and after a newer note: assert the recorded response, current latest note, and no duplicate steering. Two different IDs with the same canonical note produce one notification. Reject same-ID/different-payload reuse. Check recovery's effect-time fence because prepare is skipped.

- [x] **Project committed authority through every read.** Live `Meta`, notes snapshots/context, restore, thread snapshot, and past-session reads must expose the same committed note. Add independent fixture-file tests for a saved nonempty note and a saved clear with stale nonempty metadata. Remove old notes-specific intents, adoption, tombstones, annotation-after-acceptance, and their recovery calls after equivalent new-format durable guarantees are covered. Do not add legacy import or delivery conversion.

- [x] **Preserve standard delivery behavior.** Test a held queue accepts note+typed notification while staying held; replay does not unpark it. Test receipt projection/consumption through normal steering delivery and no-op reconciliation. Update server/hub response forwarding and generated protocol declarations. Existing non-notes queue/start/stop contracts must remain unchanged.

- [x] **Run focused checks and commit.**

```sh
go generate ./appwire/...
GOMAXPROCS=4 go test -p 4 ./agent ./server ./internal/appprojector ./appwire -run '(Notes|Note|Adopted|ClientMutation|SteeringHeld)' -count=1
(cd cmd/evener-hub && GOMAXPROCS=4 go test -p 4 . -run '(Notes|Note|ThreadRead)' -count=1)
```

Require zero exit codes. Stage only paths actually changed for this task; commit intent: `refactor(notes): accept human note and notification atomically`. Report the complete staged list, commit, red/green evidence, remaining uncertainties, and the exact new read-projection API.

### Task 2: Metadata transactions and literal file paths

**Files:**
- Modify: `agent/session_tools_notes.go`, URL-removal and metadata helpers in `agent/session_notes_rpc.go`, URL parsing in `agent/session_notes.go`.
- Test: corresponding existing notes tool/RPC tests and `agent/session_notes_adversarial_test.go`.

**Interfaces:** Keep existing agent-note and URL tool/RPC contracts. Hold `notesUpdateMu` for event ordering and acquire `metaSaveMu` before tentative metadata mutation; keep it through persistence and rollback. File-URL parsing passes a decoded path to `canonicalFilePath`; bare paths pass their literal spelling.

- [x] **Write and run the literal-percent regression.** This catches decoding a bare filename as URL syntax; the standard-library URL constructor is the independent reference.

```go
func TestBarePercentFilenamePreserved(t *testing.T) {
    root := t.TempDir()
    path := filepath.Join(root, "report%23final.md")
    if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil { t.Fatal(err) }
    want := (&url.URL{Scheme: "file", Path: path}).String()
    control, err := canonicalSessionURL(want, root)
    if err != nil || control != want { t.Fatalf("file URL control: %q, %v", control, err) }
    got, err := canonicalSessionURL("report%23final.md", root)
    if err != nil || got != want { t.Fatalf("bare path: %q, %v; want %q", got, err, want) }
}
```

Run `GOMAXPROCS=4 go test -p 4 ./agent -run '^TestBarePercentFilenamePreserved$' -count=1`; expect the old code to identify the wrong path.

- [x] **Fix decoding at the boundary.** Use `parsed.Path` from `url.Parse` for a file URL and remove `url.PathUnescape` from `canonicalFilePath`. The existing scope check still receives the decoded file-URL path before traversal validation. Add encoded traversal and literal `%2F`/`%25` controls to existing canonicalization coverage.

- [x] **Write red tests for metadata save failures racing autosave.** Use a filesystem Rename seam and channels/locks to control the real save boundaries. Assert independently loaded metadata matches the rolled-back live agent note or URL list, and rejected changes produce no success event. Move the metadata lock ahead of mutation, preserving existing serializer order. Keep mutation, persist, rollback and release in one path; do not add a second journal for agent-note writes.

- [x] **Run checks and commit.**

```sh
GOMAXPROCS=4 go test -p 4 ./agent -run '(Notes|Note|URL|Url|Canonical|BarePercent)' -count=1
GOMAXPROCS=4 go test -race -p 4 ./agent -run '(Notes|Note|URL|Url|BarePercent)' -count=1
```

Require zero exits. Stage named files only; commit intent: `fix(notes): serialize metadata writes and preserve literal paths`.

### Task 3: Shared draft controller and delayed-blur outbox submission

**Files:**
- Create: `cmd/evener-hub/frontend/src/stores/humanNoteDrafts.ts` and `humanNoteDrafts.test.ts`.
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts`, `mutationDispatcher.ts`, relevant `mutationOutbox*` types/storage/tests, and `panes/session/chrome/NotesPanel.tsx` plus its tests.
- Update existing notes store tests, fixtures, and user-facing notes documentation for the new delay. Do not redesign panel layout.

**Interfaces:**
- Consume `notes/human/set` response `{ note: string; receipt: MutationReceipt }` from Task 1.
- Produce a session-owned draft store/controller with `edit`, `focus`, `blur`, `unmount`, authoritative `sync`, and receipt/error reconciliation actions. Components identify focus owners separately but share text, edit generation, save status, and timer by session ref.
- Keep mutation transport and IndexedDB persistence in existing threads/outbox facilities. Extend dispatcher supported methods and add a typed notes response callback carrying target, mutation identity, and canonical note. Requests keep raw submitted text and existing expected-instance fencing.
- `enqueueMutationIntent` should return its committed `MutationOutboxRecord`; enqueue completion is not acknowledgment. Bind generation to identity before dispatch can return a fast result. Reuse `subscribeMutationPersistence` and `readMutationPersistence` for blocked/rejected/rejoined state.
- Last-pane release can remove the authoritative model before the deadline. Retain or reacquire the scheduled save's session subscription without changing its original instance fence. Route notes recovery to the notes editor rather than the composer recovery UI.

- [x] **Write the first delayed-blur red test using the real component/store and external FakeClient.** Seed a writable live thread, install the existing IndexedDB test seam required by the outbox, use `vi.useFakeTimers()` and `userEvent.setup({ advanceTimers: vi.advanceTimersByTime })`. Script a complete Note+Receipt response. Edit, blur, then use these assertions:

```tsx
await act(async () => { await vi.advanceTimersByTimeAsync(9_999); });
expect(seen).toHaveLength(0);
await act(async () => { await vi.advanceTimersByTimeAsync(1); });
await submitted;
expect(seen).toHaveLength(1);
expect(seen[0]).toMatchObject({ ref: model.ref, note: "draft sentinel" });
```

`submitted` is an explicit promise resolved by the external FakeClient handler, not a timeout. Use fake timers limited to `setTimeout`, `clearTimeout`, `setInterval`, and `clearInterval`, leaving IndexedDB scheduling real. Initialize the existing `IDBFactory` and `setMutationStorageForTests` seam before the runtime starts. Before changing production, run `npx vitest run src/panes/session/chrome/NotesPanel.test.tsx --maxWorkers=4` and observe the immediate old blur-save fail the no-request assertion.

- [x] **Implement shared draft and one timer per session.** Increment edit generation only for actual input. A clean incoming note may replace displayed text even while focused. Any focus owner cancels scheduling; a last-owner dirty blur schedules 10,000 ms. Timer execution reads current text, generation, liveness, capability, and instance from shared state. Submit through the existing outbox, then reconcile acknowledgments by submitted generation. Do not create a panel-local FIFO, outcome map, normalization helper, or unmount flush.

- [x] **Add red/green lifecycle tests.** Cover refocus before deadline, two panels sharing text and timer, focus without edits plus remote update, real blur then unmount still saving, close-without-blur retaining text without saving, and capability/liveness loss before timer fire. Assert failed text survives close/reopen. Use the subscribed real store for pushes, not rerender alone for multi-panel races.

- [x] **Add red/green in-flight and recovery tests.** Stored A, save B, revert to A before B succeeds must eventually save A after its own blur delay. Older B acknowledgment followed by C rejection must retain C. An ambiguous send retries the same persisted ID and payload after reconnect; definite refusal retains recoverable draft/error. Rejoin and receipt reconciliation settle notes without fabricating a user chat item or deleting an unacknowledged edit. Read persisted outbox records independently for retry assertions.

- [x] **Remove redundant machinery and run checks.** Remove the NotesPanel local queues/snapshot/outcome collections and duplicate normalization. Remove URL generation bookkeeping that has identical return branches. Keep agent-note rendering and URL behavior unchanged. Before the frontend gate, format only changed `src/` files with `npx biome check --write`.

```sh
cd cmd/evener-hub/frontend
npx vitest run src/stores/humanNoteDrafts.test.ts src/panes/session/chrome/NotesPanel.test.tsx src/stores/mutationDispatcher.test.ts --maxWorkers=4
cd ../../..
make test-web
```

Require zero exits. Stage named changed files only; commit intent: `refactor(notes): share drafts and debounce blur saves`.

### Task 4: Independent review and PR delivery

**Files:** Update this plan's checklist and the SDD progress ledger. Any production fixes require their own failing regression and a scoped re-review.

**Interfaces:** Review uses this spec, each task's report and complete commit-range diff. Delivery updates the existing PR branch.

- [ ] Review spec compliance and code quality after each implementation task. Run a final whole-branch review for cross-boundary failure/rejoin, notification ownership, draft lifetime, and preserved capability/URL behavior. Resolve load-bearing findings before delivery.
- [ ] Run the required gates on the integrated tree:

```sh
make merge-approval-gate
make vet
make test-web-browser
```

The merge gate includes lint, build, and `ROOT_FULL=1 make test`. Install dependencies first. Read all output, investigate every failure, and report environmental blocks as incomplete checks.

- [ ] Verify final changed paths, clean worktree, generated output, commit range, and current remote `shared-notes` ref. Fetch only that ref with `--no-tags`. If it moved, block overlapping changes and explicitly integrate after ref/branch preflight rather than force-pushing. With unchanged ancestry, push `HEAD:shared-notes` to origin normally.
- [ ] Report commits, PR #1070, backend/frontend behavior delivered, gate results, review disposition, dependency warning, and retained review-worktree cleanup limitation. Do not claim merge approval or merge the PR.
