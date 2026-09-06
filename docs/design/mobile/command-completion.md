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
actions must be routed explicitly before advertising them here. `/goal`, `/compact`, `/shutdown`, `/model`, and `/reasoning-effort` are implemented. Current-web built-ins
still needing native slash routing include interrupt, clear, aside,
steer, queue, drain-as-steer, copy-id,
tasks, status, and project. Some already have other native controls, which
does not establish slash-command parity. Unknown/argument-bearing command
fallthrough, attachment semantics and unavailable-command errors must match
the current web at submission.

Still required: native middle-of-draft/caret and IME tests, large catalogs and
large text, screen-reader interaction, plugin install/remove while open,
catalog fault/reconnect and hub-switch acceptance, real command execution,
and final-release regression. The full app goal remains incomplete.

## Lifecycle command submission, 6 September 2026

Native and web share the pure built-in invocation matcher. Bare `/compact`
and `/shutdown` invoke the existing capability-guarded service operations,
using the durable draft checkpoint. Successful acknowledgement clears the
checkpoint; lost acknowledgement retains uncertainty and any newer draft.
A pending uncertain delivery is not replayed. Attached messages and argless
commands with additional arguments retain the web's ordinary-message routing.
The composer identifies the operation before submission; Compact keeps a
short visible label and the full accessibility label. Shutdown returns to
the roster after acknowledgement without rehydrating the stopped runtime.

Verification: 175 native tests and TypeScript pass; 17 focused web command
tests, the full web gate, and all five browser guards pass. Both native
Release builds succeeded. On the owned isolated hub, both platforms submitted
compact and shutdown; compaction cleared its checkpoint and shutdown returned
to the roster, where both sessions reported notLoaded. No production session
was changed. Native acknowledgement-loss and stale-binding fault injection
for these commands remain manual acceptance work; SQLite/transport tests
cover acknowledgement loss, newer drafts and blocked replay.

## Model and reasoning command submission, 6 September 2026

`/model` resolves the session-scoped model/list catalog by provider/model ID
or display name, case-insensitively, using the same argument matcher as web.
`/reasoning-effort` uses the current projection's advertised ladder only when
supportsReasoning is true. It includes the web's default, explicit none label,
and current out-of-ladder value. It deliberately follows the web command
registry's zero-options behavior for an empty ladder; the settings picker's
fallback ladder is a separate current-web behavior.

Enum validation happens before the durable delivery checkpoint. Invalid
values remain editable and show an error; they do not become uncertain sends.
Model lookup completion checks screen ownership and the original draft record
before dispatch, so edits or navigation while loading cannot target stale work.
The composer and settings actions serialize during command preparation and
execution. Accepted changes refresh the session projection and update the
composer controls; transport uncertainty still uses the durable checkpoint.

Evidence: native TypeScript and all 187 tests pass, including 12 new enum and
stale-lookup cases at the SQLite/network boundaries. The focused 17 web command
tests and full web gate pass. Both Release builds passed. Both platforms
manually retained an invalid model command, accepted a corrected command,
updated the model chip, changed reasoning (iOS high, Android low), and reset
reasoning with the argument-free command. The hub read confirmed the selected
model and reasoning ladder; these actions used the owned isolated Android
parent session through two independently saved hub profiles. Both platforms
ended with empty drafts and default reasoning. This is not a concurrent
multiple-hub acceptance test. Native delayed-catalog, connection-fault and
screen-reader acceptance remain open despite the automated boundary coverage.
