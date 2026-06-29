# 8.7 — Local fuzz campaign + triage tooling — implementation plan

**Status:** PLANNED. **Date:** 2026-06-28. **Branch:** `wip/fuzzing-toolkit`.
**Charter:** [`fuzzing-toolkit-design.md`](../fuzzing-toolkit-design.md) §8.7 (and §3 promoter, §7 open decisions). **Builds on:** the BUILT promoter (`fuzz/promoter/*.go`), the campaign runner (`scripts/run-fuzz.sh`), the `fuzz` / `fuzz-nightly` Makefile targets, and (optionally) coverage measurement (8.6).

This item is reframed from "nightly CI automation" to **local, on-demand tooling**. There is no scheduler, no bot, and no elevated CI credentials. A developer runs a single local command at a time of their choosing; it searches each surface on a budget, and — when it finds a *deterministic* crash — opens exactly one human-reviewable PR (via the developer's own `gh` auth) carrying the crasher, a replaying regression test, and a reproducer, while a triage ledger records what has been found / fixed / quarantined. Nothing auto-merges. The only standing CI change is one fast seed-replay step in the existing PR gate.

## 0. Goal & non-goals

**Goal.** (1) A local campaign command that runs the per-target coverage-guided search on a configurable per-target budget, persisting any crashers it discovers. (2) Local auto-triage: flake-guard every crasher (K replays), promote only deterministic ones, and open one reviewable PR per distinct bug — never reopening a bug already filed or already covered — committing durable artifacts to the PR branch. (3) A found/fixed/quarantined ledger so there is a record of discoveries. (4) Promotion of the coverage-expanding corpus (minimized, diversity-capped) into `testdata/fuzz/`.

**Non-goals (now).** Any scheduled GitHub Actions workflow, cron, or bot-driven run. Any elevated CI permissions (`contents`/`pull-requests`/`issues: write`) — the PR gate stays `contents: read`. Auto-merge (a human always reviews). External fuzzing infra (OSS-Fuzz/ClusterFuzz). Corpus *harvesting from real traffic* (that is 8.4; if it later feeds the local tool, its secret-scrubbing is *its* responsibility — see §6 risks). Fuzzing surfaces that have no target yet.

**Guiding rule (inherited).** Never file a flake. A crash earns a PR only if it reproduces deterministically K times. This plan adds a flake-guard for the Go-native byte targets (which never reach `promoter.Promote`) on top of the promoter's existing K-replay guard for the rapid/sequence targets.

## 1. What exists today (verified against the tree)

**CI = GitHub Actions, one gate (`ci.yml`).** Triggers `push`/`pull_request` on `main`. Steps: `go build ./...`, `make vet`, `serf-namingcheck`, `serf-internalcheck`, `serf-docscheck`, `make lint-golangci`, `make test-race`. `permissions: contents: read` (verified). **There is no `make fuzz` step in the gate yet** — the charter says `make fuzz` *should* run seed corpora in the gate; wiring that one-line step (§5, step 0) is the *only* CI change this item makes. (`binaries.yml` and `trigger-build.yml` exist but are untouched here — we add no new workflow and no scheduler.)

**No `schedule:` trigger exists anywhere in the repo, and this item adds none.** Everything below is a local command a developer runs on demand.

**The campaign runner.** `scripts/run-fuzz.sh` already does the per-target bounded search. Its `TARGETS` array (`run-fuzz.sh:22-33`, verified) is the source of truth — 10 entries, each `module:package-relpath:FuzzName`. It accepts `--time DURATION` (default `60s`, verified) and an optional target allow-list, and a crash is auto-saved by the Go toolchain to that target's `testdata/fuzz/<FuzzName>/`. The Makefile wraps it as `fuzz-nightly: scripts/run-fuzz.sh $(FUZZ_ARGS)` and the gate-safe seed replay as `fuzz:` running `go test -run '^Fuzz' ./...` across every `GO_MODULES` module (`Makefile:96-107`, verified). Budgets are advisory; `--time` is the only knob and it already exists.

**The promoter (`fuzz/promoter/`), and the two distinct crasher paths.** This is load-bearing for the triage design:

- **Go-native byte targets** (8 of the 10 in `TARGETS`: `FuzzParseSSE`, `FuzzMessageDecode`, `FuzzMethodParams`, `FuzzToolArgsValidate`, the four provider `*Metamorphic` targets, `FuzzWebHandler`). A crash is saved by `go test -fuzz` as a content-addressed file under `<pkg>/testdata/fuzz/<FuzzName>/<hash>`. **That file *is* the regression artifact** — `make fuzz` replays it forever as a subtest `<FuzzName>/<hash>`. These never call `promoter.Promote`; Go's corpus is their dedup (filename = content hash).
- **Promoter / rapid targets** (`TestToolArgsSchemaFuzz` in `agent/registry_schemafuzz_test.go`, the appwire sequence target in `internal/appserver/router_seqfuzz_test.go`). When the rapid harness captures a failure it calls `promoter.Promote(ctx, Failure)` (`registry_schemafuzz_test.go:70`, verified), which runs the K-replay flake-guard, then `adapter.Emit` (→ `promoter.WriteGoTest`) writes a regression `*_test.go`, and `store.Add` records the signature in the bucket store.

**The persistence gap to close.** Today both rapid targets construct their promoter against **`t.TempDir()`** for the emit dir *and* the bucket store (`registry_schemafuzz_test.go:58-59`, `router_seqfuzz_test.go:58-59`, verified). That is correct for the gate (a fuzz run must not dirty the tree), but it means **nothing the promoter emits survives the process** — there is nothing for a PR to capture. §3.2 closes this with an env-gated persistent path that is off by default (so the gate stays clean) and on only when the local tool sets it.

Promoter API the triage relies on (all present, verified against `fuzz/promoter/*.go`): `Failure{Surface,Oracle,Stack,Detail,Artifact}`, `Signature{Oracle,Key}` + `String()` → `"<oracle>:<key>"` (the bucket id), `Outcome` (`Promoted`/`AlreadyKnown`/`Quarantined`) + `String()`, `Adapter` (`Minimize`/`Signature`/`Replay`/`Emit`), `Quarantiner.Quarantine(f, survivedRuns)`, `New(adapter, store, log, k)` (K defaults to 5 when ≤0), `BucketStore` (`OpenBucketStore`/`Has`/`Get`/`Add`/`Len`, atomic write), `ShortHash(Failure)` (12-hex), `WriteGoTest(dir, GoTest)`/`GoTest`.

## 2. The local campaign

There is **no workflow file** for the campaign. The developer runs the local tool directly:

```
scripts/fuzz-triage.sh [--time DUR] [--dry-run] [--no-pr] [target ...]
```

- `--time DUR` is the per-target budget passed straight through to `run-fuzz.sh` (default inherits `run-fuzz.sh`'s `60s`; pass e.g. `--time 5m` for a longer hunt). Budgets are advisory — there is no per-target budget map and no required value; a developer who wants the rapid state machine to get more time just runs that one target with a bigger `--time`.
- `target ...` restricts the run to one or more `module:FuzzName` entries from `run-fuzz.sh`'s `TARGETS`; default is all 10. `TARGETS` stays the single source of truth — `fuzz-triage.sh` shells out to `run-fuzz.sh` for the actual search, so there is no matrix to drift against.
- `--dry-run` performs discovery, flake-guard, and dedup decisions but opens no PR and writes no commit (used by the self-test, §8).
- `--no-pr` discovers and commits artifacts to a local branch but stops before `gh pr create` (for a developer who wants to inspect first).

**Persisting crashers.** For Go-native targets a discovered crash lands in the *working tree* at `<pkg>/testdata/fuzz/<FuzzName>/<hash>` (the Go toolchain writes it). For promoter targets, §3.2's env wiring makes `Emit`/`store.Add` write into the tree. The triage step (below) carries whatever appeared into a branch.

## 3. Auto-triage → reviewable PR (local `gh`)

Driven by **`scripts/fuzz-triage.sh`**. Pseudostructure:

```
fuzz-triage.sh [--time DUR] [target ...]
  0. reconcile ledger (§4): replay every `found` entry on the current tree;
     any that now PASS → flip to `fixed`.
  1. snapshot = git status --porcelain of testdata/fuzz + fuzz/state   (pre-run baseline)
  2. SERF_FUZZ_PERSIST=1 run-fuzz.sh --time DUR <target ...>           (the search)
  3. discover crashers = new/changed paths since snapshot:
       a. <pkg>/testdata/fuzz/<FuzzName>/<hash>         (Go-native)
       b. <pkg>/testregression_*_test.go + fuzz/state/buckets.json delta   (promoter)
  4. for each discovered crasher: flake-guard (§3.1) -> dedup (§3.3) -> open PR (§3.4)
  5. update the ledger (§4) for every crasher, whatever the outcome
  6. promote coverage-expanding corpus into testdata/fuzz (§3.5)
```

The tool uses the developer's **local `gh` auth** for the PR — no `GH_TOKEN`, no `github.token`, no CI permissions. If `gh` is not authenticated the tool stops before any push with a clear message and leaves the artifacts on the local branch (equivalent to `--no-pr`).

### 3.1 Flake-guard before any PR
- **Go-native crashers** never went through `promoter.Promote`, so the tool gives them the same discipline: re-run the saved corpus entry as a subtest K times — `cd <module> && go test -run '^<FuzzName>$/<hash>$' <pkg>` — and treat it as deterministic only if it **fails all K** runs. A crasher that passes any of the K is *quarantined*: its `testdata/fuzz` file is reverted (`git checkout --`), it is logged to the ledger as `quarantined`, and **no PR is opened**. (Go's own `-fuzz` minimization already shrank it; this guard is purely about determinism, matching the promoter's K-replay rule.)
- **Promoter crashers** already passed the K-replay guard inside `Promote` (that is why a regression `*_test.go` exists at all — `Quarantined` outcomes emit nothing). The tool trusts that and does not re-run K; it only needs to detect the emitted file. K is shared (default 5; §7).

### 3.2 Persisting promoter output (the gap fix)
Add an env-gated persistent destination so the gate stays clean but the local tool captures durable artifacts. The two rapid targets read it when constructing their promoter:
- `SERF_FUZZ_PERSIST=1` (set only by `fuzz-triage.sh`/`run-fuzz.sh` when invoked from triage) switches the adapter's `emitDir` from `t.TempDir()` to the surface's own package directory (so the generated `*_test.go` compiles in-package) and the bucket-store path from a temp file to the committed **`fuzz/state/buckets.json`** (repo-root, shared across all targets for cross-target dedup).
- Unset (the default — `make fuzz`, `make test`, every gate run) keeps the current temp-dir behavior: the promoter still runs and is still tested, but writes nothing into the tree.

Implementation: a tiny shared helper, e.g. `promoter.PersistPaths(pkgDir string) (emitDir, bucketsPath string, persist bool)` reading the env, used by both `_test.go` harnesses in place of the inline `t.TempDir()` calls (`registry_schemafuzz_test.go:58-59`, `router_seqfuzz_test.go:58-59`). ~30 LoC + two call-site edits. (Keeping it in `fuzz/promoter` avoids each surface re-implementing the env contract; it imports only stdlib, preserving the "no serf deps" property of the package.)

### 3.3 Dedup — do not reopen the same crasher
Three layers, in order:
1. **Bucket store** (`fuzz/state/buckets.json`, committed). For promoter targets, `Promote` already returns `AlreadyKnown` when `store.Has(sig)` — the tool skips those (no file is emitted, so step 3 finds nothing new). Because the store is now committed (not temp), earlier discoveries are remembered across runs and across developers who pull `main`.
2. **Go corpus content-addressing.** A Go-native crash that reproduces an already-saved input produces no new `testdata/fuzz` file (same content hash → same filename, already on disk and committed) → nothing discovered → no PR.
3. **PR/branch existence check** (covers the window where a fix is in review but not merged, and the case where a previous PR was closed-without-merge). Branch name is deterministic from the crasher signature: `fuzz/crash-<sig12>`, where `<sig12>` = `ShortHash(Failure)` for promoter targets, or the `testdata/fuzz` filename hash for Go-native ones. Before opening: `gh pr list --head fuzz/crash-<sig12> --state all` and the ledger lookup (§4) — if either knows this signature, **skip** (refresh the ledger `last_seen`, open nothing). This, with the flake-guard, is the defense against PR spam.

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
This is the **default** behavior of the tool on a confirmed deterministic crash. It runs under the developer's local `gh` credentials. The PR body carries: the surface + oracle + signature, the **reproducer** (Go-native: the `testdata/fuzz` bytes + the exact `go test -run '<FuzzName>/<hash>'` command; promoter: `Failure.Detail` + minimized `Artifact` JSON + the emitted test name), the K-replay evidence, and a one-line "this is a real, deterministic failure — review and fix; do not merge the test without a fix" instruction. Because the committed regression test (or replayed seed) is **red on `main` until the bug is fixed**, the PR's own CI (`make fuzz` in the gate, landed in step 0) goes red — making the failure impossible to ignore and the PR un-mergeable-green until fixed. This is the concrete form of the charter's `--commit` opt-in (§3.3): the developer running this tool *is* the opt-in committer, but it commits to a *branch + PR*, never to `main`.

This relies on the promoter's existing `Emit`/`WriteGoTest` (emit-only by default) plus the env-gated persistence of §3.2 — no change to the `Promote` decision logic.

### 3.5 Promoting the coverage-expanding corpus
A search run also discovers *non-crashing* inputs that expand coverage (Go keeps these in its fuzz cache, not in `testdata/`). After the crasher pass, the tool promotes a **minimized, diversity-capped** slice of the run's coverage-expanding corpus into the target's committed `testdata/fuzz/<FuzzName>/` seeds, so future `make fuzz` and future searches start from a richer corpus. Minimization reuses Go's own corpus minimization; the diversity cap (a fixed max new seeds per target per run, advisory) keeps the committed corpus from ballooning. These promotions ride into the same PR branch when a crash was filed, or — when a run finds only corpus growth and no crash — into a `fuzz/corpus-<date>` branch the tool offers to open as a low-priority PR. This ties to 8.4 (corpus) and 8.6 (coverage): the local tool is the thing that does this promotion.

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
- **fixed** — set by the reconciliation step at the *start* of each tool run (§3 step 0): for every `found` entry, replay its seed/test on the current tree; if it now passes, the bug was fixed (the PR merged) → flip to `fixed` and stamp `fixed_seen`. This is the cheap way to get a fixed-count without any webhook or scheduler.

The signature key is the promoter `Signature.String()` (`"<oracle>:<key>"`). Format choice: JSON (machine-updatable by the bash orchestrator via a 1-line `jq`, diff-reviewable, same shape/serialization as `buckets.json`). The ledger doubles as the dashboard: `jq` one-liners (or a tiny `make fuzz-ledger` pretty-printer, optional) give found/fixed/quarantined counts and the open-bug list.

## 5. File-by-file build plan + LoC

| # | File | Change | LoC |
|---|------|--------|-----|
| 0 | `.github/workflows/ci.yml` | add a `make fuzz` step to the gate — the ONLY CI change; makes auto-filed PRs go red until fixed. No new perms, no new workflow. | ~3 |
| 1 | `scripts/fuzz-triage.sh` | NEW local orchestrator: reconcile ledger, run campaign, discover crashers, flake-guard (Go-native), dedup, open PR via local `gh`, update ledger, promote corpus. Flags `--time/--dry-run/--no-pr`. | ~200–300 |
| 2 | `scripts/run-fuzz.sh` | honor `SERF_FUZZ_PERSIST` passthrough (export to the `go test` env) | ~10 |
| 3 | `fuzz/promoter/persist.go` | NEW `PersistPaths(pkgDir)` env helper (emitDir + buckets path + persist bool); stdlib-only | ~30 |
| 4 | `agent/registry_schemafuzz_test.go`, `internal/appserver/router_seqfuzz_test.go` | use `PersistPaths` instead of inline `t.TempDir()`; aggregate `Quarantine` into the ledger path | ~20 |
| 5 | `fuzz/state/` (`buckets.json` seed, `ledger.json` seed) + optional `Makefile` `fuzz-ledger` pretty-printer | committed shared bucket store path; empty ledger | ~10 |
| 6 | `fuzz/README.md` | document the local tool, the ledger, corpus promotion, how to reproduce a filed crasher locally | ~25 |
| 7 | `fuzz/promoter/persist_test.go` + a `fuzz-triage.sh --dry-run` self-test | tests for the env helper (off-by-default contract) + a dry-run triage path asserting dedup/quarantine decisions without `gh` | ~60 |

**Total ~300–460 LoC.** The orchestrator script (#1) is the bulk; everything else is wiring. (Removed from the prior draft: the scheduled `fuzz-nightly.yml` and the elevated-perms workflow block — they no longer exist in this item.)

## 6. Dependencies, risks, acceptance

**Dependencies.**
- `fuzz/promoter` (Adapter/Promote/BucketStore/WriteGoTest/ShortHash) — present and BUILT (verified).
- `scripts/run-fuzz.sh` + its `TARGETS` — present; reused as the search engine and the target source of truth.
- A developer-local, authenticated `gh` CLI — the only auth surface. No tokens stored, no CI secrets.
- **8.6 coverage measurement (optional).** If landed, the tool can additionally print a per-surface coverage summary after a run. Not a hard dependency.

**Risks & defenses.**
- *Developer time / budget.* Budgets are advisory and entirely under the developer's control via `--time`; the default short budget keeps an exploratory all-targets run quick, and a developer hunting one surface just gives it a bigger budget. No scheduler to tune.
- *Flaky-PR spam.* The two-layer flake-guard (promoter K-replay for rapid; §3.1 K-replay for Go-native) plus three-layer dedup (bucket store, Go corpus content hash, `gh pr list` + ledger by signature) is the whole defense. Acceptance below proves both halves.
- *Secret handling.* The tool only persists fuzzer-generated bytes from `testdata/fuzz` (synthetic, not real traffic) and the promoter's minimized artifacts — low secret risk. If 8.4 corpus harvesting ever feeds the tool, scrubbing is *its* gate, not this one; note it in `fuzz/README.md`.
- *Tree-dirtying the gate.* The `SERF_FUZZ_PERSIST` env defaults off, so `make fuzz`/`make test`/the gate never write artifacts; only the local tool sets it. The `persist_test.go` (#7) guards this contract.
- *Stale buckets vs. deleted tests.* If a human deletes a generated regression test without clearing its bucket, the bug could silently never refile. The reconciliation step (§4) that replays `found` entries catches the inverse (fixed); a periodic `make fuzz-ledger --verify` that warns on bucket→missing-test entries is a cheap add (folded into #1/#5).
- *Corpus bloat.* §3.5's diversity cap + Go corpus minimization bound how many new seeds a run commits.

**Acceptance.** (run the local tool — `scripts/fuzz-triage.sh` — in each scenario)
1. **Seeded deterministic crash → exactly one local PR.** Inject a deterministic bug into one fuzzed seam (e.g. a panic on a known input), run `fuzz-triage.sh` against that target: it discovers the crasher, the flake-guard fails all K, a single PR `fuzz/crash-<sig12>` is opened *via local `gh`* carrying the crasher + a **replaying** regression test + reproducer command, durable artifacts (`fuzz/state/buckets.json`, `ledger.json`, the emitted test) are committed to the branch, and the ledger gains one `found` entry. The committed test/seed is red on `main`.
2. **Re-run dedups.** Run `fuzz-triage.sh` again against the same target with the bug still present: the bucket store / Go corpus / `gh pr list` dedup short-circuits — **no second PR**, ledger `last_seen` refreshed only.
3. **Flaky failure → no PR.** Point the harness at a non-deterministic failure (fails ~50% of replays): the flake-guard quarantines it, no test is emitted/committed, no PR is opened, and the ledger records it `quarantined` with `survivedRuns`.
4. **Fix reconciliation.** After the §1 bug is fixed and merged, the next `fuzz-triage.sh` run's reconciliation flips that ledger entry to `fixed`.
5. **Gate green.** With `SERF_FUZZ_PERSIST` unset, `make fuzz` and `make test-race` write nothing into the tree (the new ci.yml `make fuzz` step passes on a clean PR; `persist_test.go` asserts the off-by-default contract).
(1)+(3) together are the charter's stated acceptance; (2)+(4) prove dedup and the dashboard; (5) proves the gate stays clean.

## 7. Decisions (resolved by Jesse — no longer open)

1. **No scheduled workflow, no elevated CI perms.** Dropped the scheduled `fuzz-nightly.yml`, the cron, and all `contents`/`pull-requests`/`issues: write` perms. Everything in this item is a local tool a developer runs on demand.
2. **The one CI change** is adding the fast `make fuzz` (committed seed-corpus replay, no search) to the existing `ci.yml` PR gate. Nothing else changes in CI.
3. **PR by default.** A confirmed deterministic crash opens a PR (carrying the red-until-fixed regression test) using the developer's local `gh` auth. The PR is the forcing function; `--no-pr` is available for inspect-first.
4. **Durable artifacts committed to the PR branch:** `fuzz/state/buckets.json` (dedup store), `fuzz/state/ledger.json` (found/fixed/quarantined), and the emitted regression tests. Enabled by the env-gated `SERF_FUZZ_PERSIST` (default off so the gate stays clean).
5. **K=5, N=4** flake-guard / stack-hash defaults (advisory; tune from real use). No cron hour and no per-target budget map — budgets are advisory and set per run via `run-fuzz.sh --time`.
6. **Coverage-expanding corpus** is minimized + diversity-capped and committed into `testdata/fuzz/` by the local tool (§3.5), tying 8.7 to 8.4/8.6.
