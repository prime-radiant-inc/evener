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

## Environment collection editor

The native environment sheet uses separate name/value fields and compact rows
with explicit removal. Adding an existing name replaces that override. Done
includes a pending pair so typed input is not silently dropped. Cancel keeps the
parent draft unchanged. Removing all entries unsets the override, matching the
web collector; it does not emit an explicit empty map. Empty values and embedded
equals signs are retained. A name containing an equals sign is rejected inline
instead of silently deleting the character as the web input does.

The sheet keeps its original field baseline and refuses stale edits, comparing
maps by keys and values independently of key order. An identical concurrent
result is accepted. Environment values are shown only inside this editor; the
settings index summarizes names. The effective-name summary is not a preview of
the unsaved draft. Path lists, fallback lists and MCP editors remain required.

Four new deterministic tests exercise replacement/value preservation, invalid
names, inheritance and stale/convergent edits. All 300 native tests, TypeScript,
and touched-file Biome pass. On iOS, the initial environment sheet was exercised
with the software keyboard: type MOBILE_ENV_FIXTURE with value a=b==, Done
without Add, Save, Reload, reopen and observe the exact pair, Remove, Done and
Save. Independent AppWire readback confirmed the owned SecondHub state root and
unset env afterward. The initial screenshot exposed a redundant Remove row;
the corrected layout places Remove beside the variable. Both iOS release builds
passed. Android release build passed, and its final sheet added an empty-valued
MOBILE_EMPTY_FIXTURE with the software keyboard visible, saved, reloaded and
reopened it. Independent AppWire readback verified the explicit empty string.
iOS read that saved pair in the final compact layout. Android restored inheritance
while the iOS sheet remained open; iOS Done refused the stale edit and retained
the draft with a conflict error. Final independent readback verified unset env.
Removing the conflict guard caused the dedicated regression test to fail;
restoring it passed all four environment tests.

Screenshots: [iOS compact row](assets/launch-settings/ios-environment.png) and
[Android empty-value row with keyboard](assets/launch-settings/android-environment.png).
Large text, long lists, screen readers, environment draft background/reconnect,
and native invalid-name/cancel checks remain pending. This is not full visual
acceptance or completion of MOB-015.

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

### Android recovery and native checks

6 September 2026, following the ANR:

- The original app PID 4007 remained live. Even a simple adb pid query stalled
  intermittently. Host sampling showed active virtual CPU execution, not a
  terminated emulator.
- A fresh native dump at 13:55:39 found both the app main thread and mqt_v_js
  waiting in their message loops. A guest top snapshot showed 380% idle of four
  CPUs; this is a later recovery observation, not the load at the ANR.
- Android Permission Controller recorded a separate service-execution ANR at
  13:54:41 (20545 ms). This supports broader emulator impairment but does not
  establish the precise cause of either failure.
- The ANR dialog remained after Wait. After collecting its screenshot and
  stacks, explicitly force-stopped the app, confirmed PID absence, and launched
  the same installed build. This was a controlled retry, not a repair.

The new app process (5052) completed the following sequential native actions:

1. Hub settings → Launch defaults → search round. The search completed and
   displayed inherited Max rounds. A nearby top snapshot showed 361% idle.
2. Opened Max rounds, entered 7, backgrounded with Home, then brought the same
   task to the foreground. The open sheet retained 7.
3. Tapped Done, backgrounded again, confirmed Nexus Launcher as the top resumed
   activity, and returned. Search round and the unsaved 7 remained; Save was enabled.
4. Saved on Android. The saved notice and 7 appeared; the already-open iOS
   screen updated to 7 without Reload or navigation.
5. Reopened the Android field, chose Use inherited value, Done, Save defaults.
   Android showed Inherited · -1 and the saved notice. iOS updated to the same
   inherited value without a manual reload. The owned fixture is restored.

Screenshots: [Android saved](assets/launch-settings/android-saved.png),
[iOS receiving the save](assets/launch-settings/ios-android-save.jpg),
[Android restored](assets/launch-settings/android-restored.png),
[original Android ANR](assets/launch-settings/android-anr.png).
The final saved/restored Android screenshots have the keyboard dismissed; they
do not prove all controls remain reachable with every keyboard or text size.

The final event-log read still contained only the original Evener ANR and the
Permission Controller ANR, with no new event during this retry. No production
code changed in this investigation. MOB-017 stays open for causal diagnosis and
repeatable performance acceptance. Native scalar path validation interruptions,
large text, screen readers, collections and broader settings remain unqualified.

Additional local diagnostics: /tmp/evener-emulator-sample.txt,
/tmp/evener-android-live-stack.txt, /tmp/evener-android-anr-events-after.txt.
Root adb was used temporarily to collect the app's native stack.

## Launch model catalog

Launch model fields now use a native virtualized catalog instead of requiring a
typed ID. The unscoped model/list request matches web global launch defaults.
The native list directly reuses the web's buildPickerRows: server ordering,
Recent, provider headings, qualified IDs, capabilities/prices/context metadata
when supplied, and unavailable-provider notices. Shared catalog types now live
outside the web React/CSS module so these pure helpers are usable by both apps.

Selecting a row changes only the scalar field's raw draft. Done applies it to
the launch layer draft, and Save defaults writes it. Use inherited value clears
the override. Existing IDs remain selected even if absent from the current list;
opening or dismissing the catalog does not coerce them to an available model.
The query and selected value survive same-hub reconnects. Catalog bindings are
disposed on transport changes and ignore obsolete reads. Errors offer retry.

Four loader tests cover the unscoped request and metadata/recent/diagnostic
mapping, omitted lists and error/retry, disposal and out-of-order completion.
All 293 native tests, native TypeScript, touched-file Biome, make test-web and
make test-web-browser pass. The browser gate includes layout, overflow, shell,
spawn and transcript-scroll guards.

Manual native Release evidence on isolated SecondHub:

- iOS displayed Recent, Fake Test Model, Fake Alternate, and the hub's Ollama
  connection-refused notice. Search Alternate narrowed to Fake Alternate.
- Selected Fake Alternate and used Home/foreground. The sheet retained the
  search and checked selection fake/fake-alternate. Done and Save defaults
  produced the saved notice and model row with that qualified ID.
- Android opened Model and showed effective/selected fake/fake-alternate, with
  Fake Alternate checked in the same catalog. Used Use inherited value, Done,
  Save defaults; the model returned to Inherited · Default with a saved notice.
  The fixture's originally unset model override is restored.

Screenshots: [iOS catalog](assets/launch-settings/ios-model-catalog.jpg),
[iOS saved](assets/launch-settings/ios-model-saved.jpg),
[Android restored](assets/launch-settings/android-model-restored.png).
These checks preceded a small error-message placement correction that makes
Done errors visible in both scalar and catalog sheets.
Final iOS and Android Release rebuilds after that correction pass and are installed.

Remaining: large-catalog and accessibility native acceptance, native catalog
failure/retry and offline selection checks, richer model metadata fixtures,
creation/per-launch integration and collection editors. MOB-017 remains open;
the functional checks do not establish Android performance reliability.

## Conflicts while a field sheet is open

An open scalar sheet owns raw text separately from the layer draft. A clean
layer can therefore refresh from another client's notification while the sheet
still contains its original value. Applying that value without checking would
silently replace the newer field. The sheet now captures its opening value and
checks the latest explicit value immediately before applying Done, including
after asynchronous path validation. A conflict keeps the sheet and text open
with a review/reopen message. Unchanged fields and changes already equal to the
desired value remain applicable. The whole-layer save preflight still applies;
this is not server-side compare-and-set.

Three new tests cover stale fields (including override removal and false),
unchanged baselines and convergent edits. Disabling the guard makes the stale
field test fail. All 296 native tests, TypeScript, touched-file Biome, diff checks
and both Release builds pass.

Manual native checks on SecondHub:

1. Opened unset Max rounds on iOS. Android saved 7. iOS received effective 7
   while its text stayed empty; Done kept the sheet open with the conflict
   message instead of applying an override removal.
2. Restored inheritance on Android, then installed its updated build.
3. Opened unset Max rounds on Android. iOS saved 8. Android received effective 8;
   Done kept the sheet and empty text with the conflict message.
4. Cancelled and reopened on Android, intentionally removed the override, and
   saved. The final row showed Inherited · -1 and the saved notice, restoring
   the fixture. Reopening therefore provides a usable recovery path.

Screenshots: [iOS conflict](assets/launch-settings/ios-field-conflict.jpg) and
[Android conflict](assets/launch-settings/android-field-conflict.png).
Native path-validation races, multiple unrelated-field edits, large text and
screen-reader announcements still need acceptance.
