# Primary Navigation Main-Pane Contract

Date: 2026-07-27
Status: Approved
Kata: `0rtb`
Branch: `wip/kata-primary-navigation`

## Decision

Primary navigation has one rule: Settings, the new-session form, and a
top-level session always occupy the main pane. Changing that primary context
replaces the current main pane and closes every secondary pane. Reopening the
same primary context keeps its secondary panes.

A nested session is contextual rather than primary. Opening one first makes its
top-level owner the primary session, replacing the current primary context when
necessary, and then opens the nested session in the secondary group to the
right. A successfully created session similarly replaces the new-session form
as the new primary session.

The browser URL, workspace store, dockview layout, and reload restoration must
all describe the same primary navigation intent.

## Root Cause

The current code has the intended placement rules, but two paths bypass them:

1. `Rail` opens session panes directly without updating the browser URL. If the
   user arrived at `/settings`, clicked a session, and clicked Settings again,
   the URL was still `/settings`. `navigate("/settings")` correctly treated the
   request as a same-path no-op, so the settings placement policy never ran.
2. `DockHost` captures routed panes before restoring a saved layout, but reopens
   them afterward through generic additive `openPane` placement. A reload at
   `/settings` can therefore restore a session in main and add Settings to the
   secondary group instead of replacing main.

Both are ownership errors: primary navigation policy is duplicated around a
generic pane-opening primitive instead of represented by one workspace
operation and one route-driven flow.

## Navigation Invariants

### Primary contexts

The following are primary:

- global Settings, including all settings sections and inbound aliases;
- the new-session form at `/new`;
- any independently addressable top-level session.

Opening a different primary context:

1. removes the previous main pane;
2. removes all secondary panes, because they belonged to the previous primary
   context;
3. opens or promotes the requested primary pane into the main slot;
4. focuses it; and
5. leaves exactly one main pane.

Opening the same primary context is idempotent. For Settings, changing only the
section updates the existing main settings pane rather than treating it as a
new primary context. Secondary panes are preserved only when the primary
identity itself is unchanged.

### Nested sessions

Opening a nested session resolves its top-level ancestor from the authoritative
tree:

- if that owner is already main, preserve the owner and existing secondary
  panes, then open or focus the nested session in secondary;
- if another session, Settings, the new-session form, or Welcome is main,
  replace the primary context with the owner, clear the old secondary group,
  and then open the nested session in secondary;
- a nested session must never occupy main, including after restoring a legacy
  layout.

### New sessions

Clicking “New session” navigates to `/new`, replaces main with the spawn form,
and clears the old secondary group. When creation succeeds, navigation moves to
the created session route and replaces the spawn form with that top-level
session in main.

## Architecture

### Atomic workspace operation

Add one store operation for primary replacement rather than repeating
close-then-open sequences at callers. It accepts a pane type, params, and a
stable primary identity: one constant for Settings, one constant for Spawn, and
the canonical session ref for a top-level session.

The operation performs one state transition:

- if the requested primary identity matches main, update/focus that pane and
  retain secondary panes;
- otherwise discard the old main and all secondary panes, remove any stale
  duplicate of the requested pane, and create the requested pane in main.

An atomic update prevents DockHost from observing intermediate empty-main or
stale-secondary states and keeps persistence from serializing a half-completed
transition.

Session lineage remains outside the generic workspace store. The store knows
only primary identity and pane placement; `AppShell` and the existing session
placement seam decide which session is the top-level owner.

### Route-driven rail navigation

Rail activation navigates to the canonical session URL instead of directly
opening a session pane. AppShell remains the single interpreter of route
intent:

- primary route -> atomic main replacement;
- nested session route -> owner replacement if needed, then nested secondary;
- new-session route -> atomic main replacement;
- settings route -> atomic main replacement or same-primary parameter update.

This also makes browser Back/Forward behavior and the address bar agree with
what the rail selected.

### Restore behavior

DockHost may restore geometry first, but the current route is authoritative for
primary placement. The routed panes captured before restore retain their slot
intent:

- captured main panes are reapplied through the primary replacement operation;
- captured secondary panes are reopened only after their main owner;
- generic additive `openPane` is not used to reapply a captured primary route.

Thus `/settings` cannot restore Settings on the right, `/new` cannot restore the
spawn form on the right, and a session or nested-session deep link obeys the
same placement contract before and after reload.

## Alternatives Rejected

### Dispatch route changes for same-path navigation

Making `navigate` emit `popstate` even when the pathname is unchanged would
mask the stale-URL symptom, but Rail would still leave the address bar naming
the wrong pane and DockHost restoration would still use additive placement.

### Patch Rail and DockHost independently

Teaching Rail to update history while separately special-casing Settings and
sessions in DockHost would fix the reported sequence, but would preserve two
copies of the primary policy and leave `/new` or future primary panes able to
drift.

### Put session lineage into the workspace store

The workspace store does not own the navigation tree and should not learn
subagent/fork ancestry. Keeping owner resolution in AppShell preserves the
existing boundary and keeps the store deterministic and host-independent.

## Testing

Tests exercise state and rendered behavior rather than generated command or
serialized-layout strings:

1. Settings -> top-level session -> Settings updates the URL on each primary
   selection, puts each target in main, and never leaves Settings secondary.
2. Reloading a saved session layout while the route is `/settings` yields
   Settings as the sole main primary context.
3. Reloading at `/new` yields the spawn form in main.
4. Opening top-level session B while session A is main removes A and all of A's
   secondary panes.
5. Reselecting the same top-level session preserves its secondary panes.
6. Opening a nested session whose owner is not main replaces main with the
   owner, clears the old secondary group, and opens the child to the right.
7. Opening another nested session for the current owner preserves the owner and
   current secondary group.
8. Successful session creation replaces the spawn form with the created session
   in main.
9. Browser Back/Forward applies the same primary and nested placement rules.
10. Desktop DockHost and mobile StackHost observe the same workspace state
    transitions.

Focused tests must be proven red before production edits. The complete frontend
suite, typecheck, lint, production build, and applicable browser layout guards
run before integration.

## Non-Goals

- Preserving secondary panes across a primary-context change.
- Adding tabs or a close affordance to the main group.
- Changing the visual design of Settings, Spawn, session panes, or the rail.
- Persisting parent/child ownership in the workspace store.
- Adding backward-compatible URL aliases.
