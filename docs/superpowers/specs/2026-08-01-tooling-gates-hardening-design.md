# Tooling gates hardening design

Date: 2026-08-01
Status: Approved
Base: `webui-workspace-shell` at `b35af9b1f`
Katas: `9fcs`, `dq4t`, `m716`, `fdhx`

## Problem

Four small tooling contracts are currently incomplete:

1. The published merge-approval gate is three commands that an operator must
   remember and invoke in order: `make lint`, `make build`, and
   `ROOT_FULL=1 make test`. There is no canonical target that owns that
   sequence.
2. `go generate ./appwire/...` rewrites both the AppWire Markdown reference and
   the frontend TypeScript protocol declarations, but `lint-generated` diffs
   only the Markdown output. A stale committed `types.gen.ts` is therefore
   repaired in the working tree without making the gate fail.
3. `scripts/gitleaks-scan.sh` always turns a missing gitleaks executable into a
   warning and a successful exit. CI currently depends on install-step ordering
   to keep its scans real, even though local development deliberately permits
   the missing-tool skip.
4. The root module imports `primeradiant.com/serf/identifier` through root
   packages and `llm`, but its own `go.mod` does not require that sibling.
   Module graph pruning means an external consumer of only the root module
   cannot obtain the identifier module from `llm/go.mod`.

The published three-command baseline is green at the base commit: all seven
non-fuzz Go modules, the full root wave, script selftests, the frontend gate,
and the production build passed. Live probes also confirmed that
`merge-approval-gate` has no rule, `SERF_GITLEAKS_REQUIRED=1` is currently
ignored with exit zero, and an offline external root consumer fails because
identifier is replaced but not required.

## Architecture

Keep each repair at its existing ownership boundary:

- The root `Makefile` owns gate composition and generated-output validation.
- `scripts/gitleaks-scan.sh` owns missing-tool policy, while each CI scan step
  explicitly opts into the strict branch.
- The root `go.mod` owns the root module's complete pruned dependency graph.

The four repairs share one worktree and one plan because they are a requested
tooling-maintenance batch, but each remains an independently testable commit
with its own Kata evidence. No wrapper framework or compatibility layer is
introduced.

## Canonical merge-approval target (`9fcs`)

Add `merge-approval-gate` to the phony target list. Its recipe invokes three
recursive makes on separate recipe lines, in this exact order:

```make
merge-approval-gate:
	@$(MAKE) lint
	@$(MAKE) build
	@ROOT_FULL=1 $(MAKE) test
```

A make target's recipe lines are ordered even when the outer invocation uses
`-j`, so the target needs neither prerequisite ordering tricks nor a global
`.NOTPARALLEL` declaration. Each recursive make's failure stops the outer
recipe immediately. Nothing pipes, filters, or otherwise masks child output or
status. `ROOT_FULL` is scoped to the test invocation, and `make fuzz` remains
outside the target.

Add `scripts/merge-approval-gate-selftest.sh` and register it with
`SELFTEST_SCRIPTS`. The selftest invokes the real outer make with `-j` while
overriding recursive `MAKE` with a fake executable. It observes calls and
environment rather than matching the Makefile recipe text. It proves:

- the successful call sequence is `lint`, `build`, `test`;
- only `test` receives `ROOT_FULL=1`;
- child stdout and stderr remain visible;
- a lint failure prevents build and test;
- a build failure prevents test; and
- no fuzz target is invoked.

Update `docs/testing.md` to make `make merge-approval-gate` the canonical
command. Retain the explicit three-command expansion for diagnosis and
evidence.

## Generated TypeScript drift (`dq4t`)

Change `lint-generated` so its one `git diff --exit-code` pathspec contains
both committed outputs:

- `docs/appwire-protocol.md`
- `cmd/serf-hub/frontend/src/protocol/types.gen.ts`

The failure message names both generated outputs and directs the operator to
run `make generate` and commit the results. Update the adjacent Makefile and
Go-workspace documentation so neither describes the Markdown file as the sole
generated output.

Extend the lint selftest with a behavioral temporary-git fixture. Its committed
Markdown output is current, its committed TypeScript output is stale, and a
fake `go generate` writes the known-current contents for both. Before the fix,
`make lint-generated` incorrectly exits zero because the TypeScript path is
outside the diff. After the fix, regeneration makes the working tree differ
from the committed stale TypeScript file and the target exits nonzero with the
useful diagnostic. The test runs the target; it does not inspect or regex-match
the rendered recipe.

## CI-required gitleaks (`m716`)

Add one exact opt-in to `scripts/gitleaks-scan.sh`:

```text
SERF_GITLEAKS_REQUIRED=1
```

When gitleaks is absent and the variable is unset or is not exactly `1`, retain
the existing warning and successful exit. When it is exactly `1`, print a clear
error that says the requested repository or corpus scan cannot run and exit
nonzero. Installed-tool behavior and scan arguments do not change.

Set the variable through step-level `env` on both CI steps that require a real
scan: the aggregate lint step, which owns the repository scan, and the
corpus-secret-scan step. Local Make targets do not set it, so local missing-tool
behavior remains unchanged.

Extend `scripts/run-module-lint-selftest.sh` with direct executions of the real
gitleaks scan script against a fake dependency boundary. One case pins the
existing optional warning and zero exit; one pins the required-mode diagnostic
and nonzero exit. Add a structured workflow test that parses the CI YAML and
requires the strict environment setting on every step that invokes either CI
scan target.

## Root identifier requirement (`fdhx`)

Add only this sibling requirement to the root module's existing sibling block:

```go.mod
primeradiant.com/serf/identifier v0.0.0
```

Do not change `golang.org/x/sys`, `golang.org/x/text`, any `go.sum`, or any
other version. The existing versioned replacement in `go.work` remains the
workspace mapping; the committed module file remains free of local paths.

Verification uses a scratch consumer outside `go.work`. Its `go.mod` requires
only `primeradiant.com/serf v0.0.0` and contains directory replacements for all
Serf modules. After populating only the scratch consumer's sums, run explicit
root package lists with `GOWORK=off`, `GOFLAGS=-mod=readonly`, `GOPROXY=off`,
and `GOSUMDB=off`, first for dependencies and then with `-test`. The current
module fails because identifier is replaced but not required; the added root
require must make both readonly probes pass. No `-mod=mod` command may point at
the repository tree.

## Error handling and evidence

All gates use bare process exits as verdicts. The merge target stops at the
first failing recursive make. Generated drift replays the diff and a concise
repair instruction through the existing quiet-lint wrapper. Required gitleaks
failure is explicit and nonzero; optional local absence remains visible but
successful. The module probe is offline and readonly against repository files.

Implementation proceeds test-first and one Kata at a time. Each focused RED
must fail for the missing behavior rather than a syntax or fixture error, then
turn GREEN under the smallest production change. Each Kata receives its own
implementation commit and is closed immediately after focused verification
with that commit as evidence.

Final acceptance is a clean `make merge-approval-gate` invocation from the new
worktree. Its bare exit is the verdict; no separate fuzz search is added.

## Out of scope

- Changing the merge-gate coverage, scheduling, or `ROOT_FULL` semantics.
- Running fuzzing from the merge-approval target.
- Adding a general gate runner or wrapper script.
- Requiring gitleaks for ordinary local development.
- Changing gitleaks detection rules or scan scope.
- Aligning the root module's `x/sys` or `x/text` versions.
- Editing any sibling module's dependency versions.
