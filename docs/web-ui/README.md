# Evener Web Hub — UI/UX

Design documentation for the web hub (`cmd/evener-hub`). Started 2026-06-16.

## Current

- **[design-system.md](design-system.md)** — the design law as shipped: tokens,
  type, space, motion, the cadence instrument, and the widget library under
  `cmd/evener-hub/frontend/src/widgets/`.
- **[decisions.md](decisions.md)** — what we chose out of the 2026-06 visual
  brainstorm, and whether the code still does it. Read this before changing a
  transcript or navigator behaviour: it distinguishes a reasoned departure from
  a regression, and several apparent regressions are neither.
- **[ux-plan-2026-07.md](ux-plan-2026-07.md)** — the five-participant study of
  the SPA against the old server-rendered build, and the plan that came out of
  it. Cited from live source comments.
- **[typography-spacing-critique-2026-09-06.md](typography-spacing-critique-2026-09-06.md)** —
  measured critique of type scale, measure, rhythm and balance on desktop and
  phone, with a proposed ramp and enforcement plan. Proposal, not shipped.
- **[keybindings.md](keybindings.md)** — the keybindings dispatcher: registry,
  scope stack, precedence layers, per-binding policy flags, and how to
  register an action or a chord, the shipped default binding map (including
  the Phase 3 navigation chords and the AskDock keyboard contract), plus the
  hub-persisted override sync (payload contract, validation split, failure
  posture).
- **[parity/](parity/)** — the behaviour-parity checklists the React rewrite was
  graded against, mined from the legacy hub before it was deleted. The code they
  cite is gone, so their `path:line` citations no longer resolve; they survive
  because thirteen live source comments cite *them* for why a behaviour is the
  way it is, and every wave plan used them as its acceptance floor.
- **[specs/](specs/)** — two dated feasibility designs (multi-pane workspace,
  observer auto-open). Point-in-time; the multi-pane one is still named as
  source of truth by its implementation plan.

## Directory fields

All directory selection uses the [shared directory-picker contract](design-system.md#directory-selection-one-shared-interaction). Read it before adding or changing a path field; older plans and parity checklists describe retired interactions.

## The examples are the running app

There is no static example gallery to keep in sync. Run the dev server and open
**`/dev/widgets`** — every widget, every documented state, in both themes,
rendered from the real tokens. `src/dev/WidgetGallery.test.tsx` fails the build
the day a widget has no section, so it cannot silently go stale.

**`/dev/type`** does the same for the type system itself: the size ramp, the
three line-heights, the eyebrow recipe, the four rhythm steps and a paragraph
at each measure, in both themes, so a ramp change is reviewed as a picture
rather than a diff.

The gallery is dev-only: `App.tsx` gates it behind `import.meta.env.DEV`, so a
production build does not contain it and there is no link to it from the app.

## history/

The 2026-06 visual brainstorm, and the planning docs for the hub that no longer
exists.

`history/mockups/` holds 23 topics, each rendering four labelled alternatives,
plus `TARGETS.md` (what each topic set out to fix) and the tokens they were
built on. `history/examples/` holds the golden reference screen, a hard-cases
screen, the three explored visual directions, and the brief all three had to
render.

These are kept for one reason: **they still look better than what shipped**, and
`decisions.md` names the specific gaps. They are not maintained, their tokens
share two names with the live ones, and the app they were built against was
deleted in `660376f78`.

Older plan documents cite these files at their previous paths, without the
`history/` prefix. Those documents are point-in-time records and were left
unedited; the filenames are unchanged, so the paths still resolve by search.

## Goal

External-product polish for a power-user, dark-first agentic coding tool.
Conversation-first, first-class subagents, honest liveness. The eight principles
that govern all of it are reproduced in [decisions.md](decisions.md) — they
outlived the mockups that tested them.
