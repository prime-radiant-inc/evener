# serf fuzzing + failure-to-regression toolkit

This module (`primeradiant.com/serf/fuzz`) is the **serf-agnostic core** of the
toolkit: the failure→regression promoter (and, later, the schema→generator).
Nothing here imports any `primeradiant.com/serf` package — `go.mod` declares no
serf dependency, so the module will not build if that boundary is violated. That
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
make fuzz-coverage  # focus-set coverage map + gap map (committed corpus, deterministic)
scripts/run-fuzz.sh --time 5m            # all targets, 5 min each
scripts/run-fuzz.sh llm:FuzzParseSSE     # one target
scripts/run-fuzz.sh --list               # the target list (single source of truth)
```

## Coverage measurement (`make fuzz-coverage`)

`make fuzz-coverage` answers the question `make fuzz` cannot: *how much of each
parse surface does the corpus actually exercise?* It replays every target's
**committed corpus** under `go test -coverprofile` (no `-fuzz`, so deterministic
and reproducible from a clean checkout), then `cmd/serf-fuzzcov` reports, per
target:

- **FOCUS-SET %** — the primary, drivable-to-100% metric: line coverage of the
  specific decode/parse seam the target fuzzes. The seam is declared as the
  trailing `focus` field of each `scripts/run-fuzz.sh` `TARGETS` entry (a file
  like `sse.go`, or a function like `adapter.go#decodeStream`); an empty focus
  means the whole SUT package.
- **FLOOR** — the committed ratchet value from `scripts/fuzzcov-floors.txt`. A
  surface's focus % may never drop below its floor.
- **PKG %** — the secondary whole-package number, for visibility / zero-spotting
  only. A narrow decoder legitimately scores low here (e.g. the appwire frame
  decoder is ~14% of all of `appwire`, which also holds the client/transport/
  router glue it has no business covering).

It also prints a **GAP MAP**: every decode/parse package across the workspace
(`. agent llm auth fuzz`) with *zero* fuzz coverage — the holes where no target
exists yet. Genuinely out-of-scope packages are excluded via the
reason-required `scripts/fuzzcov-ignore.txt` (each entry needs a `# reason`; a
reasonless entry is a hard error, so the file is reviewed like code).

```sh
make fuzz-coverage           # advisory: print the report, exit 0
make fuzz-coverage CHECK=1   # exit non-zero on a focus-set regression or an un-ignored gap
make fuzz-coverage BLESS=1   # raise the ratchet floors to the current measured %
```

The single source of truth for the target list is `scripts/run-fuzz.sh --list`;
`scripts/fuzz-coverage.sh` consumes it, so the report lines and the campaign
runner can never drift. The focus % is driven toward 100% by committing the
coverage-expanding inputs that `make fuzz-nightly` discovers into
`testdata/fuzz/<FuzzName>/`, then `make fuzz-coverage BLESS=1` to lock the gain
into the floor. Enforcement is **advisory** today (decision per the 8.6 plan);
the gap floor is the candidate for the existing `ci.yml` PR gate later.

`make fuzz` runs `go test -run '^Fuzz' ./...` per module: with no `-fuzz` flag
the toolchain does **not** random-search — it replays each target's seed corpus
plus any saved `testdata/fuzz/<FuzzName>/` crashers as ordinary, deterministic
unit tests. `make test` already executes these seeds (Go runs Fuzz seed corpora
as subtests), so the gate covers them; `make fuzz` is the explicit entry point.

## The free regression loop

When `make fuzz-nightly` (or `go test -fuzz`) finds a failing input, the Go
toolchain writes it to that target's `testdata/fuzz/<FuzzName>/<hash>`. That file
is committed and re-runs forever as a deterministic regression test — no promoter
needed for single-input `testing.F` targets. The promoter in `promoter/` exists
for the surfaces Go does *not* auto-promote (stateful sequences, non-`testing.F`
runners).

## Oracles (never bare "no panic")

- **SSE** — chunk-invariance (the parser must yield identical events whether fed
  one byte at a time or in one buffer) plus blocking-vs-timeout path agreement.
- **AppWire frame / params** — decode→encode is idempotent after one
  normalization pass (catches codec instability that "no panic" misses).
- **Tool args** — panic-hunt on the `Schema.Validate` seam, which is *not*
  recover-wrapped at runtime. The target drives decode+validate only; it does not
  execute tools (shell/web/job are non-deterministic and unsandboxable by a
  temp-dir env).

## Seed corpus

Seeds are inline `f.Add(...)` calls in each target (the Go-native convention):
canonical edge values plus OWASP-style adversarial shapes (wrong types, deep
nesting, huge numbers, invalid UTF-8, lone surrogates). `corpus/` is reserved for
cross-surface OWASP fuzz-vector files shared by the HTTP surface in a later phase.

On top of those hand-written seeds, `serf-fuzz-harvest` mines **real recorded
traffic** into each target's `testdata/fuzz/<FuzzName>/` (Go auto-loads it under
`make fuzz`) — real provider framing quirks and tool-argument shapes for free.

## Harvesting real traffic into the corpus

`cmd/serf-fuzz-harvest` walks recorded serf state and emits seeds:

| Surface | Source | Targets |
|---|---|---|
| `sse` | `api-raw.jsonl` stream bodies (needs `SERF_LOG_RAW_HTTP=1` when the traffic flows) | `FuzzParseSSE` + the matching provider metamorphic decoder |
| `toolargs` | transcript tool-call args (always recorded) | `FuzzToolArgsValidate` |
| `appwire` | `appwire-frames.jsonl` (needs `SERF_RECORD_APPWIRE=1`) | `FuzzMessageDecode`, `FuzzMethodParams` |
| `http` | `hub-http.jsonl` (needs `SERF_RECORD_HTTP=1`) | `FuzzWebHandler` (GET routes reverse-mapped) |
| `jobs` | `sessions/<SID>/jobs.jsonl` (always recorded) | staged for 8.1's jobstore-Event targets |

```sh
serf-fuzz-harvest                       # default: all surfaces, both state roots, shape-scrubbed
serf-fuzz-harvest --surface sse,toolargs --dry-run
serf-fuzz-harvest --state-dir ~/.local/state/serf --surface toolargs
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
`SERF_FUZZ_CAPTURE_ENV=1` marks a dedicated capture box, and is forced off for a
personal `~/.serf` source.

### The recorders (opt-in, default off)

AppWire frames and inbound hub HTTP requests are not written to disk in normal
operation. Two default-off recorders capture them for harvesting, with **no
behavior change** when their env var is unset:

- `SERF_RECORD_APPWIRE=1` → `<stateRoot>/appwire-frames.jsonl` (WS frame recorder
  in `appwire.WSTransport`).
- `SERF_RECORD_HTTP=1` → `<HubStateRoot>/hub-http.jsonl` (hub HTTP middleware).

These logs hold raw, unscrubbed bytes (like `api-raw.jsonl`) and are **never
committed**; scrubbing happens only in the harvester.

## Secret scanning (gitleaks)

```sh
make secret-scan        # gitleaks over the whole repo (part of `make lint`)
make fuzz-corpus-scan   # gitleaks over only the fuzz seed corpora
```

Both use the committed `.gitleaks.toml`; the harvester's write-time barrier
shells out to the same engine so the writer and the repo gate cannot drift. When
gitleaks is not installed the scan skips with a warning (it is required in CI).
