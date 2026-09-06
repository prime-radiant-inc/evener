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
actions must be routed explicitly before advertising them here. Submission
supports goal, compact, shutdown, model, reasoning-effort, interrupt, steer,
queue, drain-as-steer, copy-id, tasks, status, and aside. Remaining native
slash routes are clear and project. Some already have other native controls,
which does not establish slash-command parity. Unavailable-command feedback
and full built-in completion integration remain acceptance work.

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

## Turn commands, 6 September 2026

Product sources: current web palette commands, hasActiveTurn, composerMutationIntent,
and the turn mutation receipt contracts. `/steer` sends only its argument through
turn/steer, even with waiting messages. `/queue` appends the argument. The
explicit `/drain-as-steer` sends empty input with the observed queue revision.
Those three commands require the live active-turn ID; `/interrupt` is scoped
to the session and deliberately has no active-turn gate. Capability guards
remain in the native controls and shared service. Receipts are decoded by the
existing service before the durable command checkpoint can be cleared.

The shared projection retains the server activeTurnId, or the inProgress turn
from the read as the current web does. Live starts and matching completions
update the ID; an older completion cannot clear a newer active turn. A newer
live ID also survives an older in-flight hydration. Tests cover both read
representations, this race, all four wire routes, queue revision binding and
pre-dispatch refusal without a turn.

Verification: 197 native tests and TypeScript, plus 432 focused shared
projection/service/store tests pass. Both Release builds passed and were
installed. Separate owned sessions on the isolated hub were exercised manually
on iOS and Android: queue depth became one; explicit steering retained that
entry and revision; draining emptied the queue and advanced its revision;
interrupt cleared the draft and both sessions ended idle with no active turn.
No production hub session was changed. The fixtures are named Native commands
iOS and Native commands Android. Native stale-queue/connection-fault recovery,
large-text layout and screen-reader operation remain manual acceptance work.

The full mobile suite also passed 2,241 tests before the final active-turn race
correction. That correction has an additional regression case: a completion
received without its start cannot be undone by an in-flight read. The final
native suite, focused shared suite and both typecheck/lint checks pass. The
final Release builds include this correction; the four-action manual sequence
above preceded that final state-only change.

## Local session commands, 6 September 2026

`/tasks` opens the existing native Tasks sheet; `/status` opens the existing
Session sheet. These adapt the current web panel toggles to the platform's
sheet navigation. `/copy-id` copies the full session reference, as the web
command does, and announces success for accessibility. None creates a network
delivery checkpoint. Success clears only the unchanged command draft; a local
failure preserves the editable command, and newer drafts survive asynchronous
clipboard completion. Existing pending-delivery and storage guards still apply.

Verification: all 200 native tests and TypeScript pass. Both Release builds
passed and were installed. Both platforms opened/dismissed the correct Tasks
and Session sheets with the command draft cleared. Android pasted the copied
reference into the composer and iOS simctl read back its pasteboard; both
matched their distinct isolated test session refs exactly. The Android paste
was removed without sending. Clipboard failure and newer-draft preservation
have automated platform-boundary/SQLite coverage, not manual OS fault injection.

Project reveal uses navigation location lookup, as implemented below. Current web uses that lookup
and reveals the matching project/session. A generic project-list navigation
would not satisfy that behavior. Offline local-command access and complete
screen-reader navigation remain acceptance work.

## Aside creation, 6 September 2026

The native `/aside` command follows the web's thread/fork request exactly:
parent ref, aside=true, and required sourceTurnId="". The shared service gates
forkFromTurn and rejects a missing/empty child ref or the parent returned as
its own fork. The response may be the server's minimal child identity rather
than a hydrated thread; navigation loads the child's transcript separately.

The command uses the durable draft checkpoint before dispatch. After a valid
response the checkpoint clears before navigation. The current screen alone
may open the returned session, on its existing hub and a pushed native route.
A newer parent draft survives; a lost acknowledgement remains uncertain and
is not automatically retried. If the screen loses ownership before the reply,
the created session is not opened over another destination.

Verification: 204 native tests and TypeScript, shared typecheck/lint and 432
focused shared tests pass. Both Release builds passed. The isolated hub's
running daemons advertised forkFromTurn=false, so the command was correctly
disabled. After stopping the two owned test daemons and reopening their saved
transcripts, the hub advertised the capability and both platforms created
asides. Clipboard readback confirmed distinct child refs (iOS
local:034K4P6zhjWGuxYhkYsjfj; Android local:034K4Odj0vXNqLqTJGaPIJ), inherited
transcripts were visible, and native Back returned to the correctly named
parent with an empty command draft. No message was sent in either child.
The live-versus-saved capability behavior is a current server limitation,
not a native override. Loss/stale-binding behavior has SQLite/transport tests;
manual OS/network interruption and accessibility acceptance remain open.

## Clear and live continuation, 6 September 2026

The native `/clear` command now uses the current web/server thread/clear
contract: stable ref, expected instance, and correlated mutation receipt.
The response replaces the conversation and activity projections together,
retires older reads, and resets paging through the existing fresh-open path.
The durable command checkpoint settles before adopting the response; a newer
draft survives. A malformed or lost response remains uncertain without replay.
A closed or different conversation cannot adopt the old response.

Manual E2E exposed a hub defect: after clear, the subscription capture used
the response's replacement Thread.ID while relay publication used the stable
request ref. A read through the stable ref silently received no live events.
The hub now carries the relay's publication key through its read handoff and
uses that same key for subscription registration. A WebSocket daemon fixture
exercising the real local source and hub relay failed before this fix and
passes with it. No native polling or instance-ref workaround was added.

Both installed Release apps were tested against the rebuilt isolated hub.
Each cleared its owned fixture, removed the old transcript marker, and kept
the stable ref. A follow-up sent from each composer appeared live with running
controls; Stop returned the real session to idle. Direct hub reads confirmed
replacement instances, old markers absent and follow-ups present:

- iOS stable ref local:034K4egRYkyfFWF1BYJokC; replacement 034K4wUImmZQos9PgKoCMb.
- Android stable ref local:034K4eh98MuwuE6SuLbPN0; replacement 034K4wKVa2F8Wu7IJDgixR.

[Observed iOS follow-up](assets/clear/ios-followup.png) and
[Android follow-up](assets/clear/android-followup.png) show live continuation,
not visual acceptance. The fake provider renamed the fixtures to Fake Session.
The production hub was not changed.

Verification: 209 native tests, native TypeScript/touched Biome, shared
TypeScript/Biome and all 2,241 shared tests passed. The full hub package tests
passed, including relay/subscription coverage. This is not the full repository
merge gate. Clear during an already-started read remains fail-closed in the
shared service; full web concurrency parity, OS interruption and accessibility
acceptance remain open. Project reveal is implemented below; built-in completion remains pending.

## Locate the session in navigation, 6 September 2026

Native `/project` now uses the current web rail's location lookup and project,
pinned-section, then live/needs-you precedence. The destination stays on the
originating hub, reads the exact project tier or section, follows pages without
mixing revisions, expands ancestors, and marks the target selected. Native Back
returns to the conversation. This local command never sends a prompt or creates
a delivery checkpoint, and a newer draft survives its asynchronous lookup.

Missing/mismatched locations and stale lists show an error or refresh path.
Leaving cancels paging. Navigation invalidation now includes section and
pin-section resources. When the hub omits the target branch, the app reports
that it cannot locate the row rather than inventing a position.

Manual isolated-hub evidence:

- iOS local:034K4egRYkyfFWF1BYJokC: after three newer fixtures were added,
  refreshing followed the proxy's two-row pages and selected the target on
  page two. [Paged target](assets/project-reveal/ios-paged.png).
- Android local:034K4eh98MuwuE6SuLbPN0: the parent expanded to reveal and select
  its nested current session. [Nested target](assets/project-reveal/android-nested.png).
  The screenshot also records an invalidation notice from fixture changes.
- Stopping and archiving the owned iOS fixture made the hub return tier
  archived; the app opened it and selected Clear validation iOS.
  [Archived target](assets/project-reveal/ios-archived.png). Live sessions stay
  in Current according to the hub even when marked archived.
- Both platforms returned to the original conversation with the command draft
  cleared. Production sessions were not touched.

Both Release builds, 223 native tests and TypeScript pass. Tests cover lookup
selection, missing/mismatched responses, paging and ancestor paths, cancellation,
revision changes, section invalidation, and local-command draft behavior.
Device accessibility, very long-list scroll placement, omitted-branch recovery,
and pinned/needs-you manual paths remain acceptance work. These screenshots
establish behavior, not final visual quality.


Independent review found and closed a blur/refocus race: each location request
now owns an abort signal permanently cancelled on blur. A delayed response
cannot navigate after the screen regains focus. The regression failed before
the fix and passes afterward; the reviewer confirmed it closed. Final builds
were smoke-tested again with iOS project navigation and Android nested reveal.
The delayed blur/refocus path has automated coverage, not a fresh manual fault
injection claim.
