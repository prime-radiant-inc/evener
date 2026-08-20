# Developing Evener

Dev-facing docs for working on this repo: setup, environment, worktrees,
performance, naming, and the agent-run scenario harness. Docs about the
`make` gates themselves (building, testing, linting, coverage, fuzzing)
land here in a later phase; for now those still live at `docs/testing.md`
and `docs/fuzzing.md`.

- **[environment.md](environment.md)** — every environment variable evener
  reads, keyed to the `envvars` package that's their source of truth.
- **[worktrees.md](worktrees.md)** — the `manage_worktree` tool and delegate
  worktree isolation: what a worktree is for here and how it's cleaned up.
- **[agentic-testing.md](agentic-testing.md)** — the practical guide for
  running a `test/scenarios/` card against a live `evener-hub` + `evener`:
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
- **[conventions/](conventions/)** — naming conventions for serialized
  identifiers, working in the `go.work` multi-module workspace, and running
  a fleet of agents against this repo without them stepping on each other.

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/repo.mk, then run `make generate`. -->
<!-- END GENERATED -->
