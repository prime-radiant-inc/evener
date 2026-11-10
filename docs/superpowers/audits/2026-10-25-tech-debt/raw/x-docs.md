# x-docs: cross-cutting documentation audit

Worktree: `.worktrees/debt-audit` at `a6c0b9c561` (origin/main). Read-only audit. No test suites run.

## Overview and inventory

Counts below come from `git ls-files docs`, `wc -l`, and `du` on this worktree.

| Measure | Value |
| --- | --- |
| Tracked files under `docs/` | 1,663 (1,169 `.md`, 377 `.png`, 48 `.html`, rest json/txt/js) |
| Markdown lines under `docs/` | ~598,600 |
| Tracked bytes under `docs/` | ~115 MB (73.7 MB `docs/design`, 29.2 MB `docs/superpowers`, 7.5 MB `docs/web-ui`) |
| `docs/superpowers/plans/` | 480 files, 402,756 lines (67% of all markdown) |
| `docs/superpowers/specs/` | 441 files, 117,976 lines |
| `docs/design/` | 384 files, of which 299 are in `design/mobile/assets` |
| `docs/web-ui/` | 136 files, 14,566 md lines |
| Published user guide | 31 pages in `docs-site/publication.json`; 8 other top-level `docs/*.md` unpublished |
| Spec/plan creation rate | 130 specs in 2026-10 alone; 52 plans in 2026-10 |
| Local markdown link check (my script, all tracked `.md`) | 1,550 links, 58 unresolved; 54 of those sit in archive trees or are code-sample placeholders |

What is healthy, so the fixes below stay proportionate:

- `docs/product/subsystems.md` exists and AGENTS.md links to it correctly. Every backticked repo path in `docs/product/*.md` resolves. The product guide matches its own rules (evergreen, no dated narratives in README/principles).
- `docs/appwire-protocol.md` (4,741 lines) is generated from `appwire/protocol.go` and a template, and `make lint-generated` checks freshness. That is the pattern the rest of the reference docs should copy.
- `docs/environment-variables.md` and `docs/developing-evener/environment.md` cover every registry variable (110 in `envvars/envvars.go`, 73 public, all 73 present in the public page). The names are in sync; two summaries are not (finding 5).
- `docs-site/scripts/check.mjs` already link-checks all non-archive markdown, so the maintained surface mostly stays link-clean.

The debt is concentrated in four places: the historical corpus that is two-thirds of the tree, the lack of a single live/archive boundary, hand-copied reference tables with no drift gate, and a few evergreen docs that have become changelogs.

## Findings

### x-docs-1 (high): the historical plan corpus is 400k lines of unreliable, mostly uncited text

Locations:
- `docs/superpowers/plans/` (480 files, 402,756 lines), `docs/superpowers/specs/` (441 files, 117,976 lines)
- `docs/superpowers/README.md:1-32` (the "treat them as history" rules)
- Largest: `plans/2026-08-29-provider-registry-step1.md` (8,697 lines), `step3` (8,372), `step2` (6,496), six iPhone-redesign phase plans at 4,800-6,000 lines each

Evidence:
- Checkbox status carries no signal. Of 480 plans: 31 all-checked, 92 with no boxes, 341 with every box unchecked, 16 mixed. The 341 are not unfinished work. Example: `plans/2026-05-24-evener-tui-ux-pass.md` has 242 open boxes for a TUI that `plans/2026-10-16-legacy-tui-removal.md` deleted.
- 240 plan/spec files mention the removed TUI; 10 plan filenames are about it (`ls plans | grep -i tui`).
- Status is free text: 231 of 441 specs have a `Status:` line, with 30+ distinct phrasings ("frozen for PAR", "ready for TDD", "approved direction (Jesse...)"). Only 3 say "Implemented". There is no machine-readable shipped/superseded marker.
- Citation reach is small. Outside `docs/superpowers`, 91 distinct `docs/superpowers/...` path strings appear in code, scripts and docs (184 line hits in 115 files). By a prefix-match heuristic about 66 of the 921 plans+specs are cited from outside the tree (roughly 7%). The rest are read by nobody except by grep, where they add noise: a search for any feature term hits stale plan code blocks first.
- Some plans are embedded fixtures: `mobile-native/src/dev/demoSessions.ts:208,219,260` and `mobile-native/scripts/demo-hub.mts:139` reference `plans/2026-09-25-settle-race.md` as fake demo data. Check that file's fate before deleting (see risk).
- The README admits the problem: "A plan is not a promise... Where a plan and a guide disagree, the guide is right." A corpus whose own index says to distrust it is cost without benefit; git retains everything.

Impact: ~67% of markdown lines, slower grep/review/LLM context pulls, wrong-answer risk (agents quote plan code that no longer exists), and every docs lint walks it. Spec production is accelerating (130 in October), so the problem grows.

Fix plan:
1. Rule: a plan is deleted in the PR that completes it (the PR description and git history are the record). Add this sentence to `docs/superpowers/README.md` and AGENTS.md's PR section.
2. One-time prune of `plans/`: delete every plan not cited from outside the tree. Keep the ~15-20 that code comments cite for rationale (list produced by the same grep as above, e.g. `plans/2026-09-04-webui-keybindings-p4-plan.md`, `plans/wave4-report.md`, `plans/2026-07-15-inline-*`), and move each kept one next to its spec as `specs/<name>-plan.md` or fold into the spec.
3. For specs: add a one-line machine-readable front-matter field `status: shipped|superseded|proposed` (no free text) and prune `shipped` specs that no code or evergreen doc cites. Keep cited ones, since comments use them for "why".
4. Before deleting a cited-by-fixture plan, repoint `demoSessions.ts`/`demo-hub.mts` at a fixture file under `mobile-native` (these are demo strings, the path need not exist).
5. Update `docs/superpowers/README.md` and the `ARCHIVE` list in `docs-site/scripts/check.mjs` if paths move (see finding 3).
Test strategy: `make docs-check` (links), plus `git grep` for every deleted basename across non-docs code to prove nothing cites it.

Size: net deletion of roughly 350-400k lines; ~100 lines of README/rule edits. Risk: low-medium. Code comments cite a spec or plan by path; deleting a cited one breaks a rationale pointer (not a build). The citation grep makes that exact and checkable. Do not rewrite history.

### x-docs-2 (high): ~70 MB of unreferenced PNG evidence committed under docs/

Locations:
- `docs/design/mobile/assets/` (299 files, 295 png; ~73 MB for `docs/design/mobile`)
- `docs/web-ui/specs/assets/2026-08-29-plugins-segmented-workspace/` (20 unreferenced), `.../2026-07-27-transcript-tiered-density/` (5), `docs/web-ui/sidebar-watches/screenshots/` (3)
- Largest: `docs/design/mobile/assets/2026-09-30-reopen-before-jumped.png` (952 KB), `...reopen-before-shown-short.png` (828 KB)

Evidence:
- 377 tracked images total 83.2 MB. A basename search over all tracked text files finds no reference to 301 of them (69.5 MB). 270 of those are in `docs/design/mobile/assets`.
- The plans that created them say they are PR attachments: `plans/2026-09-26-iphone-redesign-phase2-board.md:1878` "Save them under `docs/design/mobile/assets/2026-09-2x-redesign-phase2-board-*.png`... and attach them to the phase's last PR". The files were committed as well, but nothing links to the committed copy.
- `docs/design/README.md` calls this tree "records, not references".

Impact: every clone and every `docs/` walk carries 70 MB of dead images; biggest single contributor to repo working-tree size in `docs/`.

Fix plan: delete the unreferenced images (keep the ~76 that something links). Script the check as `git ls-files docs | image basename not found in tracked text` so it is reproducible. Add a rule to `docs/design/README.md`: screenshots for a PR go in the PR, not the tree. Keep the two JSON fixtures in the same dir only if referenced (they are named `*-install.json` etc.; verify by basename first). Tests: `make docs-check`. Note git history still holds the blobs; do not rewrite history.

Size: ~300 files deleted, ~5 doc lines. Risk: low. A rendered HTML mockup could reference an image by relative path without the basename in a text file I scanned only if generated at runtime; the basename grep covered `.html`, so that is covered.

### x-docs-3 (high): no single live/archive boundary; living docs sit in "record" directories and the boundary is hand-maintained in three places

Locations:
- Archive roots: `docs-site/scripts/check.mjs:11-21` (`ARCHIVE`: 9 prefixes), `:27-35` (`LIVING_IN_ARCHIVE`: 7 exceptions)
- Per-directory disclaimers: `docs/design/README.md:1-30`, `docs/specs/README.md:1-25`, `docs/superpowers/README.md:1-32`, `docs/subagent-management/README.md:1-25`
- Design-record homes: `docs/design/`, `docs/specs/` (4 specs), `docs/superpowers/specs/` (441), `docs/web-ui/specs/` (9), `docs/web-ui/history/`, `docs/web-ui/parity/`, `docs/subagent-management/` (7), `docs/research/` (7), `docs/superpowers/research/` (14), `docs/original-attractor-specs/` (3)

Evidence of misfiled living docs (all excluded from link checking because their directory is in `ARCHIVE`):
- `docs/design/context.md` (412 lines, edited 2026-10-25) is the current context-manager subsystem reference (`:6-40` describes `agent/internal/contextmgr`), yet lives in the directory whose README says "records, not references". Only a dated record links to it.
- `docs/design/session-activity-api.md:3-8` calls itself "the target contract for session activity", beside the 1,004-line guide `docs/product/session-activity.md`.
- `docs/design/stop-cancellation-outbox.md` is cited by 8 frontend code comments (`stores/threads.ts:3968`, `mutationOutboxIndexedDB.ts:106`, ...) and by `docs/product/message-recovery.md:229` as the design of record.
- `docs/subagent-management/11-delegate-resource-model.md:3` says "Shipped evergreen product authority" and `10-runtime-contracts.md:3` "Proposed evergreen spec", in a directory the checker treats as archive.
- `docs/web-ui/decisions.md` is in `LIVING_IN_ARCHIVE` (`check.mjs:33`) while `docs/web-ui/README.md:20` calls it "historical choices".
- `docs/design/mobile/{acceptance,ios-build-distribution,ios-v1-remaining}.md` are "living" per `check.mjs:28-30` but sit in `design/mobile`.
- Unchecked real breaks hide here: `docs/web-ui/history/ux-and-implementation-plan.md:26,35` link to `design-system.md` that does not exist at that relative path.
- Top-level naming collisions: `docs/skills.md` (user guide) vs `docs/skills/` (two SKILL.md files for agents); `docs/tools/` (2 files about agent tools) vs the user-facing `docs/*.md`.
- `docs/design/IMPLEMENTATION_PROMPT.md:1-30` is an agent hand-off prompt with a dead worktree path (`/home/jesse/git/prime-radiant/evener/.worktrees/fuzzing-toolkit`), not documentation.

Impact: a reader cannot tell from the path whether a doc is true today. Every move of a doc between "living" and "record" requires editing the checker, and living docs in archive dirs get no link or drift checks.

Proposed information architecture (smallest change that works):
```
docs/                      user guides + generated reference (published or published-adjacent)
docs/product/              ownership, principles, friction (unchanged)
docs/developing-evener/    contributor docs (unchanged)
docs/reference/            subsystem references that are living but not user-facing:
                           context.md, session-activity-api.md (once shipped, merge into product/session-activity.md),
                           subagent-management/11, web-ui/{design-system,keybindings,decisions}
docs/archive/              everything dated: superpowers/, design/ (records), specs/, research/, original-attractor-specs/, web-ui/{history,parity,specs,mockups}
```
Steps:
1. Move the living docs listed above out of record directories (they stay put in git history); update inbound links.
2. Move every record tree under `docs/archive/` in one pass, so `ARCHIVE` in `check.mjs` becomes the single prefix `docs/archive/` and `LIVING_IN_ARCHIVE` is deleted. Keep one README at `docs/archive/README.md` replacing the four near-identical disclaimers.
3. Update the 98+ code/doc path citations (`git grep "docs/superpowers\|docs/design/\|docs/web-ui/parity"` finds them: 184 + 34 + 53 lines) with a scripted sed, verified by `make docs-check` and a `git grep` for old prefixes.
4. Delete `docs/design/IMPLEMENTATION_PROMPT.md`.
Tests: `make docs-check` and `docs-site` `doc-links.test.mjs` fixtures updated for the new single prefix.

Size: ~2k lines of path edits (scripted), ~100 lines README/checker. Risk: medium. Many in-code comments cite old paths; a scripted rewrite plus grep gate makes this mechanical. Do after finding 1 so fewer files move.

### x-docs-4 (medium): evergreen docs cite source paths and names that no longer exist, and `naming.md` contradicts the lint config

Locations (all opened):
- `docs/developing-evener/conventions/naming.md:31,45,87,89,91,169,171` cite `internal/appwire/` and `internal/appsource/`; neither exists (AppWire is the top-level `appwire/` module dir; `cmd/evener-hub/internal/appsource` is the real path per `.golangci.yml:84-85`). `:47` cites `internal/hubapi/` (real: top-level `hubapi/`). `:49,167` cite `agent/transcript.go` (does not exist; there is an `agent/transcript/` package). `:51-52,169` cite `internal/launchconfig/` (real: `cmd/evener-hub/internal/launchconfig`).
- Contradiction with `.golangci.yml:73-100`: naming.md `:51` says launch config JSON/TOML is snake_case, but the lint override sets `cmd/evener-hub/internal/launchconfig` to `json: camel`.
- `naming.md:181-183`: says `mcpServers` is "Marked `// evener:naming-ignore`" in `agent/mcp_config.go` and `agent/plugin.go`. Neither file exists; the tags are in `agent/mcpconfig/config.go`, `agent/plugin/plugin.go:46` and `internal/plugins/catalog.go:35`, and they use `//nolint:tagliatelle // upstream .mcp.json key spelling`. The naming-ignore marker applies only to TOML (`cmd/evener-tomlcheck/main.go:227`).
- `naming.md:30` "203 snake-case JSON tags vs 169 camelCase" and `:185-190` "have all migrated" are migration history, not rules.
- `docs/architecture.md:141` lists `internal/tool` as a sibling subsystem; actual path is `agent/internal/tool` (`:261` has it right). The "Inside the agent module" section names ~14 of the ~53 packages under `agent/` and `agent/internal/`.
- `docs/developing-evener/fuzzing.md:295,305,314,326` name `TestDelegateSeqFuzz`, `TestDelegateSeqFuzzReplayClean` and file `agent/delegate_seqfuzz_test.go`, which do not exist (`git ls-files | grep seqfuzz` lists 10 other files; `forcecompact_doublelayer_seqfuzz_test.go` and `appstack_seqfuzz_test.go` exist but are missing from the table at `:312-323`). The same dead name is also used in `internal/devtool/gatesurface/drift_test.go:84`, which should be checked (outside this slice).
- `docs/developing-evener/testing.md:355-356` cites `internal/sshconn`; no such path.
- `docs/developing-evener/agent-test-serial-prefix.md:107` cites `TestDispose_Depth2Cascade_SharedBudget`, which has no declaration.
- `docs/product/history-content.md:70`: `internal/appserver.HandleThreadHistory` exists (`internal/appserver/history_content.go:20`), flagged by my script only because of the symbol suffix; no action.
- `docs/tools/transcripts.md:13-14` points to `docs/specs/session-transcript-tools.md`, which does not exist (the spec is `docs/superpowers/specs/2026-08-09-read-transcript-tools-design.md`).
- `docs/tools/memory.md:86` mentions "the existing generic CLI/TUI"; the TUI was removed (commit `afe78699b2` "remove the legacy evener TUI (#4725)", plan `docs/superpowers/plans/2026-10-16-legacy-tui-removal.md`).
- `docs/terminal-bench.md:132` "Edit `agent/prompts/base.md`": the only file is `agent/prompts/system.md.tmpl`.
- `docs/web-ui/README.md:40` says "thirteen live source comments cite" the parity docs; `git grep docs/web-ui/parity` outside `docs/` finds 7 lines in 7 files. `:42` says `specs/` holds "two dated feasibility designs"; it holds 9 files.
- `AGENTS.md:125` says `scripts/layoutguard`; the path is `cmd/evener-hub/frontend/scripts/layoutguard`.

Impact: the docs a contributor reads first send them to files that do not exist; the naming doc is wrong about which packages follow which casing, which is exactly what it exists to settle.

Fix plan: correct each reference at its source (one small PR, ~40 lines). For naming.md, delete the migration-status section and the "203 vs 169" sentence, replace the carve-out list with a pointer to the `overrides:` block in `.golangci.yml` as the source of truth. Add a cheap gate: extend `docs-check`'s link walker (or a Go audit test like the existing `*_audit_test.go` files in root) to resolve backticked repo paths in the maintained trees. My 40-line script found 33 candidates over 313 paths; about 20 were real, so false positives are manageable with an allowlist for runtime paths like `auth/*.json`.
Test strategy: the new path gate is the red-first test (it fails on the list above, passes after the fix).

Size: ~40 lines of docs edits, ~80 lines for the gate. Risk: low.

### x-docs-5 (medium): contributor env-var table is a hand-copied second source with live contradictions

Locations:
- `docs/developing-evener/environment.md` (152 lines, 104 table rows) vs `docs/environment-variables.md` (140 lines, 78 rows) vs `envvars/envvars.go` (110 vars)
- Contradictions: `environment.md:14` `EVENER_ALLOWED_DECISIONS` "Restricts tool-decision modes allowed by the active profile" vs registry `envvars.go:61` "workflow routing... not tool approval modes" (same as `environment-variables.md:44`). `environment.md:21` `EVENER_OFFLINE` "embedded snapshot only" vs registry `envvars.go:74` and `environment-variables.md:50` "can still use a newer valid local catalog cache".
- History in a reference table: `environment.md` KIMI_API_KEY row "Meaning changed at the flag day"; MOONSHOT_API_KEY row "New name for what KIMI_API_KEY used to mean".
- Enforcement: `git grep` finds no test or generator tying either page to the registry (only `agent/execenv/local_env_test.go` mentions the filename). `testing.md:1914` instructs authors to "Document it in `docs/developing-evener/environment.md`", i.e. a manual third step.

Impact: names stay in sync by luck (they do today: all 110 names present), meaning drifts silently. A contributor trusting the contributor page gets wrong semantics for two variables.

Fix plan: follow the `appwire-protocol.md` precedent. Generate the contributor table from `envvars` (name, visibility, summary, secret flag) with a small `internal/` generator + template (or `go:generate`), add the file to the `lint-generated` output list at `make/linting.mk:201`. Keep the prose-only sections (precedence, sandbox caches) in a hand-written page. Move richer per-variable prose that is not in the registry (EVENER_HOST_TEMP_BASES, EVENER_SCRATCH_DIR) into the registry `Summary`, or into the owning subsystem guide, so there is one source. Drop the history phrases. Update `testing.md:1903-1916` to "add one `envvars.Var` row; run `make generate`".
Test strategy: generated-output freshness gate; a unit test in `envvars` that every `Public` var appears in `docs/environment-variables.md` (name presence is behavior, not copy).

Size: ~150 lines of generator + template, ~100 lines of table removed. Risk: low-medium (registry summaries become user-visible text; review their wording).

### x-docs-6 (medium): `testing.md` (1,973 lines) is a rolling changelog, against the evergreen rule

Locations: `docs/developing-evener/testing.md`, e.g. `:125,175` ("Jesse, 2026-07-20/21", "Jesse, 2026-08-17"), `:223` ("retired when that runner ported to Go"), `:336` ("behind #495"), `:1000`, `:1037-1043` ("One in-band check used to be the exception... #4601 ... issue #3966"), `:1193` ("On 2026-08-08, Apple container 1.2.2 ran..."), `:1303-1367` ("Four classes were removed on 2026-10-14 (Jesse's ruling, issue #4615)... PR #4497's head... #4608... #4629... #4609, #4614, #4618, #4622"), `:1516` (a dated mistake story), `:1781`, `:1820`, `:1884`, `:1918-1946` ("Historical provider observations", live requests of May 21, 2026, for removed test selectors).

Evidence: 39 lines match the date/issue/"no longer/used to/retired" pattern in this one file, versus 0-14 in every other evergreen page (`friction.md` 14, `llm-provider-config-and-launch.md` 13). `AGENTS.md:15-30` and `docs/product/README.md:47-56` forbid "dated narratives, rollout logs, and PR progress" in these references. The file also contains the single longest section (`Post-Merge Gate`, `:831-1230`, ~400 lines) and a generated target table at `:1948+` that duplicates `make help`.

Impact: the policy page is too long to read, buries the standing rules under incident stories, and cites stale things (finding 4).

Fix plan: 1) delete "Historical provider observations" (`:1918-1946`) entirely; 2) rewrite each issue/date-anchored paragraph as the present-tense rule plus the owning test or script name, dropping issue numbers and rulings' dates (git blame keeps them); 3) split by the existing H2 boundaries into `testing.md` (policy, flakes, agent tests, destructive ops), `testing-gates.md` (post-merge gate, native gate, timing ratchet, browser guards), `testing-recipes.md` (the "A Test That Never Runs..." seed/driver recipes); 4) drop the hand-copied target table in favor of the already generated one in `building.md`/`linting.md` or a link to `make help`.
Test strategy: `make docs-check`; confirm any heading anchors cited elsewhere (`git grep "testing.md#"`) still resolve.

Size: ~300-400 lines deleted, rest moved. Risk: low. Keep the Copy-Is-Not-a-Test-Oracle section intact; AGENTS.md references it by name (`testing.md:62`).

### x-docs-7 (medium): finished plans and a private runbook sit in the top-level evergreen docs folder

Locations:
- `docs/resume-model-context-fix-plan.md` (304 lines), unpublished and unreferenced by any guide. It is a dated plan whose fix landed: `cmdutil/cmdutil.go:84-96` `ResolveResumeModelRef` makes persisted resume metadata win over `EVENER_MODEL` (the plan's problem 2), `agent/session_config.go:280-286` carries `ResolveProfile` (problem 1). Its cited test `agent/session_restore_profile_test.go:84` does not exist. Other docs call it "a dated plan" (`superpowers/specs/2026-08-20-makefile-and-docs-decomposition-design.md:344`).
- `docs/terminal-bench.md` (256 lines), unpublished, referenced from nowhere outside `docs/superpowers`. It is a personal runbook with a LAN IP and login (`:10`, `:98`, `:158`: `jesse@192.168.118.101`), `StrictHostKeyChecking=no` scp commands, and a dead edit target (`:132`).
- `docs/design/IMPLEMENTATION_PROMPT.md` (see finding 3).
- `docs/superpowers/agent-handoff-notes.md` (65 lines, written 2026-07-05 "for sessions on any host") and `issue-3689-evidence.md` (282 lines) sit loose at the `superpowers` root beside `handoffs/` and `proofs/`.

Impact: contributors and agents find these by listing `docs/` and treat them as current; one contains infrastructure detail that should not be in a repo meant to be shared.

Fix plan: delete `resume-model-context-fix-plan.md`, `IMPLEMENTATION_PROMPT.md`, `agent-handoff-notes.md`; move `terminal-bench.md` out of the repo (Jesse's notes) or reduce to a 20-line "how to run the benchmark" page without host credentials; move `issue-3689-evidence.md` into `proofs/` or delete with the proof set. Check `git grep` for each basename first (done for the first three: only dated records reference them).

Size: ~900 lines deleted. Risk: low; ask Jesse before deleting the terminal-bench runbook, since the content is his operational notes.

### x-docs-8 (medium): AGENTS.md carries ~100 lines of gate lore that belongs in contributor docs, plus anecdotes and a typo

Locations: `AGENTS.md` (252 lines, 15 KB)
- `:106-175` "Frontend gates": ~70 lines. Includes point-in-time claims: `:156-160` "(verified byte-identical as a point-in-time measurement on #1676; re-check with `tsc --listFilesOnly` rather than trusting that count to stay current)"; `:239-241` "two gate runs in one loop died to comment rewraps alone"; `:213-215` "an orphaned job once killed both the comment post and the detector silently".
- The same `node_modules`-symlink / `npm ci` rule appears at AGENTS.md `:160-175`, `docs/developing-evener/README.md:64`, `testing.md:975-990`, `agentic-testing.md:191`, `building.md:22`, and the "root `npx biome` checks nothing" rule at AGENTS.md `:113-120` and `docs/developing-evener/linting.md:23-30`. Same rule, five-plus homes.
- `:38-52` session-activity paragraph: narrow subsystem rules (recorded producers, "settled run generation", paging-test requirements) that `docs/product/session-activity.md` should own; the guide has the producer link (`:296`) but not the paging-test rule.
- `:209` heading typo `**Simplify-code*`.
- `:177-205` AppWire import rules (30 lines) duplicate `appwire-client/typescript/README.md` intent and `make lint-package-imports` output.
- `mobile-native/AGENTS.md` and `mobile-native/CLAUDE.md` are 3 and 1 line; CLAUDE.md is a duplicate pointer file.

Impact: the top-level agent instruction file is long enough that agents skim it, and any edit to gate behavior needs 5 coordinated doc edits. Because it is loaded into every agent session, bloat is paid on every task.

Fix plan: target ~120 lines. Keep: worktree rule, product behavior/evergreen rule, testing boundary (including "Copy Is Not a Test Oracle" pointer), the "run `make test-web` from the repo root; Biome via `make lint-biome`" one-liners, merge/RoboRev rules. Move: frontend-gate details and symlink rule into `docs/developing-evener/building.md` (one canonical "never `npm ci` through a symlinked `node_modules`" section, and link from the other four); AppWire import rules into `appwire-client/typescript/README.md`; session-activity testing rules into `docs/product/session-activity.md`. Delete the incident anecdotes and "#1676" measurement note. Fix the typo and `layoutguard` path. Leave `mobile-native/CLAUDE.md` unless the tool needs it.
Test strategy: `make docs-check` plus `rg` for any code that quotes AGENTS.md headings.

Size: ~130 lines removed from AGENTS.md, ~60 added elsewhere. Risk: low. Jesse owns this file's voice; propose the move before editing (it is instruction text with consequences).

### x-docs-9 (medium): the same topics are documented in two to five homes

Locations / evidence:
- Session activity: `docs/product/session-activity.md` (1,004 lines), `docs/design/session-activity-api.md` (375), `docs/superpowers/plans/session-activity-api.md` (204) plus four more `plans/session-activity-*.md`, `docs/specs/2026-10-09-session-activity-push-updates-design.md`, `docs/superpowers/specs/2026-10-25-issue-3638-session-activity-docs.md` (25 files matching "activity" under docs). The design doc's own header says it must not be cited as evidence of behavior while the product guide describes verified behavior.
- Delegate contracts: `docs/subagent-runtime-contracts.md` (337), `docs/subagent-management/10-runtime-contracts.md` (365, with a historical-record banner at `:5`), `11-delegate-resource-model.md` (2,244), `docs/job-control.md` (1,927). `subagent-management/README.md:7-13` itself lists the first and last as "the durable, current contracts" and says the numbered docs remain "for the reasoning".
- Hub listener precedence is stated at `docs/evener-hub-endpoints.md:5` and `docs/evener-hub.md:386-387`.
- Memory is intentionally layered in three (`docs/memory.md` user, `docs/product/memory.md` ownership, `docs/tools/memory.md` tools) and each links the others; leave as is.
- `docs/developing-evener/design-system.md` (113 lines, procedure) vs `docs/web-ui/design-system.md` (1,199, law): layered on purpose and cross-linked; leave.
- Provider docs: `llm-providers.md` (969), `llm-provider-config-and-launch.md` (482), `connecting-a-provider.md` (140), `configuration.md` (256) all published; `llm-provider-config-and-launch.md:481-482` points at a design spec for "the flag day", and has 13 history phrases (`:81,180,276,318,352,470`).

Impact: when behavior changes, someone has to find and update 2-5 pages; reviewers cannot tell which is authoritative.

Fix plan: for each topic pick one owner and reduce the others to a pointer or delete. Session activity: after the API ships, fold `design/session-activity-api.md` into `product/session-activity.md` and delete both it and the five `plans/session-activity-*.md`. Delegates: leave `subagent-runtime-contracts.md` and `job-control.md` as owners; move `subagent-management/11` into `docs/reference/` if it is truly shipped authority (finding 3), archive 06-10. Hub: keep the precedence sentence only in `evener-hub-endpoints.md`. Strip "no longer/flag day" sentences from the provider guides.
Test strategy: `make docs-check`; grep for anchors that other pages link to before deleting.

Size: ~1.5k lines deleted/merged. Risk: medium for delegate docs (high-traffic contracts); do each topic as its own PR.

### x-docs-10 (low): `docs/web-ui/README.md` is an index plus a 300-line behavior spec

Locations: `docs/web-ui/README.md` (420 lines): index at `:1-45`; behavior sections for provider setup (`:46-72`), Settings (`:74-171`), history demand (`:172`), browser transcript cache (`:189`), reading position (`:250`), Next-needs-you control (`:274`), Session Overview (`:313`), workspace geometry (`:339`), exited-session controls (`:350`), directory fields (`:367`), then a second history index (`:395-420`).

Evidence: AGENTS.md says "Keep navigation shallow"; the README is both the directory index and the only home of these client behaviors, outside `docs/product/` and unlinked from the subsystem map's client rows (check S-rows for web client). Statements such as "Organization does not change persistence or introduce new synchronization" (`:131`) describe a past reorg.

Fix plan: split behavior sections into `docs/web-ui/behavior.md` (or per-topic files under `docs/reference/web-ui/` per finding 3); leave a 40-line README index. Strip change-narration sentences. Link the new page from `docs/product/subsystems.md` client rows.
Size: ~420 lines moved, ~20 edited. Risk: low.

### x-docs-11 (low): docs-site link check is the only gate; no check for backticked paths, and no check that `docs/*.md` is in the manifest or intentionally unpublished

Locations: `make/repo.mk:74-100` (`docs-check`), `docs-site/scripts/check.mjs` (links and fragments), `docs-site/publication.json` (31 pages).
Evidence: 8 top-level pages are unpublished with no declared reason (`architecture.md`, `evener-hub-endpoints.md`, `evener-hub-state.md`, `evener-hub-web-routing.md`, `openai-spend-diagnostics.md`, `resume-model-context-fix-plan.md`, `session-auto-naming.md`, `terminal-bench.md`). Stale backticked source paths (finding 4) pass `docs-check` because only markdown link syntax is resolved.
Fix plan: add an `unpublished` list (with a one-line reason per entry) next to `pages` in `publication.json`, and have `check.mjs` fail on any `docs/*.md` in neither list. Add the backticked-path resolver from finding 4 to the same script. Tests: `docs-site/scripts/check.test`-style fixtures for a missing path and an undeclared page.
Size: ~100 lines of JS plus ~15 lines JSON. Risk: low.

## Not findings (checked, no action)

- `docs/appwire-protocol.md` size is fine: it is generated.
- Link health of maintained docs: no unresolved links in `docs/product`, `docs/developing-evener`, top-level `docs/*.md` beyond the placeholders (`docs/product/memory.md` `path`).
- Product docs vs the code spot-checks (subsystem rows, `HandleThreadHistory`, `ResolveResumeModelRef`, make-target names in AGENTS.md such as `lint-biome`, `test-web-browser`, `web-preflight.sh`) all resolve.
- AGENTS.md vs `~/.claude` global rules: no conflict found. AGENTS.md authorizes admin squash merge on green CI plus clean RoboRev; that is a project-level grant and consistent with `roborev-merge-rule` memory.

## Recommended fix sequence

1. Finding 7 and 4 first: small, independent, immediately correct the misleading evergreen docs (and add the backticked-path gate from 4/11 so they stay fixed). ~200 lines.
2. Finding 5: generate the contributor env table; fixes the two live contradictions. ~250 lines.
3. Finding 2: delete the unreferenced 70 MB of images (mechanical, zero code risk).
4. Finding 1: add the "plan is deleted when its PR completes" rule, then prune uncited plans, then specs. Do this before any directory moves so fewer files are renamed.
5. Finding 3: create `docs/reference/` and `docs/archive/`, scripted path rewrite, collapse `ARCHIVE`/`LIVING_IN_ARCHIVE`, delete the per-directory README duplicates.
6. Finding 6, 8, 9, 10: trim testing.md, slim AGENTS.md (with Jesse's approval of the voice), de-duplicate topic homes, split the web-ui README. One PR each; evergreen docs updated in the same PR as any code or checker change.
7. Finding 11 last: gate that keeps the new structure honest (`unpublished` list, path resolver).
