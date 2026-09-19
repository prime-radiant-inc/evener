# /simplify — #1890 (S2) @d5af6cefe7e2807c686e90b3ec13efd33d0c34f1

Scope: reuse / simplification / efficiency / altitude only. Correctness is RoboRev's.
Reviewed: `git diff -M pr-1889-review..pr-1890-review` (4 files, +210/-5).
Jesse's ruling that the `RemoveMarketplace` save-before-delete reorder stays is
respected throughout — nothing below asks for that behaviour back.

---

## 1. `internal/plugins/marketplaces.go:316-319` — `cloneRemovalFailed` is the fourth spelling of one rule
Body: `Fprintf(m.stderr(), "warning: …: %v\n", …, removeErr)` then
`fmt.Errorf("… ; see the hub's log for detail", …)`. That is the same
log-raw-then-return-scrubbed shape as `saveFailed`, `storeChangeRollbackFailed`
(S1) and `migrationFileSaveFailed` / `markerAfterFailedMove` (S3).
**Cost:** by the end of the stack six methods encode one rule; the rule lives in
six doc comments rather than one place, and a seventh site that logs the scrubbed
error instead of the raw one is invisible in review.
**Simpler:** this site genuinely differs (its own message, its own sentinel), so the
call site stays — but the bodies are mechanical. One `m.scrubbed(raw error, format
string, args ...any) error` that logs `raw` and returns `fmt.Errorf(format,
args...)` reduces each of the six to a single line, leaving only what actually
differs: the sentence.
**Severity:** follow-up (booked as part of the stack-wide altitude item in
`1897-simplify.md` finding 7; nothing here duplicates an existing helper outright).

## 2. `internal/plugins/errors.go:17-24` — `ErrMarketplaceUnregisteredCloneRemains` is exported with no production consumer
S3's own test comment states it plainly: "Neither this RPC handler nor the CLI
(`cmd/evener/plugincmd.go`) branches on that sentinel today — both pass the error
through unclassified." Its only observers are the three new tests.
**Cost:** exported API surface (a package's error contract is the hardest thing to
retract) whose behavioural effect today is zero; the doc comment's promise ("a
caller distinguishes this from a plain refusal") describes no caller that exists.
**Simpler:** either keep it unexported (`errMarketplaceUnregisteredCloneRemains`)
until a caller branches on it — the tests are in-package and pass unchanged — or
classify it at the RPC boundary in the same PR so the export has a user.
**Severity:** follow-up, low. Flagging the export, not the reorder or the returned
error, both of which are the ruling.

## 3. `internal/plugins/marketplaces_path_scrub_test.go:162-252` — the retry test re-runs the previous test's whole body
`TestRemoveMarketplaceRetryAfterCloneRemovalFailureReportsNotFound` (`:213`)
repeats `TestRemoveMarketplaceCloneRemovalFailureNamesNoPath` (`:162`) line for
line — same `NewManager`/`makeMarketplaceRepo`/`AddMarketplace`, same selective
`marketplaceRemoveAll` stub, same first-call sentinel assertion — and then adds
the retry.
**Cost:** ~25 duplicated lines and a second real `git clone` to reach a state the
sibling test already reaches; a change to the failure injection has to be made
twice.
**Simpler:** one test whose second half is the retry (the no-path and
`ListMarketplaces` assertions of the first test all still hold at that point), or
a shared `removeWithFailingCloneRemoval(t) (*Manager, string)` helper.
**Severity:** follow-up.

## 4. `internal/plugins/marketplaces_path_scrub_test.go:128-289` — the S1 injection-and-assert block is pasted four more times
Same finding as `1889-simplify.md` item 6, now with four more instances (and a
third copy of the selective `marketplaceRemoveAll` stub at `:167-173`,
`:224-230`). The helpers proposed there would absorb these as written.
**Severity:** follow-up.

## 5. Efficiency — four new tests, four real `git clone`s, one shared fixture
All four `gitAvailable()`-gated tests build `makeMarketplaceRepo(t, "market-a")`
and `AddMarketplace` it; only the `Manager` must be fresh per case.
**Simpler:** one parent test that creates the source repo once, with four subtests
each taking a fresh `NewManager(t.TempDir())`.
**Severity:** follow-up, low.

## 6. `internal/plugins/marketplaces.go:320-332` and the new test comments — the `""`/`".."` rationale is written out three times
The unvalidated-name reasoning ("`\"\"` resolves to the marketplaces directory
itself, `\"..\"` to its parent") appears in `RemoveMarketplace`'s 13-line doc
comment, in `TestRemoveMarketplaceRetryAfterCloneRemovalFailureReportsNotFound`'s
comment, and again in `TestRemoveMarketplace_LookupMissTouchesNothingOnDisk`'s.
`errors.go`'s sentinel comment repeats the applied-with-litter explanation a
fourth time.
**Cost:** four copies of one paragraph drift apart; `RemoveMarketplace`'s doc
comment is longer than `RemoveMarketplace`.
**Simpler:** the invariant in the doc comment ("a lookup miss never derives a path
from the unvalidated name"), the reason in the test that enforces it, and a
cross-reference instead of a restatement in the other two.
**Severity:** follow-up, low.

Also checked, no finding: `coverage_marketplaces_fuzz_test.go:246-255` correctly
inverts the fuzz coverage case to expect the error and explains why; `mustExist`
and `makeMarketplaceRepo` are reused rather than re-invented; no dead code left
by the reorder (the old best-effort warning at the removal site is gone, not
orphaned).

---

## Verdict: follow-up only (6 findings, 0 must-fix)

#1890 is the smallest and cleanest of the three: the reorder is a genuine
simplification of the old swallow-a-warning-and-report-success shape, `mustExist`
and the existing repo fixtures are reused, and nothing is left dead. The two items
worth a follow-up PR are the exported sentinel that no production code branches on
yet (S3's own test comment is the evidence) and the retry test that re-runs its
sibling's entire body for a second real `git clone`. `cloneRemovalFailed` itself is
fine as a site — it is the sixth copy of the log-raw-return-scrubbed *body* that
argues for one shared helper, and that argument is booked against #1897 where the
generalized helper already exists. Merge-ready on quality grounds.
