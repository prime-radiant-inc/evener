# Comprehensive fix plan — coverage-audit campaign

Goal: take the campaign from "honest but not mergeable" to "clean, real coverage, gate-green." Driven by the audit data in `COVERAGE_AUDIT_REVIEW.md`. Ordered so each phase leaves the tree green and is independently committable.

Guiding rule: a test must be able to **fail** when the behavior it names breaks. Anything that can't is rewritten or deleted — we don't keep coverage that gives false confidence.

---

## Phase 0 — Safety check (done)

A sloppy global edit mangled two `t.Errorf` **message** strings in `tree_test.go` (`01A`→`-11A`). Verified the mangling did **not** touch any assertion *values* — contained to those two messages. No further corruption in the changeset. ✅ (Repaired in Phase 1.)

## Phase 1 — Gate blockers (mechanical, ~0 risk)

1. `gofmt -w` the 10 non-clean files (import order + table alignment):
   archive/roster/tree (hubcore), merge/wire (launchconfig), fspaths app_paths, tuiprim pane, serf main/serve/upgrade.
2. Fix the two corrupted `t.Errorf` messages in `tree_test.go` (701, 776): `-11A`→`01A`.
3. Collapse the double trailing blank line in `upgrade_test.go` (gofmt covers this).
4. **Gate:** `gofmt -l` empty + `go test ./...` green (both modules).

Commit: `style(tests): gofmt + repair corrupted assertion messages`.

## Phase 2 — Coverage theater (the false-confidence blockers — 7 tests)

Each rewritten so the named behavior is the *only* thing that makes the test pass (mutation-checked):

1. **`binresolve` `TestSiblingDir_AbsFailsWithVeryLongPath`** — zero assertions. Either delete, or assert the real invariant: `ok ⇒ filepath.IsAbs(dir)`, `!ok ⇒ dir==""`.
2. **`fspaths` `…/limit greater than 30 is capped`** — create **>30 uniquely-named** dirs (`fmt.Sprintf("dir%02d", i)`); assert `len(Data) == 30` exactly.
3. **`hubcore/prober` `TestStatusProber_NonOKStatus`** — return 404 **with valid JSON body** so the status guard is the only thing producing `ok=false`; assert returned id/status empty.
4. **`serf-doctor` `TestRun_APILogFlags/summary`** — assert against a string the per-call table emits and the summary omits (e.g. require `"uncached"` in full output, assert absent under `--summary`).
5. **`serf-doctor` `TestRun_TreeDepthAndObservers/depth`** — add a grandchild to the fixture; assert the depth-limit note appears + grandchild sid absent at depth 1, present at depth 2.
6. **`binresolve` `TestIsExecutable_Directory`** — actually `os.Mkdir` the `serf-hub` sibling dir so the `IsDir()` branch runs; assert Resolve rejects it.
7. **`hubapi` deleted `TestClientURLPreservesQueryString`** — restore it (or fold an equivalent `?`-path assertion into the new suite). Net coverage must not regress.

**Gate:** for each, confirm the test goes **red** under the named mutation, then green when reverted (spot-check the two `high` ones at minimum).

Commit: `test: make coverage-theater tests actually assert their named behavior`.

## Phase 3 — High-value minors

### 3a. Flaky permission tests (9 tests, 7 files) — environment-dependent
All use `chmod 0o000/0o555` to force I/O errors; **root bypasses permission bits**, so under a root CI/Docker runner they don't exercise the error path (and several would *fail*, expecting an error that doesn't occur). No house idiom exists. Add one shared helper (e.g. `internal/testutil` or per-package `requireNonRoot(t)` → `if os.Geteuid()==0 { t.Skip("permission-based error injection is a no-op as root") }`) and call it at the top of each. Files: hostlock, hubcore/archive, hubcore/past, hubedge/auth_token, launchconfig/io (×4), launchconfig/resolver (×2), rendezvous. Also fix `model_display`'s `os/user.Current()` home-dir subtests to set `HOME`/inject rather than read the real user.

### 3b. Tautological hubapi path tests (8 subtests) — re-implements the SUT
`TestClientSession/Send/Tasks/Interrupt/Compact/Clear/Fork/SetModel` build the expected URL with the **same `ref.PathEscaped()`** the SUT uses, so a bug in path-escaping escapes. Replace with **hardcoded** expected path strings. This is the bulk of the 573-line file and the highest-leverage minor fix.

### 3c. More coverage-theater (5) + one wrong-expected
- `mcpstatus` HTTP/SSE "HEAD rejection→GET fallback" (×2): a 405 HEAD is `err==nil` in Go, so the GET fallback never runs — restructure to actually force the fallback, or drop the misleading names.
- `serf-hub/main` `TestCurrentExecutable` os.Args-fallback half — `os.Executable()` reads `/proc/self/exe`, independent of `os.Args`; the fallback is unreachable as written. Drop that half or inject the seam.
- `serf/serve` `TestAgentToServerDetailedStatus_Empty` — zero-value input proves nothing about field mapping; add a populated case.
- `rendezvous` `TestWrite_MarshalDoesNotPanic` — can't reach the marshal-error branch; rename to the happy-path it actually tests or remove.
- `model_display` `…/home_no_subdir_unchanged` — returns via the early `len<=maxLen` path, never the no-subdir branch; adjust `maxLen` so the intended branch runs.
- **DECISION NEEDED — `tuitext` `TestTruncateText/unicode wide characters`:** expects the full 6-wide string back for a width-3 request, which **contradicts** `TruncateText`'s documented display-width contract. Either the **SUT is buggy** (doesn't truncate wide runes) or the **test expectation is wrong**. Needs a look before fixing — flagging rather than guessing. (Scope note: if it's a real SUT bug, fixing it is beyond "fix the tests.")

### 3d. Duplicates (5) — delete dead weight
- `apptranscript`: remove the now-superseded `TestSharedTranscriptHelpers` the junior left in place.
- `binresolve` `TestIsExecutable_NonExecutableFile` dup of pre-existing `TestResolveRejectsNonExecutableExplicit`.
- `mcpstatus` 4 SSE tests that are line-for-line dups of the HTTP cases (shared `case "http","sse":`) — keep one representative.
- `provider` `TestWithCheapModel` subtests dup of existing `TestWithCheapModel_*`.
- `fspaths` `…/command kind without separator` dup of `…/command kind on PATH`.

Commit: `test: deflake permission tests, de-tautologize hubapi paths, drop duplicates`.

## Phase 4 — Selective assertion strengthening (subset of 48)

Don't grind all 48 — do the cheap, high-value ones where a stronger assertion catches a real bug class; skip pure style. Highest-value:
- `events` mapping test: also assert fields that should be empty (`Delta==""`, `ToolCall==nil`) so spurious extra fields are caught.
- `server`, `serf/main`, `provider`, `fspaths` (the 3–4-finding files): tighten the named ones from the report.
Leave the rest as-is; note what we skipped in the commit so it's honest, not silently "all addressed."

Commit: `test: strengthen high-value assertions`.

## Phase 5 — Verify & land

1. `gofmt -l` empty; `go vet ./...` clean (both modules).
2. `go test ./...` green in root **and** `agent/` modules; `-race` on the touched packages.
3. `make lint` (the project gate) clean.
4. **Coverage didn't regress:** re-run the per-package coverage from the junior's `report.md` numbers; confirm the restored hubapi query-string + the fixed theater tests *raised* real coverage, not just statement %.
5. Remove stray artifacts from the worktree (`*.out`, `fspaths.test`).
6. Squash-or-group commits, `--no-ff` merge to main per house workflow. (Push only on request.)

---

## Execution (decided)

- **Fan-out workflow**, run as **sequential waves** so phased commits stay clean: solo Phase 1 → fan-out Phase 2 → commit → fan-out Phase 3 (incl. tuitext SUT fix) → commit → fan-out Phase 4 → commit → solo Phase 5 gate + merge. Files are disjoint within a wave (one agent owns one file), so in-place edits are safe; a file touched across waves just gets sequential commits. Each agent runs `gofmt -w` + its package tests; theater fixers must show the test goes red under the named mutation.

## Decisions (locked)
1. **tuitext wide-char** → **Fix the SUT now**: implement display-width-aware truncation in `TruncateText` (TDD), assert correct truncated output. Folded into Phase 3c.
2. **weak-assertion scope** → **All 48**.
3. **Execution** → **Fan-out workflow** (waves).
4. **Commits** → **Phased** (Phase 1 / 2 / 3 / 4), then `--no-ff` merge.
