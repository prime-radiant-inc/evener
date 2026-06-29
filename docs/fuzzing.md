# Fuzzing serf — a developer's guide

This is the front door to serf's fuzzing toolkit: a large set of `testing.F`
("native") and `rapid.Check` ("stateful") targets that exercise every package
that decodes untrusted/model-generated input and every API surface, plus the
tooling that measures coverage, gates completeness, and turns any failure into a
permanent regression test. Start here, then follow the pointers.

| You want to… | Read |
| --- | --- |
| run/operate the fuzzer day to day | this doc |
| operational reference (env vars, triage internals, recorders) | [`fuzz/README.md`](../fuzz/README.md) |
| **add** a fuzz target to a new surface (the methodology) | [`docs/skills/fuzzing-an-api-surface/SKILL.md`](skills/fuzzing-an-api-surface/SKILL.md) |
| the architecture / why it's built this way | [`docs/design/fuzzing-toolkit-design.md`](design/fuzzing-toolkit-design.md) |

`scripts/run-fuzz.sh`'s `TARGETS` array is the **single source of truth** for
every target; `--list` emits it and the coverage/triage/gap tools all consume it.
Each entry is tagged `native` (driven by `go test -fuzz`) or `rapid` (a `Test*`
function driven by `rapid.Check` under ordinary `go test`).

## Running it

There are two modes: the **gate** (deterministic, fast, no search) and the
**search** (coverage-guided, finds new inputs).

```sh
make fuzz            # GATE: replay the committed seed corpus + saved crashers
                     # across every Fuzz* target. Deterministic, no search.
                     # This is what CI runs; keep it green.

make fuzz-nightly                          # SEARCH: coverage-guided, all targets, 60s each
make fuzz-nightly FUZZ_ARGS="--time 10m"   #   …10 minutes each (any go -fuzztime value)
scripts/run-fuzz.sh llm:FuzzParseSSE       # SEARCH: just one target (module:FuzzName)
```

`make fuzz-nightly` is `scripts/run-fuzz.sh`; native targets run via
`go test -fuzz`, rapid targets via `go test -run`. Consecutive searches go
**deeper**: Go persists the coverage-expanding corpus in `$GOCACHE/fuzz`, so each
run starts from a richer corpus than the last. A failing input is auto-saved (see
[Triaging a crasher](#triaging-a-crasher)).

```sh
make fuzz-gap-check  # FAST static gate (the blocking CI floor): every decode/parse
                     # package has a target or a reasoned ignore. No coverage replay.
make fuzz-coverage   # measure focus-set coverage per target + print the gap map
```

## Memory safety (don't take the host down)

A coverage-guided search accumulates its corpus in memory, and a long run on a
target with large inputs — or several runs at once — can climb into tens of GB.
Left unbounded, that fires the kernel's **global** OOM killer, which has twice
taken the whole host (and its network) down, requiring a manual reboot.

So every heavy target runs under a hard memory ceiling, enforced by cgroup-v2 via
`scripts/run-capped.sh` and wired into the Makefile (`make test`, `make fuzz`,
`make fuzz-nightly`, `make fuzz-coverage`, `make fuzz-triage`) and into
`scripts/run-fuzz.sh` itself, so even a **direct** `scripts/run-fuzz.sh` is
protected. There are two ceilings:

- **per run** (`SERF_MEM_MAX`, default 16G) — one runaway is OOM-killed alone;
- **shared total** (`SERF_MEM_TOTAL`, default 32G) — all concurrent serf runs
  join one slice, so launching several at once still can't exhaust the host.

A runaway now shows up as a *scope/slice* OOM in `journalctl --user`, never
`global_oom`, and `tailscale`/SSH stay up throughout. Tune the ceilings for a
bigger box (`SERF_MEM_MAX=24G make fuzz-nightly`) or disable entirely with
`SERF_MEM_MAX=0`. Where systemd user scopes aren't available (some CI
containers) the wrapper prints a warning and runs uncapped — CI runners impose
their own cgroup limit.

If a *single* target legitimately needs more than the per-run cap, that is a
signal worth a `go test -memprofile` pass: a search that grows without bound is
usually retaining inputs/state it should free between iterations.

## Reading the coverage map

`make fuzz-coverage` replays each target's committed corpus under `-coverprofile`
and prints, per target, two numbers:

- **focus-set %** — coverage of the decode/parse **seam** the target is meant to
  drive (the `focus` field in the registry: a file like `sse.go`, a function like
  `adapter.go#decodeStream`, or empty = the whole SUT package). This is the
  primary, drive-toward-100 metric.
- **whole-package %** — secondary context.

It also prints the **gap map**: decode/parse packages with *zero* fuzz coverage
(none should remain except the toolkit's own packages, listed with reasons in
`scripts/fuzzcov-ignore.txt`).

The focus % is ratcheted: floors live in `scripts/fuzzcov-floors.txt` and only go
up. After legitimately raising a target's coverage, lock it in:

```sh
make fuzz-coverage CHECK=1   # fail on a focus-set regression OR a gap breach (local/manual)
make fuzz-coverage BLESS=1   # raise each floor to the current measured %
```

Interpreting a **low focus %**: use `go tool cover -func` on the profile to see
which blocks are uncovered. A block with a `0` count that a crafted input *could*
reach means a missing seed — add an `f.Add` for it. A block that no input can
reach from the seam (an unreachable error arm, a result type the target's tools
never return) is genuinely unreachable — document it in the target with a one-line
reason rather than chasing it.

## Triaging a crasher

When a search finds a failure, the Go toolchain **auto-saves the input** to
`<pkg>/testdata/fuzz/<FuzzName>/<hash>` and that file becomes a permanent
regression seed `make fuzz` replays forever. To work it:

```sh
go test ./<pkg>/ -run '<FuzzName>/<hash>'              # reproduce the one input
go test ./<pkg>/ -run '^$' -fuzz '^<FuzzName>$' -fuzzminimize   # shrink it
```

Then root-cause it, fix the bug (TDD — confirm the saved seed goes red→green), and
**keep the crasher committed** as the regression. If the fix is a behavioral
decision rather than a clear bug, leave the seed and scope the oracle, with a
comment, so the finding stays visible.

For an on-demand campaign that triages for you, `make fuzz-triage` runs the search,
applies a flake-guard (a failure must reproduce K times to count), dedups against
prior findings, and opens a PR by default carrying the generated regression test.
See [`fuzz/README.md`](../fuzz/README.md) for its flags and `SERF_FUZZ_PERSIST`.

## When the gap gate fails on your PR

`make fuzz-gap-check` blocks a PR that adds a **decode/parse package with no fuzz
target**. Two ways to clear it:

1. **Add a target** for the package's real decode seam — follow
   [the skill](skills/fuzzing-an-api-surface/SKILL.md) and register it in
   `scripts/run-fuzz.sh`. This is almost always the right answer.
2. If the package genuinely has no real parse surface (a generated file, pure
   tooling), add it to `scripts/fuzzcov-ignore.txt` **with a reason** — the gate
   rejects a reasonless entry, so it's reviewed like code.

## Choosing an oracle

"Never panic" is the floor, never the whole oracle — a program can be deeply wrong
without crashing. Pick the strongest oracle the surface admits:

| Surface shape | Oracle | Example target |
| --- | --- | --- |
| any | **never panic** (floor) | every target |
| a codec (decode↔encode) | **round-trip / fixed point** — decode→encode→decode is stable | `FuzzMessageDecode`, `FuzzSessionMetaRoundTrip` |
| a parser with a semantics-preserving transform | **metamorphic** — re-chunking / whitespace / reordering must not change the result | `FuzzParseSSE`, `FuzzOpenAIResponsesMetamorphic` |
| a state machine / replayed log | **invariant / monotonicity** — status only advances, history doesn't shrink, no orphaned state | `FuzzJobEventLogReplay`, `TestRouterSeqFuzz`, `TestLifecycleSeqFuzz` |
| an HTTP / RPC handler | **never 5xx**, **never wedge**, **never escape** the sandbox FS | `FuzzWebHandler`, `FuzzWebMutatingHandler`, `FuzzAppWireDispatch` |
| a decoder whose output must not silently drift | **golden / snapshot differential** — the decoded output of the committed corpus, canonically re-encoded and pinned; a refactor that changes it (no panic, round-trip still holds) fails the gate | `appwire/golden_test.go` (`TestMessageDecodeGolden`, `TestMethodParamsGolden`, `TestCodexItemDecodeGolden`) |

## The golden / snapshot differential

The round-trip and never-panic oracles catch a decoder that *crashes* or
*can't re-read its own output*. Neither notices a refactor that quietly changes
**what** a clean decode produces — a dropped field, a remapped value, a reordered
shape — when nothing panics and the fixed point still holds. The golden snapshot
is that missing **differential** oracle: it compares the current decoders against
a committed picture of their own past behavior.

For each appwire decode target, `appwire/golden_test.go` replays the **committed
seed corpus** (shared verbatim with the fuzz target via a package-level seed
slice, so there is one source of truth), decodes each input, canonically
re-encodes the decoded value (`encoding/json` sorts map keys, so the bytes are
stable run-to-run), and stores the result under
`appwire/testdata/golden/<Target>.json`. The check runs in `make test` and
`make fuzz`; it fails on any diff.

```sh
make fuzz-goldens   # REGEN: rewrite the snapshots from the current decoders.
                    # Run ONLY after an INTENDED decoder change, then commit the diff.
```

A decode that *errors* records only `"decoded": false`, never the error text:
stdlib error strings are not part of our decoder's contract and churn across Go
toolchains, so snapshotting them would flake on a compiler upgrade instead of
flagging a real behavior change.

## Secret scanning & corpus harvesting

The seed corpora include shape-scrubbed real traffic (`cmd/serf-fuzz-harvest`
sanitizes recorded sessions into seeds), so two gitleaks scans guard against a
real secret slipping in:

```sh
make secret-scan        # gitleaks over the whole working tree
make fuzz-corpus-scan   # gitleaks over the committed seed corpora
```

Deliberately-fake test keys and a few non-secret false positives are allowlisted
in `.gitleaks.toml` (regexes + a `docs/superpowers/` path entry); the corpora are
*not* path-allowlisted, so the corpus scan genuinely inspects the seeds. gitleaks
must be installed for these to run (they warn-skip if absent; CI installs it).
See [`fuzz/README.md`](../fuzz/README.md) for harvesting and the recorders.
