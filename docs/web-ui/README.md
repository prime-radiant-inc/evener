# Serf Web Hub — UI/UX Overhaul

Working docs for the redesign of the web hub UI (`cmd/serf-hub`). Started 2026-06-16.

- **[design-system.md](design-system.md)** — brand + UI style guide: principles, color/type/
  spacing tokens, the transcript component grammar, sidebar IA, controls layout.
- **[ux-and-implementation-plan.md](ux-and-implementation-plan.md)** — the problems, the UX
  approach, subagent surfacing, scroll + liveness, the Codex-aligned live-thinking plan, and the
  current implementation status.
- **[examples/](examples/)** — self-contained HTML mockups. Open in a browser.
  - `01-golden-live-session.html` — the canonical reference (live session workspace).
  - `02-hard-cases.html` — error, inline image, very long output, heavy subagent fan-out.
  - `direction-{a,b,c}-*.html` — the three explored directions (history).
  - `_mockup-brief.md` — the shared content/interaction brief the mockups were built to.

Goal: external-product polish for a power-user, dark-first agentic coding tool — evolve the
existing Tokyo-Night aesthetic, fix the information design (conversation-first, first-class
subagents, honest liveness), don't rebrand.
