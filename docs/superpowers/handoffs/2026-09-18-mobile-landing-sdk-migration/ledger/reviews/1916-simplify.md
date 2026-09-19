# /simplify — PR #1916 (D25d-1a: native MutationOutboxStorage write path over expo-sqlite)

Head reviewed: `8d7732de2baa376378c94d627dbd8a5b895044da`, own diff vs `origin/main` (`ead2caba7`).
Quality only — reuse, simplification, efficiency, altitude. Correctness is RoboRev's. Nothing below
changes what the phone does (ruling 4 stands: the phone adopts the web's durable queueing, and the
oracle for every contract here is `cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.ts`).

Files: `mobile-native/src/mutationOutboxStorage.ts` (382 new lines),
`mobile-native/src/mutationOutboxStorage.test.ts` (364 new lines).

---

## 1. `replace` re-copies `insertNew`'s whole INSERT, and both column lists are order-coupled to `insertValues` — must-fix-before-merge

`mobile-native/src/mutationOutboxStorage.ts:299-331`.

`insertNew` and `replace` each spell out the same 16 column names and the same 16 `?`
placeholders; `replace` then spells 14 of them a third time as `x = excluded.x`. Both are fed by
`insertValues` (`:333-355`), whose array order must match those column lists positionally.

Concrete cost: a column added to `Row` has to be edited in five places that no compiler relates to
each other — `Row`, the `schema` template, `insertValues`, `insertNew`'s column list, `replace`'s
column list *and* its `DO UPDATE SET` clause. Get the order wrong in one of them and SQLite happily
writes `payload` into `attachments`: a silent data corruption with no type error and no test that
would catch it (the tests assert `payload` for two records only).

Simpler form — one column list, both statements derived from it:

```ts
const COLUMNS = [
	"client_mutation_id", "version", "origin_client_id", "target_ref", "thread_id", "method",
	"payload", "attachments", "optimistic_display", "composer_text", "intent_sequence",
	"created_at", "state", "attempted", "recovery_kind", "recovery_reason",
] as const;

const insertSQL = (table: string) =>
	`INSERT INTO ${table} (${COLUMNS.join(", ")}) VALUES (${COLUMNS.map(() => "?").join(", ")})`;
const replaceSQL = (table: string) =>
	`${insertSQL(table)} ON CONFLICT (client_mutation_id) DO UPDATE SET ${COLUMNS.slice(1)
		.map((column) => `${column} = excluded.${column}`)
		.join(", ")}`;
```

`insertValues` then reads as the value row for `COLUMNS` and the ordering coupling has exactly one
place to be wrong instead of three. Roughly 20 lines of literal SQL become three derived strings,
and the two methods keep their names and their distinct conflict policies (which is the right
distinction — the comments at `:292-298` and `:309-314` are worth keeping verbatim).

Rated must-fix because `replace` duplicates a helper that exists three lines above it, in this PR's
own new code, and the duplication is the order-coupled kind.

## 2. `insertValues`'s `"state" in record` ternary has a dead branch — must-fix-before-merge

`mobile-native/src/mutationOutboxStorage.ts:350`:

```ts
"state" in record ? record.state : "accepted",
```

All three members of the parameter union declare `state`:
`MutationOutboxRecord.state: MutationOutboxState`, `MutationOptimisticRecord.state: "accepted"`,
`MutationRecoveryRecord` inherits the outbox one
(`appwire-client/typescript/state/mutation/records.ts:70-90`). So `"state" in record` is statically
always true and the `"accepted"` fallback is unreachable.

Concrete cost: a reader has to go check whether some record type really can arrive without a state —
and the dead literal reads as if `"accepted"` were a default this layer is entitled to invent, which
is exactly the thing the surrounding design says it must not do (the comment at `:77-80`: the table
IS the state).

Simpler form: `record.state,`. It typechecks directly on the union. The sibling
`"attempted" in record` guard on the next line is *not* dead — `MutationOptimisticRecord` has no
`attempted` — so it stays.

## 3. `settleReceipt` materializes three full records where two are existence probes — follow-up

`mobile-native/src/mutationOutboxStorage.ts:223-227`.

Three `get<>()` calls, each a `SELECT *` plus a full `fromRow` — and `fromRow` runs three
`JSON.parse`es per row (`:110-112`). Only the first one that exists is used as `source`; the other
two are read solely so `:256`, `:259` and `:260` can ask "was there a row?". So a settle that finds
its record in the outbox still parses the optimistic and recovery rows' payload, attachments and
display JSON and throws them away — up to six wasted `JSON.parse`es per receipt, on the phone's UI
thread (this port is synchronous).

Simpler form: one `exists(table, id)` probe (`SELECT 1 FROM ${table} WHERE client_mutation_id = ?`,
reusing `getFirstSync`) for the two booleans, and `get()` only for whichever table is the source:

```ts
const inOutbox = this.exists(TABLES.outbox, clientMutationId);
const inRecovery = this.exists(TABLES.recovery, clientMutationId);
const inOptimistic = this.exists(TABLES.optimistic, clientMutationId);
const sourceTable = inOutbox ? TABLES.outbox : inRecovery ? TABLES.recovery : inOptimistic ? TABLES.optimistic : undefined;
if (!sourceTable) return false;
const source = this.get<MutationRecord<A>>(sourceTable, clientMutationId);
```

Same `outbox ?? recovery ?? optimistic` precedence, same booleans, one row materialized instead of
three. Follow-up rather than must-fix: it is an efficiency/clarity win, not duplication, and the
current form is a faithful transcription of the oracle's three `get`s (which the web needs because
IndexedDB has no cheap existence probe — SQLite does).

## 4. The savepoint helper is a fourth encoding of a pattern already inlined three times — follow-up

`mobile-native/src/mutationOutboxStorage.ts:371-381` vs `mobile-native/src/draftRepository.ts:189`,
`:249-251` and `mobile-native/src/creationDraftRepository.ts:86,129-131` and `:139,146-148`.

The new `transaction(name, body)` is the right shape — it is the generalization the three existing
sites should have had. But it lands in a third file, so the app now carries four hand-written
`SAVEPOINT` / `RELEASE` / `ROLLBACK TO ...; RELEASE ...` sequences, three of which still get the
rollback string wrong-by-one-typo away from silently leaving a savepoint open.

Concrete cost: the next sqlite repository copies whichever one it sees first; and the existing three
never gain the `try/catch` rigor this one has.

Simpler form: `mobile-native/src/sqliteSavepoint.ts` exporting
`withSavepoint<T>(db: {execSync(sql: string): void}, name: string, body: () => T): T` (the body of
`:371-381` unchanged), with `MutationOutboxSQLite.transaction`, `DraftRepository.write`,
`CreationDraftRepository.write` and `.clear` all calling it. Follow-up, not must-fix: the pattern
exists inline three times, not as a helper, so this PR is not bypassing an available function — and
rewriting three files it does not otherwise touch is out of scope for a row already stacked.

## 5. Two sqlite ports and a tenth copy of the same node:sqlite test double — follow-up

`mobile-native/src/mutationOutboxStorage.ts:57-62` vs `mobile-native/src/draftRepository.ts:24-28`;
`mobile-native/src/mutationOutboxStorage.test.ts:35-43` vs
`mobile-native/src/draftRepository.test.ts:14-19` (and eight more —
`git grep -n "execSync: (sql)" -- mobile-native` returns ten hits across
`creationDraftRepository.test.ts`, `draftDocument.test.ts`, `draftLibrary.test.ts`,
`draftRepository.test.ts`, `forkActions.test.ts`, `goalCommand.test.ts` ×3, `imageSelection.test.ts`
and now this file).

`MutationOutboxDatabase` is `DraftDatabase` plus `getAllSync` with widened param/return types, so the
app describes the same expo-sqlite surface twice; and every suite that needs a real engine
re-derives the same four-line `DatabaseSync` → port adapter.

Concrete cost: `getAllSync` exists on only one of the two ports, so the next repository that needs a
multi-row read picks a port by accident; and a change to the adapter shape (a fifth method, a
`prepare` cache) is a ten-file edit.

Simpler form: one `SqliteSync` port in `mobile-native/src` with the four methods at the widest
useful param types, both repositories taking it, and one
`mobile-native/src/testing/sqliteDatabase.ts` exporting
`openSqlite(path): {database: DatabaseSync; port: SqliteSync}` that the ten suites call. Follow-up
and worth its own GitHub issue: the duplication is pre-existing and repo-wide, and this PR is
following the house pattern rather than inventing a new one.

## 6. The test's `intent()` builder duplicates the web's, and the package's `testing.ts` is where it belongs — follow-up

`mobile-native/src/mutationOutboxStorage.test.ts:72-81` vs
`cmd/evener-hub/frontend/src/stores/mutationOutbox.test.ts:25-40`.

Same `TARGET = "local:thread-1"`, same `threadId`, same `method: "turn/queue"`, same
`payload: {ref, input: [{type: "text", text}]}` — the web's carries an extra `expectedTurnId` and
that is the whole difference. Meanwhile
`appwire-client/typescript/state/mutation/testing.ts:49-67` already hosts exactly this class of
builder (`outboxRecord`, `recoveryRecord`) for the package's own suites and has no intent builder.

Concrete cost: two hosts' suites drift on what a representative intent looks like, which is the same
drift that makes an oracle comparison unfalsifiable a round later.

Simpler form: add `export function mutationIntent(overrides: Partial<MutationIntent> = {}): MutationIntent`
to `state/mutation/testing.ts` beside `outboxRecord`, and have both suites call it with their own
overrides. Follow-up: it touches the web's suite, and `testing.ts`'s export surface is a package
decision.

## 7. Altitude: the accepted-record whitelist and the "carries input" predicate are the same rule written twice, in two encodings — follow-up

`mobile-native/src/mutationOutboxStorage.ts:231-254` vs
`cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.ts:190-214`.

Two host-free decisions are duplicated verbatim-in-intent across the adapters:

- the predicate (`projectionState === "pending"` and the display is an object with an array
  `input`), written here as `Array.isArray((display as { input?: unknown }).input)` and on the web as
  `"input" in display && Array.isArray(display.input)` — two encodings of one rule, which is the
  shape that keeps coming back in review;
- the twelve-field explicit `accepted` record, whose *whole point* is that it is not a spread
  (`:238-240` and the web's `:201-204` both carry a comment saying so).

Neither touches IndexedDB or SQLite. Both are pure functions of a `MutationRecord`, and the web's
record types are the package's parameterized by its `Blob`-carrying attachment
(`cmd/evener-hub/frontend/src/stores/mutationOutbox.ts:21-26`), so one package function typechecks
for both hosts.

Concrete cost: the next change to what an accepted record carries (a field added to
`MutationRecord`) is correct in one adapter and silently wrong in the other — the leak the comments
are there to prevent, reintroduced by the copy.

Simpler form: in `appwire-client/typescript/state/mutation/records.ts`:

```ts
export function carriesOptimisticInput(projectionState: string, display: unknown): boolean;
export function acceptedRecord<A extends MutationAttachmentRef>(source: MutationRecord<A>): MutationOptimisticRecord<A>;
```

with both adapters calling them. Follow-up rather than must-fix: it edits the web adapter, so it is
its own row — but it is the most valuable follow-up on this PR and should be filed rather than
journaled.

## 8. Over-general surface: `fromRow`'s second generic, the module's exports, and `protected` with no subclass — follow-up

`mobile-native/src/mutationOutboxStorage.ts:102-120`, `:81`, `:126-127`, `:299-381`.

- `fromRow<A, T extends MutationRecord<A>>(row): T` ends in `as unknown as T`, so `T` does no type
  work at all: it is a caller-chosen cast. `get<T>` (`:357-360`) and `list<T>` (1b) already do that
  cast at the boundary. Simpler: `fromRow<A>(row: Row): MutationRecord<A>` returning the widest
  shape, with the one unchecked cast staying in `get`/`list`. The `row.version as 1` at `:104` is
  redundant under the outer cast and goes with it.
- `TABLES` (`:81`) and `fromRow` (`:102`) are `export`ed but referenced only inside this module
  (`git grep` for both over `mobile-native/src appwire-client/typescript cmd/evener-hub/frontend/src`
  hits this file only; the test imports `MutationOutboxSQLite`, `MutationOutboxDatabase` and `Row`).
  Module-private is the honest visibility.
- `insertNew` / `replace` / `insertValues` / `get` / `delete` / `transaction` are `protected` with no
  subclass anywhere, in a class that otherwise uses `#` for its fields. `#` for these too, matching
  the fields. Same for the public `readonly db` (`:127`) — no caller reads it, including the tests.

Concrete cost: each of these invites a use that does not exist, and the `protected`/`#` split inside
one class reads as if subclassing were planned.

Follow-up: all mechanical, none of it dead in the strict sense (each member has an in-module caller).

## Not findings — checked and clean

- **Table-scoped accessors.** The brief asked whether `insertNew`/`replace` and the three-table
  lookups could collapse into one table-scoped accessor. They already have: every one takes `table`
  as its first parameter, the state machine is the table membership (`:77-80`), and `settleApplied`
  (`:267-275`) loops `Object.values(TABLES)` instead of writing three deletes. Wrapping that in a
  `table(TABLES.outbox)` accessor object would add ceremony, not remove it.
- **Row ↔ record codec.** The package has no codec for these records to reuse — `records.ts` is
  shapes and the provenance rule only, and the web's adapter stores structured clones with no
  serialization step. `fromRow`/`insertValues` are genuinely this adapter's own.
- **`settleApplied`'s three unconditional deletes** are both simpler and cheaper than the oracle's
  three gets plus conditional deletes. Right call.
- **`SecureRandomSource` injection** goes through the package's `createSecureUUID` rather than
  naming a host global, and the test at `:138-151` falsifies that by deleting `globalThis.crypto`.
  That is the `appwire-client-never-names-a-host-global` rule honored on the adapter side, with
  evidence.
- **`changedRows`** (`:49-51`) earns its place: `number | bigint` is the real difference between
  `node:sqlite`'s `run()` and expo-sqlite's `runSync`, and deciding a write's boolean from
  `sqlite3_changes64()` removes the oracle's preceding `get` from four methods.

---

## Verdict: fix before merge (2 must-fix, 6 follow-up)

Fix before merge, on two small things. The write path is well-shaped work — the table-is-the-state
design removes a state machine the oracle does not have, `changedRows` removes four read-then-write
pairs, and the savepoint helper and injected `SecureRandomSource` are both the right generalizations
— but two items should not land as they are. `replace` re-copies `insertNew`'s entire 16-column
INSERT and then a third copy as `excluded.x` assignments, all three positionally coupled to
`insertValues`'s array with nothing relating them, so a column added to `Row` has five places to be
edited and one silent data-corruption failure mode; deriving both statements from a single `COLUMNS`
constant is about twenty lines smaller and leaves one place to be wrong (finding 1). And
`insertValues`'s `"state" in record ? record.state : "accepted"` has a statically unreachable branch
that reads as a default this layer is not entitled to invent (finding 2). The six follow-ups are all
real but none belongs in this PR: three are pre-existing repo-wide duplications this PR joins rather
than creates (the fourth inline savepoint, the second sqlite port, the tenth node:sqlite test
double), one is an efficiency win worth taking when the read path settles (`settleReceipt`
materializing three records to use one), one is test-builder drift against the web, and the last —
lifting the accepted-record whitelist and the "carries input" predicate into
`state/mutation/records.ts` so the two adapters stop encoding one rule two ways — is the most
valuable of them and should be filed as an issue rather than journaled.
