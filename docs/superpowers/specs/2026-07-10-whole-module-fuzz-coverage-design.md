# Whole-Module Fuzz Coverage Design

Date: 2026-07-10
Status: Approved for planning and implementation
Base: `main` at `2ae123e1`

## Goal

Raise deterministic fuzz-reachable statement coverage above 95.0% in every Go
workspace module: root (`.`), `agent`, `llm`, `auth`, `envvars`, `fuzz`, and
`invariant`.

The work begins with the agent core. It favors real behavioral front doors over
line-oriented helper tests, and refactors production code only where a narrow
external-effect seam is necessary to make real behavior deterministic.

## Non-Negotiable Coverage Contract

1. A module passes only when its raw covered-statement ratio is strictly greater
   than 95.0%. Rounded display values do not determine success.
2. The denominator contains all executable Go statements in every package in the
   module. Generated or platform-impossible *whole files* may be excluded only
   through a reviewed manifest with an exact reason. Production business and
   orchestration code cannot be excluded.
3. Coverage includes only registered deterministic fuzz entry points:
   native Go `Fuzz*` targets replaying committed seeds/crashers and registered
   stateful/property fuzz tests replayed with a fixed seed bank and fixed check
   count. Ordinary unit, integration, live-provider, and E2E tests do not
   increase this metric.
4. Every target runs offline. Provider behavior is driven through a scripted
   `llm.ProviderAdapter`; network, subprocess, filesystem, and clock behavior
   are replaced only at their external boundaries. A provider credential alone
   must never activate a live request.
5. The coverage tool produces a canonical self-package profile for each package
   and unions target profiles only within that package. This preserves the
   existing honest denominator and avoids cross-test-binary profile inflation.
6. Floors can only increase after measured results exceed the target. `BLESS=1`
   is not a way to waive a regression or lower the 95.0% requirement.

## Current State

The existing global fuzz coverage script measures only root, `agent`, and `llm`.
Its last honest agent floor is 54.2% (10,656/19,661 statements). It replays only
`^Fuzz` tests, so registered rapid/stateful fuzz tests are not included. The
registry also omits fourteen committed agent native fuzz targets, including the
target needed to clear the current `agent/sandbox` gap-gate failure.

Go 1.25 can lazy-build `covdata` for `go tool`, but `go test -coverprofile` on a
package without a test binary still calls the absent toolchain binary directly.
The runner must not depend on that path. The final gate instead requires every
production package to own at least one registered local fuzz surface, so every
package has a real test binary and canonical local profile. Before that state is
reached, preflight fails with the exact package missing its local fuzz surface.

The clean baseline additionally exposes two unrelated existing agent fork tests:
`TestW3Init_ForkSession_OpenError` and
`TestW3Init_ForkSession_NewWriterError`. Their repair is a parallel Task 0 and
does not change the coverage contract.

## Architecture

### 1. Coverage Manifest And Measurement

`scripts/run-fuzz.sh` remains the authoritative target manifest, but its entries
are mechanically audited against discovered native `func Fuzz...` declarations.
The audit fails for missing, stale, or duplicate registrations. Native targets
and rapid/stateful entries remain explicitly tagged; ordinary `test` entries
remain reachability checks and are excluded from fuzz coverage.

`scripts/fuzz-coverage-global.sh` becomes an all-workspace runner. For each
module/package it runs every registered native target in deterministic seed-replay
mode and every registered rapid/stateful target using the fixed seed bank, each
with `-tags serffuzz` and a package-local coverage profile. It unions those
profiles into the package profile, then calculates raw statement counts across
all packages in the module. A package with no registered local fuzz surface is a
preflight error, never an omitted or fabricated zero-profile package.

The runner reports package and module gaps, writes stable machine-readable output
for tests, and compares each module against its own floor. The root Makefile adds
all workspace modules to the global coverage invocation and exposes a single
deterministic check command. Initially it is allowed to be slow; runtime work is
explicitly deferred until coverage is achieved.

### 2. Narrow Exclusions

An exclusion manifest is file-scoped and requires the module, package-relative
path, reason, and a classification of either generated source or unavailable
platform behavior. Validation rejects missing files, non-generated non-platform
entries, duplicate entries, and exclusions that do not remove any profile block.
The report prints every applied exclusion. No package-wide or symbol-wide waiver
is permitted.

### 3. Agent-Core Testability Boundary

The agent milestone is behavioral and offline. It extends the existing real
`Session` test harness with `agent/internal/agenttest.ScriptedAdapter`, fake
clocks, deny-by-default execution environments, and real in-memory or
fault-injecting persistence. The harness drives structured operation programs,
not arbitrary shell/network/process execution.

The first front door is the job/watch/delegate state machine. It exercises job
creation, output, terminal transitions, watch delivery, delegate routing,
restore, and re-arm paths through the actual `Session` and `jobManager`.
Its oracles compare folded durable state with live state and assert terminal
monotonicity, exactly-once notification delivery, and absence of orphaned jobs.

Worktree/file-tool behavior follows through a scripted Git/process boundary and
an in-memory filesystem. Sandbox/execenv, MCP/plugin, context-manager, and
transcript paths then use the same policy: inject only the external effect, keep
the Serf behavior real, and pair broad front doors with differential or invariant
oracles. Extract a pure decision core only when it removes a genuine dependency
tangle; do not move code merely to inflate coverage.

### 4. Subsequent Modules

After `agent` exceeds 95.0%, each module is completed independently while the
global contract remains common:

- Root: AppWire/HTTP/CLI front doors over injected filesystem, transport, and
  process boundaries.
- `llm`: full adapter request/response paths over fake `http.RoundTripper`s,
  preserving existing stream/non-stream and cross-provider differentials.
- `auth`: OAuth/config/store paths over fake HTTP, clock, and filesystem seams.
- `envvars`, `fuzz`, and `invariant`: deterministic codec, registry, state, and
  fault-injection targets over their real package APIs.

Each module gets a coverage-gap map before work starts, a focused set of
behavioral fuzzers, and a verification run before its floor changes.

## Safety And Error Handling

- Fuzzers do not execute arbitrary shell commands, real Git operations against
  user repositories, provider calls, or network requests. Unsafe operation
  classes stop at decode/validate or pass through a vetted fake boundary.
- Seed corpora remain committed, deterministic, and secret-scanned. Any crasher
  is minimized, flake-guarded, and preserved as a regression input.
- `invariant.Hold` assertions run under `serffuzz` only; their conditions stay
  side-effect-free so production behavior remains unchanged.
- A coverage-run failure is fatal and names the exact target/package/toolchain
  prerequisite. The report never silently treats a failed target as zero or
  omits it from the denominator.

## Delivery Sequence

1. Repair the existing baseline fork-test failure in parallel.
2. Make coverage measurement accurate for all workspace modules and establish a
   reproducible baseline without blessing floors.
3. Raise `agent` above 95.0% through the behavioral front-door lanes.
4. Complete root, `llm`, `auth`, `envvars`, `fuzz`, and `invariant` in separate,
   conflict-minimized lanes.
5. Enforce the all-module check, then optimize runtime without weakening the
   coverage contract.

## Success Criteria

- Every workspace module reports raw deterministic fuzz-reachable coverage
  greater than 95.0%.
- The all-module coverage command is reproducible offline and fails on missing
  targets, failed replays, invalid exclusions, or a coverage regression.
- `make test`, `make fuzz`, and the coverage check remain free of live provider
  traffic by default.
- The agent milestone uses real session behavior and external-boundary fakes,
  with semantic oracles stronger than a no-panic check.
