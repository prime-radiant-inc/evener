# Adversarial Review — coverage-audit campaign

**Reviewed:** 36 test files (~3,550 added lines) + 1 production change.
**Method:** one adversarial auditor per file (read test **and** subject-under-test, mutation thinking) → refute-biased skeptic re-checked every critical/high finding against the real code. 44 agents. The skeptic refuted **0 of 7** serious findings but downgraded several severities — the findings below are the survivors, and I hand-verified the load-bearing ones myself.

## Verdict: sound foundation, **not mergeable as-is**

The campaign is honest work. Across 3,550 lines there are **no** mock-tested assertions, **no** mocks in e2e tests, **no** fabricated/wrong expected values that lock in a bug (one trivial exception), and the fixtures are real on-disk data with captured output. Six files are clean ("solid"), and the lone production change is a **genuine fix** (guards a real slice-out-of-range panic; no behavior change for the two constant callers). The verifier calibration was good — nothing fabricated.

But two buckets must be cleared before this merges.

---

## Bucket 1 — Gate blocker (mechanical, must fix): 10 files are not `gofmt`-clean

`go test` passes, but nobody ran the formatter. `gofmt -l` flags **10** of the changed files (import ordering + table-struct alignment):

```
cmd/serf-hub/internal/hubcore/archive_test.go      cmd/serf/main_test.go
cmd/serf-hub/internal/hubcore/roster_test.go       cmd/serf/serve_test.go
cmd/serf-hub/internal/hubcore/tree_test.go         cmd/serf/upgrade_test.go
cmd/serf-hub/internal/launchconfig/merge_test.go   cmd/serf-hub/internal/fspaths/app_paths_test.go
cmd/serf-hub/internal/launchconfig/wire_test.go    cmd/serf-tui/internal/tuiprim/pane_test.go
```

This alone fails the repo gate. Fix: `gofmt -w` on those files. (The audit only caught one of these — reality is 10.)

---

## Bucket 2 — Coverage theater (real false-confidence): fix or delete

Tests that execute a branch but assert nothing that would catch the bug they're named for. These are exactly what the "no false confidence" rule targets — each one I confirmed by hand.

| # | File · test | Severity | What's wrong | A bug that escapes |
|---|---|---|---|---|
| 1 | `internal/binresolve/sibling_test.go` · `TestSiblingDir_AbsFailsWithVeryLongPath` | **high** | **Zero assertions** — the only body is a comment-only `if ok {}` block | `SiblingDir` could `return "", true` for any input and this passes |
| 2 | `cmd/serf-hub/internal/fspaths/app_paths_test.go` · `…/limit greater than 30 is capped` | **high** | Claims to test the 30-cap but only creates **10** dirs (`dir0..dir9`, `i%10` collide; `MkdirAll` idempotent) so the cap branch never runs; asserts `<=30` not `==30` | Delete the `limit > 30` cap entirely — still passes |
| 3 | `cmd/serf-hub/internal/hubcore/prober_test.go` · `TestStatusProber_NonOKStatus` | medium | The 404 response body is invalid JSON, so `ok=false` comes from the **JSON-decode failure**, not the status guard — a duplicate of `TestStatusProber_BadJSON` | Delete the `StatusCode != 200` guard — probe accepts non-200 daemons, test still passes |
| 4 | `cmd/serf-doctor/main_test.go` · `TestRun_APILogFlags/summary` | medium | Forbids substring `"round 1"` which appears in **neither** summary nor full output — always true | Delete the `if opts.SummaryOnly` early-return so `--summary` dumps the full table — still passes |
| 5 | `cmd/serf-doctor/main_test.go` · `TestRun_TreeDepthAndObservers/depth` | low | Asserts only that the root sid prints (unconditional for any tree); fixture has no grandchildren so depth=1 and depth=∞ are identical output | `Tree` could ignore `--depth` entirely — still passes |
| 6 | `internal/binresolve/sibling_test.go` · `TestIsExecutable_Directory` | medium | Named for the `IsDir()` rejection branch but never creates a `serf-hub` directory, so the branch is never exercised — passes because the sibling path simply doesn't exist | Delete `info.IsDir()` from `isExecutable` — branch stays uncovered, test passes |
| 7 | `hubapi/client_test.go` · `TestClientURLPreservesQueryString` (**deleted**) | low | The junior **deleted** the only test covering `URL()`'s query-string split; no new test uses a `?` path — net coverage **regression** while adding coverage elsewhere | `URL()` could drop `RawQuery` silently — nothing fails |

Fixes are in the per-finding detail (`serious.md` in scratchpad, or ask me) — each is a few lines.

---

## Bucket 3 — Minor (nice-to-have, not blocking)

Across the 30 "minor-issues" files: **48 weak-assertion** nitpicks (could assert more, e.g. also assert fields that should be empty), **9 flaky-ish** (real `$HOME`/cwd/`os.Current` env coupling — none currently failing), **5 more low-grade coverage-theater**, **5 duplicate**, **3 tautological**, 1 non-pristine (the gofmt import in roster), 1 `wrong-expected` (a `tuitext` unicode-width case pins behavior that contradicts the doc'd contract — worth a look), 1 brittle. These are normal test-review polish; they don't block a merge.

## Production change — keep it

`cmd/serf-tui/internal/modeldisplay/model_display.go`: `if maxLen <= 0 { return p }` in `AbbreviatePath`. **Correct.** Without it, `maxLen=0` on a non-empty path computes a negative tail and panics (`p[len(p)+1:]`). Both callers pass a constant `32`, so no behavior change — pure hardening, with a test that pins the new branch. Mild YAGNI, but it converts a latent panic into a safe no-op. Keep.
