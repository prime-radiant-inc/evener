# Coverage

## A Coverage Number Is Two Tracks, Not One

The default gate's coverage number and the fuzz family's coverage are measured
separately, by design. `go test ./agent -short`'s `-cover` output measures only
the imperative test suite — the seqfuzz/schemafuzz family t.Skip()s there.
`make coverage-floor` answers the honest "how much is exercised at all" number:
per module it unions the test track with the deterministic native seed-corpus
replay (the rapid family is env-gated and not part of either track). Do not read
a default-gate coverage number as "whole-repo coverage including fuzz" — it
never was, and now it's explicit.

The corollary runs the other way too, and it is the one that misleads: a
default-gate number is not "how much of this package is tested". Several
packages keep whole families of behavioural checks in `check*` functions that
only a native *program* fuzz target calls — `FuzzLaunchConfigBehaviorProgram`
invokes 98 of them. These `Fuzz*BehaviorProgram` targets carry no `evenerfuzz`
build tag; what excludes them from the test track is the same
`-run '^(Test|Example)'` filter (`scripts/lib/gate-surface-lib.sh`'s
`GATE_TEST_RUN`) that excludes every other `FuzzXxx` name, so the test track
cannot see that work at all: `cmd/evener-hub/internal/appsource` reads 66.4%
there and 83.1% under its own program target, and four modules that look
incomplete on both tracks separately are in fact fully covered.

So before concluding a package is under-tested — and certainly before writing a
test to raise its number — read `make coverage-floor`, which unions the two
tracks. The test you were about to write may already exist under the other build
tag.

A fuzz target that replays a fixed matrix of production calls and discards the
results (`_ = f(x)`) earns lines only as EXECUTED: its oracle is a panic or a
`-race` failure, so it cannot fail on a wrong answer. Such targets do not belong
in the tree: a call site earns its coverage from a test that asserts a result
against an independently written literal, which is what turns EXECUTED into
TESTED. The floors in `scripts/coverage/coverage-floors.txt` are a local report,
not a gate CI enforces, so deleting a replay target without a replacement
assertion is a legitimate way to lower them; lower the affected row by hand.

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/coverage.mk, then run `make generate`. -->
| Command | Summary | What it proves | Trigger | Requires | Fails when |
| --- | --- | --- | --- | --- | --- |
| `make coverage-floor` | The repo's one coverage ratchet: per module, the union of the test track and the deterministic fuzz-replay track, plus the frontend's vitest line coverage. | Per-module statement coverage reached by any deterministic test (the test track unioned with the deterministic fuzz-seed replay), plus the frontend's vitest line coverage, against scripts/coverage/coverage-floors.txt. | Local/on-demand; not required CI (heavier than make test). Bare invocation reports; CHECK=1 to gate; BLESS=1 to raise floors. | Deterministic; no provider calls. The fuzz half replays committed seed corpora only (go test without -fuzz), never a search. | Under CHECK=1, any row falls below its floor beyond the tolerance band; a floored row that cannot be measured fails loudly rather than skipping. |

### Other targets

| Command | Summary |
| --- | --- |
| `make e2e-cover` | Measure end-to-end coverage of the real evener binary via `go build -cover` + GOCOVERDIR, unioned with the unit profile. EVENER_E2E_LIVE=1 additionally runs the live provider scripts. Local and on-demand, not a gate. |
| `make coverage-gaps` | Rank a coverage profile's uncovered statements by count, not percentage, so coverage work targets the largest real gaps. Takes a profile: `make coverage-gaps PROFILE=path/to.cov GAP_ARGS="--by file"`. |
<!-- END GENERATED -->
