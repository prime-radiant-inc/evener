# Native session creation

Use superpowers:subagent-driven-development for the screen implementation and review.

## Intent and design

Jesse asked for iterative delivery of a native iOS and Android client supporting
multiple Evener hubs. The first slice can open existing conversations. This
iteration adds a New session action on the selected hub's session list, a native
form for project directory, harness, model, reasoning effort, and opening prompt,
and navigation into the session returned by thread/start.

Reuse mobile/src/services/newSession.ts and the current protocol types. Load
recent projects and harnesses from the selected hub. Model catalogs depend on
both project and harness. Default selections defer to hub configuration. An
explicit model choice must come from the current catalog; an incompatible
reasoning selection is cleared. Show errors and keep entered text on failure.
Never automatically retry a start: a dropped reply can mean the session exists.
Bind the form and completion to the hub that opened it; obsolete responses must
not navigate or overwrite current state. Use native controls, safe areas,
keyboard handling, accessible names, and the existing light/dark palette.

No deletion of Tauri code or modifications to Jesse's signing edits. Do not
create sessions on production as an incidental test. Use the explicitly labeled
network playground for manual mutations, without claiming provider E2E.

## Task 1: Native creation form

- Add NewSession route and session-list action.
- Implement form and small testable controller as needed, with existing service.
- Exercise selection reset, stale response suppression, double submission, and
  input preservation using meaningful behavioral tests.
- Run native tests and typecheck. Commit only owned files; review spec and quality.

## Task 2: Integration and simulator verification

- Extend the loopback playground with metadata and creation for independent
  conversations, retaining canonical refs and per-thread notifications.
- Test create then open/send through actual shared AppWire client/services.
- Exercise creation on both native simulators and capture evidence.
- Record tests and remaining limitations; commit without staging signing files.

This iteration does not satisfy feature completeness. Remaining work includes
session management, search/navigation organization, richer conversation input and
rendering, queue/steering/goals, tasks/jobs/subagents, settings/provider/plugin
management, native distribution, accessibility and real Evener E2E coverage.
Interactive voice with barge-in remains outside v1.
