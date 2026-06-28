# 8.7 — Nightly CI campaign + auto-triage — implementation plan

**Status:** PLANNED. **Date:** 2026-06-28. **Branch:** `wip/fuzzing-toolkit`.
**Charter:** [`fuzzing-toolkit-design.md`](../fuzzing-toolkit-design.md) §8.7 (and §3 promoter, §7 open decisions). **Builds on:** the BUILT promoter (`fuzz/promoter/*.go`), the campaign runner (`scripts/run-fuzz.sh`), the `fuzz` / `fuzz-nightly` Makefile targets, and (optionally) coverage measurement (8.6).

This is the automation that turns a one-shot fuzzing run into a standing capability: a scheduled job that searches each surface on a real budget, and — when it finds a *deterministic* crash — files exactly one human-reviewable PR carrying the crasher, a replaying regression test, and a reproducer, while a triage ledger records what has been found / fixed / quarantined. Nothing here auto-merges.

## 0. Goal & non-goals

**Goal.** (1) A scheduled campaign running the per-target coverage-guided search on a configurable budget, persisting any crashers it discovers. (2) Auto-triage: flake-guard every crasher (K replays), promote only deterministic ones, and open one reviewable PR per distinct bug — never reopening a bug already filed or already covered. (3) A found/fixed/quarantined ledger so there is a dashboard of discoveries.

**Non-goals (now).** Auto-merge (a human always reviews). External fuzzing infra (OSS-Fuzz/ClusterFuzz). Corpus harvesting from real traffic (that is 8.4; if it later feeds the nightly, its secret-scrubbing is *its* responsibility — see §6 risks). Fuzzing surfaces that have no target yet.

**Guiding rule (inherited).** Never file a flake. A crash earns a PR only if it reproduces deterministically K times. This plan adds a *CI-level* flake-guard for the Go-native byte targets (which never reach `promoter.Promote`) on top of the promoter's existing K-replay guard for the rapid/sequence targets.

## 1. What exists today (verified against the tree)

**CI = GitHub Actions.** Three workflows under `.github/workflows/`:
- `ci.yml` — the gate. Triggers `push`/`pull_request` on `main`. Steps: `go build`, `make vet`, the three `serf-*check` tools, `make lint-golangci`, `make test-race`. `permissions: contents: read`. **There is no `make fuzz` step in the gate yet** — the charter says `make fuzz` *should* run seed corpora in the gate; wiring that one-line step is a prerequisite this plan also lands (§5, step 0).
- `binaries.yml` — release builds. Relevant because it is the in-repo precedent for everything 8.7 needs: per-job `permissions: contents: write`, `workflow_dispatch`, and `gh` CLI driven by `GH_TOKEN: ${{ github.token }}` (`binaries.yml:58,72,84,107`). The auto-PR step mirrors this exactly.
- `trigger-build.yml` — repository-dispatch to `sen-deploy`; not relevant here.

**No `schedule:` trigger exists anywhere in the repo.** The nightly campaign is a new scheduled workflow; we are *not* assuming a CI provider — GitHub Actions is what serf actually runs (cited above).

**The campaign runner.** `scripts/run-fuzz.sh` already does the per-target bounded search. Its `TARGETS` array (`run-fuzz.sh:22-33`) is the source of truth — 10 entries, each `module:package-relpath:FuzzName`. It accepts `--time DURATION` and an optional target allow-list, and a crash is auto-saved by the Go toolchain to that target's `testdata/fuzz/<FuzzName>/`. The Makefile wraps it as `fuzz-nightly: scripts/run-fuzz.sh $(FUZZ_ARGS)` and the gate-safe seed replay as `fuzz: go test -run '^Fuzz' ./...` (`Makefile:101-107`).

**The promoter (`fuzz/promoter/`), and the two distinct crasher paths.** This is load-bearing for the triage design:

- **Go-native byte targets** (8 of the 10 in `TARGETS`: `FuzzParseSSE`, `FuzzMessageDecode`, `FuzzMethodParams`, `FuzzToolArgsValidate`, the four provider `*Metamorphic` targets, `FuzzWebHandler`). A crash is saved by `go test -fuzz` as a content-addressed file under `<pkg>/testdata/fuzz/<FuzzName>/<hash>`. **That file *is* the regression artifact** — `make fuzz` replays it forever as a subtest `<FuzzName>/<hash>`. These never call `promoter.Promote`; Go's corpus is their dedup (filename = content hash).
- **Promoter / rapid targets** (`TestToolArgsSchemaFuzz` in `agent/registry_schemafuzz_test.go`, the appwire sequence target in `internal/appserver/router_seqfuzz_test.go`). When the rapid harness captures a failure it calls `promoter.Promote(ctx, Failure)` (`registry_schemafuzz_test.go:70`), which runs the K-replay flake-guard, then `adapter.Emit` (→ `promoter.WriteGoTest`) writes a `testregression_*_test.go`, and `store.Add` records the signature in the bucket store.

**The persistence gap to close.** Today both targets construct their promoter against **`t.TempDir()`** for the emit dir *and* the bucket store (`registry_schemafuzz_test.go:58-59`, `router_seqfuzz_test.go:59`). That is correct for the gate (a fuzz run must not dirty the tree), but it means **nothing the promoter emits survives the process** — there is nothing for a PR to capture. §3.2 closes this with an env-gated persistent path that is off by default (so the gate stays clean) and on only under the nightly.

Promoter API the triage relies on (all present): `Failure{Surface,Oracle,Stack,Detail,Artifact}`, `Signature{Oracle,Key}` + `String()` (the bucket id), `Outcome` (`Promoted`/`AlreadyKnown`/`Quarantined`), `Adapter` (`Minimize`/`Signature`/`Replay`/`Emit`), `Quarantiner.Quarantine(f, survivedRuns)`, `BucketStore` (`OpenBucketStore`/`Has`/`Get`/`Add`/`Len`, atomic write), `ShortHash(Failure)` (12-hex), `WriteGoTest`/`GoTest`.

## 2. The nightly campaign

**New workflow `.github/workflows/fuzz-nightly.yml`.** Shape (concrete, modeled on `binaries.yml`):

```yaml
name: Fuzz Nightly
on:
  schedule:
    - cron: "0 7 * * *"          # 07:00 UTC daily; open decision §7
  workflow_dispatch:
    inputs:
      fuzztime: { description: "per-target budget", default: "5m" }
      targets:  { description: "space-separated module:FuzzName allow-list", default: "" }
permissions:
  contents: write                # commit crasher + generated test to a branch
  pull-requests: write           # open the triage PR
  issues: write                  # ledger-as-issue fallback (open decision §7)
concurrency:
  group: fuzz-nightly            # never overlap runs (corpus/ledger writers)
  cancel-in-progress: false
jobs:
  campaign:
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false            # one crashing target must not abort the others
      matrix:
        target: [ ... the 10 entries from run-fuzz.sh TARGETS ... ]
    steps:
      - uses: actions/checkout@v6          # full history; needed for branch+PR
      - uses: actions/setup-go@v6
        with: { go-version-file: go.work }
      - name: Campaign + triage
        env: { GH_TOKEN: ${{ github.token }} }
        run: scripts/fuzz-triage.sh --time "${{ inputs.fuzztime || '5m' }}" "${{ matrix.target }}"
```

Notes:
- **Targets/budgets reuse `run-fuzz.sh`.** The matrix is the `TARGETS` array (one matrix leg per target = parallel fan-out + isolated logs + per-leg timeout). To avoid drift between the workflow matrix and `run-fuzz.sh`, the matrix is the single source: `scripts/fuzz-triage.sh` shells out to `run-fuzz.sh --time <budget> <module:FuzzName>` for exactly the one target it was handed. Budget is `fuzztime` (default 5m for nightly; 60s remains the `run-fuzz.sh` default for manual use). A future refinement (open decision §7) is a per-target budget map (cheap parsers 1m, the rapid state machine 10m) read from a small `fuzz/nightly-budgets.txt`.
- **Persisting crashers.** For Go-native targets, a discovered crash lands in the *working tree* at `<pkg>/testdata/fuzz/<FuzzName>/<hash>` (the Go toolchain writes it). The triage step (below) is what carries it into a branch — the campaign step itself only runs the search and leaves new files in the tree. For promoter targets, §3.2's env wiring makes `Emit`/`store.Add` write into the tree as well.

## 3. Auto-triage → reviewable PR

Driven by **`scripts/fuzz-triage.sh`** (the orchestrator the workflow calls per target). Pseudostructure:

```
fuzz-triage.sh --time DUR <module:FuzzName>
  1. snapshot = git status --porcelain of testdata/fuzz + fuzz/state   (pre-run baseline)
  2. SERF_FUZZ_PERSIST=1 run-fuzz.sh --time DUR <module:FuzzName>       (the search)
  3. discover crashers = new/changed paths since snapshot:
       a. <pkg>/testdata/fuzz/<FuzzName>/<hash>         (Go-native)
       b. **/testregression_*_test.go + fuzz/state/buckets.json delta   (promoter)
  4. for each discovered crasher: flake-guard (§3.1) -> dedup (§3.3) -> file PR (§3.4)
  5. update the ledger (§4) for every crasher, whatever the outcome
```

### 3.1 Flake-guard before any PR
- **Go-native crashers** never went through `promoter.Promote`, so the CI gives them the same discipline: re-run the saved corpus entry as a subtest K times — `cd <module> && go test -run '^<FuzzName>$/<hash>$' <pkg>` — and treat it as deterministic only if it **fails all K** runs. A crasher that passes any of the K is *quarantined*: its `testdata/fuzz` file is reverted (`git checkout --`), it is logged to the ledger as `quarantined`, and **no PR is opened**. (Go's own `-fuzz` minimization already shrank it; this guard is purely about determinism, matching the promoter's K-replay rule.)
- **Promoter crashers** already passed the K-replay guard inside `Promote` (that is why a `testregression_*_test.go` exists at all — `Quarantined` outcomes emit nothing). The triage step trusts that and does not re-run K; it only needs to detect the emitted file. K is shared (start 5; open decision §7 in the charter).

### 3.2 Persisting promoter output (the gap fix)
Add an env-gated persistent destination so the gate stays clean but the nightly captures artifacts. The two rapid targets read it when constructing their promoter:
- `SERF_FUZZ_PERSIST=1` (set only by `run-fuzz.sh` when invoked from triage, or by `fuzz-triage.sh` directly) switches the adapter's `emitDir` from `t.TempDir()` to the surface's own package directory (so the generated `*_test.go` compiles in-package) and the bucket-store path from a temp file to the committed **`fuzz/state/buckets.json`** (repo-root, shared across all targets for cross-target dedup).
- Unset (the default — `make fuzz`, `make test`, every gate run) keeps the current temp-dir behavior: the promoter still runs and is still tested, but writes nothing into the tree.

Implementation: a tiny shared helper, e.g. `promoter.PersistPaths(pkgDir string) (emitDir, bucketsPath string, persist bool)` reading the env, used by both `_test.go` harnesses in place of the inline `t.TempDir()` calls. ~30 LoC + two call-site edits. (Keeping it in `fuzz/promoter` avoids each surface re-implementing the env contract; it imports only stdlib, preserving the "no serf deps" property of the package.)

### 3.3 Dedup — do not reopen the same crasher
Three layers, in order:
1. **Bucket store** (`fuzz/state/buckets.json`, committed). For promoter targets, `Promote` already returns `AlreadyKnown` when `store.Has(sig)` — triage skips those (no file is emitted, so step 3 finds nothing new). Because the store is now committed (not temp), yesterday's discoveries are remembered across nightly runs.
2. **Go corpus content-addressing.** A Go-native crash that reproduces an already-saved input produces no new `testdata/fuzz` file (same content hash → same filename, already on disk and committed) → nothing discovered → no PR.
3. **PR/branch existence check** (covers the window where a fix is in review but not merged, and the case where a previous PR was closed-without-merge). Branch name is deterministic from the crasher signature: `fuzz/crash-<sig12>`, where `<sig12>` = the promoter `Signature.String()` short hash for promoter targets, or the `testdata/fuzz` filename hash for Go-native ones. Before opening: `gh pr list --head fuzz/crash-<sig12> --state all` and the ledger lookup (§4) — if either knows this signature, **skip** (refresh the ledger `last_seen`, open nothing). This is the defense, together with the flake-guard, against PR spam.

### 3.4 Opening the PR (never auto-merged)
For a surviving, novel, deterministic crasher:
```
git switch -c fuzz/crash-<sig12>
git add <testdata/fuzz file | testregression_*_test.go> fuzz/state/buckets.json fuzz/state/ledger.json
git commit -m "test(fuzz): regression for <surface>/<oracle> <sig12>"   # + Claude-Session trailer
git push -u origin fuzz/crash-<sig12>
gh pr create --base main --head fuzz/crash-<sig12> --label fuzz-crash \
  --title "Fuzz crash: <surface> <oracle> (<sig12>)" --body-file <generated body>
```
The PR body carries: the surface + oracle + signature, the **reproducer** (for Go-native: the `testdata/fuzz` bytes + the exact `go test -run '<FuzzName>/<hash>'` command; for promoter: the `Failure.Detail` + minimized `Artifact` JSON + the emitted test name), the K-replay evidence, and a one-line "this is a real, deterministic failure — review and fix; do not merge the test without a fix" instruction. Because the committed regression test (or replayed seed) is **red on `main` until the bug is fixed**, the PR's own CI (`make fuzz` in the gate, landed in step 0) goes red — making the failure impossible to ignore and the PR un-mergeable-green until fixed. This ties directly to the charter's `--commit` opt-in idea (§3.3): the nightly *is* the opt-in committer, but it commits to a *branch + PR*, never to `main`.

This relies on the promoter's existing `Emit`/`WriteGoTest` (emit-only by default) plus the env-gated persistence of §3.2 — no change to the `Promote` decision logic.

## 4. Triage ledger

**`fuzz/state/ledger.json`** (committed; lives beside `buckets.json` under a new `fuzz/state/`). One record per distinct signature, append/update only:
```json
{
  "<oracle>:<key>": {
    "surface":    "toolargs",
    "oracle":     "error-shape",
    "sig":        "a1b2c3d4e5f6",
    "status":     "found | quarantined | fixed",
    "first_seen": "2026-06-28T07:00:00Z",
    "last_seen":  "2026-06-29T07:00:00Z",
    "pr":         "https://github.com/.../pull/123",
    "test_path":  "agent/testregression_toolargs_error_shape_a1b2c3.go",
    "detail":     "schema-valid input rejected by validator"
  }
}
```
- **found** — deterministic crasher, PR opened (or already open).
- **quarantined** — failed the flake-guard; logged with `survivedRuns`, never a PR. (Aggregated from the promoter's `Quarantiner` for rapid targets, and from §3.1's revert path for Go-native ones. A repeatedly-quarantined signature is itself a signal worth surfacing.)
- **fixed** — set by a reconciliation step at the *start* of each nightly: for every `found` entry, replay its seed/test on current `main`; if it now passes, the bug was fixed (the PR merged) → flip to `fixed` and stamp `fixed_seen`. This is the cheap way to get a fixed-count without webhook plumbing.

Format choice: JSON (machine-updatable by the bash orchestrator via a 1-line `jq`, diff-reviewable, same shape/serialization as `buckets.json`). The ledger doubles as the dashboard: `jq` one-liners (or a tiny `make fuzz-ledger` pretty-printer, optional) give found/fixed/quarantined counts and the open-bug list. A human-facing rendering can come later; the charter only asks for the record to exist.

## 5. File-by-file build plan + LoC

| # | File | Change | LoC |
|---|------|--------|-----|
| 0 | `.github/workflows/ci.yml` | add a `make fuzz` step to the gate (charter's stated intent; makes auto-filed PRs go red until fixed) | ~3 |
| 1 | `.github/workflows/fuzz-nightly.yml` | NEW scheduled+dispatch workflow, matrix over the 10 targets, calls `fuzz-triage.sh` | ~60 |
| 2 | `scripts/fuzz-triage.sh` | NEW orchestrator: run campaign, discover crashers, flake-guard (Go-native), dedup, open PR, update ledger | ~180–280 |
| 3 | `scripts/run-fuzz.sh` | honor `SERF_FUZZ_PERSIST` passthrough (export to the `go test` env); optional per-target budget map lookup | ~15 |
| 4 | `fuzz/promoter/persist.go` | NEW `PersistPaths(pkgDir)` env helper (emitDir + buckets path + persist bool); stdlib-only | ~30 |
| 5 | `agent/registry_schemafuzz_test.go`, `internal/appserver/router_seqfuzz_test.go` | use `PersistPaths` instead of inline `t.TempDir()`; aggregate `Quarantine` into the ledger path | ~20 |
| 6 | `fuzz/state/` (`buckets.json` move, `ledger.json` seed) + `.gitignore`/`Makefile` `fuzz-ledger` pretty-printer (optional) | relocate bucket store to committed shared path; empty ledger | ~10 |
| 7 | `fuzz/README.md` | document the nightly, the ledger, how to reproduce a filed crasher locally | ~25 |
| 8 | `fuzz/promoter/persist_test.go` + a `scripts/fuzz-triage.sh` dry-run self-test | tests for env helper + a `--dry-run` triage path asserting dedup/quarantine decisions without `gh` | ~60 |

**Total ~250–500 LoC**, matching the charter estimate. The orchestrator script (#2) is the bulk; everything else is wiring.

## 6. Dependencies, risks, acceptance

**Dependencies.**
- `fuzz/promoter` (Adapter/Promote/BucketStore/WriteGoTest/ShortHash) — present and BUILT.
- `scripts/run-fuzz.sh` + its `TARGETS` — present; reused as the search engine and the target source of truth.
- `gh` CLI + `GH_TOKEN: ${{ github.token }}` — already the in-repo pattern (`binaries.yml`).
- **8.6 coverage measurement (optional).** If landed, the nightly can additionally upload a per-surface coverage report as a workflow artifact, so the campaign reports "searched N targets, exercised X% of each surface" — the honesty number the charter wants. Not a hard dependency; 8.7 ships without it.

**Risks & defenses.**
- *CI minutes / budget.* 10 targets × budget, parallelized by the matrix. Default 5m/target keeps a nightly under ~10 wall-clock min with fan-out. `concurrency: fuzz-nightly` + `cancel-in-progress:false` prevents overlap; `workflow_dispatch` inputs allow ad-hoc longer runs without editing the file. Per-target budget map (open decision) lets the rapid state machine get more time than a byte parser.
- *Flaky-PR spam.* The two-layer flake-guard (promoter K-replay for rapid; §3.1 K-replay for Go-native) plus three-layer dedup (bucket store, Go corpus content hash, `gh pr list` + ledger by signature) is the whole defense. Acceptance below proves both halves.
- *Secret handling.* 8.7 only persists fuzzer-generated bytes from `testdata/fuzz` (synthetic, not real traffic) and the promoter's minimized artifacts — low secret risk. If 8.4 corpus harvesting ever feeds the nightly, scrubbing is *its* gate, not this one; note it in `fuzz/README.md`. The PR branch uses `github.token` scoped by per-job `permissions:` (contents+PR write only) — no long-lived PAT.
- *Tree-dirtying the gate.* The `SERF_FUZZ_PERSIST` env defaults off, so `make fuzz`/`make test`/the gate never write artifacts; only the nightly sets it. The `persist_test.go` (#8) guards this contract.
- *Stale buckets vs. deleted tests.* If a human deletes a generated regression test without clearing its bucket, the bug could silently never refile. The reconciliation step (§4) that replays `found` entries catches the inverse (fixed); a periodic `make fuzz-ledger --verify` that warns on bucket→missing-test entries is a cheap add (folded into #2/#6).

**Acceptance.**
1. **Seeded deterministic crash → exactly one PR.** Inject a deterministic bug into one fuzzed seam (e.g. a panic on a known input), run `fuzz-triage.sh` against that target: it discovers the crasher, the flake-guard fails all K, a single PR `fuzz/crash-<sig>` is opened carrying the crasher + a replaying regression test + reproducer command, and the ledger gains one `found` entry. The committed test/seed is red on `main`.
2. **Re-run does not duplicate.** Run `fuzz-triage.sh` again against the same target with the bug still present: the bucket store / Go corpus / `gh pr list` dedup short-circuits — **no second PR**, ledger `last_seen` refreshed only.
3. **Flaky failure → no PR.** Point the harness at a non-deterministic failure (fails ~50% of replays): the flake-guard quarantines it, no test is emitted/committed, no PR is opened, and the ledger records it `quarantined` with `survivedRuns`.
4. **Fix reconciliation.** After the §1 bug is fixed and merged, the next nightly's reconciliation flips that ledger entry to `fixed`.
(1)+(3) together are the charter's stated acceptance; (2)+(4) prove dedup and the dashboard.

## 7. Open questions for Jesse

1. **CI provider specifics.** Confirmed GitHub Actions (`.github/workflows/`). OK to add a new `schedule:`-triggered workflow with `contents: write` + `pull-requests: write` (the same elevated perms `binaries.yml` already uses for releases)? Any org policy on Actions opening PRs / pushing branches I should know about?
2. **PR vs. issue.** Plan defaults to a **PR** (carries a red, mergeable-only-when-fixed regression test — strongest forcing function). Prefer an **issue** instead for some/all oracles (e.g. file an issue for `wedge`/`http-5xx` where there may be no clean test to commit, but a PR for `panic`/`error-shape`)? The `issues: write` perm is included to keep that option open.
3. **Budget.** Default nightly = 5m/target (≈10 min wall-clock with the matrix). Want a per-target map (e.g. byte parsers 1m, the appwire sequence machine 10m) in `fuzz/nightly-budgets.txt`, or is a flat budget fine to start? And the cron hour (plan says 07:00 UTC).
4. **Where `fuzz/state/` lives + committing generated artifacts.** Plan commits `buckets.json` + `ledger.json` + generated tests to the PR branch (never `main` directly). Confirm that is the desired "opt-in commit" boundary (charter §7 decision #1), vs. keeping artifacts as workflow artifacts only and committing solely on human merge.
5. **K and N** reuse the charter's flake-guard/stack-hash values (start K=5, N=4). Tune from the first weeks of nightly data — fine to defer.
