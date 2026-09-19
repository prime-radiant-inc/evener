# /simplify — #1889 (S1) @0655494cb4303c0b79b260098bbfef232685bec4

Scope: reuse / simplification / efficiency / altitude only. Correctness is RoboRev's.
Reviewed: `git diff -M origin/main...pr-1889-review` (6 files, +599/-14).

Checked first, no hit: no pre-existing error-scrubbing or "log raw, return safe"
helper anywhere in the repo (`git grep "see the hub's log"` is new in this stack;
the only other `scrub` functions — `agent/sandbox/secret_env.go`,
`cmd/evener-hub/project_delete.go` — are a different domain). The
`fmt.Fprintf(m.stderr(), "warning: …")` idiom is pre-existing and correctly
reused. No existing `fs.PathError`-injection fixture in `internal/plugins`
either (no stub/patch helper exists in the package's test files), so the new
test file duplicates nothing that predates it — its repetition is internal.

---

## 1. `internal/plugins/install.go:56-59` + `internal/plugins/marketplaces.go:104-105` — two literal copies of the same log-and-scrub pair
The boundary scrub is written twice with byte-identical format strings
(`"warning: saving %s failed: %v\n"` / `"saving %s failed; see the hub's log for
detail"`), differing only in the filename constant.
**Cost:** S3 then writes the same body a third time as
`migrationFileSaveFailed(fileName, saveErr)` — i.e. exactly the extraction
missing here — and does not retire these two. Three copies of one two-line
function ship in the stack. (#1896's item 1, "saveRegistry needs its own
saveFailed-shaped helper", is already satisfied by this PR and is stale.)
**Simpler:** one method, `storeFileSaveFailed(fileName string, err error) error`,
with `saveRegistry` and `saveMarketplaces` both calling it. Identical strings, so
no test changes.
**Severity:** follow-up here (two small copies); **must-fix at S3's head**, where
the generalized helper exists and leaves these two as duplicates. See
`1897-simplify.md` finding 1 — one 6-line fix closes it for all three.

## 2. `internal/plugins/marketplaces.go:272-278` — `saveFailed` discards `saveErr` and then re-derives what `saveErr` already said
`saveErr` at all seven call sites is already the scrubbed error from
`saveMarketplaces`/`saveRegistry` (or, at `saveRename`, a message composed of
them). Its text already names the file. `saveFailed` throws it away, takes
`fileName` as a third parameter to say the same thing again, logs the
already-scrubbed text a second time, and re-attaches `errStoreBetweenNames` by
hand because the `%w` chain was dropped.
**Cost:** an 8-line helper plus a 3-argument call at seven sites; two stderr
lines per failure, the second of which tells a reader of the hub's log to "see
the hub's log for detail"; a hand-rolled `errors.Is` branch that `%w` gives free.
**Simpler:** `func (m *Manager) saveFailed(name string, saveErr error) error {
return fmt.Errorf("marketplace %q: %w", name, saveErr) }` — one line, no
`fileName` argument, no log, no sentinel branch. The rendered wire strings and
`errors.Is(err, errStoreBetweenNames)` are unchanged for every call site (the
scrubbed cause already carries both the filename and the sentinel), so the new
tests in `marketplaces_path_scrub_test.go` pass as written.
**Severity:** follow-up.

## 3. `internal/plugins/marketplaces.go:291-297` — `storeChangeRollbackFailed` repeats `saveFailed`'s sentinel-preserving if/else verbatim
Same four-line shape: `if errors.Is(x, errStoreBetweenNames) { return
fmt.Errorf(msg+": %w", …, errStoreBetweenNames) }; return fmt.Errorf(msg, …)`.
S3's `markerAfterFailedMove` makes it three copies (with
`errRenameRollbackIncomplete`).
**Cost:** the same rule ("keep whatever sentinel the cause carried") encoded three
times; a fourth site that forgets it loses `errors.Is` silently, which is what
`TestEditMarketplaceRenameBetweenNamesRollbackFailureSurvivesIdentity` exists to
catch.
**Simpler:** wrap the cause with `%w` instead of discarding it (finding 2), which
removes the branch at all three sites; or, if the text must not carry the cause,
one `keepSentinel(cause error, msg string, args ...any) error`.
**Severity:** follow-up.

## 4. `internal/plugins/marketplaces.go:644` — the between-names branch composes a long message out of two already-scrubbed errors, for the log only
`m.saveFailed(name, marketplacesFileName, fmt.Errorf("saving %s failed (%w);
restoring %s failed (%w), so %w: it still keys …", …, err, …, restoreErr,
errStoreBetweenNames, newName))`: `err` and `restoreErr` are already scrubbed, so
the composed sentence repeats "saving known_marketplaces.json failed" twice and
"see the hub's log for detail" twice, and `saveFailed` discards all of it except
the `errors.Is` check. The log line it produces reads
`warning: saving marketplace "acme" failed: saving known_marketplaces.json failed
(saving known_marketplaces.json failed; see the hub's log for detail); restoring
installed_plugins.json failed (saving installed_plugins.json failed; see the
hub's log for detail), so …`.
**Cost:** the longest single line in the diff exists to carry one sentinel and
one already-logged fact.
**Simpler:** `return m.saveFailed(name, marketplacesFileName, errors.Join(err,
restoreErr, errStoreBetweenNames))` — `errors.Is` still finds the sentinel, both
causes are already scrubbed and already logged at the boundary, and nothing is
composed for a reader who never sees it.
**Severity:** follow-up.

## 5. `internal/plugins/marketplaces.go:642-647` — both `saveRename` branches now return the same wire sentence
Before: "marketplace %q not renamed: saving %s failed, so the store is back as it
was" vs. the between-names sentence. After: both render "marketplace %q: saving
known_marketplaces.json failed; see the hub's log for detail"; only
`errors.Is(errStoreBetweenNames)` separates them, and neither the RPC handler nor
`cmd/evener/plugincmd.go` branches on it today.
**Cost:** a user who gets a failed rename can no longer read whether the store is
intact — the most useful half of the old message, and it named no path.
**Simpler:** keep the distinction in the safe text: "…; the store is back as it
was" on the clean branch. Free, no path.
**Severity:** follow-up (RoboRev owns whether the message loss is a defect; listed
here because restoring it costs one string).

## 6. `internal/plugins/marketplaces_path_scrub_test.go` (whole file), `install_test.go:415-451`, `seed_test.go:55-80` — the injection-and-assert shape is copy-pasted ~12 times
Repeated verbatim: `m := NewManager(t.TempDir()); m.Stderr = io.Discard`; the
`orig… := marketplaceAtomicWriteFile / t.Cleanup(restore) / assign a
&fs.PathError` block (19 `fs.PathError` literals in this file alone); the
`err == nil` / `strings.Contains(err.Error(), path)` / `strings.Contains(err.Error(),
name)` assertion triple. The selective `installSaveRegistry` "let the first call
land, fail the second" stub appears twice with its comment duplicated word for
word (`:308-320` and `:380-392`), and the selective `marketplaceRemoveAll` stub
three times.
**Cost:** ~90 lines of the 436 are mechanical repetition; adding the next scrub
site means pasting the block again (S2 and S3 each did).
**Simpler:** two helpers in this file, reused by the `install_test.go` and
`seed_test.go` additions: `failWriteTo(t *testing.T, path string)` (stub +
`t.Cleanup`) and `assertScrubbed(t *testing.T, err error, paths []string, want
...string)`. The per-test comments — which are the valuable part — stay.
**Severity:** follow-up.

## 7. Efficiency — six of the ten new tests pay for a real `git clone`
`makeMarketplaceRepo*` is called 15 times in the new file; the four
`TestEditMarketplace…` tests differ only in which primitive they fail.
**Simpler:** a parent test with subtests sharing one `makeMarketplaceRepoWithPlugin`
source (the repo URL is reusable across `AddMarketplace` calls; only the `Manager`
needs to be fresh).
**Severity:** follow-up, low. Not a scan-repeatedly problem; just test wall time.

## 8. Altitude — the new doc comments restate the mechanism at length, including caller lists that will go stale
`marketplaces.go:85-93` enumerates all seven `saveMarketplaces` callers;
`install.go:45-50` enumerates all five `saveRegistry` callers;
`marketplaces.go:629-635` re-explains the scrub a third time.
**Cost:** `git grep` answers "who calls this" and stays right; a hand-kept list in
a doc comment is a second copy of the call graph that the next caller silently
falsifies.
**Simpler:** state the invariant once ("every store write is scrubbed here, so no
caller can leak the path") and drop the enumerations.
**Severity:** follow-up, low.

---

## Verdict: follow-up only (8 findings, 0 must-fix)

Nothing in #1889 duplicates a helper that predates it and nothing is dead — the
one hard duplication in the stack (the two-line boundary scrub written twice here
and a third time in S3 as a helper) only becomes must-fix once S3's
`migrationFileSaveFailed` exists, so it is booked against #1897 and this PR is
clean to merge on its own. The substantive theme is that `saveFailed` discards an
already-scrubbed cause and then spends a parameter, a second log line and a
hand-rolled `errors.Is` branch re-deriving what that cause already carried;
wrapping with `%w` instead collapses findings 2, 3 and 4 into roughly minus
twenty lines with identical wire strings. The test file is thorough and its
per-case comments are genuinely good, but ~90 of its 436 lines are a pasted
injection-and-assert block that two local helpers would absorb, and the same
block is already being pasted onward by S2 and S3 — worth doing before a fourth
site copies it again.
