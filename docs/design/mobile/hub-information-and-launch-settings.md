# Native hub information and launch settings

Status: implementation direction for MOB-015 and MOB-007, 6 September 2026.
Feature authority is the current web and typed hub contracts, not the old mobile
UI. This document does not mark those backlog items complete.

## Read-only hub information

`evener/settings/overview` supplies hub version/commit, listen address, run
folder, spawn timeout, bearer-token age, past-index metadata, state folder,
agents, Codex launch entries and discovered MCP servers. The native view must
retain every exposed field; absent scalar/pointer data is unavailable. Go omitempty encodes empty agent and
Codex collections, MCP server lists, and zero index counters as omitted fields;
normalize those to the empty/zero values established by the server implementation. Token age is metadata, never a token reveal.

Web authority: settings sections `general.tsx`, `hub.tsx`, `storage.tsx`,
`agents.tsx`, `launchCodex.tsx` and `stores/settingsOverview.ts` under
`cmd/evener-hub/frontend/src`. Typed response: `SettingsOverviewResponse` in
`protocol/types.gen.ts`.

Use one hub-bound screen with a small version/address summary and disclosures
for Runtime and storage, Agents, Codex launches and discovered MCP servers.
Long paths wrap inside details; compact rows show names and secondary values.
Agent edit paths are server paths. Do not launch a phone editor for a hub path.
The web's external-editor action needs an explicit supported remote-file flow
before native can claim that action is covered.

Load through the connected client's overview RPC, retain known data on refresh
failure, expose retry, and invalidate the whole screen lifetime when the hub or
connection changes. No cross-hub global cache. Return from background/reconnect
must obtain current state. MCP's own discovery error is distinct from overview
transport failure. Environment entries expose key names only, as the wire does.

## Editable launch settings

Web authority: `sections/launchServer.tsx`, `launchShared/LaunchConfigForm.tsx`,
`launchShared/schema.ts`, `launchShared/fields.tsx`,
`launchShared/collectionFields.tsx`, and `stores/launchConfig.ts`.

The global form loads `evener/launch/schema`, `evener/launch/getLayer` with
`cwd: "/", layer: "global"`, and resolved effective defaults from
`evener/launch/resolve`. Save uses `evener/launch/setLayer` and its returned
resolved diagnostics. Path validation uses `evener/path/validate`, not phone
filesystem access. Directory assistance can use the native HubDirectoryField;
file-capable fields require file suggestions with the server's path-kind rules.

A native settings index groups the server schema into compact rows. Editing one
field opens the suitable native control and shows both its explicit override and
its effective/inherited value. Removing an override must be distinct from false,
zero, or empty values. Save preserves all unrelated fields in the layer, including
fields the current screen does not render. Use server choices, driver support,
layer applicability and diagnostics. Do not build a generic JSON editor or invent
an update catalog. Array, map, MCP, prompt and file fields remain required.

Coordinate project/per-launch scope with creation: resolve using the chosen hub
working directory, show provenance, preserve repository trust/hash checks, and
use the same schema interpretation across creation and global settings. An
uncertain save is reconciled by reading; do not replay a write on reconnect.
External `evener/launch/updated` notifications must refresh clean views and warn
an active editor about changed state before it overwrites another client's work.

## Delivery and evidence

1. Implement hub information loading and its native disclosures. Test late reads,
   refresh errors and missing optional sections; verify all displayed fields on
   two different isolated hubs and both native platforms.
2. Implement typed launch-layer loading/edit/save behavior with server schema and
   effective provenance. Test preservation of unrelated fields, explicit false/0,
   removing overrides, write failure/readback, external changes and hub disposal.
3. Build native field editors by schema kind, including collections and MCP.
   Reuse pure web schema helpers where their contracts fit; inspect those helpers
   before reuse. Verify server path validation and launch-layer applicability.
4. Complete per-project/per-launch and trust behavior in creation; manually create
   sessions that demonstrate the chosen overrides actually take effect.
5. Native acceptance: keyboards, large text, screen readers, nested detail
   navigation, long paths, partial errors, two hubs and background/reconnect.
   Visual acceptance must measure visible content and remove redundant labels,
   oversized controls and repeated paths rather than merely passing screenshots.

MOB-016 hub upgrade remains separate and must use an isolated disposable hub for
native mutation/restart acceptance. All broader app requirements remain open in
the authoritative backlog.


## Launch-layer controller progress

6 September 2026: `mobile-native/src/launchSettings.ts` implements one connected
hub/cwd/layer editor. It loads schema, explicit layer and effective resolution,
keeps resolution failure separate from editable-layer failure, and preserves
unrelated fields. Edits are constrained to schema fields applicable to the layer.
False/zero are explicit overrides; undefined removes the override. Value-kind and
path validation still belong to the forthcoming native editor/server validation.

Before whole-layer save it reads the current layer and refuses a changed baseline.
Dirty editors receiving relevant launch notifications retain their draft and require
reload. Saves exclude overlapping edits/saves, read back uncertain outcomes, and
never replay a mutation. There is no server compare-and-set; another client can
still write between preflight and mutation. Do not claim atomic conflict safety.

Ten deterministic controller tests cover override semantics, unrelated-field
preservation, field applicability, stale preflight, dirty external notification,
uncertain write/readback, partial resolution failure, disposal, overlapping saves,
retry after failed preflight and obsolete loads (some tests cover multiple related
assertions). All 282 native tests, TypeScript and touched-file Biome pass.

`mobile-native/scripts/launch-settings-smoke.mts` checks the exact isolated hub
state root before changing maxRounds, saving through the controller, independently
reading back and checking untouched fields, then restoring and comparing the whole
original layer. It passed against SecondHub; the original layer was restored.
This is real AppWire/controller evidence, distinct from the native checks below.

## Native scalar editors

6 September 2026: Hub settings now opens Launch defaults. The screen uses the
server's applicable global schema fields, labels, groups, descriptions and choices.
Search narrows the compact setting rows. One-field sheets edit scalar values;
Done changes the local draft and Save defaults writes the layer. Removing an
override is explicit. Invalid integers and unavailable enum choices are rejected.
Directory fields reuse hub directory completion; paths validate on the server.

Manual iOS Release checks against the isolated SecondHub:

- Opened Max rounds from search with its original override unset/effective -1.
- Entered 7 with the software keyboard, tapped Done and Save defaults. The row
  showed 7 and a saved notice; reopening resolved the effective value to 7.
- Entered 7.5 and tapped Done. The editor stayed open with the draft intact and
  “Enter a whole number.”
- Chose Use inherited value, Done and Save defaults. The row returned to
  Inherited · -1 with a saved notice, restoring the fixture's original setting.

Screenshots: [saved override](assets/launch-settings/ios-saved.jpg) and
[restored inheritance](assets/launch-settings/ios-restored.jpg). Both show the
actual software keyboard and reachable save controls.

The Android emulator cold-booted and opened the installed app connected to
SecondHub, but subsequent UIAutomator calls timed out and screen capture stalled.
No Android scalar-editor or cross-device update acceptance is claimed here.
Do not restart the app or emulator solely because these observation calls timed out.

Fresh validation: all 285 native tests and TypeScript pass. Three parser tests
cover inheritance/false/zero, integer and choice validation, and collection
rejection. Full repository merge gates and visual acceptance are not established.

Remaining: collection/environment/MCP editors, model catalog selection (the
current model field accepts an ID), file completion, project/per-launch layers,
path-error native checks, external
update conflicts, two-hub isolation, large text and screen-reader acceptance.
This slice does not close MOB-015.

## Reconnect draft retention

The editor now belongs to the hub screen rather than the current WebSocket.
Disconnects detach notifications and invalidate in-flight reads/saves without
discarding the layer draft or the open scalar sheet's text. Reconnecting reads
the actual layer, preserves an unsaved draft, and flags changes to its baseline.
No save is replayed. Save and Reload are unavailable offline; path validation
requires the selected hub and ignores replies from a superseded connection.
Changing the selected hub still disposes its editor.

Four regression tests were observed failing before implementation, then passing:
offline draft retention and explicit save, reconnect conflict, obsolete preflight
without mutation, and an already-applied write with a late reply. The latter
checks that a newer draft remains untouched and only one write was sent.
All 289 native tests, TypeScript, touched-file Biome and both Release builds pass.

Manual iOS Release on SecondHub:

1. Entered 8 in the Max rounds sheet, pressed Simulator Home, confirmed the home
   screen, and foregrounded the same app process. The open field still contained 8.
2. Applied Done, backgrounded again, then foregrounded the same process. Search
   and the unsaved 8 remained, with Save defaults enabled. See
   [reconnected draft](assets/launch-settings/ios-reconnected-draft.jpg).
3. Saved; iOS showed the success notice. Android's launch-default screen read 8
   from the same hub. This proves cross-device readback, not notification delivery.
4. Android hit an ANR during search, so restored the override using iOS.
   The final iOS row showed Inherited · -1 with the saved notice.

Android ANR: 6 September 2026, 13:51 PDT, PID 4007, input dispatch waited
5491 ms for key R in MainActivity. The 13:51:21 native dump caught the main thread
in GetLongField → HybridDestructor::getNativePointer → FabricUIManagerBinding::
drainPreallocateViewsQueue. The dump was delayed and Java stack collection hit
its deadline. Guest CPU pressure avg10 was 57.91; aggregate CPU was 89%, with
Gboard, system_server, app, compositor and graphics service active.
These observations do not isolate the cause. MOB-017 owns investigation.

Local diagnostic files: /tmp/launch-android-anr.log,
/tmp/launch-android-activity.log and /tmp/launch-android-anr-trace.txt. The latter
contains historical entries too; the relevant entry starts at 13:51:29 on Sep 6.
Android removal, native reconnect, path-validation interruptions, two-hub
isolation, process-death persistence and accessibility acceptance remain unproven.
