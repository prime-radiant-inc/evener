# Native command and skill completion

The current web composer is the feature reference: `Composer.tsx`,
`slashCompletion.ts`, `stores/commandCatalog.ts`, and the palette's
`slashCommandInvocation` / `visibleCatalogCommands` functions. No feature is
derived from the old mobile UI.

Typing a slash token at a collapsed caret position opens an inline native
list above the input. It uses the current web fuzzy matcher and text-splice
function. Choosing a row inserts the invocation, returns the caret to the
input, closes completion, and does not send. Dismiss preserves the draft.
No-match tokens leave the ordinary composer alone, matching the web.

The catalog comes from `evener/command/list`; session plugin membership and
skills come from a bounded `thread/read` without turns. Only plugin commands
whose plugin is loaded in that session are offered. Plugin invocations retain
their `/plugin:name` qualification; user commands and skills use their
advertised names. The qualification/filter functions are extracted into a
pure module used by web and native. The completion helper now accepts only
the built-in descriptor fields it consumes, avoiding a dependency on the web
registry and its browser-only UI imports.

The picker owns its requests per client/session. Plugin updates and matching
thread resync refresh it; in-flight invalidations coalesce into a trailing
read. Failed or foreign-session reads cannot replace the last valid catalog,
and stale results cannot be selected until refresh succeeds. Leaving the
picker disposes its requests. Native rows have at least 48-point touch areas;
visible hints use two lines, with full descriptions retained as accessibility
hints. The list scrolls within a bounded height.

## Evidence, 6 September 2026

- Native TypeScript, touched-file Biome, and all 161 native tests pass.
  Three new transport-boundary tests exercise loaded-plugin filtering,
  qualified insertions, advertised skills, foreign-session/error retention,
  and disposed-result rejection. Existing web completion/command tests
  exercise matching and splice semantics; the focused set has 85 passes.
- `make test-web` passes unit tests, typecheck and lint. All five
  `make test-web-browser` guards pass after the shared helper extraction.
- Both Release builds succeeded and were installed on iPhone 17 Pro /
  iOS 26.5 and Pixel 7 / Android API 35.
- Both native composers loaded the real isolated session's advertised
  `doctoring-evener` skill from `/doc`, inserted `/doctoring-evener ` without
  sending, and retained that draft across native restarts. Final-build
  software-keyboard checks on both platforms show the compact hint, input,
  model/reasoning controls, and Send remaining visible. Dismiss preserves
  `/do` and removes the list. No live provider call was needed for selection.

## Remaining command work

This picker currently offers catalog commands and skills. Built-in session
actions must be routed explicitly before advertising them here. Only the
existing `/goal` interception is implemented. Current-web built-ins still
needing native slash routing include compact, interrupt, clear, aside,
shutdown, model, reasoning-effort, steer, queue, drain-as-steer, copy-id,
tasks, status, and project. Some already have other native controls, which
does not establish slash-command parity. Unknown/argument-bearing command
fallthrough, attachment semantics and unavailable-command errors must match
the current web at submission.

Still required: native middle-of-draft/caret and IME tests, large catalogs and
large text, screen-reader interaction, plugin install/remove while open,
catalog fault/reconnect and hub-switch acceptance, real command execution,
and final-release regression. The full app goal remains incomplete.
