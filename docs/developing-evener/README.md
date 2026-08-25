# Developing Evener

Dev-facing docs for working on this repo: setup, environment, worktrees,
performance, naming, and the agent-run scenario harness, plus the `make`
gates themselves — building, testing, linting, coverage, and fuzzing.

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

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/repo.mk, then run `make generate`. -->
| Command | Summary |
| --- | --- |
| `make tools` | Install the CI-pinned golangci-lint and gitleaks versions from .tool-versions, so a local make lint runs exactly what CI runs. |
| `make refresh-model-catalog` | Replace the vendored LiteLLM model-catalog snapshot with the current upstream and run the catalog sanity tests. |
| `make generate` | Run the appwire and maketargetsdoc `go generate` directives: the AppWire protocol reference and frontend TypeScript declarations from appwire/protocol.go, and the per-family make-target tables in docs/developing-evener/. |
| `make clean` | Remove the built binaries from the repo root. |
| `make help` | Print every make target, grouped by family, with its one-line summary. |
<!-- END GENERATED -->
