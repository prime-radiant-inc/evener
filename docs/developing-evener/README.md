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
- **[design-system.md](design-system.md)** — how to build hub web UI on
  the design system: compose from the widget library, tokens only, and what
  to do when no widget fits.
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
- **[tutorial-tours.md](tutorial-tours.md)** — how the CLI and web tutorial
  screenshots are executed, retained, and reviewed as a proposed asset patch.
  The 24-row matrix excludes native, which remains deferred.
- **[../../docs-site/README.md](../../docs-site/README.md)** — the publisher for the
  public guide: the publication manifest, link and export rules, and the
  GitHub Pages workflow. Edit chapters in `docs/`, never in `.generated/`.
- **[conventions/](conventions/)** — naming conventions for serialized
  identifiers, working in the `go.work` multi-module workspace, and running
  a fleet of agents against this repo without them stepping on each other.

## Documentation

The public guide is built from canonical Markdown in `docs/`. Only pages listed
in `docs-site/publication.json` are published; everything else stays in the
repository. The site tracks `main`.

From the repository root, install the site dependencies and Chromium before
running the documentation gate. Never run `npm ci` through a symlinked
`docs-site/node_modules` shared by other worktrees.

```sh
npm --prefix docs-site ci
(cd docs-site && npx playwright install chromium)
make docs-check
```

The gate reuses a fresh, real install (or installs it if missing), runs
Chromium-dependent publisher tests, checks the publication manifest, links,
fragments, assets, and committed tutorial evidence, then builds the site. It is
part of `make merge-approval-gate`. CI runs the same build plus the tutorial
tours and browser checks in
`.github/workflows/docs.yml`.

To preview the site, see [docs-site/README.md](../../docs-site/README.md). For a
single page's rules: one H1, ordinary Markdown and GFM tables, no raw HTML
components, and local links that resolve from the canonical source path. A link
to an unpublished file resolves to GitHub on `main` rather than failing, so a
page can cite an implementation file without making it public.

## First contribution

Install the Go version in `go.mod`, Node 22 or newer, npm, Git, and Python 3
(`python3` on PATH). Start in an isolated checkout so builds and installs do not
disturb another contributor's worktree. For example, from an existing clone:

```sh
git worktree add -b docs/my-first-change ../evener-docs main
cd ../evener-docs
make build-runtime
./evener --version
```

`make build-runtime` builds the web UI first, then the runtime and development
binaries; it may install frontend dependencies. See [Building](building.md) for
the build contract. Edit a canonical Markdown page under `docs/`, such as this
README, rather than a generated page under `docs-site/.generated/`. Then install
the documentation tools and check the edit:

```sh
npm --prefix docs-site ci
(cd docs-site && npx playwright install chromium)
make docs-check
```

If you change an AppWire catalog or a `##` make-target annotation, run
`make generate` and review the generated diff before committing it. Review your
own diff and run focused checks for the files you changed; [Testing](testing.md)
explains which checks apply. CI owns the full lint, test, browser, and build
gates. Open a review with the commands you ran and any checks you could not run.

Import rules for the TypeScript trees: the web and native apps import the AppWire
package by name (`@evener/appwire-client`), never by path, and native never
imports `cmd/evener-hub/frontend/src`. Code both apps need goes in the package,
as a framework-free store or function with a thin React binding in each app.
`make lint-package-imports` enforces both.

A Go string enum that clients must share joins the generated TypeScript by
adding a row to `stringEnums` in `internal/appwirets/emit.go`: the generator
emits its values as a runtime catalog and types every field of that Go type by
the derived union. Client limits that mirror a Go constant are pinned by a test
that reads the Go source (`appwire-client/typescript/wireLimits.test.ts`)
instead of being generated.

## Git hooks

`make hooks` sets `core.hooksPath=scripts/hooks` in the clone's shared git
config, so every worktree runs its own checkout's hooks. The one hook today is
`scripts/hooks/pre-commit`: it formats the staged TypeScript and re-stages it,
so formatting never reaches review as a finding.

- `mobile-native/src` goes through mobile-native's Biome
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
| Command | Summary | What it proves | Trigger | Requires | Fails when |
| --- | --- | --- | --- | --- | --- |
| `make docs-check` | Build and validate the public documentation site. | Every page in docs-site/publication.json parses with one H1, its local links and fragments resolve, published assets exist, committed tutorial evidence matches its manifest, and VitePress renders the site. Local links across the repository's non-archived Markdown resolve too. | Local pre-merge via make merge-approval-gate; CI's Documentation workflow runs the same check plus tutorial tours and browser checks. | Node 22, npm, Python 3, the docs-site install, and Playwright Chromium (install with `npm --prefix docs-site ci`, then `(cd docs-site && npx playwright install chromium)`). docs-site/node_modules must not be a symlink to another worktree's install. A real install newer than the lockfile is reused instead of reinstalled. | The publisher's tests fail, a manifest entry or link is invalid, a published asset or tutorial file is missing, or the build fails. Evidence whose relevant source bytes have changed warns rather than fails. |

### Other targets

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
