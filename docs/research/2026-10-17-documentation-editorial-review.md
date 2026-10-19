# Documentation editorial review, 2026-10-17

This record describes the documentation overhaul's research, independent reviews,
repairs, and verification. It is an internal audit record, excluded from the
public site's publication manifest and search index.

## Scope

The public corpus contains 29 Markdown pages plus the repository README. It
covers first use, daily work, configuration, extensions, automation, AppWire,
and operations. It tracks main and excludes the retired terminal interface.
The CLI remains documented.

Competitor research and its reading exceptions are recorded in
[Documentation patterns](2026-10-17-documentation-patterns.md) and the companion
[coverage ledger](2026-10-17-documentation-coverage.json). Historical pages and
generated symbol catalogs were not all read in full; the ledger distinguishes
full reading, sampling, and inventory-only coverage.

## Independent lenses

Each reviewer received the complete public corpus and a requirement to verify
consequential claims against implementation. Reviews used OpenAI and Lunaroute.
The two named-person lenses simulate editorial perspectives; neither person
participated or endorsed this work.

| Lens | Provider and model | Coverage and evidence |
| --- | --- | --- |
| High-school English teacher | OpenAI, gpt-6-astra | Complete source inventory, built HTML, comprehension and logical sequence |
| Journalist | Lunaroute, glm-5.3 | Complete source inventory, source-backed factual checks, built HTML, deterministic fixture execution |
| Novice user | Lunaroute, glm-5.3 | Complete guide reading, structural sampling of generated AppWire catalogs, built HTML, first-task fixture before and after repair |
| Boris Cherny-inspired product engineer | OpenAI, gpt-6-astra | Complete source inventory, implementation and generator review, deterministic tests, built HTML |
| Tim O'Reilly-inspired technical publisher | OpenAI, gpt-6-astra | Complete source inventory, reference coverage and ownership, generated links, built HTML |
| Professional editor | Lunaroute, glm-5.3 | Complete guide inventory, structural inspection of generated AppWire regions, source checks, HTML inspection in Chrome |

HTML inspection does not establish browser interaction. The parent separately
ran Playwright against a served site under the GitHub Pages base path. No review
claimed a live provider walkthrough, installer execution, or native app install.

## Material findings and repairs

| Finding | Repair and evidence |
| --- | --- |
| Sandbox worktrees falsely promised sibling-read confidentiality | Distinguished write separation from read access. Read-only and workspace-write permit reads outside the lane except masked paths; restricted mode retains explicit grants. Checked the policy resolver. |
| Unqualified fail-closed wording contradicted implicit delegate fallback | Scoped the guarantee to explicitly requested modes and linked the fallback's unconfined-shell risk. |
| Plugin references disagreed about manifest precedence | Documented Claude-first loading, Codex fallback only on absence, and no malformed-manifest fallback. Checked the loader. |
| Delegate metadata allowed an invalid scalar wildcard | Documented scalar `all`, accepted named-tool forms, and rejection of scalar `*` and unrestricted markers in lists. Checked the parser. |
| Hook documentation promised a nonexistent status display and secret-safe warnings | Replaced the display claim with observable fixture output; distinguished metadata events from warnings that can include hook output. |
| Core nested AppWire schemas were missing | Changed the documentation generator to traverse JSON-visible nested AppWire types and link method, notification, and field types. Regression tests failed before the change and passed afterward. External types remain documented leaves. |
| Job reference contained proposed rather than shipped timeout and approval behavior | Traced the shell schema, parser, runtime timer, buffered path, and escalation allowlist. Marked retained timeout parsing as compatibility-only and removed nonexistent shell approval states. |
| Doctor selectors were promised but undocumented | Added discovery commands, selector forms, ambiguity recovery, state-root precedence, and privacy cautions. |
| CLI parser defaults were confused with effective defaults | Explained each sentinel separately. Source tracing found that ordinary one-shot sessions default to unlimited rounds, although the CLI help still says 200. The reference flags that unresolved help-text discrepancy. |
| First-provider procedures lacked a bridge | Linked Settings and the guided Connect provider dialog in both directions and explained their shared host connection and credential ownership. |
| Upgrade syntax and executable prerequisites were incomplete | Added upgrade channels/tags, defined the separate `llmcall` executable, and linked extension fixture checkout instructions without requiring an Evener source build. |
| Current references narrated implementation history | Rewrote provider, Ollama, and deployment passages around current behavior; retained migration material under its owning section. |
| Installer reruns risked overwriting the downloaded script | Put fresh-directory creation and conflict recovery before the download. |
| Device pairing lacked client context | Linked iPhone setup and identified the separate Playdate client repository without inventing distribution guarantees. |

Review findings were adjudicated, not accepted mechanically. Examples:

- A default-environment Python run does not prove that `__pycache__` always
  appears. Bytecode settings and Git exclusions vary; the guide retains “may.”
- The shared TypeScript client owns the documented heartbeat cadence. Searching
  only the frontend missed it; the reviewer withdrew the concern after tracing
  the shared client.
- The `orclaude` instance name does not establish a model restriction. The
  migration recipe now describes its explicit protocol and model-glob settings.
  The recipe supplies its surface pin; the cited OpenRouter overlay block
  contains reasoning controls instead.
- The absent first-task fixture on remote main before merge was a publication
  dependency, not evidence that the branch's working fixture was broken.

## Verification

At the integrated editorial checkpoint, the parent ran:

```sh
npm --prefix docs-site run build
(cd docs-site && DOCS_BROWSER_CHANNEL=chrome npx playwright test)
go test ./internal/appwiredoc ./internal/appwirets \
  ./docs/examples/integrations ./docs/examples/extensions/... -count=1
```

The build ran 15 structural tests, including a real VitePress route regression,
staged all 29 pages, and checked rendered pages, fragments, base-prefixed assets,
and Markdown/LLM export membership. The browser suite passed desktop keyboard
navigation and local search, mobile navigation, and Mermaid SVG rendering on
initial load and client navigation. The five Go packages passed uncached.

The deliberate first-task bug remains in the delivered fixture. Independent
scratch runs reproduced its three failures, repaired only the implementation,
and passed all four fixed expectations without changing the checker.

Two four-angle code-quality reviews covered the publication tooling and the
later generator change. Accepted cleanups preserved behavior and tests. Both
AppWire generation targets were checked for freshness; the TypeScript output
needed no change after catalog-summary edits.

Known build warnings were left visible: the lazy Mermaid bundle exceeds Vite's
chunk-size warning threshold, and the local Node runtime reports a
`module.register()` deprecation. Neither was hidden by weakening a check.

## CI follow-up

The first PR run exposed gaps that local Chrome checks did not catch: the pinned
Chromium run timed out before Mermaid finished layout, the MCP example lacked
registered fuzz coverage, and a reflection loop in the generator test violated
the pinned modernize linter. The static aggregate failed because of that lint
finding. No automatic RoboRev review appeared during the wait.

Adding real MCP client/server fuzz coverage exposed a schema-validation bug:
the example accepted missing `message` values despite declaring the field
required. The example now uses the SDK's typed tool registration, with rejection
and subsequent successful-call checks. The fuzz target is registered without an
ignorelist exception. The reflection test now uses the standard field iterator.
The browser test now observes Mermaid's completed DOM replacement before running
its original SVG assertions, without increasing timeouts. CPU throttling
reproduced the failure and passed after the repair. Independent source review
accepted all three corrections. Parent reruns passed the build, pinned Chromium
and Chrome suites, Go packages, tagged fuzz seeds, fuzz gap/registry checks, and
scoped Go lint. A new Ubuntu CI run must confirm the browser repair there.

## Final disposition

All six reviewers accepted the integrated source and built HTML. The editor's
structural qualification covers the expanded 418-section AppWire type catalog,
not a line-by-line review of every generated field. The final small corrections
add charge disclosures beside hook and MCP examples, use the verified read-only
hook invocation, clarify truncated output's ellipsis, and normalize prose branding.

Main advanced during review. Its TUI removal, sandbox fixes, native composer,
and settings changes are integrated. The final combined tree passed the same
29-page build, 15 publication tests, three served-site Chrome checks, and five
uncached Go packages. Settings navigation now uses Session defaults and the
project's contextual settings; the sandbox guide retains native approval support
and documents root-directory identity checks.
GitHub Pages deployment has not yet been verified. Workflow implementation and
successful deployment are separate acceptance checks.
