# Fuzzing evener — a developer's guide

This is the front door to evener's fuzzing toolkit: a large set of `testing.F`
("native") and `rapid.Check` ("stateful") targets that exercise every package
that decodes untrusted/model-generated input and every API surface, plus the
tooling that measures coverage, gates completeness, and turns any failure into a
permanent regression test. Start here, then follow the pointers.

| You want to… | Read |
| --- | --- |
| run/operate the fuzzer day to day | this doc |
| operational reference (env vars, triage internals, recorders) | [`fuzz/README.md`](../../fuzz/README.md) |
| **add** a fuzz target to a new surface (the methodology) | [`docs/skills/fuzzing-an-api-surface/SKILL.md`](../skills/fuzzing-an-api-surface/SKILL.md) |
| the architecture / why it's built this way | [`docs/design/fuzzing-toolkit-design.md`](../design/fuzzing-toolkit-design.md) |

`scripts/fuzz/run-fuzz.sh`'s `TARGETS` array is the **single source of truth** for
every target; `--list` emits it and the coverage/triage/gap tools all consume it.
Each entry is tagged `native` (driven by `go test -fuzz`) or `rapid` (a `Test*`
function driven by `rapid.Check` under ordinary `go test`).

## Running it

There are two modes: the **gate** (deterministic, fast, no search) and the
**search** (coverage-guided, finds new inputs).

```sh
make fuzz            # GATE: replay native committed seeds/crashers and every
                     # registered Rapid surface across its fixed seed bank.
                     # Deterministic, no search. This is what CI runs; keep it green.

make fuzz-nightly                          # SEARCH: coverage-guided, all targets, 60s each
make fuzz-nightly FUZZ_ARGS="--time 10m"   #   …10 minutes each (any go -fuzztime value)
scripts/fuzz/run-fuzz.sh llm:FuzzParseSSE       # SEARCH: just one target (module:FuzzName)
```

`make fuzz-nightly` is `scripts/fuzz/run-fuzz.sh`; native targets run via
`go test -fuzz`, rapid targets via `go test -run`. Consecutive searches go
**deeper**: Go persists the coverage-expanding corpus in `$GOCACHE/fuzz`, so each
run starts from a richer corpus than the last. A failing input is auto-saved (see
[Triaging a crasher](#triaging-a-crasher)).

```sh
make fuzz-gap-check  # FAST static gate (the blocking CI floor): every decode/parse
                     # package has a target or a reasoned ignore. No coverage replay.
```

## Memory safety

A coverage-guided search accumulates its corpus in memory, and a long run on a
target with large inputs can climb into tens of GB. Run searches on a host that
can spare the RAM; if a single target legitimately needs more than the machine
comfortably has, that is a signal worth a `go test -memprofile` pass: a search
that grows without bound is usually retaining inputs/state it should free
between iterations.

## Coverage

`make coverage-floor` is the "how much is exercised" number; run it before
concluding a package is under-tested. For what it measures and why a
default-gate `-cover` run answers neither "how much is covered" nor "how well
is this tested", see [A Coverage Number Is Two Tracks, Not
One](coverage.md#a-coverage-number-is-two-tracks-not-one) in `coverage.md`.

The blocking fuzz gate is the static `make fuzz-gap-check` above, not a
coverage replay. The rapid (seqfuzz/schemafuzz) family is env-gated and takes
part in neither track — its assurance comes from `make fuzz` and
`make test-fuzz`, not from a coverage number.

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
See [`fuzz/README.md`](../../fuzz/README.md) for its flags and `EVENER_FUZZ_PERSIST`.

## Continuous fuzzing (local, on-demand)

`make fuzz-continuous` rotates over every native target, giving each a bounded
search turn round after round, until you Ctrl-C or a `--total` budget elapses.
It is deliberately **local and on-demand — there is no scheduled CI**: you start
it when you want cycles and stop it when you don't. Each turn delegates to
`fuzz-triage`, so a new deterministic crash is flake-guarded, deduped, and turned
into one reviewable PR — the same pipeline, driven continuously.

A loop beats one long run because Go's coverage-guided corpus persists in
`$GOCACHE/fuzz` across invocations: each turn a target gets **resumes** where the
last left off and goes deeper, while rotation keeps every target progressing
instead of spending the whole budget on the first.

```sh
make fuzz-continuous                                   # all native targets, until Ctrl-C
make fuzz-continuous FUZZ_ARGS="--total 2h --time 5m"  # 2h session, 5m per target per turn
make fuzz-continuous FUZZ_ARGS="--sweep llm:FuzzParseSSE agent:FuzzPluginManifestParse"
```

Rapid targets are excluded from the rotation — they are bounded property checks
the normal suite runs, not coverage-guided searches that deepen with corpus. Per
turn the loop passes `--no-corpus` (the committed corpus is promoted by an
explicit `make fuzz-triage`); the `$GOCACHE` corpus still accumulates regardless.

## Pinpointing a regression with bisect

Once you have a saved crasher file, `make fuzz-bisect` finds the commit that
introduced it. It drives `git bisect run`, replaying that one corpus entry at
each step (a commit where the target does not build, or does not yet exist, is
skipped, not misjudged), and confirms the crash reproduces at `--bad` but not at
`--good` before bisecting. The working tree is restored afterward.

```sh
make fuzz-bisect FUZZ_ARGS="--target llm:FuzzParseSSE \
  --crasher llm/testdata/fuzz/FuzzParseSSE/<hash> --good <ref>"
```

`--crasher` must be the Go fuzz corpus file (it begins `go test fuzz v1`) — the
file the toolchain saved when it found the crash. `--bad` defaults to `HEAD`.

## When the gap gate fails on your PR

`make fuzz-gap-check` blocks a PR that adds a **decode/parse package with no fuzz
target**. Two ways to clear it:

1. **Add a target** for the package's real decode seam — follow
   [the skill](../skills/fuzzing-an-api-surface/SKILL.md) and register it in
   `scripts/fuzz/run-fuzz.sh`. This is almost always the right answer.
2. If the package genuinely has no real parse surface (a generated file, pure
   tooling), add it to `scripts/coverage/fuzzcov-ignore.txt` **with a reason** — the gate
   rejects a reasonless entry, so it's reviewed like code.

## Choosing an oracle

"Never panic" is the floor, never the whole oracle — a program can be deeply wrong
without crashing. Pick the strongest oracle the surface admits:

| Surface shape | Oracle | Example target |
| --- | --- | --- |
| any | **never panic** (floor) | every target |
| a codec (decode↔encode) | **round-trip / fixed point** — decode→encode→decode is stable | `FuzzMessageDecode`, `FuzzSessionMetaRoundTrip` |
| a parser with a semantics-preserving transform | **metamorphic** — re-chunking / whitespace / reordering must not change the result | `FuzzParseSSE`, `FuzzOpenAIResponsesMetamorphic` |
| a state machine / replayed log | **external invariant / monotonicity** (rapid sequences) — status only advances, history doesn't shrink, no orphaned state | `FuzzJobEventLogReplay`, `TestRouterSeqFuzz`, `TestLifecycleSeqFuzz`, `TestJobstoreSeqFuzz`, `TestCompactionSeqFuzz`, `TestHubMultiSessionSeqFuzz` |
| any subsystem with a load-bearing internal assumption | **internal invariant** — `invariant.Hold()` asserts it at the point logic could first go wrong; live only under `-tags evenerfuzz`, so the never-panic oracle catches it (see [Internal invariants under evenerfuzz](#internal-invariants-under-evenerfuzz)) | the `invariant.Hold` sites in `appprojector`, `apptranscript`, `appwire`, `jobstore`, the llm decoders |
| an HTTP / RPC handler | **never 5xx**, **never wedge**, **never escape** the sandbox FS | `FuzzWebHandler`, `FuzzWebMutatingHandler`, `FuzzAppWireDispatch` |
| a decoder whose output must not silently drift | **golden / snapshot differential** — the decoded corpus output, canonically re-encoded and pinned; a refactor that changes it (no panic, round-trip still holds) fails the gate | `appwire/golden_test.go` |
| two code paths that must compute the same thing | **differential** — drive both from one input, assert they agree modulo an allow-list (the strongest class; it found both real decoder bugs) | `FuzzCrossProviderDifferential`, `FuzzStreamVsNonStreamDifferential`, `FuzzTranscriptReadersAgree`, `FuzzTurnPagingEquivalence` |

## Internal invariants under evenerfuzz

External oracles (round-trip, no-panic) only see a bug once it reaches a surface.
Internal invariants catch a *logic* bug at the point it first happens. The
`primeradiant.com/evener/invariant` module exposes:

```go
invariant.Hold(cond bool, format string, args ...any)  // assert cond; else panic with the message
invariant.Enabled                                      // untyped const: false in prod, true under evenerfuzz
```

`Hold` is **zero-cost in a normal build**: the no-op form is inlined away — the
condition is not evaluated and the args are not boxed (verified by disassembly), so
production binaries are byte-unchanged. Built with **`-tags evenerfuzz`** a violated
invariant panics, so the existing never-panic oracle reports it for free. The whole
fuzz path (`make fuzz`, `run-fuzz.sh`, `fuzz-triage`) builds with
that tag automatically; `make test` and `go build` stay tag-free.

To add one: import the module and assert a load-bearing assumption where it must
hold (e.g. a folded job status never leaves a terminal state; an emitted item
carries its turn id). Conditions and args must be **side-effect-free** (they don't
run in production). For an invariant whose *condition* is expensive to compute,
guard it with `if invariant.Enabled { ... }` so that cost compiles out too. Verify
the invariant is actually TRUE before asserting it — a wrong invariant is a
false-positive crash — and prove it's reached (flip it false, watch a fuzz target
trip it, restore).

## Differential oracles

The strongest class: drive two things that must agree from one input and assert
they match. evener has several flavors — both real decoder bugs this codebase has
found came from here:

- **golden / snapshot** — a decoder vs a committed picture of its own past output
  (below).
- **cross-provider** (`FuzzCrossProviderDifferential`) — one canonical logical
  response encoded to each provider's wire format, decoded by each real adapter,
  asserted equivalent modulo an allow-list. Found the anthropic streaming
  finish-reason bug.
- **stream vs non-stream** (`FuzzStreamVsNonStreamDifferential`) — a provider's
  streaming decoder vs its non-streaming decoder for the same content. The axis
  both real bugs lived on; a standing regression guard for them.
- **two-path equivalence** — independent code paths over the same data:
  `FuzzTranscriptReadersAgree` (the three transcript scanners),
  `FuzzTurnPagingEquivalence` (`WindowTurns` vs `PageTurns`).

Each carries a documented **allow-list** of legitimately path-specific fields
(raw payloads, provider ids, reasoning encoding, …) excluded from the comparison;
a divergence outside the allow-list is a real bug — investigate it, don't widen
the allow-list to hide it.

### Structure-aware generators

Most raw-byte execs die at the first `json.Unmarshal`. The `Fuzz…Structured`
targets instead turn fuzz bytes (via the `fuzz/schemagen` byte `Source`) into a
*valid-but-adversarial* input — an appwire frame, a transcript, a provider SSE
event sequence — so coverage-guided search explores the structured space. Measured
input acceptance rises from ~0% (raw) to ~90–100% (structured). Add one when a
target's surface has a rich grammar and the raw-byte target mostly bounces off the
parser; keep the generator deterministic for the same bytes (no map-range / time /
rand).

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

The seed corpora include shape-scrubbed real traffic (`cmd/evener-fuzz-harvest`
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
See [`fuzz/README.md`](../../fuzz/README.md) for harvesting and the recorders.

## The seqfuzz/schemafuzz Family Lives Only in `make test-fuzz`

A Jesse ruling: no fuzz-family test — including a smoke-depth iteration —
belongs in `make test`, `go test ./...`, or any of their variants. This is a
different exclusion from the `evenerfuzz`-tagged native `FuzzXxx` targets and
`seed100`-style edge suites, which never compile into a default build because
of their build tag. The tests this ruling targets are ordinary
`func TestXxx(t *testing.T)` functions that call `rapid.Check` to run a
stateful/sequence property fuzzer — no build tag hides them, so a plain
`go test ./agent` used to run them, at whatever depth `rapid.Check`'s own
`testing.Short()` awareness picked.

Each test in the family is now individually gated at the top of its body:

```go
func TestDelegateSeqFuzz(t *testing.T) {
	if os.Getenv("EVENER_FUZZ_TESTS") != "1" {
		t.Skip("fuzz: skipped by default; run `make test-fuzz`, or EVENER_FUZZ_TESTS=1 go test ./agent -run TestDelegateSeqFuzz -count=1 -v")
	}
	...
```

The gate is an env-var check rather than reusing `-short`, precisely so that
`go test ./agent` with no flags stays fuzz-free too — matching the ruling's
spirit rather than only its `-short` letter. The gate is the first statement
in every one of these functions, including the two (`TestDelegateSeqFuzz`,
`TestLifecycleSeqFuzz`) that resolve `fuzz/promoter.PersistPaths` before
deciding whether to call `t.Parallel()`: the skip must fire before any of that
path resolution or parallelism decision runs.

The family, by file and function:

| File | Function |
| --- | --- |
| `agent/delegate_seqfuzz_test.go` | `TestDelegateSeqFuzz` |
| `agent/watch_seqfuzz_test.go` | `TestWatchSeqFuzz` |
| `agent/lifecycle_seqfuzz_test.go` | `TestLifecycleSeqFuzz` |
| `agent/jobs_fc2_seqfuzz_test.go` | `TestJobsFc2DescendantMergeSeqFuzz` |
| `agent/registry_schemafuzz_test.go` | `TestToolArgsSchemaFuzz` |
| `agent/internal/contextmgr/compaction_seqfuzz_test.go` | `TestCompactionSeqFuzz` |
| `agent/internal/contextmgr/maybecompact_fc1_seqfuzz_test.go` | `TestFc1MaybeCompactSeqFuzz` |
| `agent/internal/jobstore/seqfuzz_test.go` | `TestJobstoreSeqFuzz` |
| `internal/appserver/router_seqfuzz_test.go` | `TestRouterSeqFuzz` |
| `internal/appserver/hub_multisession_seqfuzz_test.go` | `TestHubMultiSessionSeqFuzz` |

A test that merely has "fuzz" in its name is not automatically in this family
— `TestDelegateSeqFuzzReplayClean`, `TestToolArgsAdapter_PromotesDeterministicFailure`,
and the other promoter-adapter tests alongside these files replay one fixed,
deterministic artifact (or a synthetic failure) rather than running a
`rapid.Check` search; they stay in the default suite because they are fast and
their coverage is otherwise lost. The `job_delegate_seed100_fuzz_test.go` file
and its siblings named for the same "seed100" convention are native `FuzzXxx`
targets under the `evenerfuzz` build tag — already excluded from every default
build, not part of this change.

Three other entry points drive this same rapid family and needed
`EVENER_FUZZ_TESTS=1` threaded through so this ruling would not silently blind
them: `make fuzz`'s fixed-seed rapid replay loop (`scripts/fuzz/run-fuzz.sh`'s
`rapid` case, used by `fuzz-nightly`/`fuzz-triage`/`fuzz-continuous` too) and
`scripts/fuzz/fuzz-oracle-audit.sh`'s `run_seeds`, which replays a mutation against
`TestJobstoreSeqFuzz` and must never read that test's now-default skip as the
oracle failing to catch the mutation.

For how this family's exclusion interacts with the coverage-floor number —
why a default-gate `-cover` run cannot see it, and why that's neither "how
much is covered" nor "how well is this tested" — see [A Coverage Number Is
Two Tracks, Not One](coverage.md#a-coverage-number-is-two-tracks-not-one) in
`coverage.md`.

## Proving a Type Survives a Round Trip

When two code paths must agree about a struct — a decoder and a
projector, a live path and a reload path — a hand-written fixture proves
only that today's fields survive. A field added next month passes,
because nothing in the test knows it exists.

Build the fixture by walking the **type** with reflection instead. Fill
every field with a distinguishable value, decode the same bytes through
both paths, and report divergent fields by name.

`agent/session_client_mutation_doctor_drift_test.go` is the worked
example. `clientMutationSnapshot` is unexported, so evener doctor's
mutations reader mirrors the persisted shape and decodes it with
`DisallowUnknownFields`; the test walks the snapshot type with
reflection and marshals it the way the save path does, so a field the
mirror has never heard of makes the doctor refuse the store and name it.
Verified by adding a synthetic field to `clientMutationSnapshot` and
watching the test name it unprompted, with no test edit.

The gotchas in a fixture builder are all in the leaves. This example
demonstrates two: `json.RawMessage` must contain valid JSON, and any
other `[]byte` marshals as base64. Two more are general technique that
this particular type never exercises, because `clientMutationSnapshot`
carries no `time.Time`, no float and no `any` — a `time.Time` needs whole
seconds wherever the encoding is RFC 3339, and floats must be integral to
survive widening through `any`. The last gotcha is the structural one: an
unhandled `reflect.Kind` must fail loudly rather than skip, because a
builder that silently skips a kind is a test that silently stops covering
it. `fillEveryField` has no `reflect.Interface` case, so an `any` field
added to the snapshot tomorrow stops the test rather than going quietly
untested.

Two corollaries:

- **Always prove it by mutation.** Route one path through a struct that
  omits a field and confirm the test names that field. A drift test that
  has never failed is a decoration.
- **Delete the round-trip test when the second path dies.** Once a
  mirror type is gone, both sides of its round-trip test are the same
  `json.Unmarshal` and it cannot fail. Keeping it leaves a comment
  claiming coverage that no longer exists.

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/fuzzing.mk, then run `make generate`. -->
| Command | Summary | What it proves | Trigger | Requires | Fails when |
| --- | --- | --- | --- | --- | --- |
| `make test-fuzz` | Run the seqfuzz/schemafuzz stateful rapid.Check family at full depth: delegate, watch, lifecycle, jobs descendant-merge, tool-args schema, jobstore, two context-compaction surfaces, appserver router, appserver multi-session. See "The seqfuzz/schemafuzz Family Lives Only in make test-fuzz" in docs/developing-evener/fuzzing.md. | Each surface's rapid state machine runs its full default check count (no -short reduction), catching sequence bugs the focused unit suites cannot. | Local pre-merge/post-merge for these surfaces; not run in CI's default make test job. EVENER_FUZZ_TESTS=1 opts each test back in from its default t.Skip. | No network, no provider calls; fully offline (deny exec env, fake clock, scripted adapters). | Any surface's oracle/invariant failure or panic is nonzero. |
| `make fuzz-seeds` | Replay every module's committed seed corpus (and saved crashers) under the evenerfuzz tag — the runtime half of the tagged-source gate whose compile half is make lint-evenerfuzz. | Every committed evenerfuzz-tagged seed still passes, not merely still compiles. | Absorbed as a step of make fuzz; stands alone for fast iteration. Stays out of make test: 144s across the workspace against make test's ~70s. | go test -run '^Fuzz' with no -fuzz, so deterministic, no search. | Any tagged seed no longer passes. |
| `make fuzz` | Replay every native FuzzXxx target's seed corpus plus saved crashers, and every registered Rapid property surface, as ordinary deterministic tests — the CI fuzz gate. | Fuzz invariants compile and execute, committed fuzz inputs remain safe, Rapid properties replay under a fixed coverage seed bank, and decode goldens remain stable. | Required CI deterministic corpus gate; local pre-merge when warranted. | Builds with -tags evenerfuzz so internal/invariant assertions are live; no fuzz search or provider calls; sets EVENER_FUZZ_TESTS=1 so the seqfuzz/schemafuzz family's default skip does not swallow the replay. | Any compile, replay, invariant, Rapid, or golden failure is nonzero. |
| `make mutation-floor` | Gate the gremlins kill score against a curated efficacy floor. | Each curated package's test efficacy (gremlins mutation kill score) meets the floor. | Nightly/manual; not required CI. Slow. | gremlins installed. | MIN=<n> is set and any curated package's kill efficacy drops below it. With no MIN, this only reports. |
| `make fuzz-oracle-audit` | Prove every fuzz oracle reddens on its bug class by reintroducing a known fault from fuzz/mutations/ in a throwaway worktree. | Each mutation's target FAILS once the mutation is applied — an oracle that stays green on a known bug is caught. FUZZ_ARGS=--gap-only lists native targets with no mutation yet. | Manual, on-demand. | A throwaway worktree per mutation; real go test runs. | A mutated target's oracle does not fail (a blind spot), or a target fails to build under audit. |
| `make fuzz-gap-check` | The fast, static gap gate: assert every decode/parse package has a registered fuzz target, or a reasoned ignore. | Every discovered decode/parse package has a registered fuzz target or an explicit ignore, derived from scripts/fuzz/run-fuzz.sh --list without replaying any corpus. | Required CI; local quick check. | Seconds, deterministic; no network or corpus replay. | An uncovered package, or a registry/tool failure, is nonzero. |
| `make fuzz-registry-check` | Compare native and explicitly marked rapid targets in the authoritative manifest against AST-discovered workspace declarations. | scripts/fuzz/fuzz-targets.txt matches AST-discovered native/Rapid declarations exactly. | Wrapped by the required lint-fuzz-registry; also runs standalone. Well under a second. | Static AST analysis only; no ordinary tests, fuzz search, or network activity. | A discovered target has no registry row, or a registry row has no discovered target. |
| `make fuzz-corpus-scan` | Run gitleaks over only the fuzz seed corpora — the corpus-scoped subset of secret-scan, for fast harvester feedback. | The committed fuzz seed corpora contain no secret matching the gitleaks ruleset. The corpora are deliberately not path-allowlisted, so the seeds are genuinely inspected — here, and by the repo-wide secret-scan that shares this ruleset. | Required CI; local harvester feedback. | gitleaks; local absence warns and returns zero unless EVENER_GITLEAKS_REQUIRED=1. | A finding, or a required-tool absence under EVENER_GITLEAKS_REQUIRED=1, is nonzero. |

### Other targets

| Command | Summary |
| --- | --- |
| `make fuzz-goldens` | Regenerate the decode SNAPSHOT goldens from the current decoders, and the hub credential-wire fixtures its clients decode. Run ONLY after an intended change, then commit the diff. |
| `make fuzz-nightly` | Run the unbounded coverage-guided search per target, bounded by a per-target time budget. Manual/nightly only — never in the gate. |
| `make fuzz-triage` | The local, on-demand campaign + auto-triage tool: search each surface, flake-guard and dedup any crasher, and open one reviewable PR per distinct deterministic bug via the developer's local gh. |
| `make fuzz-continuous` | The local, on-demand continuous loop: rotate over every native target with a bounded search turn per round, routing any new crasher through fuzz-triage. Runs until Ctrl-C or --total. |
| `make fuzz-drive` | Generate real provider traffic (varied coding tasks through the evener one-shot CLI, recorders on) and harvest it into the seed corpus. Makes live, paid provider calls — run on demand, not in CI. |
| `make fuzz-bisect` | Find the commit that introduced a saved crasher via git bisect, replaying one corpus entry per step. |
| `make fuzz-mutation-score` | Measure detection sufficiency with gremlins: the per-package kill rate, where the surviving (LIVED) mutants are the weak-oracle worklist. Nightly/manual; needs gremlins installed. |
| `make fuzz-ledger` | Pretty-print the triage ledger — found/fixed/quarantined counts and the open-bug list — from fuzz/state/ledger.json. |
<!-- END GENERATED -->
