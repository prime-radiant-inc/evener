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

`cmd/evener-hub/cov_*_test.go` pulls the union number in the other direction.
(The `cov_`-prefixed name is not the marker — some, like
`agent/execenv/cov_s4_local_test.go`, are ordinary untagged `TestXxx`
suites. The marker is the shape below: a `FuzzXxx` target, not a `TestXxx`
function — the `//go:build evenerfuzz` tag is not part of it, and most of
`cmd/evener-hub`'s cov_* files carry no such tag, including
`cov_auth_instances_fuzz_test.go` below. Its own seed shape isn't universal
either: it ignores a single seed byte, `f.Add(byte(0))`, but several other
cov_* files seed multiple bytes that select between behaviors.) Each of
`cmd/evener-hub`'s cov_* files is a deterministic replay matrix that calls
production functions and discards most results (`_ = f(x)`). Their oracle is
real — a panic or a `-race` failure still fails the build — but thin: a call
site with no assertion cannot fail on a wrong answer, only a crash. So
statements these files reach count as EXECUTED toward coverage-floor, not
TESTED — read the number that way for `cmd/evener-hub`, where they are a large
share of the fuzz track. Upgrading a call site's target from panic-net to an
assertion against an independently-written literal (see
`cov_auth_instances_fuzz_test.go`) turns EXECUTED into TESTED for that call
site; it is not required for the rest of the file to keep earning its lines.

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/coverage.mk, then run `make generate`. -->
<!-- END GENERATED -->
