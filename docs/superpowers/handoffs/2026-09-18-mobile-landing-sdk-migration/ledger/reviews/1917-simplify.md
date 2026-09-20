# /simplify — PR #1917 (D25d-1b: native MutationOutboxStorage read path, stacked on #1916)

Head reviewed: `ee0013dc819e945dc878df6f3812b67ee680488f`, own diff vs `pr-1916-review`
(`8d7732de2`); base `origin/main` (`ead2caba7`). Quality only — reuse, simplification, efficiency,
altitude. Correctness is RoboRev's. Nothing below changes what the phone does (ruling 4 stands).

Own diff: `mobile-native/src/mutationOutboxStorage.ts` +95/−21 (seven read methods, the `list`
helper, comment rewrites), `mobile-native/src/mutationOutboxStorage.test.ts` +115/−7 (six read-path
tests). Findings that belong to the write path are in `1916-simplify.md` and are not repeated here.

---

## 1. The class still does not declare `implements MutationOutboxStorage<A>` — must-fix-before-merge

`mobile-native/src/mutationOutboxStorage.ts:121-125`.

The header comment says "MutationOutboxSQLite implements the package's MutationOutboxStorage port"
and the rewritten file comment says landing 1b is "what makes it satisfy the full port" — but the
declaration is a bare `export class MutationOutboxSQLite<A ...>`, there is no
`satisfies`/`implements` anywhere, and nothing else in the tree references the class
(`git grep MutationOutboxSQLite -- mobile-native/src appwire-client/typescript cmd/evener-hub/frontend/src`
returns this file and its test only). The test imports `MutationIntent` and never names
`MutationOutboxStorage`.

Concrete cost: the one claim this PR exists to make — thirteen methods now match the port — is
checked by nobody. The class has no caller to typecheck it against, so a signature that drifts from
`appwire-client/typescript/state/mutation/outbox.ts:75-103` (an `options` field, a `ReadonlySet` that
became an array) surfaces in the wiring PR instead of here, after both halves have merged.

Simpler form, one word:

```ts
export class MutationOutboxSQLite<A extends MutationAttachmentRef = MutationAttachmentRef>
	implements MutationOutboxStorage<A>
```

with `MutationOutboxStorage` added to the existing type-only import from
`@evener/appwire-client/state/mutation`. Reading the port against the class, all thirteen signatures
line up and the `protected` members do not conflict, so this should compile as-is — worth confirming
with the package's `tsc` rather than taking my word for it.

I am stretching the stated rubric to call this must-fix (it is neither duplication nor dead code).
The reason: the class is deliberately unreferenced until the wiring PR, so the compiler is its only
reader, and this is the one-word change that makes it read anything at all. Flagging the stretch
rather than quietly relabeling it.

## 2. `nextDispatchable` materializes every record for the target to read one — follow-up

`mobile-native/src/mutationOutboxStorage.ts:320-323`:

```ts
const first = this.list<MutationOutboxRecord<A>>(TABLES.outbox, targetRef)[0];
return first?.state === "submitting" ? first : undefined;
```

`list` does `SELECT *` for the whole target and maps every row through `fromRow`, which runs three
`JSON.parse`es per row (`:110-112`). The dispatcher calls this per ref on every discovery pass, and
it wants one record.

Concrete cost: a target with a twelve-deep queue parses thirty-six JSON blobs, synchronously on the
phone's UI thread, to answer a question about one row — and it does that on every scan (the package's
interval is 2s, `outbox.ts:147`).

Simpler form:

```ts
const row = this.db.getFirstSync<Row>(
	`SELECT * FROM ${TABLES.outbox} WHERE target_ref = ? ORDER BY intent_sequence LIMIT 1`,
	targetRef,
);
const first = row ? fromRow<A, MutationOutboxRecord<A>>(row) : undefined;
return first?.state === "submitting" ? first : undefined;
```

Behaviour-identical (lowest `intent_sequence` for the ref, then the `submitting` check), one row
instead of N. The oracle scans the whole store because IndexedDB has no `LIMIT`
(`cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.ts:143-150,413-417`); SQLite does, so
transcribing the scan gives up the one thing this store is better at. Follow-up by the rubric, but
it is a five-line change and the cheapest efficiency win in the PR.

## 3. `restoreProvenAbsent` materializes full records to collect ids, then writes one statement per record — follow-up

`mobile-native/src/mutationOutboxStorage.ts:332-343`.

Same `list` full-materialization as finding 2 (every record for the target, three `JSON.parse`es
each), and everything downstream uses `record.state` and `record.clientMutationId` only — payload,
attachments and display are parsed and discarded. Then the reopen is N `UPDATE`s in a loop.

Concrete cost: on a target with a deep queue this is a full parse of the queue plus one statement per
blocked record, where the store can answer with two statements and no JSON at all.

Simpler form — select the ids, filter, reopen in one statement:

```ts
const blocked = this.db
	.getAllSync<{ client_mutation_id: string }>(
		`SELECT client_mutation_id FROM ${TABLES.outbox}
		 WHERE target_ref = ? AND state = 'blockedUnknown' ORDER BY intent_sequence`,
		targetRef,
	)
	.map((row) => row.client_mutation_id)
	.filter((id) => !authoritativeIds.has(id));
if (blocked.length)
	this.db.runSync(
		`UPDATE ${TABLES.outbox} SET state = 'submitting'
		 WHERE client_mutation_id IN (${blocked.map(() => "?").join(",")})`,
		...blocked,
	);
return blocked;
```

The `IN` list is bounded by the number of blocked records on one target (never by
`authoritativeIds`, which is a whole snapshot and would risk SQLite's parameter cap), the
`ORDER BY` keeps today's return order, and the savepoint stays — with a single statement it becomes
belt-and-braces rather than the only thing holding the loop together, which is strictly better.
Note this changes what the atomicity test at `:454-469` of the test file is exercising (a mid-loop
abort becomes a mid-statement abort); the assertion — neither record reopened — still holds, but the
comment explaining *why* would need rewriting with it, which is why this is a follow-up rather than
an in-PR edit.

## 4. No index on `(target_ref, intent_sequence)`, which every read path filters and sorts on — follow-up

`mobile-native/src/mutationOutboxStorage.ts:137-148` (the schema, from #1916) against the read paths
added here: `list` (`:347-353`), `nextDispatchable`, `restoreProvenAbsent`, `listOptimistic`,
`listTargetRefs`.

The three tables get a `client_mutation_id` primary key and nothing else, so every one of those
queries is a full table scan plus a sort. The oracle creates exactly the index they want, on both
stores it reads this way: `outbox.createIndex("byTargetSequence", ["targetRef", "intentSequence"], { unique: true })`
(`cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.ts:447-457`).

Concrete cost: small today — an outbox holds a handful of rows, and I am not going to claim a
user-visible cost I have not measured. The real cost is that the store the whole read path is built
on has no statement of what it is keyed by, so the scan is a property of the schema rather than a
choice, and it is the shape a growing queue degrades on.

Simpler form, one line per table in the constructor beside the `CREATE TABLE`s:

```sql
CREATE INDEX IF NOT EXISTS ${table}_target_sequence ON ${table} (target_ref, intent_sequence)
```

Additive, no behaviour change, and it makes `ORDER BY intent_sequence` a lookup instead of a sort.
(The web's outbox/optimistic index is additionally `unique`, i.e. it enforces one record per
target+sequence. Whether SQLite should enforce that too is a correctness question — RoboRev's, not
mine — so I am only asking for the index, not the constraint.)

## 5. `listTargetRefs`: two redundant `DISTINCT`s and a sort in JS — follow-up

`mobile-native/src/mutationOutboxStorage.ts:293-298`:

```ts
`SELECT DISTINCT target_ref FROM ${TABLES.outbox} UNION SELECT DISTINCT target_ref FROM ${TABLES.optimistic}`
...
return rows.map((row) => row.target_ref).sort();
```

`UNION` already de-duplicates (that is the whole difference from `UNION ALL`), so both `DISTINCT`
keywords are dead weight; and the ordering is done in JS after the store could have done it.

Concrete cost: a reader has to decide whether the `DISTINCT`s mean something the `UNION` does not,
and the result set is built twice (once de-duplicated by the store, once re-ordered in JS).

Simpler form:

```ts
const rows = this.db.getAllSync<{ target_ref: string }>(
	`SELECT target_ref FROM ${TABLES.outbox} UNION SELECT target_ref FROM ${TABLES.optimistic} ORDER BY target_ref`,
);
return rows.map((row) => row.target_ref);
```

Same result, and `ORDER BY` on a `UNION` is well-defined. (`.sort()`'s default lexicographic
comparison on strings is what `ORDER BY target_ref` gives for ASCII refs, which is what these are —
`local:thread-1`. Worth a second look if refs ever carry non-ASCII, in which case the collation
belongs in the store either way.)

## 6. `list`'s two branches duplicate the statement — follow-up

`mobile-native/src/mutationOutboxStorage.ts:347-353`. The two `getAllSync` calls differ only by a
`WHERE` clause and one parameter, so `SELECT * FROM ${table}` and `ORDER BY intent_sequence` are each
written twice.

Concrete cost: trivial today; it is the shape where a change to the projection or the ordering gets
made on one branch.

Simpler form:

```ts
const scope = targetRef === undefined ? "" : " WHERE target_ref = ?";
const params = targetRef === undefined ? [] : [targetRef];
const rows = this.db.getAllSync<Row>(`SELECT * FROM ${table}${scope} ORDER BY intent_sequence`, ...params);
```

## 7. Altitude: the read-path contracts are hand-transcribed from the oracle a third time — the port-conformance gap — follow-up

`mobile-native/src/mutationOutboxStorage.test.ts:366-469` (this PR's six read-path tests) against
`cmd/evener-hub/frontend/src/stores/mutationOutbox.test.ts`'s `MutationOutboxIndexedDB` block.

Every one of the new tests carries an `Oracle: ...` comment pointing at a web test by line number
(e.g. `:540` for the blocked-lower-sequence rule) and then re-states that contract in SQLite terms by
hand. The same is true of the twelve write-path tests in #1916. So the port now has two adapters and
two independently hand-maintained suites, related only by comments naming line numbers in a
1040-line file — line numbers that go stale the first time the web's suite gains a test.

Concrete cost: nothing tells either adapter that it has diverged from the port's contract. When
`nextDispatchable`'s blocking rule or `settleReceipt`'s source precedence changes, one adapter's
suite is updated and the other's comment still claims parity. This is the gap the retired lane
flagged, and it is exactly the class of drift that eats review rounds.

Simpler form: a package-level conformance suite —
`appwire-client/typescript/state/mutation/outboxConformance.ts` exporting

```ts
export function describeMutationOutboxStorage<A extends MutationAttachmentRef>(
	name: string,
	open: () => { storage: MutationOutboxStorage<A>; close: () => void },
): void
```

holding the host-free contracts (gap-free per-target sequencing, `settleReceipt`'s
pending-input-carrying rule and its `outbox ?? recovery ?? optimistic` precedence, `markUnknown`'s
`onlyAttempted` guard, `nextDispatchable`'s same-target blocking, `restoreProvenAbsent`'s
proven-absent rule), with the web's suite and this one each calling it over their own factory and
keeping only their host-specific tests (the web's Blob handling, cross-tab identity, stalled-storage
deadlines; the phone's raw-row and savepoint-rollback assertions, which are genuinely SQLite's).
`state/mutation/testing.ts` is the established home for this subpath's test-only exports, so the
package already has the shape for it.

Follow-up, not must-fix: it is a row of its own (it rewrites part of the web's suite), it is the
largest single item either PR raises, and it is the one that should be a GitHub issue rather than a
journal entry.

## Not findings — checked and clean

- **The five one-line delegating reads** (`getOutbox`, `getOptimistic`, `listOptimistic`,
  `getRecovery` at `:300-314`) are pure port adapters over `get`/`list` with no logic to share and no
  variation between them beyond the table and the record type. Nothing to collapse: collapsing them
  would mean not implementing the port.
- **Payload JSON is parsed once per row, in one place.** The brief asked whether the read path
  re-parses per row where a single mapper would do — `fromRow` is that single mapper and every read
  goes through it (`get` at `:422`, `list` at `:352`). The waste is *which rows* get mapped
  (findings 2 and 3), not how many times each one is.
- **`listOptimistic` pushes the filter and the sort into SQL** where the oracle does both in JS
  (`mutationOutboxIndexedDB.ts:165-174`). Right call, and it is the same call findings 2, 3 and 5 are
  asking for consistently everywhere else.
- **`restoreProvenAbsent`'s savepoint** is a real improvement over #1916's read of the same method's
  shape: the oracle gets atomicity free from its IndexedDB transaction, and the loop here needed it
  explicitly. Correct, and the test at `:454-469` falsifies it with a trigger rather than a mock.
- **The comment rewrites** (`:1-19`, `:51-54`) remove the "dead code by construction" framing that
  1a needed and replace it with the pair's actual shape. No stale D25d-1a/1b references left in the
  source; the test file's header keeps one deliberate note about why the write tests read raw rows.

---

## Verdict: fix before merge (1 must-fix, 6 follow-up)

Fix before merge, on one word. The read path is the right half of the port done the right way round
— `listOptimistic` pushes filtering and ordering into the store where the oracle does them in JS,
`restoreProvenAbsent` earns its savepoint and proves it with a trigger rather than a mock, and the
five delegating reads are honest one-liners with nothing to collapse — but the class still never says
`implements MutationOutboxStorage<A>`, and since it deliberately has no caller until the wiring PR,
that leaves the PR's entire claim (thirteen signatures now match the package's port) checked by
nobody; adding it is one word and one type import, and I would not merge the pair without it. The
six follow-ups split into three cheap efficiency items the store is already able to do better than
the oracle it was transcribed from (`nextDispatchable` parsing a whole queue to read one row,
`restoreProvenAbsent` parsing full records to collect ids and then writing one statement each,
`listTargetRefs`'s redundant `DISTINCT`s and JS sort), one additive schema line the oracle already
has and this schema lacks (an index on `target_ref, intent_sequence`, which every read here filters
and sorts on), one two-branch statement duplication in `list`, and the altitude item that matters
most: both adapters' contracts are now hand-transcribed from each other with `Oracle:` comments
citing line numbers, where a package-level `describeMutationOutboxStorage` conformance suite would
have one set of contracts and two factories. That last one is the gap the retired lane flagged and
should be filed as an issue.
