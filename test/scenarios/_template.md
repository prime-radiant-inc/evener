# <area>-<short-slug>: <one-line title>

**What this covers**: <kata IDs, commit SHAs, or symptoms> — the thing
that was broken before and the surface that should now hold.

## Pre-state

- <what must be true: hub running, no sessions, signed in via OAuth, etc.>
- <commands to reach that state if not already there — say nothing about
  the hub and the card inherits `docs/agentic-testing.md`'s Setup
  checklist, which builds the SPA before the hub binary; a card that
  builds its own `serf-hub` has to `make build-web` first or every page
  route answers 503 and only the browser steps notice>

## Steps

1. <action described by intent + concrete handle>
   Example: "Open `/new` in a browser tab and confirm the form
   renders with a model chip showing `(pick a model)`."
2. <next step>
3. ...

## Expected

- After step N: <observable outcome the agent can check>
- Falsification: <what would make this test fail; if you see X,
  the regression is back>

## Cleanup

- <what to delete / restore / kill>
- Skip if the scenario is read-only or self-cleaning.

## Sharp edges

- <gotchas noticed during recording: timing, ordering, env
  prerequisites>
- Skip if none.
