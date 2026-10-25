# Issue #4243: one per-kind table for delegate journal events

## Scope and authorization

Resolve GitHub issue #4243 in `prime-radiant-inc/evener`: a delegate journal
event kind is a five-place hand edit, and a kind missing from `cloneEvent`
silently aliases a payload between an event and its clone with nothing to catch
it. Jesse authorized the work through the delegate brief; this lane implements
after spec PAR.

This isolated lane (`issue-4243-delegatestore-kind-table`) started at
`dc6c93e081`, fetched `origin/main`, and fast-forwarded to
`f6900f5d8ff3e436a474054a7211fff746d1f451` before writing this spec.

The change is limited to:

- `agent/internal/delegatestore/fold.go`
- `agent/internal/delegatestore/store.go`
- `agent/internal/delegatestore/envelope_fuzz_test.go`
- a new `agent/internal/delegatestore/envelope_kind_table_test.go`
- this spec

Non-goals: no new event kinds, no event-semantics, serialization, projection,
delivery-ordering or fold-behavior changes; no `touchedDelegates` refactor; no
`event.go` or `Event` struct change; no store-format or read-path change; no
changes outside `agent/internal/delegatestore`; no labels.

## Revision log

Spec PAR round 1 returned REVISE from both reviewers. Resolved in this revision:

1. Reuse `fuzz/aliascheck` (`Populate`, `FindSharedStorage`) for the clone
   isolation proof instead of a bespoke JSON-populate plus recursive walk. The
   package exists for exactly this bug and already backs the sibling
   `jobstore.cloneEvent` test (`agent/internal/jobstore/event_clone_fuzz_test.go`)
   and `agent/clone_aliasing_fuzz_test.go`.
2. Replace `payload func(*Event) any` with `present func(*Event) bool`, so the
   typed-nil-interface trap cannot make a missing payload count as present.
3. Enumerate `EventKind` constants from every non-test `.go` file in the
   package, not only `event.go`, matching the cited precedent.
4. Assert payload-field coverage in both directions: every `Event` payload
   field must be claimed by exactly one row, and every row must claim a
   declared kind's field. This closes "new payload field, no row".
5. Add an oracle for `apply` bindings (pairwise-distinct function identities)
   and state that the existing per-kind fold/apply tests cover a wrong binding.
6. Point the isolation test at the independently derived kind-to-field mapping,
   not the production row's accessor.
7. Extend the mutation proofs to a wrong-field row and a validation mutation,
   and add `go vet` and the fuzz/lint gates to verification.

Spec PAR round 2 approved both revisions. The one remaining minor was the
payload-field predicate below; the reviewers supplied its wording, so this
revision applies it without reopening the design.

Implementation note (after implementation PAR and simplify-code): the eight
flat-payload clone bodies share one `clonePointee` helper, and `cloneEvent`
clones into a caller-owned destination (`cloneEventInto`) so the journal
read/write paths do not allocate a fresh `Event` per clone. Both are
behavior-preserving; the table still drives every kind.

## The five hand-kept lists today

At `f6900f5d8f`, adding an `EventKind` means editing all of these by hand:

1. `fold.go:539-551` — the `payloads := []bool{...}` presence list in
   `validateEventEnvelope`.
2. `fold.go:559-584` — the kind switch that decides which payload matches in
   `validateEventEnvelope`.
3. `fold.go:159-186` — the kind switch in `applyEvent`.
4. `store.go:351-400` — the `if event.X != nil` chain in `cloneEvent`.
5. `envelope_fuzz_test.go:11-26` — the `envelopeKinds` kind/payload table the
   envelope fuzz test enumerates.

Two drift paths are open. `FuzzDelegateEventEnvelope` enumerates its own hand
list, so a kind added to the enum but forgotten there is simply not exercised.
A kind missing from `cloneEvent` is worse: nothing notices, and the returned
event shares its payload with the original, so a caller mutating one mutates
the other. Only `AttentionChanged` and `UpdatePosted` have hand-written clone
isolation tests (`store_test.go:17` and `store_test.go:522`); the other nine
kinds have no isolation check at all.

## Design

### One production table

Add to `fold.go`:

```go
// eventKindSpec is the single per-kind description of a delegate journal
// event: how to see whether its payload is attached, how to deep-clone that
// payload, and how to apply it.
type eventKindSpec struct {
	kind    EventKind
	present func(*Event) bool
	clone   func(dst, src *Event)
	apply   func(State, Event) error
}
```

`present` is a plain bool, not an `any`-returning accessor: in Go a nil
`*Payload` stored in `any` is a non-nil interface, and that spelling would make
`validateEventEnvelope` count a missing payload as present and accept a foreign
payload on a durable journal event. A bool cannot express that trap.

A single `eventKindSpecs` slice holds one row per kind, in `Event`'s
payload-field order. `eventKindSpecFor(kind)` does the lookup.

Three drivers replace the hand lists:

- `validateEventEnvelope` makes one pass over `eventKindSpecs`: it counts
  present payloads, and records whether the event's kind is known and whether
  that kind's own payload is present. It then returns the same three errors, in
  the same precedence, as today: empty delegate id, unknown kind, and
  `count != 1 || !matching`.
- `applyEvent` looks its kind up and calls `spec.apply`; an unknown kind keeps
  the existing error.
- `cloneEvent` loops over `eventKindSpecs` and calls every row's `clone`, each
  of which guards its own payload field. Every per-kind clone body moves
  verbatim from today's `if` chain, so no payload's clone behavior changes.

The mapping is preserved exactly:

| kind | payload | apply | clone body |
| --- | --- | --- | --- |
| `delegate_created` | `Created` | `applyCreated` | new `DelegateCreated`, descriptor through `cloneDescriptor` |
| `delegate_run_started` | `RunStarted` | `applyRunStarted` | struct copy |
| `delegate_terminal_prepared` | `TerminalPrepared` | `applyTerminalPrepared` | struct copy with `cloneTerminalPacket` |
| `delegate_run_finished` | `RunFinished` | `applyRunFinished` | struct copy with `cloneOutcome` and `cloneTerminalPacket` |
| `delegate_resumability_closed` | `ResumabilityClosed` | `applyResumabilityClosed` | struct copy |
| `delegate_subtree_stop_requested` | `SubtreeStopRequested` | `applySubtreeStopRequested` | struct copy |
| `delegate_subtree_stop_completed` | `SubtreeStopCompleted` | `applySubtreeStopCompleted` | struct copy |
| `delegate_delivery_acknowledged` | `DeliveryAcknowledged` | `applyDeliveryAcknowledged` | struct copy |
| `delegate_attention_changed` | `AttentionChanged` | `applyAttentionChanged` | struct copy |
| `delegate_update_posted` | `UpdatePosted` | `applyUpdatePosted` | struct copy |
| `delegate_intent_stated` | `IntentStated` | `applyIntentStated` | struct copy |

A new kind is now one row in the table, plus whatever new payload type and
apply function its semantics need. It is no longer an edit to five lists.

### Independent completeness net

The table cannot be checked against itself. The new test file derives its
expectations independently:

- **Kinds.** It parses every non-test `.go` file in this package with
  `os.ReadDir` + `go/parser` and collects the `EventKind`-typed string
  constants, the same shape as `agent/steering_kind_coverage_test.go`. Scanning
  the whole package, not one named file, means a kind declared in a new
  `event_*.go` file cannot hide.
- **Payload fields.** A payload field is an `Event` field whose type is a
  pointer; the four scalar fields `Kind`, `Seq`, `TS` and `DelegateID` are the
  only non-payload fields. The derivation filters by pointer type and never by
  whether the derived name is already a declared kind — the latter would hide a
  new payload field that has no kind, which is exactly the drift the set
  comparison must catch. Each payload field's JSON tag `x` names kind
  `delegate_x`; that convention holds for all eleven kinds today and becomes
  the documented table contract.

`TestEventKindTableComplete` then asserts:

1. the set of declared kinds, the set of kinds derived from `Event` payload
   fields, and the set of row kinds are equal (each with no duplicates);
2. every kind has exactly one row, and no row names an undeclared or
   field-less kind;
3. each row's `present` is true exactly when that kind's derived field is
   attached, and false for every other row's field (so one row cannot claim
   another's payload);
4. every row's `clone` and `apply` are non-nil, and the `apply` function
   identities are pairwise distinct (a copy-pasted row that forgets to change
   `apply` fails here; the existing per-kind fold/apply tests cover a wrong but
   distinct binding behaviorally).

A new kind with no row fails at (1). A new payload field with no kind and row
fails at (1) too, which is what keeps `validateEventEnvelope` counting every
payload. A row bound to the wrong payload fails at (3). A row missing clone or
apply handling fails at (4).

`TestCloneEventIsolatesEveryKind` drives one case per table row. The field it
populates, and the payload pointer it compares, both come from the
independently derived kind-to-field mapping, never from the row, so a misbound
row cannot make the test agree with itself. For each row it:

- allocates that payload, fills every settable pointer, slice and map with
  `aliascheck.Populate` (`fuzz/aliascheck`, depth 8), attaches it to a fresh
  `Event`;
- calls `cloneEvent` and asserts `reflect.DeepEqual` of the whole event;
- asserts `aliascheck.FindSharedStorage(reflect.ValueOf(original),
  reflect.ValueOf(clone), "Event")` is empty, which walks both values and
  rejects any shared pointer, non-empty slice backing array or map at any
  depth.

`aliascheck` exists for precisely the forgotten-field bug this issue describes,
leaves `time.Time`'s unexported internals alone, and is already the oracle for
the sibling `jobstore.cloneEvent` test. Reusing it removes a bespoke JSON
round-trip, a hand-rolled walker and their failure modes. A new row gets an
isolation case automatically: there is no per-kind hand-written test to
remember.

### Preserved fuzz envelope contract

`envelope_fuzz_test.go` keeps `FuzzDelegateEventEnvelope`'s cross product and
its seeds, but derives its kind/payload pairs from `Event`'s own payload fields
(JSON tag naming) rather than from `eventKindSpecs`. It remains an independent
oracle: if a table row claims the wrong payload, or a row is missing, the fuzz
expectation (accept only the kind's own payload) disagrees with
`validateEventEnvelope`. Field order matches the old table order, so the
in-file seed indices keep their current meaning; there is no saved
`testdata/fuzz/FuzzDelegateEventEnvelope` corpus to disturb.

The target's comment is rewritten: it no longer claims to catch a kind that has
no payload field (a field-less kind is invisible to a payload-derived
enumeration). `TestEventKindTableComplete` owns that case. Seed order now
follows `Event` field declaration order; a future reordering of `Event` is an
event-schema change that must review the in-file seeds, and the fuzz file's
comment says so.

## Behavior preservation

- No `Event`, `EventKind`, payload struct, JSON tag or store-format change.
- `validateEventEnvelope` accepts and rejects exactly the same events with the
  same errors and precedence.
- `applyEvent` calls the same per-kind function for every known kind and
  returns the same unknown-kind error.
- `cloneEvent` produces an independently mutable event with the same payload
  content per kind; nested mutable data stays deep-copied as today.
- Fold, apply, replay, projection revisions, delivery order and fuzz seeds are
  untouched.

## Non-goals

- No change to `touchedDelegates` or any fold semantics.
- No consolidation of payload validation (`validateDescriptor`,
  `validateTerminalPacket`, ...).
- No change to `read_events.go`, `read_page.go`, callers, or clients.
- No new event kind, and no change to `event.go`'s declarations.
- No unrelated journal refactor and no labels.

## Red-first and mutation proof

Baseline before any edit:
`cd agent && go test ./internal/delegatestore/ -count=1` passes (`ok ... 0.599s`).

The completeness test does not exist before the table, so it cannot detect a
missing clone branch against the hand lists. The red proof is therefore made
against the table with deliberate defects, each reverted and confirmed by
`git diff`:

1. A row whose `clone` is a no-op makes `TestCloneEventIsolatesEveryKind` fail
   with the aliased kind named; fill the row in and it passes.
2. A row whose `present` reads another kind's field makes
   `TestEventKindTableComplete` fail at assertion (3); restore and it passes.
3. A temporary extra `EventKind` constant in a new non-test package file (with
   no row and no payload field) makes `TestEventKindTableComplete` fail naming
   the kind; remove the file and it passes.
4. With a row's `present` temporarily bound to another field, a short
   `FuzzDelegateEventEnvelope` run fails because its derived expectation is
   independent; restore and it passes.
5. Against the old hand code, the two existing clone tests cover only two of
   eleven kinds and neither fails if another kind's clone branch is deleted.
   The committed table-driven case covers all eleven and does fail. This is
   demonstrated by proof 1 on the new table, not asserted against the old code.

The temporary mutations are removed before commit; `git status` and `git diff`
confirm no test kind or `event.go` change is committed.

## Verification

1. `cd agent && go test ./internal/delegatestore/ -count=1` — package suite.
2. `cd agent && go test -tags evenerfuzz -run '^$' ./internal/delegatestore/ -count=1` —
   tagged file compiles.
3. `cd agent && go test -tags evenerfuzz -run '^FuzzDelegateEventEnvelope$' -fuzz '^FuzzDelegateEventEnvelope$' -fuzztime=10s ./internal/delegatestore/ -count=1` —
   short local fuzz run.
4. `cd agent && go test . -run 'TestDelegate' -count=1` — root agent delegate
   journal tests that append and fold through this package.
5. `cd agent && go vet ./internal/delegatestore/`.
6. `make build`.
7. `gofmt -l` on the changed files (or `make lint-gofmt`).
8. `make lint-fuzz-registry` and `make lint-evenerfuzz` if their scope covers
   the edited fuzz file; the target name, signature and registry entry do not
   change, so they should report no drift.

Then: PAR on this frozen spec (two competing reviewers), implementation, PAR on
the implementation diff, simplify-code, commit, RoboRev branch review, push, PR
against `main`. CI owns the full suite.

## Risks and open questions

- The `delegate_` + JSON-tag convention is the completeness test's contract. A
  future kind that breaks it fails the test by name; the fix is to name the
  field consistently, not to weaken the check.
- The `apply` binding oracle is pairwise distinctness, not name matching. A
  deliberately swapped pair of distinct bindings is behaviorally caught by the
  existing per-kind fold/apply tests; name matching would pin implementation
  names the repo does not otherwise require.
- `aliascheck.Populate` leaves scalar fields zero, which is fine: the isolation
  proof is about mutable storage, and every pointer, slice and map in the
  payload types is populated. If a future payload type holds mutable storage
  only reachable through an unexported field, that type owns its own internals
  and `aliascheck` deliberately skips it; the production clone must still copy
  it, and the type's own tests are the right place to prove that.
