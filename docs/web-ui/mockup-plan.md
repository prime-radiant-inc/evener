# Serf Web Hub — Comprehensive UX Mockup Plan

Status: **planning** (2026-06-17). Drives the goal: extensively re-test the UX against the
design guidelines + subagent persona review panels, mock up everything worth improving with **≥4
alternatives each**, present them as one navigable web UX, and Slack @jesse a link.

Sequencing: this runs AFTER the current work lands (scroll pill merged) and is interactively
tested + honed. Interactive testing + the persona panels will **augment** the target list below.

## Target inventory (pre-test — augment after interactive testing)

Each target gets ≥4 distinct alternatives.

### A. Deferred edge cases (design-system.md §7 — "still needs rules + exemplars")
1. Main-agent error promotion (to the chrome, not just a transcript row)
2. Nested subagents (a subagent that itself fans out)
3. Multiple steers in one turn
4. Permission / approval prompt (interactive AND needs-you — which color wins?)
5. Plan / todo list rendering
6. Diff / patch rendering
7. Agent asking the user a question (inline answer affordance)
8. Empty / just-started session
9. Stalled agent (beyond the liveness line — full treatment)
10. Daemon disconnect / reconnect
11. Multi-image gallery
12. Silent-success tool (no output)
13. Interrupted turn
14. Failed-then-retried tool
15. Error-findability (scroll-track markers + attention-aware "jump to latest")

### B. Refine the shipped surfaces (re-test against guidelines)
16. Live session workspace (the golden — overall composition)
17. Sidebar IA (tiers, rollups, density; the flat "Live" rail overlap is a known follow-up)
18. Subagent module (layouts under light vs heavy fan-out; live-ticking duration is a follow-up)
19. Thinking block (collapsed/preview/streaming treatments; section breaks)
20. Liveness ("no updates for Ns" + the pinned livebar variants)
21. Composer / bottom controls layout
22. New-content pill ("↓ N new" / "↓ needs you") placement + style

## Method
1. **Re-read** design-system.md + ux-and-implementation-plan.md; treat the principles (color =
   needs-your-eye, one containment device, conversation-leads, scarce emphasis, honest liveness,
   mono-for-machine-text) as the rubric.
2. **Persona review panels** (subagents): run distinct lenses over the live UI + each target —
   e.g. power-user-scanning, first-impression/craft, accessibility/colorblind, information-density,
   calm/motion, error-legibility. Capture concrete "wants improving" findings → feed the inventory.
3. **Mock up** each target with ≥4 alternatives (self-contained HTML using the real design tokens,
   like docs/web-ui/examples/*.html). Fan out via worktree subagents per target; I review + assemble.
4. **Assemble** one navigable "comprehensive example web UX" page that links every target →
   its 4 alternatives, side-by-side, reactable.
5. **Host + Slack**: serve the page somewhere @jesse can reach (remote) and Slack him the link.
   (Resolve the Slack mechanism — no Slack MCP tool is currently connected; options: a webhook,
   a CLI, or surface the link for Jesse if no programmatic path exists.)

## Open questions to resolve before presenting
- Slack delivery path (no Slack tool connected yet).
- Hosting/URL reachable by a remote Jesse (the hub binds 0.0.0.0:9180 — could serve the mockups
  there, or a dedicated static serve on a bound port).
