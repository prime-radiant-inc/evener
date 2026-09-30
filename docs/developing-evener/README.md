# Developing Evener

Dev-facing docs for working on this repo: setup, environment, worktrees,
performance, naming, and the agent-run scenario harness, plus the `make`
gates themselves — building, testing, linting, coverage, and fuzzing.

For product intent and cross-component ownership, start with the
[product guide](../product/README.md) and [subsystem map](../product/subsystems.md).
Keep those references current when changing the behavior they describe.

- **[building.md](building.md)** — build, distribution, and install targets,
  and the frontend install prerequisite they share with the frontend test
  gates.
- **[testing.md](testing.md)** — the test reliability policy, the
  post-merge gate, the test-family targets, and the gates that are not make
  targets.
- **[linting.md](linting.md)** — the static checks that gate merges without
  running tests: formatting, generated-output freshness, compile floors, and
  the repo secret scan.
- **[coverage.md](coverage.md)** — why a default-gate coverage number is two
  tracks, not one, and what it does and doesn't mean.
- **[fuzzing.md](fuzzing.md)** — the front door to evener's fuzzing toolkit:
  the `testing.F`/`rapid.Check` targets, coverage, gating, and regression
  promotion.
- **[environment.md](environment.md)** — every environment variable evener
  reads, keyed to the `envvars` package that's their source of truth.
- **[worktrees.md](worktrees.md)** — the `manage_worktree` tool and delegate
  worktree isolation: what a worktree is for here and how it's cleaned up.
- **[agentic-testing.md](agentic-testing.md)** — the practical guide for
  running a `test/scenarios/` card against a live `evener hub` + `evener`:
  hermetic workdirs, the setup checklist, and recipes for common scenario
  shapes.
- **[agent-test-serial-prefix.md](agent-test-serial-prefix.md)** — measured
  cost of the `agent` package's serial-prefix tests and why most of that
  time is stuck rather than parallelizable.
- **[performance-profiling.md](performance-profiling.md)** — tools for
  measuring and optimizing evener's per-round framework overhead.
- **[dev-checklist.md](dev-checklist.md)** — the manual checklist that runs
  against your own real dev hub and session history, not an isolated
  checkout; skip it when running the automated scenario sweep.
- **[issue-triage.md](issue-triage.md)** — how open GitHub issues are
  categorized, labeled, and ranked: the label vocabulary, the eval-only
  test, and the triage procedure.
- **[conventions/](conventions/)** — naming conventions for serialized
  identifiers, working in the `go.work` multi-module workspace, and running
  a fleet of agents against this repo without them stepping on each other.

## Git hooks

`make hooks` sets `core.hooksPath=scripts/hooks` in the clone's shared git
config, so every worktree runs its own checkout's hooks. The one hook today is
`scripts/hooks/pre-commit`: it formats the staged TypeScript and re-stages it,
so formatting never reaches review as a finding.

- `mobile-native/src` and `mobile/src` go through mobile-native's Biome
  (`format --write`); `cmd/evener-hub/frontend/src` and
  `appwire-client/typescript` go through the frontend's (`check --write` with
  the linter off, which also organizes imports). Each runs from its own tree
  with its own config and pinned version, never from the repo root.
- Only staged files are touched, and a tree with nothing staged costs nothing.
  A file with unstaged edits on top of its staged version is refused, because
  re-staging would sweep the edits into the commit.
- A tree whose `node_modules` has no Biome fails the commit and prints the
  command that installs it. It never skips.
- It does nothing during a merge commit, where the index holds everything the
  other side brought in. It only runs on branches that contain
  `scripts/hooks/`; on an older branch git finds no hooks directory and skips
  every hook without a message, so merge `origin/main` first.
- The hook then runs `.git/hooks/pre-commit` if you have one. `make hooks`
  refuses to run when another `core.hooksPath` is set or another hook is
  installed in `.git/hooks`, since either would stop running.

The tests for both scripts are in `hooks_test.go` (part of `make test`); `EVENER_HOOKS_REAL_BIOME=1 go test -run PreCommitHook .` also runs the hook against the real frontend Biome.

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/repo.mk, then run `make generate`. -->
| Command | Summary |
| --- | --- |
| `make tools` | Install the CI-pinned golangci-lint and gitleaks versions from .tool-versions, so a local make lint runs exactly what CI runs. |
| `make tools-golangci` | Install the CI-pinned golangci-lint version from .tool-versions. |
| `make tools-gitleaks` | Install the CI-pinned gitleaks version from .tool-versions. |
| `make hooks` | Install the checked-in git hooks (core.hooksPath=scripts/hooks): pre-commit formats staged TypeScript with each tree's own pinned Biome. |
| `make refresh-model-catalog` | Replace the embedded models.dev snapshot in llm/registry/data/ with the current upstream and run the converter tests and overlay report. |
| `make generate` | Run the appwire and maketargetsdoc `go generate` directives: the AppWire protocol reference and frontend TypeScript declarations from appwire/protocol.go, and the per-family make-target tables in docs/developing-evener/. |
| `make clean` | Remove the built binaries from the repo root. |
| `make help` | Print every make target, grouped by family, with its one-line summary. |
<!-- END GENERATED -->
