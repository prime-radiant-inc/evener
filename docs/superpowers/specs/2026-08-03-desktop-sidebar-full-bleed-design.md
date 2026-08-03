# Desktop Sidebar Full-Bleed Layout

## Context

The desktop shell currently applies `padding: var(--space-4)` to the shared `.content` row in `cmd/serf-hub/frontend/src/shell/AppShell.module.css`. Because the desktop `RailHost` is the first flex child, that parent padding creates visible insets around the left sidebar on all four sides.

## Design

Remove the desktop `.content` padding while retaining its existing row gap. The rail will therefore align with the shell's top, bottom, and left edges, while the workspace remains separated from the rail by `gap: var(--space-3)`.

Leave the existing mobile media query unchanged. Mobile already sets both `padding: 0` and `gap: 0`, preserving its full-bleed layout and avoiding any behavior change below the desktop breakpoint.

No changes are needed in `Rail.module.css`: the rail already fills its flex row height with `height: 100%`, owns its right border, and has a desktop width independent of the parent inset.

## Regression coverage

Add or update deterministic frontend layout contract coverage to verify:

- desktop `.content` has no parent padding;
- desktop `.content` retains the rail/workspace gap;
- the mobile override continues to specify zero padding and zero gap.

Run the existing relevant frontend test stream plus `make web-preflight` and `make build-web` before declaring the change complete.

## Scope

This is a single CSS/layout correction. It does not alter rail rendering, rail width, mobile drawer behavior, workspace sizing, or shell structure.
