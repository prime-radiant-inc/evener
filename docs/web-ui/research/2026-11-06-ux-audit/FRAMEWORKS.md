# Agentic UX engineering skills and frameworks

Research for Evener, requested by Jesse ("go look for agentic UX engineering skills and frameworks").
All sources accessed **2026-11-06**. Star counts and `pushed_at` dates are what the GitHub REST API returned that day; they move. Nothing here was installed or run against Evener.

## Evener's actual starting point (verified before judging fit)

Read from `cmd/evener-hub/frontend/package.json` and the guard scripts:

- React 19.2, Vite 8, TypeScript 6, **Biome 2.5.5** for lint/format (no ESLint).
- Styling is **CSS Modules plus `src/styles/tokens.css`** (no Tailwind, no CSS-in-JS, no Storybook).
- Browser guards drive **headless Chrome over the Chrome DevTools Protocol** (`scripts/browserGuardCdp.mjs`), with geometry assertions rather than pixel snapshots (see the comment block at the top of `scripts/layoutguard/run.mjs`). Playwright is **not** a dependency: it appears only as an optional peer of `@vitest/browser-playwright` in `package-lock.json`, and no script imports it.
- `src/styles/token-contract.test.ts` already walks every CSS file under `src/` and bans chromatic literals outside `tokens.css`, enforces per-widget `.module.css` naming, and checks dark/light token blocks.
- No axe-core, Lighthouse, Chromatic, Percy, or Storybook dependency exists today.

The brief said "Playwright-based browser guards". The guards are CDP-based. Any Playwright-only integration needs a small adapter, or a second browser stack.

---

## 1. Agent skills and procedures for critique, frontend design, UX review, accessibility, design-system compliance

### 1.1 Anthropic Agent Skills — `frontend-design`
- Source: https://github.com/anthropics/skills (accessed 2026-11-06), skill at https://raw.githubusercontent.com/anthropics/skills/main/skills/frontend-design/SKILL.md
- What it does: A `SKILL.md` (`name: frontend-design`) that makes the agent act as a design lead: write a compact design plan (4–6 named colours, type roles, ASCII wireframes, principles), review the plan against the brief before coding, build, then self-critique with screenshots. It names five "AI-default" looks to avoid (cream + serif + terracotta; near-black + acid accent; broadsheet rules; identical SaaS cards; all-caps eyebrow/middle-dot template chrome). It also lists quality-floor constraints: mobile responsiveness, visible focus, reduced motion, accessible contrast, CTA copy that names the action.
- License/maturity: repo has no root licence; README says "many skills are open source (Apache 2.0)" and the skill carries a `LICENSE.txt`. ~187.8k stars, pushed 2026-10-28, not archived.
- Install/invoke: installable as an Agent Skill directory (copy into `.claude/skills/`, or install the `example-skills` plugin: `/plugin marketplace add anthropics/skills` then `/plugin install example-skills@anthropic-agent-skills`). No invocation command inside the file; it is auto-selected by description.
- Fit: Good for *new UI direction* and for catching templated visual choices. Weak for reviewing an existing React codebase: it is prose guidance, not a checklist with file:line findings.
- Limits: no tool use, no accessibility measurement, no design-system check. "Screenshot and review if the environment allows" is optional.

### 1.2 Anthropic `knowledge-work-plugins` — design plugin
- Source: https://github.com/anthropics/knowledge-work-plugins (accessed 2026-11-06); plugin README https://raw.githubusercontent.com/anthropics/knowledge-work-plugins/main/design/README.md
- What it does: An installable Claude plugin with six slash commands (`/critique`, `/design-system`, `/handoff`, `/ux-copy`, `/accessibility`, `/research-synthesis`) and matching auto-selected skills. The three relevant skills:
  - `design-critique`: 5-dimension framework (first impression, usability, hierarchy, consistency, accessibility), emits a Markdown report with severity 🔴/🟡/🟢 and top-3 priorities. Reviews Figma URLs, screenshots, or descriptions — **not code**.
  - `accessibility-review`: WCAG 2.1 AA audit by POUR principle with a per-criterion findings table, contrast table, keyboard and screen-reader tables. Its own tips say it is a judgement review, not a tool run: "manual testing with VoiceOver/NVDA catches things I can't."
  - `design-system`: audit/document/extend modes; checks naming consistency, hardcoded hex colours, arbitrary spacing, custom fonts, component completeness. Figma-aware if a connector exists; no code parsing prescribed.
- License/maturity: Apache-2.0, ~34.9k stars, pushed 2026-11-06 (same day). Active.
- Install/invoke (exact, from the plugin README):
  ```bash
  claude plugin marketplace add anthropics/knowledge-work-plugins
  claude plugin install design@knowledge-work-plugins
  ```
- Fit: The best ready-made *critique vocabulary* for Evener's screenshots. Command names are stable and the outputs are structured. Not a code reviewer, so pair it with 1.3.
- Limits: Judgement-only. No axe run, no DOM, no source files. Figma-centric.

### 1.3 Vercel agent skills — `web-design-guidelines`, `composition-patterns`, `react-best-practices`
- Source: https://github.com/vercel-labs/agent-skills (accessed 2026-11-06); guidelines at https://github.com/vercel-labs/web-interface-guidelines
- What it does:
  - `web-design-guidelines` reviews UI code against the Vercel Web Interface Guidelines. It **fetches the rules at review time** from `https://raw.githubusercontent.com/vercel-labs/web-interface-guidelines/main/command.md` and reports terse `file:line` findings. The guidelines file is 17 categories, ~104 bullets: accessibility, focus states, forms, animation, typography, content handling, images, performance, navigation/state, touch, safe areas, dark mode, i18n, hydration, hover states, copy, anti-patterns.
  - `composition-patterns`: React composition rules (avoid boolean-prop proliferation, compound components, provider state, React 19 `use()` over `useContext`, no `forwardRef`).
  - `react-best-practices`: 70 performance rules in 8 priority groups.
- License/maturity: `vercel-labs/agent-skills` reports **no licence** (`license.spdx_id: null`), ~33.6k stars, pushed 2026-10-28. The guidelines repo itself is MIT, ~1.1k stars, pushed 2026-10-05.
- Install/invoke: `npx skills add vercel-labs/agent-skills` (exact, from the repo README). Then natural-language triggers ("review my UI").
- Fit: **Best code-level review fit for Evener.** It reads source, not a running app; `file:line` output suits PR review; the rule set covers forms, focus, motion, and copy that Evener's guards do not.
- Limits: Fetching rules at review time means a network dependency and a moving target (no pinned revision). It is a prompt/rule collection, not an executable check. Repo-level licence is absent — check before vendoring.

### 1.4 Impeccable
- Source: https://github.com/pbakaus/impeccable (accessed 2026-11-06); README https://raw.githubusercontent.com/pbakaus/impeccable/main/README.md
- What it does: A "design vocabulary" skill for coding agents. Commands: `critique` (scored UX review against usability heuristics, ranked fix order), `audit` (accessibility, performance, theming, responsive findings ranked P0–P3), `polish` (final pass). It ships a deterministic "AI slop" detector that uses no LLM and no API key, plus URL scans driven by the locally installed Chrome/Edge. Project context lives in `PRODUCT.md` and `DESIGN.md`; artefacts in `.impeccable/`.
- License/maturity: Apache-2.0, ~104.1k stars, pushed 2026-11-05, not archived. Rust engine with a Node installer.
- Install/invoke (exact, from README):
  ```bash
  npx impeccable install
  # then in the agent:
  /impeccable init
  /impeccable critique the pricing cards
  /impeccable audit
  /impeccable pin audit     # makes /audit its own command
  ```
  Codex uses `$impeccable`. `npx impeccable update` refreshes.
- Fit: Strongest single "review a running surface and rank what to fix" tool here. No LLM key, deterministic detector, URL scanning. It fits a React/Vite app because it reviews the rendered page and optional source context, not a specific framework.
- Limits: Extra binary and its own config/state (`.impeccable/`, `PRODUCT.md`, `DESIGN.md`). It is a separate toolchain, not an Evener skill; the README's accuracy claims are unbenchmarked. Chrome-only URL scans.

### 1.5 Superpowers
- Source: https://github.com/obra/superpowers (accessed 2026-11-06)
- What it does: A process/methodology skill pack: brainstorming, writing plans, executing plans, TDD, systematic debugging, subagent-driven development, code review, worktrees, verification. **No design, frontend, UX, or accessibility skill** is in its `skills/` listing (15 skills, all process). Its optional visual companion is only used by brainstorming.
- License/maturity: MIT, ~313.1k stars, pushed 2026-11-03.
- Install/invoke: per-harness plugin install, e.g. `/plugin install superpowers@claude-plugins-official` or `/plugin marketplace add obra/superpowers-marketplace`. Full list in its README.
- Fit: Keep it for process. Do not expect UX coverage from it. The companion repo **superpowers-chrome** (https://github.com/obra/superpowers-chrome, MIT, ~371 stars, pushed 2026-10-10) is the relevant browser piece — CDP-based, zero dependencies, one `use_browser` MCP tool with auto-capture of screenshots, HTML, markdown. Closest in spirit to Evener's existing CDP guards.
- Limits: `superpowers-chrome` is a browser driver, not a UX auditor. Its auto-capture cannot see closed shadow roots or cross-origin iframes; screenshots are unscrubbed pixels.

### 1.6 ui-ux-pro-max
- Source: https://github.com/nextlevelbuilder/ui-ux-pro-max-skill (accessed 2026-11-06)
- What it does: A large *design-intelligence catalogue* behind an agent skill: 80 UI styles, 192 industry rules, 192 palettes, 74 font pairings, 34 landing patterns, 119 UX guidelines, 22 stacks. Its v2 flagship is a Design System Generator: `search.py "<brief>" --design-system` emits layout, style, palette, type, effects, anti-patterns, and a pre-delivery checklist. It gives guidance; the README says it does not rewrite the app.
- License/maturity: MIT for the open-source Basic version (paid Premium tier exists), ~145.8k stars, pushed 2026-11-05.
- Install/invoke (exact): `npm install -g ui-ux-pro-max-cli` then `uipro init --ai claude`; or `/plugin marketplace add nextlevelbuilder/ui-ux-pro-max-skill` then `/plugin install ui-ux-pro-max@ui-ux-pro-max-skill`. Requires Python 3 for `scripts/search.py` (stdlib only, no network).
- Fit: Useful for *new* screens and for a house style reference. Heavy for reviewing existing screens; the catalogue is generic, not Evener-specific.
- Limits: Advice catalogue, not an inspection tool. No DOM, screenshots, axe, or source parsing. Paid tier blurs the licensing story.

### 1.7 Emil Kowalski's design skills
- Source: https://github.com/emilkowalski/skills (accessed 2026-11-06)
- What it does: 17 skills aimed at interaction craft: `review-animations`, `improve-animations`, `find-animation-opportunities`, `choreograph`, `animation-performance`, `apple-design`, `pick-ui-library`, `prototype`, `mobile-native`, `break-ui` (worst-case data: long names, huge counts, empty lists), `ask-sonner`, and more.
- License/maturity: MIT, ~53.9k stars, pushed 2026-11-04.
- Install/invoke: `npx skills@latest add emilkowalski/skills` (exact).
- Fit: `break-ui` and `review-animations` are directly useful for a chat/dock UI that must survive long transcripts and narrow panes. Narrow scope, opinionated.
- Limits: Animation/interaction craft, not accessibility or design-system compliance. No automated checks.

### 1.8 Purpose-built, tiny audit skills
- `brumley-agents/claude-ui-ux-audit-skill` — https://github.com/brumley-agents/claude-ui-ux-audit-skill (accessed 2026-11-06). A Claude Code skill that walks a running app at 1440/768/375, screenshots each state, runs axe-core on every URL, runs Lighthouse mobile against a production build, writes `report.md` (P0–P3), screenshots, and a Playwright smoke test. Requires three MCP servers (`@playwright/mcp`, `a11y-mcp`, `lighthouse-mcp`). **3 stars, no licence, pushed 2026-04-30, created 2026-04-30.** Conceptually the closest end-to-end pipeline to what Evener needs; too young to depend on.
- `mdigital/ux-heuristic-audit` — https://github.com/mdigital/ux-heuristic-audit (accessed 2026-11-06). Crawls a sitemap (or nav), captures desktop 1440×900 and mobile 390×844 screenshots, accessibility tree, DOM outline, `@axe-core/playwright` violations, console errors, plus three interactive probes (search, empty-form submit, 10-Tab focus trace), then scores Nielsen's 10 heuristics /5 and writes `REPORT.md` + PDF. **0 stars, no licence, pushed 2026-04-14.**
- Form: both are installable skills (clone into `~/.claude/skills/`), not just prompt collections. Both need Playwright + Node.
- Fit for Evener: good blueprints to copy, bad dependencies. Evener has no Playwright and no skill install path for them.

---

## 2. Browser-based UX audit agents and harnesses

### 2.1 Playwright MCP
- Source: https://github.com/microsoft/playwright-mcp (accessed 2026-11-06)
- What it does: An MCP server that exposes `browser_*` tools: navigate/click/type/fill form, `browser_snapshot` (accessibility tree), `browser_take_screenshot`, console and network inspection, tabs, storage/cookies, request routing, tracing/video, PDF, plus opt-in `browser_verify_*` assertions. It acts on the accessibility tree, not pixels; screenshots are for humans, not for choosing actions.
- License/maturity: Apache-2.0, ~39.2k stars, pushed 2026-11-05.
- Install/invoke (exact):
  ```bash
  claude mcp add playwright npx @playwright/mcp@latest
  # or generic config: npx @playwright/mcp@latest
  ```
- Fit: Excellent browser reach for an agent; but it introduces a Playwright/Chromium stack alongside Evener's CDP guards. Note its own README recommends the separate `playwright-cli` for coding agents, because MCP loads "large tool schemas and verbose accessibility trees" into context.
- Limits: It is a driver, not a UX judge. No heuristics, no axe, no scoring. Snapshot-heavy.

### 2.2 Playwright CLI + skills
- Source: https://github.com/microsoft/playwright-cli (accessed 2026-11-06)
- What it does: Token-efficient CLI wrapper over Playwright for coding agents: sessions, navigation, selectors, screenshots, storage state, network mocking, tracing, video, test generate/heal. `playwright-cli install --skills` installs reference skills for common tasks.
- License/maturity: Apache-2.0, ~15.0k stars, pushed 2026-11-05.
- Install/invoke (exact):
  ```bash
  npm install -g @playwright/cli@latest
  playwright-cli --help
  playwright-cli install --skills
  ```
- Fit: If Evener wants agent-authored browser flows without MCP context bloat, this is the better Playwright entry point. Still a second browser stack.
- Limits: Driver only; "does not force page data into LLM" means the agent sees less unless it asks.

### 2.3 Chrome DevTools MCP
- Source: https://github.com/ChromeDevTools/chrome-devtools-mcp (accessed 2026-11-06)
- What it does: MCP server over a live Chrome: performance traces with actionable insights (optionally CrUX), console messages with source-mapped stacks, network requests, screenshots, navigation, and DOM/DevTools data. A `--slim` mode exposes a reduced tool set; CLI mode exists (`chrome-devtools navigate_page`, `take_screenshot`).
- License/maturity: Apache-2.0, ~54.9k stars, pushed 2026-11-05.
- Install/invoke (exact config):
  ```json
  {"mcpServers":{"chrome-devtools":{"command":"npx","args":["-y","chrome-devtools-mcp@latest"]}}}
  ```
  Claude Code plugin: `/plugin marketplace add ChromeDevTools/chrome-devtools-mcp` then `/plugin install chrome-devtools-mcp@chrome-devtools-plugins`.
- Fit: **Closest match to Evener's existing CDP approach**, and it adds performance traces and console/network forensics the guards lack. Best browser-side fit after 2.4.
- Limits: Not a UX heuristics tool; no accessibility scoring. Requires Chrome stable.

### 2.4 Superpowers Chrome (see also 1.5)
- Source: https://github.com/obra/superpowers-chrome (accessed 2026-11-06)
- What it does: Zero-dependency CDP driver with a 17-command CLI (`start`, `tabs`, `new`, `navigate`, `click`, `fill`, `select`, `eval`, `extract`, `attr`, `html`, `screenshot`, `markdown`, `wait-for`, `wait-text`, `raw`) or a single `use_browser` MCP tool with auto-capture.
- License/maturity: MIT, ~371 stars, pushed 2026-10-10.
- Install/invoke: `/plugin install superpowers-chrome@superpowers-marketplace`, or MCP via `npx github:obra/superpowers-chrome`.
- Fit: Very close to Evener's `browserGuardCdp.mjs`; could replace bespoke plumbing for agent-driven exploration. Not an auditor.
- Limits: Auto-capture misses closed shadow roots and cross-origin iframes; secret guard is not a security boundary.

### 2.5 browser-use
- Source: https://github.com/browser-use/browser-use (accessed 2026-11-06)
- What it does: Python/TypeScript library for LLM browser agents: local, cloud, or remote-over-CDP browsers, 31 browser actions, custom tools, structured output, and replay of a saved run with new values (still model calls for extract/summary). Requires an LLM (`ChatOpenAI`, `ChatAnthropic`, `ChatGoogle`, Ollama, or its own `bu-2-0`).
- License/maturity: MIT, ~120.2k stars, pushed 2026-11-06. Very active.
- Install/invoke: `uv add browser-use`; run the script with `uv run agent.py`. Quickstart uses `OPENAI_API_KEY`.
- Fit: Good for "agent as user" scenario exploration where the path is unknown. Poor for deterministic CI gates: replay breaks on the first unmatched step and still calls the model.
- Limits: Non-deterministic, model-cost per run, no built-in UX rubric or a11y engine.

### 2.6 Stagehand
- Source: https://github.com/browserbase/stagehand (accessed 2026-11-06)
- What it does: TypeScript/Python/Go SDK over Playwright that mixes deterministic Playwright calls with three AI primitives: `observe()` (returns real selectors for a natural-language target), `act()` (self-healing action), `extract()` (schema-validated data). Local Chrome or Browserbase cloud; session persistence via `userDataDir`.
- License/maturity: MIT, ~26.0k stars, pushed 2026-11-06.
- Install/invoke: `pnpm add @browserbasehq/stagehand 'zod@~4.4.3'`; local runs need Chrome.
- Fit: Best "gradually harden an agent flow into a script" option: start with `act()` then replace with `page.locator(...)`. But it sits on Playwright, which Evener does not have.
- Limits: Requires an LLM key and Chrome; AI steps are non-deterministic; no a11y or heuristic layer.

### 2.7 AgentDesk (ambiguous name)
- Sources: https://github.com/revanthpp/AgentDesk (0 stars, MIT, pushed 2026-06-28; browser sandbox for computer-use agents: policy, approvals, trajectory recording, deterministic eval, replayable reports) and https://github.com/agentdesksdk/agentdesk (1 star, MIT, pushed 2026-09-03; WebMCP capability-virtualization runtime).
- What it does: Neither is a UX audit agent. The first is a control plane/sandbox for computer-use agents; the second is a WebMCP runtime. A third unrelated "AgentDesk" is an insurance agency SaaS.
- License/maturity: MIT for both; 0 and 1 stars; single-author projects.
- Install/invoke: not verified beyond repository metadata.
- Fit: Low. Interesting only as a pattern for recording and replaying agent trajectories.
- Limits: Not maintained at scale, no UX rubric, no Evener integration.

### 2.8 WebArena and BrowserGym (harnesses)
- WebArena: https://github.com/web-arena-x/webarena (accessed 2026-11-06). Self-hostable web environments (shopping, shopping admin, Reddit, GitLab, map, Wikipedia) and 812 examples scored by task success. Apache-2.0, ~1.6k stars, last push 2025-11-26 (quiet for ~11 months relative to access date). Setup needs Docker/AMI, `playwright install`, `.auth` cookies, an LLM key, and `python run.py --instruction_path ...`.
- BrowserGym: https://github.com/ServiceNow/BrowserGym (accessed 2026-11-06). Gymnasium environments wrapping WebArena, VisualWebArena, WorkArena, MiniWoB, AssistantBench, WebLINX, OpenApps, TimeWarp, Knows. `pip install browsergym` + `playwright install chromium`. ~1.4k stars, pushed 2026-10-28, licence NOASSERTION. README explicitly says it is "not a consumer product".
- Fit: These measure **agent task success on standard sites**, not the usability of Evener's UI. Useful if Evener wants to benchmark its own agent's browsing ability; not a UX review tool.
- Limits: Heavy setup, self-hosted sites, no heuristic or visual-quality reporting.

---

## 3. UX simulation and evaluation frameworks

### 3.1 UXAgent
- Source: https://github.com/neuhai/UXAgent and paper https://arxiv.org/abs/2504.09407 (both accessed 2026-11-06). Paper: "UXAgent: A System for Simulating Usability Testing of Web Design with LLM Agents", Lu et al., submitted 2025-04-13, revised 2025-09-19.
- What it does: Generates personas, runs LLM agents through a real website via a "Universal Browser Connector", logs interactions and screenshots, then lets researchers review qualitative surveys and quantitative logs or interview agents. Python, Playwright Chromium, Flask + a Node UI for the experiment console.
- License/maturity: Repository has **no licence**, ~118 stars, last push 2025-11-05 (stale at access date).
- Install/invoke (exact, from README):
  ```bash
  git clone git@github.com:neuhai/UXAgent.git
  uv sync
  uv run playwright install chromium
  export ANTHROPIC_API_KEY=sk-ant-123
  uv run -m src.simulated_web_agent.main --intent "Buy a Jacket from Amazon" --start-url "https://www.amazon.com" --max-steps 20 --wait-for-login
  ```
- Fit: The best-known open prototype for agent-as-user usability testing. It is a research system, not CI infrastructure; output goes to `runs/<timestamp>` with logs, traces, screenshots, metrics.
- Limits: Needs an LLM key, no licence, unmaintained since 2025-11, no scoring rubric, no Evener harness. The paper's own 16-researcher evaluation reported enthusiasm plus concerns about LLM-agent use in UX studies.

### 3.2 Synthetic Heuristic Evaluation (evidence)
- Source: https://arxiv.org/abs/2507.02306 (accessed 2026-11-06). Zhong, McDonald, Hsieh, submitted 2025-07-03.
- What it does: Compares multimodal-LLM synthetic heuristic evaluation against experienced UX practitioners on two apps. The LLM found 73% and 77% of known issues; five human evaluators found 57% and 63%. It was consistent and strong on layout issues, but struggled to recognise some UI components/conventions and cross-screen violations.
- License/maturity: Research paper; no tool released in the abstract. cs.HC.
- Fit: The strongest single argument for adding an LLM screenshot critique layer — and a warning that it misses component semantics and cross-screen rules.
- Limits: Two apps, one model generation, not a reproducible product.

### 3.3 "Can GPT-4o Evaluate Usability Like Human Experts?"
- Source: https://arxiv.org/abs/2506.16345 (accessed 2026-11-06). Submitted 2025-06-19.
- What it does: Compares GPT-4o heuristic evaluation against HCI experts on a web system with a Nielsen-based prompt. GPT-4o identified **21.2%** of the issues humans found, added 27 new issues, and produced false positives from hallucination and prediction. It did better on aesthetic/minimalist design and match-to-real-world; worse on flexibility, control, and efficiency.
- Fit: Contradicts the optimistic result in 3.2. Together they say: LLM heuristic review is useful as a *supplement* with human triage, not a replacement. Model and prompt choice dominate.
- Limits: One model, one system, screenshot-only input.

### 3.4 Human-in-the-loop review tooling
- Chromatic and Percy (see 4.5, 4.6) are the practical human-review layer: baseline approval, per-change diffs, reviewer assignment/discussions, CI status. Chromatic explicitly supports reviewer assignment and team sign-off.
- No open-source, local-first simulator with a review UI surfaced besides UXAgent's experiment console.

---

## 4. Visual, accessibility, and interaction regression tooling

### 4.1 axe-core and @axe-core/playwright
- Source: https://github.com/dequelabs/axe-core (accessed 2026-11-06); Playwright wrapper https://github.com/dequelabs/axe-core-npm/tree/develop/packages/playwright
- What it does: Accessibility engine for rendered DOM. `@axe-core/playwright` injects axe into a Playwright page: `new AxeBuilder({ page }).withTags(['wcag2a','wcag2aa']).analyze()`. Supports `withRules`, `withTags`, `disableRules`, `include`/`exclude`. Deque/Storybook state axe catches "up to 57% of WCAG issues"; the rest needs manual keyboard and screen-reader testing.
- License/maturity: MPL-2.0, ~7.8k stars, pushed 2026-11-05; npm `axe-core` 4.14.0 and `@axe-core/playwright` 4.14.0, MPL-2.0.
- Install/invoke: `npm install playwright` + `npm install @axe-core/playwright` (for the wrapper). With Evener's CDP guards, axe can be injected with `Runtime.evaluate` instead; that adapter does not exist yet.
- Fit: **Highest value per line of code** in this whole report. It is deterministic, local, and additive to the existing guards. It does not care about React, CSS Modules, or Tailwind.
- Limits: Rendered DOM only; no keyboard flow, focus order in practice, screen-reader announcements, or cognitive load. Wrapper's `include`/`exclude` cannot take multiple selectors in one array; package does not follow SemVer.

### 4.2 Lighthouse CI
- Source: https://github.com/GoogleChrome/lighthouse-ci (accessed 2026-11-06)
- What it does: Runs Lighthouse per commit, asserts against results (accessibility, SEO, offline, performance best practices), tracks scores, sets performance budgets on scripts/images, compares two versions, and runs repeats to reduce variance.
- License/maturity: Apache-2.0, ~7.1k stars, pushed 2026-11-03.
- Install/invoke (exact from README):
  ```bash
  npm install && npm install -g @lhci/cli@0.16.x
  npm run build
  lhci autorun
  ```
  README points to `docs/configuration.md`; it does **not** name `lighthouserc.js` in the snippet (unverified detail).
- Fit: Good CI gate against a built Vite bundle, no SaaS. Complements axe (broader score, less specific a11y findings).
- Limits: Lab data only, score variance across hardware, catches little about interaction flows. Needs a server (or static dist) and an approved budget.

### 4.3 Playwright visual comparisons
- Source: https://playwright.dev/docs/test-snapshots (accessed 2026-11-06)
- What it does: `await expect(page).toHaveScreenshot()` writes a golden on first run and pixel-compares later with `pixelmatch`. Options: `threshold` (default 0.2), `maxDiffPixels`, `stylePath`, `type: 'webp'`. Baselines live in `<test-file>-snapshots/` and include browser/platform in the filename. Update with `--update-snapshots`.
- License/maturity: Apache-2.0 (Playwright itself); built into `@playwright/test`. Current maintenance is high.
- Install/invoke: needs `@playwright/test`; no Evener Playwright today.
- Fit: Cheap visual regression *if* Evener adds Playwright. Evener deliberately uses geometry assertions instead because pixel diffs are "brittle across fonts/platforms" (`layoutguard/run.mjs` comment). This tool contradicts an explicit local design decision.
- Limits: Same-OS/browser requirement; captures hover state unless avoided; no semantic understanding.

### 4.4 Storybook test-runner and a11y addon
- Source: https://github.com/storybookjs/test-runner (accessed 2026-11-06); docs https://storybook.js.org/docs/writing-tests/accessibility-testing
- What it does: The test-runner executed every story in a real browser as a Jest test (render smoke test, play functions, optional axe). The a11y addon runs axe-core in the Accessibility panel and reports violations/passes/incomplete; docs say it covers WCAG 2.0/2.1 A+AA plus best practices, and axe catches up to 57% of WCAG issues.
- License/maturity: test-runner MIT, ~280 stars, **archived**; README warns "Official support for Storybook Test Runner has ended" and points Vite users to Storybook's Vitest integration. npm `@storybook/test-runner` 0.26.0 still publishes. a11y addon is active (`npx storybook add @storybook/addon-a11y`).
- Fit: Evener has no Storybook. Adopting Storybook just for a11y is a large detour; the addon is redundant with direct axe-core.
- Limits: Story-level, not flow-level; archived runner; async/Suspense stories can escape checks; `region` rule disabled by default in Storybook.

### 4.5 Chromatic
- Source: https://www.chromatic.com/docs/ (accessed 2026-11-06)
- What it does: Cloud UI testing: visual tests (layout/font/colour), interaction tests, and axe-based accessibility tests with remediation tips; reviewer assignment and discussions; runs in cloud browsers/viewports; TurboSnap reduces usage cost. Builds on Storybook, React Native, Vitest, Playwright, or Cypress.
- License/maturity: Commercial SaaS from the Storybook team; active. Price not on the docs page read.
- Install/invoke: sign up, connect project, push; no CLI command captured.
- Fit: Good if a SaaS is acceptable and Evener adds Storybook/Playwright. Otherwise the account requirement is the blocker.
- Limits: Hosted; usage-based; unstable renders are flagged and do not block builds; no source-level design-system rules.

### 4.6 Percy (BrowserStack)
- Source: https://percy.io/ (accessed 2026-11-06)
- What it does: Captures UI screenshots, compares against approved baselines, highlights diffs, supports Storybook/Playwright/Cypress and CI, cross-browser and responsive widths, approval workflow.
- License/maturity: Commercial SaaS, BrowserStack product; plan-based screenshot/concurrency quotas.
- Install/invoke: SDK + CI token per project; exact commands not captured.
- Fit: Same category as Chromatic; cloud dependency.
- Limits: Visual only, not a functional runner; dynamic content needs stabilisation; baselines need maintenance.

### 4.7 Momentic
- Source: https://momentic.ai/ (accessed 2026-11-06)
- What it does: AI QA platform. Tests are plain-English YAML in the repo (`fileType: momentic/test/v2`) with `act`, `assert`, `click`, reusable modules; runtime locator recovery and auto-healing that proposes test-file diffs; MCP for coding agents; local CLI, CI, or hosted browsers/simulators.
- License/maturity: Commercial. Commands verified from the page: `momentic run`, `momentic doctor`, `momentic ai heal`, `momentic ai classify --interactive`, `momentic run --parallel auto`.
- Fit: Strong option for scenario tests and maintenance, but cloud/sign-in and vendor dependency.
- Limits: Proprietary runtime; healing can mask real UI regressions if diffs are rubber-stamped.

### 4.8 Meticulous
- Source: https://www.meticulous.ai/ (accessed 2026-11-06)
- What it does: Records real user sessions via a script tag, generates visual E2E tests from recorded flows/branches, replays them on PRs, mocks backend responses by saving/replaying them, and shows workflow impact.
- License/maturity: Commercial SaaS (`app.meticulous.ai`, project ID, CI integration).
- Fit: Best at "what did this PR change for real user flows" without writing tests. Requires traffic and a cloud account.
- Limits: Unrecorded paths are uncovered; mocked backends hide server regressions; no design-system rules.

### 4.9 QA Wolf
- Source: https://www.qawolf.com/ (accessed 2026-11-06)
- What it does: Managed "AI Test Factory": agents + QA experts create/run/maintain Playwright/Appium E2E tests across web, iOS, Android, Windows; investigate failures and report verified bugs. MCP endpoint `https://app.qawolf.com/api/mcp`.
- License/maturity: Commercial service; automation stack is open-source Playwright/Appium, product is not open source.
- Fit: Low for Evener's local-first posture; high if the team wants tests maintained for them.
- Limits: Account/sales; tests are E2E, not UX heuristics; no design-system drift.

### 4.10 Biome a11y rules (existing, no new tool)
- Source: https://biomejs.dev/linter/rules/use-alt-text/ and https://biomejs.dev/linter/ (accessed 2026-11-06)
- What it does: Biome has an `a11y` rule group ("rules focused on preventing accessibility problems"); `lint/a11y/useAltText` is recommended and enforced by default. This is static JSX analysis, complementing axe's rendered-DOM analysis.
- Fit: Already installed. Free coverage with no new dependency.
- Limits: Static only; cannot see computed contrast, focus order, or runtime ARIA state.

---

## 5. Design-system compliance and drift detection

### 5.1 Buoy
- Source: https://github.com/ahoybuoy/buoy (accessed 2026-11-06); README https://raw.githubusercontent.com/ahoybuoy/buoy/main/README.md
- What it does: "Design system guardian for code." Scans React/Vue/Svelte/Angular/Lit/Stencil/Alpine/HTMX for hardcoded colours, arbitrary spacing, Tailwind escape hatches (`p-[13px]`), naming inconsistencies (`ButtonNew`, `ButtonV2`), unused components, semantic prop mismatches, repeated class patterns, framework sprawl, and detached component instances. Maps adoption/unmanaged UI surface and impact ("blast radius") from owned components/tokens. Commands: `buoy show all --json`, `buoy drift check`, `buoy drift fix --apply`, `buoy rescue plan`, `buoy dock`; an MCP server and a Claude plugin.
- License/maturity: MIT, **20 stars**, created 2025-12-28, pushed 2026-10-21. Young but active.
- Install/invoke (exact):
  ```bash
  npx ahoybuoy show all
  npx ahoybuoy drift check
  npx @buoy-design/cli mcp install claude
  /plugin marketplace add ahoybuoy/buoy
  /plugin install buoy@buoy
  ```
- Fit: **Best maintained open candidate for design-system drift**, and the only one here that ships both a CLI gate and an agent/MCP surface. MIT and local.
- Limits: README says nothing about CSS Modules (Evener's styling), so its colour/spacing detection may not parse `.module.css`; Tailwind-specific rules are irrelevant. 20 stars means little field testing. No documented "raw control" rule. Its `rescue` mode leaves ambiguous findings review-only.

### 5.2 ui-drift
- Source: https://github.com/pcabel85/ui-drift (accessed 2026-11-06)
- What it does: AST-based React/TypeScript auditor: design-system adoption % (approved imports vs hand-rolled components), duplicate component families (button/card/modal), inline styles and token violations (`style={{}}`, hardcoded colours/spacing), wrapper sprawl. Rolls into a 0–100 health score; `--json`, `--html`, `--score-only` (exits 1 under 70). Config `ui-drift.config.json` with `designSystemImports`, `internalDSPaths`, `ignorePaths`.
- License/maturity: **No licence**, **0 stars**, pushed 2026-03-15, README says "Experimental (v0.2)".
- Install/invoke (built from source; no published CLI command):
  ```bash
  git clone https://github.com/pcabel85/ui-drift.git
  cd ui-drift && npm install && npm run build
  node dist/cli.js ./my-app --json
  ```
- Fit: **Conceptually the closest to Evener's ask** — it detects component adoption gaps and duplicate component families in React/TS, which Buoy only partly does. But no licence and 0 stars make it a blueprint, not a dependency.
- Limits: No CSS Modules mention; no raw-control rule; no interaction-pattern analysis; roadmap items (GitHub Action, trend reports, rule packs) unchecked.

### 5.3 Subpixel
- Source: https://www.subpixel.design/ (accessed 2026-11-06)
- What it does: Claims to scan AI-generated code for hardcoded colours/spacing/breakpoints/font sizes, reinvented components, magic media queries, deprecated prop combinations, ARIA/focus/keyboard gaps, and wrong semantic token names. Claims local-only operation, `npx subpixel init`, `npx subpixel scan ./src`, `subpixel fix --interactive`, CI via GitHub Actions.
- License/maturity: Site claims "open source and free to use", but **the GitHub link points at `https://github.com` with no repo path**, and npm `subpixel@0.4.1` is an unrelated image-generation package (`github.com/KingPin/SubPixel`). Repo, package name, and licence are **unverified**.
- Fit: If the claims hold, it is the closest to "raw control / reinvented component" detection. Treat as marketing until the repo and package resolve.
- Limits: Unverified install path; commercial "Signal Engine" upsell; no docs read.

### 5.4 Omlet
- Source: https://omlet.dev/ (accessed 2026-11-06); repo https://github.com/zeplin/omlet
- What it does: Component analytics for React: CLI scans the codebase for components and usage; VS Code extension and dashboard show adoption, legacy components, unused props, similar components. From the Zeplin team.
- License/maturity: CLI `@omlet/cli` 2.3.0, MIT; repo ~73 stars, pushed 2026-08-20. The product is moving open source; the dashboard is a hosted service.
- Install/invoke: `npx @omlet/cli init` (exact from site).
- Fit: Good adoption metrics if a hosted dashboard is acceptable. It reports usage; it does not enforce a rule or fail CI.
- Limits: React-only; dashboard/account; no hardcoded-style or token rules documented; no drift gate.

### 5.5 react-scanner
- Source: https://github.com/moroshko/react-scanner (accessed 2026-11-06)
- What it does: Static AST scanner that extracts rendered React components, props, and locations into JSON; processors `count-components`, `count-components-and-props`, `raw-report`. TypeScript supported.
- License/maturity: MIT, ~661 stars, pushed 2026-04-20 (maintenance quiet).
- Install/invoke: `npx react-scanner -c /path/to/react-scanner.config.js` (exact).
- Fit: The cheapest way to measure component adoption in Evener's TSX and to list unused components. No styling assumptions, so it works with CSS Modules.
- Limits: Static only; no runtime/dynamic usage; non-literal props become AST type strings unless customised; does not report subcomponents by default; no severity or score.

### 5.6 stylelint-declaration-strict-value
- Source: https://www.npmjs.com/package/stylelint-declaration-strict-value (accessed 2026-11-06)
- What it does: Stylelint rule requiring specified CSS properties to use a variable/function/keyword instead of a literal — the canonical way to ban hardcoded colours/spacing outside a token file.
- License/maturity: MIT, v1.12.1; Stylelint plugin ecosystem is mature.
- Fit: Would duplicate what `src/styles/token-contract.test.ts` already does for Evener, and Evener has no Stylelint. Low value unless the project standardises on Stylelint.
- Limits: Requires Stylelint; no React/TS awareness; no component adoption.

### 5.7 Biome GritQL plugins (custom rules without ESLint)
- Source: https://biomejs.dev/linter/plugins/ (accessed 2026-11-06)
- What it does: Custom lint rules in `.grit` files, configured under `plugins` in `biome.json`, with `path`, `includes`, `resolutionKind`. Target languages: JavaScript, CSS, JSON. Rewrites are unsafe unless `fix_kind = "safe"`.
- Fit: **The practical way to add Evener-specific drift rules** (e.g. ban native `<button>`/`<input>` outside a primitives directory, require `data-testid` patterns, ban raw hex in TSX) without adding ESLint.
- Limits: Plugins lack Biome's semantic model and file kind; they run on every file; GritQL and target language must both be supported.

---

## Shortlist, ranked

1. **Anthropic design plugin (`design-critique`, `accessibility-review`, `design-system`)** — https://github.com/anthropics/knowledge-work-plugins (accessed 2026-11-06). Apache-2.0, active the day of access, one install command, structured outputs with severities. Best ready critique/a11y procedure for an agent that already runs Claude. Weakness: reviews images/Figma, not Evener source.
2. **Impeccable** — https://github.com/pbakaus/impeccable (accessed 2026-11-06). Apache-2.0, very active, deterministic "AI slop" detector, `critique`/`audit` with P0–P3 ranking and URL scans. Best "review the running app and rank fixes" tool. Weakness: separate engine/config, unbenchmarked accuracy.
3. **Vercel `web-design-guidelines` skill** — https://github.com/vercel-labs/agent-skills (accessed 2026-11-06). The best *source-level* UI reviewer: 17 categories, ~104 rules, `file:line` output, installs with `npx skills add vercel-labs/agent-skills`. Weakness: no repo-level licence and rules are fetched live.
4. **axe-core (with Evener's CDP guards)** — https://github.com/dequelabs/axe-core (accessed 2026-11-06). MPL-2.0, maintained, deterministic, no framework assumptions. Best accessibility ROI; needs a small CDP injection adapter because Playwright is absent.
5. **Buoy** — https://github.com/ahoybuoy/buoy (accessed 2026-11-06). MIT, active, CLI + MCP + CI drift gate, adoption and duplicate-component reporting. Best design-system drift candidate that is maintained, with the caveat that CSS Modules support is unverified.

## Proposed combination for Evener

| Need | Recommendation | Why this one |
|---|---|---|
| Design critique and heuristic review | Anthropic design plugin for screenshot/Figma-stage critique; Impeccable `critique`/`audit` for a running URL | Two complementary modes: design-stage judgement vs. scored runtime review. Both are installable and local. |
| Source-level UX/design review | Vercel `web-design-guidelines` skill on changed `.tsx`/`.module.css` | Only shortlisted skill that reads Evener source and reports `file:line`, covering focus, forms, motion, copy, i18n. |
| Scenario / browser checks | Keep `browserGuardCdp.mjs`; inject axe-core over CDP; add Lighthouse CI on the built bundle | No new browser stack; reuses the existing Chrome lifecycle; axe + Lighthouse are deterministic CI gates. Add Playwright CLI only if agent-authored flows become necessary. |
| Design-system drift | Keep `token-contract.test.ts`; add `react-scanner` adoption counts; pilot `buoy drift check`; add a Biome GritQL rule for raw controls | Evener's token contract already bans CSS literals. react-scanner and Buoy add component-level adoption, which the contract does not cover. GritQL covers the missing raw-control rule without ESLint. |
| Optional visual regression | Playwright `toHaveScreenshot` only if the team reverses its geometry-over-pixels decision | The current guards deliberately avoid pixel snapshots. Chromatic/Percy would add a SaaS account. |
| Optional simulated-user study | UXAgent, run manually, as a research probe | Interesting for pre-design usability questions; unmaintained, unlicensed, not CI-grade. |

Suggested first slice (smallest end-to-end path): install the Anthropic design plugin and the Vercel guidelines skill, then run both against the hub's settings and session surfaces and compare their findings. In parallel, inject axe-core into one existing guard and add its violations to that guard's report. That proves the critique layer and the check layer before any drift tool is trialled.

## Notable gaps no existing tool covers

- **Raw control detection** (a native `<button>`/`<input>`/`<select>` where the design-system component exists): Buoy's "detached components" and ui-drift's "DS adoption" are adjacent, but neither documents a native-element rule, and ui-drift has no licence. Needs a custom GritQL or AST rule.
- **Interaction-pattern consistency** (every dialog traps focus and closes on Escape; every menu uses the same keyboard model): axe checks roles/attributes, not behaviour across components. No tool found audits the pattern, only instances.
- **Agent-as-user task-success testing on a local app in CI**: the maintained options are commercial (Momentic, QA Wolf); the open one (UXAgent) is an unmaintained, unlicensed research prototype. Browser-use/Stagehand can drive the flow but are non-deterministic.
- **CSS Modules + design tokens adoption**: most drift tools assume Tailwind, inline styles, or Figma. Evener's own `token-contract.test.ts` is stronger than the off-the-shelf options for literals; component adoption is the uncovered half.
- **Keyboard/screen-reader behaviour in a flow**: axe covers at most ~57% of WCAG issues, by Deque's own figure; no shortlisted tool automates focus order, announcements, or 200% zoom behaviour.

## What I could not verify

- **Subpixel** (https://www.subpixel.design/): no repository path, no resolvable npm package (npm `subpixel` is an unrelated image tool), no licence file seen. Confidence that it is open source: low.
- **AgentDesk**: at least three unrelated projects share the name. The two GitHub candidates are 0- and 1-star projects, and neither is a UX audit agent. No install commands were read.
- **Chromatic and Percy pricing/quota details**: not on the pages read. Both are commercial SaaS; exact cost unknown.
- **Buoy's CSS Modules support**: not stated in the README. Assumed unverified.
- **Lighthouse CI config filename**: README references `docs/configuration.md` but the snippet does not name `lighthouserc.js`; treat that filename as conventional, not verified here.
- **Framework/accuracy claims for Impeccable, ui-ux-pro-max, and Subpixel**: upstream marketing, no independent benchmark read.
- **Nothing was installed or executed.** All install/invoke commands are quoted from upstream docs; all star counts and dates come from the GitHub API on 2026-11-06. No tool was tested against Evener's UI.
