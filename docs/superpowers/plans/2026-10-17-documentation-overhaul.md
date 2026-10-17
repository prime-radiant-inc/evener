# Evener Documentation Overhaul Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Execute bounded tasks with independent checks and review.

**Goal:** Publish comprehensive, readable, source-verified Evener user documentation automatically from main to GitHub Pages.

**Architecture:** Canonical Markdown remains in `docs/`. An explicit publication manifest stages only public pages into a VitePress site and generates plain Markdown exports. Task guides link to exact existing or generated references.

**Tech Stack:** Markdown, Node.js, VitePress, Node's test runner, GitHub Actions and GitHub Pages. Existing Go generators remain authoritative for AppWire.

**Spec:** `docs/superpowers/specs/2026-10-17-documentation-overhaul-design.md`

## Global constraints

- Public documentation tracks main and labels development behavior.
- No TUI instructions or promotion; retain the noninteractive CLI.
- No product behavior changes or unsupported feature promises.
- Preserve unrelated files and internal historical records.
- Use Writing Clearly and Concisely and exact source evidence.
- Static site base is `/evener/`; default address is `https://prime-radiant-inc.github.io/evener/`.
- Publish an explicit allowlist, never the whole docs tree.
- Never print secrets in examples or verification output.
- Tests added or changed must follow `docs/developing-evener/testing.md`.

## Review focus

1. A release installer may yield behavior older than main. Installation must name both tracks and the site's version policy.
2. Relative links must work in repository Markdown, staged HTML and exported Markdown without leaking internal plans into search.
3. Configuration sources merge differently. Check each subsystem against its resolver, including trust and reload timing.
4. A transport write or model answer does not prove work succeeded. Procedures need independent result checks and protocol docs need explicit lifecycle guarantees.
5. A novice must finish without undocumented commands, hidden credentials, or assumed installed SDK packages.

## Work ownership

Writers use separate worktrees when concurrent. Each task owns only its named files. Parent integrates completed branches, resolves links and keeps one coherent site. Reviewers are read-only.

## Task 1: Research and editorial foundation

**Files:** design above; `docs/research/2026-10-17-documentation-patterns.md`.

- [ ] Reconcile Claude guide and six-reference coverage and corrected Codex coverage. Record methodology and exceptions honestly.
- [ ] Preserve a concise sourced synthesis in the repository research record, excluding downloaded competitor corpora from the site and repository.
- [ ] Apply professional-editor design findings. Keep researcher recommendations separate from verified Evener behavior.
- [ ] Commit the design, plan and research record after reading the staged diff.

## Task 2: Publishable site and documentation checks

**Create:** `docs-site/package.json`, lockfile, `docs-site/README.md`, `docs-site/publication.json`, `docs-site/.vitepress/config.mjs`, `docs-site/scripts/prepare.mjs`, `docs-site/scripts/check.mjs`, corresponding `*.test.mjs`, `.github/workflows/docs.yml`.
**Modify:** `.gitignore` only for site dependency/generated/build directories.
**Interface:** publication manifest rows identify canonical repository Markdown sources, site paths, titles and navigation groups. `npm --prefix docs-site run check` validates sources and exports; `npm --prefix docs-site run build` produces the Pages artifact. Document exact output directory in package README and workflow.

- [ ] Read repository testing guidance and existing workflow conventions.
- [ ] Write failing fixture-based tests for publication selection and link handling: nested relative paths, spaces, fragments, links to source files, excluded docs, external links, and link-like text inside code fences.
- [ ] Run the tests and observe failure from missing implementation.
- [ ] Implement the smallest staging/link checking needed. Use a Markdown parser rather than regex-rewriting code fences or prose indiscriminately. Prefer generator-native link behavior where possible.
- [ ] Stage a small existing first-run path and AppWire reference to prove ordinary Markdown, generated tables and source links render.
- [ ] Generate complete static Markdown exports and `llms.txt` from the same manifest. Test output membership and usable links.
- [ ] Add local search, section navigation, main-version notice, repository/edit links and restrained styling. Check the nested Pages base path.
- [ ] Add PR build/check and main-only Pages deployment using the same artifact; minimal permissions, concurrency and manual dispatch.
- [ ] Run unit checks and the production build. Open generated pages in a browser; check mobile layout, keyboard navigation and search.
- [ ] Commit only owned site/workflow paths after reviewing the staged diff.

## Task 3: First task, daily use and conceptual model

**Own:** `README.md`, `docs/index.md`, `docs/getting-started.md`, `docs/daily-work.md`, `docs/concepts.md`, `docs/native-app.md`, `docs/memory.md`, plus a small deterministic tutorial fixture under `docs/examples/first-task/` if needed.
**Consume:** design and source-of-truth map in `docs/product/subsystems.md`.
**Produce:** coherent onboarding and user workflow chapters with verified cross-links, safe examples and independent checks.

- [ ] Inspect installation, launch, browser/native and memory sources before writing claims.
- [ ] Provide a small first-task fixture whose starting condition and expected result are independently testable without model credentials.
- [ ] Run the fixture's failing starting test and verify its expected repaired state in temporary space, without altering the delivered starting fixture.
- [ ] Rewrite the README and first-run path; distinguish shell commands from agent prompts and release from snapshot.
- [ ] Cover steering, queueing, stopping, resuming, forks, reviewing changes, delegates/jobs/watches and retained state without exposing irrelevant implementation detail.
- [ ] Explain native access only through verified supported installation and pairing paths; label limitations rather than inventing distribution guarantees.
- [ ] Audit every command/route/claim against source and list evidence in the task report.
- [ ] Run link and fixture checks, obtain early novice/editor feedback, fix findings, and commit owned files.

## Task 4: Configuration and operation

**Own:** `docs/configuration.md`, `docs/connecting-a-provider.md`, `docs/llm-providers.md`, `docs/llm-provider-config-and-launch.md`, `docs/ollama.md`, `docs/evener-hub.md`, `docs/evener-hub-remote-operations.md`, `docs/sandboxing.md`, `docs/troubleshooting.md`, `docs/security.md`, `docs/environment-variables.md`.
**Consume:** launchconfig, envvars, provider and credential sources; existing operating guides.
**Produce:** source-verified configuration and operational guides with one authority per rule.

- [ ] Inventory relevant settings sources and scope; record precedence, merge, trust, inspection and reload separately for launch, providers, credentials and extension configuration.
- [ ] Rewrite guides for task-first use and concrete resolution examples. Keep exact reference detail; do not replace it with broad summaries.
- [ ] Route user environment settings through a public reference grounded in `envvars/envvars.go`; coordinate contributor links to avoid divergent duplicate lists.
- [ ] Explain sandbox modes, credentials, storage, remote execution and privacy limits with warnings beside risky actions.
- [ ] Build symptom-led troubleshooting from verified diagnostic commands; protect work and secrets, and distinguish retry-safe failures from uncertain outcomes.
- [ ] Parse complete TOML/JSON snippets and verify supported flags against source/help. Do not run destructive recipes or live provider requests.
- [ ] Run targeted checks and editorial review; commit owned files and report any untouched internal TUI references separately.

## Task 5: Extension authoring and use

**Own:** `docs/skills.md`, `docs/hooks.md`, `docs/plugins.md`, `docs/mcp.md`, `docs/extensions.md` and extension example fixtures under `docs/examples/extensions/`.
**Consume:** skill/plugin/hook/MCP implementation and existing contracts.
**Produce:** extension chooser, complete minimal examples, precise format and lifecycle references.

- [ ] Verify discovery roots, naming, trust, enablement, reload and teardown against implementation.
- [ ] Write a chooser for skills, commands, plugins, hooks, MCP and delegate roles using actual supported capabilities.
- [ ] Preserve exact existing hook contracts while separating first-use procedures from exhaustive reference sections.
- [ ] Supply minimal create/load/invoke/verify/remove workflows. State where code executes and what permissions it inherits.
- [ ] Parse example metadata and configuration with the real loaders or targeted deterministic tests where feasible.
- [ ] Run relevant parser/loader tests, links and source audits; obtain editor feedback and commit owned paths.

## Task 6: CLI, automation and integration

**Own:** `docs/cli.md`, `docs/automation.md`, `docs/integrations.md`, `internal/appwiredoc/protocol.md.tmpl`, generated `docs/appwire-protocol.md`, `docs/job-control.md`, `docs/subagent-runtime-contracts.md`; integration examples under `docs/examples/integrations/` as needed.
**Consume:** `cmd/evener/main.go`, command implementations, AppWire catalog/types/router tests, `appwire-client/typescript/README.md`.
**Produce:** runnable CLI recipes, exact lifecycle and output contracts, minimal SDK/wire entry path linked to the generated catalog.

- [ ] Verify CLI dispatch, flags, defaults, exits and output formats independently from TUI sources.
- [ ] Write CLI and automation guides with bounded safe examples, result validation and failure handling.
- [ ] Verify package distribution before showing installation commands; use documented source-checkout setup if publication cannot be established.
- [ ] Explain authentication, initialization, IDs, subscriptions, partial output, terminal outcomes, cancellation, retries, reconnect, pagination and backpressure without invented guarantees.
- [ ] Remove TUI framing from the AppWire template; keep transport and catalog facts source-owned, then regenerate only intended outputs.
- [ ] Validate request/response examples against actual types and targeted tests. Do not imply JSON-RPC conventions absent from the implementation.
- [ ] Run generator checks and relevant deterministic protocol/client tests; commit owned files.

## Task 7: Integrate and review the full public corpus

**Own:** publication manifest/navigation, cross-link fixes agreed with completed writers, editorial findings record under `docs/research/`.

- [ ] Integrate finished isolated branches under repository merge rules; inspect diffs and resolve factual conflicts.
- [ ] Add all approved pages to the publication manifest. Check there are no navigation stubs, duplicate rule owners or orphaned public guides.
- [ ] Run complete site checks/build and inspect rendered output, including large reference pages and search.
- [ ] Dispatch the six read-only editorial roles in the design across OpenAI and Lunaroute. Give each the full manifest and exact source/build paths, require concrete findings, and explicitly label simulated named-person lenses.
- [ ] Consolidate findings by root cause, fix them, and have affected reviewers recheck substantive changes.
- [ ] Run an independent source-factual audit of commands, configuration and protocol examples; editorial fluency alone is insufficient.
- [ ] Confirm published content has no TUI promotion, secret values, unmarked schematic examples or internal history leaks.

## Task 8: Verify and publish

- [ ] Run targeted final gates and inspect their implementations before reporting their output.
- [ ] Run required simplification/review process for publication code; inspect final staged diff and commit exact paths.
- [ ] Push the branch to origin, open a PR and shepherd CI/review according to repository policy.
- [ ] Enable GitHub Pages Actions publishing if authorized account permissions permit. Observe actual deployment after merge before claiming the site is live.
- [ ] Check the public homepage, representative guides, reference page, search assets and Markdown index at the deployed base path.
- [ ] Report delivered paths/URL, checks run, review outcomes, research exceptions and any publication or live-example limitation.
- [ ] Remove disposable research downloads and temporary files after preserving the cited synthesis and coverage evidence; leave the finished site and branch intact.
