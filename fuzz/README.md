# evener fuzzing + failure-to-regression toolkit

> New to the fuzzer? Start at [`docs/developing-evener/fuzzing.md`](../docs/developing-evener/fuzzing.md) — the
> developer's guide (running, reading coverage, triaging a crasher). This file is
> the operational reference it points back to.

This module (`primeradiant.com/evener/fuzz`) is the **evener-agnostic core** of the
toolkit: the failure→regression promoter (and, later, the schema→generator).
Nothing here imports any `primeradiant.com/evener` package — `go.mod` declares no
evener dependency, so the module will not build if that boundary is violated. That
structural guarantee *is* the portability test (the eventual superpowers skill
carries this module unchanged).

The per-surface fuzz **targets** do NOT live here — Go requires `testing.F`
targets to sit in the package under test (they call unexported seams and their
`testdata/fuzz/` corpus lives beside them). They are:

| Surface | Target | File |
|---|---|---|
| SSE frame parser | `FuzzParseSSE` | `llm/sse_fuzz_test.go` |
| AppWire frame decode | `FuzzMessageDecode` | `appwire/jsonrpc_fuzz_test.go` |
| AppWire per-method params | `FuzzMethodParams` | `appwire/params_fuzz_test.go` |
| Tool-arg decode+validate | `FuzzToolArgsValidate` | `agent/tool_args_fuzz_test.go` |

## Running

```sh
make fuzz           # seed corpus + saved crashers as deterministic tests (gate-safe)
make fuzz-nightly   # bounded coverage-guided search per target (60s each by default)
make fuzz-triage    # local campaign + auto-triage: search, flake-guard, dedup, open a PR
scripts/run-fuzz.sh --time 5m            # all targets, 5 min each
scripts/run-fuzz.sh llm:FuzzParseSSE     # one target
scripts/run-fuzz.sh --list               # the target list (single source of truth)
```

Two cross-cutting facts about every run above:

- **`-tags evenerfuzz` is on automatically.** The whole fuzz path builds with it so
  the internal `invariant.Hold()` assertions (`primeradiant.com/evener/invariant`)
  are live — a tripped invariant panics and the never-panic oracle catches it.
  `make test` / `go build` stay tag-free and byte-unchanged. See
  [`docs/developing-evener/fuzzing.md`](../docs/developing-evener/fuzzing.md) → *Internal invariants*.
- **Memory**: a coverage-guided search can balloon into tens of GB — run searches on a host that can spare the RAM. See [`docs/developing-evener/fuzzing.md`](../docs/developing-evener/fuzzing.md) → *Memory safety*.

## Coverage measurement

`make coverage-floor` answers the question `make fuzz` cannot: *how much of
each module does any deterministic test exercise?* Per module it unions the
unit-test track with the committed-corpus replay (no `-fuzz`, so deterministic
and reproducible from a clean checkout), plus the frontend's vitest line
coverage, and ratchets the rows in `scripts/coverage/coverage-floors.txt`.
`make fuzz-gap-check` is the static blocking gate: every decode/parse package
must have a registered target or a reasoned ignore
(`scripts/coverage/fuzzcov-ignore.txt`, reviewed like code).

The single source of truth for the target list is `scripts/fuzz/run-fuzz.sh --list`.
Coverage gains found by `make fuzz-nightly` are locked in by committing the
new inputs into `testdata/fuzz/<FuzzName>/`.

`make fuzz` runs `go test -run '^Fuzz' ./...` per module: with no `-fuzz` flag
the toolchain does **not** random-search — it replays each target's seed corpus
plus any saved `testdata/fuzz/<FuzzName>/` crashers as ordinary, deterministic
unit tests.

## The free regression loop

When `make fuzz-nightly` (or `go test -fuzz`) finds a failing input, the Go
toolchain writes it to that target's `testdata/fuzz/<FuzzName>/<hash>`. That file
is committed and re-runs forever as a deterministic regression test — no promoter
needed for single-input `testing.F` targets. The promoter in `promoter/` exists
for the surfaces Go does *not* auto-promote (stateful sequences, non-`testing.F`
runners).

## Local campaign + auto-triage (`make fuzz-triage`)

`scripts/fuzz-triage.sh` is the on-demand capstone: a developer runs it at a
time of their choosing and it turns a found, *deterministic* crash into exactly
one reviewable PR — never a flake, never a duplicate, never an auto-merge. There
is **no scheduler and no bot**; the only standing CI change is the fast `make
fuzz` seed replay in the existing PR gate, which makes an auto-filed crasher's
red-until-fixed regression test keep its PR un-mergeable-green until the bug is
fixed.

```sh
make fuzz-triage                              # search all surfaces, open a PR per new deterministic bug
make fuzz-triage FUZZ_ARGS="--time 5m"        # longer per-target budget
make fuzz-triage FUZZ_ARGS=--no-pr            # commit artifacts to a local branch, stop before the PR
make fuzz-triage FUZZ_ARGS=--dry-run          # discover + decide, write nothing, open nothing
scripts/fuzz-triage.sh llm:FuzzParseSSE       # restrict to one target (run-fuzz.sh TARGETS entry)
```

What a run does, in order:

1. **Reconcile the ledger** — replay every `found` entry on the current tree; any
   that now passes flips to `fixed` (the cheap, scheduler-free fixed-count).
2. **Search** under `EVENER_FUZZ_PERSIST=1` (see below) for `--time` per target.
3. **Discover** new crashers as files that appeared since a pre-run `git status`
   snapshot: Go-native `…/testdata/fuzz/<FuzzName>/<hash>` files, and promoter
   `testregression_*_test.go` files.
4. **Flake-guard** each Go-native crasher — re-run the saved corpus entry **K=5**
   times; deterministic only if it fails all K. A crasher that passes any replay
   is reverted, logged `quarantined`, and never filed. (Promoter crashers already
   passed `Promote`'s K-replay guard, so the emitted test's mere existence is the
   proof.)
5. **Dedup**, three layers: the committed bucket store, Go's content-addressed
   corpus (a re-found input produces no new file), and `gh pr list` + the ledger
   by the deterministic branch name `fuzz/crash-<sig12>`.
6. **Open one PR** (default) per surviving, novel, deterministic crash, via the
   developer's **local `gh`** auth — no `GH_TOKEN`, no CI permissions. If `gh` is
   missing/unauthenticated the tool stops before any push and leaves the artifacts
   on a local branch (same as `--no-pr`).
7. **Promote the coverage-expanding corpus** — copy up to `EVENER_FUZZ_MAX_SEEDS`
   (default 8 per target per run) new inputs from Go's fuzz cache into the
   committed `testdata/fuzz/<FuzzName>/` seeds.

### `EVENER_FUZZ_PERSIST` (default off)

The two rapid-runner surfaces emit their regression test + bucket record through
`promoter.Promote`. In the gate they construct it against throwaway temp dirs so
a fuzz run never dirties the tree. `promoter.PersistPaths` (in `promoter/`,
stdlib-only — it reads the env directly rather than via evener's `envvars` registry
to keep the no-evener-deps boundary) switches the emit dir to the surface's package
and the bucket store to the committed **`fuzz/state/buckets.json`** only when
`EVENER_FUZZ_PERSIST` is truthy — which `fuzz-triage.sh` sets for the search. Unset
(every `make fuzz` / `make test` / gate run), behavior is byte-identical to
before: the promoter still runs and is still tested, but writes nothing.

### Triage ledger (`fuzz/state/ledger.json`)

A committed, signature-keyed record of every distinct finding — `found` (PR
filed), `fixed` (reconciled), `quarantined` (failed the flake-guard, with
`survived_runs`). `make fuzz-ledger` pretty-prints the counts and the open-bug
list. The bucket store and ledger live under `fuzz/state/` and ride into a
crasher's PR branch, so dedup memory survives across runs and across developers
who pull `main`.

### Testing the triage logic

fuzz-triage has no automated test today: an old attempt drove it with a
stubbed `go`/`gh` toolchain, which docs/developing-evener/testing.md's ban
on faking the toolchain in a test rules out. The header's contract and the
ledger/bucket file formats are the living contract; honest tests arrive
when the tool ports to Go
(docs/superpowers/specs/2026-08-17-dev-tooling-in-go-design.md,
"port-on-touch").

### Secret handling

The tool only persists fuzzer-generated bytes (synthetic, from `testdata/fuzz`)
and the promoter's minimized artifacts — low secret risk. If real-traffic corpus
harvesting (`evener fuzz-harvest`, above) ever feeds this tool, scrubbing is *its*
gate, not the triage tool's.

## Continuous loop (`make fuzz-continuous`)

`scripts/fuzz-continuous.sh` wraps the triage capstone in a rotation: it gives
each native target a bounded search turn, round after round, until `--total`
elapses or Ctrl-C. Same no-scheduler stance — you start and stop it. Each turn is
a `fuzz-triage` run for one target (with per-turn `--no-corpus`), so crashers are
flake-guarded, deduped, and PR'd by the same pipeline; the loop adds rotation, a
total budget, and a session summary that lists any new ledger signatures.

```sh
make fuzz-continuous                                   # all native targets, until Ctrl-C
make fuzz-continuous FUZZ_ARGS="--total 2h --time 5m"  # bounded session, 5m per turn
make fuzz-continuous FUZZ_ARGS="--sweep agent:FuzzPluginManifestParse"  # one-pass over a subset
```

Round-robin (default, one target per turn) or `--sweep` (all selected targets per
round). Rapid targets are excluded — they are bounded property checks, not
coverage-guided searches that deepen across turns via `$GOCACHE/fuzz`. Seams
`EVENER_FUZZ_RUNNER` (registry) and `EVENER_FUZZ_TRIAGE` (per-turn engine) exist
for advanced use; the tool has no automated test of its own today (a
stub-driven attempt was rejected under the same fake-toolchain policy,
docs/developing-evener/testing.md).

## Regression bisect (`make fuzz-bisect`)

`scripts/fuzz-bisect.sh` finds the commit that introduced a crasher. Given a
target, a saved Go corpus file (`--crasher`, begins `go test fuzz v1`), and a
known-good ref (`--good`; `--bad` defaults to `HEAD`), it confirms the crash
reproduces at `--bad` and not at `--good`, then drives `git bisect run`, replaying
that one corpus entry at each step under `-tags evenerfuzz`. A commit where the
target does not build or does not yet exist is **skipped** (exit 125), not
misjudged; the working tree is restored on exit. Seams `EVENER_FUZZ_GO` and
`EVENER_FUZZ_TAGS` (plus `EVENER_FUZZ_RUNNER`) let the tool be pointed at a
different toolchain or a throwaway repository instead of the real one.

## Oracles (never bare "no panic")

- **SSE** — chunk-invariance (the parser must yield identical events whether fed
  one byte at a time or in one buffer) plus blocking-vs-timeout path agreement.
- **AppWire frame / params** — decode→encode is idempotent after one
  normalization pass (catches codec instability that "no panic" misses).
- **Tool args** — panic-hunt on the `Schema.Validate` seam, which is *not*
  recover-wrapped at runtime. The target drives decode+validate only; it does not
  execute tools (shell/web/job are non-deterministic and unsandboxable by a
  temp-dir env).
- **Internal invariants** — `invariant.Hold()` assertions in production code, live
  under `-tags evenerfuzz`, caught by the no-panic oracle (the `…SeqFuzz` rapid
  models also assert sequence invariants externally).
- **Differential** — two paths that must agree: cross-provider, stream-vs-non-stream,
  golden/snapshot, and two-path equivalence. This class found both real decoder
  bugs this codebase has caught.
- **Structure-aware generation** — the `Fuzz…Structured` targets generate
  valid-but-adversarial inputs so search reaches deep logic instead of bouncing off
  the parser.

See [`docs/developing-evener/fuzzing.md`](../docs/developing-evener/fuzzing.md) → *Choosing an oracle* for the full
table and when to use each.

## Seed corpus

Seeds are inline `f.Add(...)` calls in each target (the Go-native convention):
canonical edge values plus OWASP-style adversarial shapes (wrong types, deep
nesting, huge numbers, invalid UTF-8, lone surrogates). `corpus/` is reserved for
cross-surface OWASP fuzz-vector files shared by the HTTP surface in a later phase.

On top of those hand-written seeds, `evener fuzz-harvest` mines **real recorded
traffic** into each target's `testdata/fuzz/<FuzzName>/` (Go auto-loads it under
`make fuzz`) — real provider framing quirks and tool-argument shapes for free.

## Harvesting real traffic into the corpus

`cmd/evener-fuzz-harvest` walks recorded evener state and emits seeds:

| Surface | Source | Targets |
|---|---|---|
| `sse` | canonical `sessions/*.api.jsonl` `api_attempt` response bodies | `FuzzParseSSE` + the matching provider metamorphic decoder |
| `toolargs` | transcript tool-call args (always recorded) | `FuzzToolArgsValidate` |
| `appwire` | `appwire-frames.jsonl` (needs `EVENER_RECORD_APPWIRE=1`) | `FuzzMessageDecode`, `FuzzMethodParams` |
| `http` | `hub-http.jsonl` (needs `EVENER_RECORD_HTTP=1`) | `FuzzWebHandler` (GET routes reverse-mapped) |
| `jobs` | `sessions/<SID>/jobs.jsonl` (always recorded) | staged for 8.1's jobstore-Event targets |

```sh
evener fuzz-harvest                       # default: all surfaces, both state roots, shape-scrubbed
evener fuzz-harvest --surface sse,toolargs --dry-run
evener fuzz-harvest --state-dir ~/.local/state/evener --surface toolargs
```

**Sanitization is the load-bearing part.** By default every string/number leaf
is **shape-scrubbed** to a length-bucketed placeholder; structure, SSE framing,
and a small allowlist of structural enum values (`type`, `role`, `status`,
`kind`, `object`, `finish_reason`, `event`) survive — nothing else. So committed
seeds carry no PII or secrets *by construction*, and content-hash dedup collapses
near-identical traffic so "commit everything" stays bounded and re-runs are
idempotent. An always-on abort gate (high-confidence secret regexes; an entropy
quarantine under `--keep-values`) drops any leaking seed and fails the run.

`--keep-values` (real values, never committed) is refused unless
`EVENER_FUZZ_CAPTURE_ENV=1` marks a dedicated capture box, and is forced off for a
personal evener state root source.

### The opt-in recorders

AppWire frames and inbound hub HTTP requests are not written to disk in normal
operation. Two default-off recorders capture them for harvesting, with **no
behavior change** when their env var is unset:

- `EVENER_RECORD_APPWIRE=1` → `<stateRoot>/appwire-frames.jsonl` (WS frame recorder
  in `appwire.WSTransport`).
- `EVENER_RECORD_HTTP=1` → `<HubStateRoot>/hub-http.jsonl` (hub HTTP middleware).

These recorder logs hold raw, unscrubbed bytes and are **never committed**;
scrubbing happens only in the harvester. Canonical per-session API logs also
contain exact provider request/response bodies and must receive the same private
handling; their recording is independent of these fuzz-recorder switches.

## Secret scanning (gitleaks)

```sh
make secret-scan        # gitleaks over the whole repo (part of `make lint`)
make fuzz-corpus-scan   # gitleaks over only the fuzz seed corpora
```

Both use the committed `.gitleaks.toml`; the harvester's write-time barrier
shells out to the same engine so the writer and the repo gate cannot drift. When
gitleaks is not installed the scan skips with a warning (it is required in CI).
