# Native session creation

## Hub directory browsing

The project directory field uses the shared hub path picker while connected.
Typing invalidates the current model selection without requesting a catalog on
every keystroke. Selecting a directory or leaving the field refreshes models for
that directory. Offline, manual input remains editable in the form store. All
paths come from the selected hub; phone filesystem paths are not substituted.

## Manual evidence — 6 September 2026

Release builds on iPhone17Pro/iOS26.5 and Pixel7/API35 browsed the isolated
SecondHub through port9200. Both selected the returned `skills` directory under
`/private/tmp/evener-native-second-v22jd1ct/mobile-path-fixture/`, with the real
software keyboard open. The results closed and the full path was retained.
Screenshots: [iOS](assets/creation/ios-selected-directory.png),
[Android](assets/creation/android-selected-directory.png).

Both native apps created empty sessions with explicit scripted models. An
independent AppWire `thread/list` read confirmed exactly two sessions at the
selected directory: iOS `034KM13HmzXeLQQjymwHiJ` and Android
`034KM3oJHbeVxIcwxGxZby`. The server normalized the final trailing slash. No live
provider request or production hub mutation was used for these checks.

The initial iOS attempt using the hub default failed because this fixture has no
default model. An independent read found no session; a diagnostic request
returned `model is required`. The form had hidden that actionable error behind
its uncertainty text. It now prefixes hub WireError messages while retaining
input, replay protection and the warning to inspect the list before retrying.
The regression test failed before the fix and passes afterward. Final iOS and Android native
readback confirmed the actual server message is visible with the directory kept.

TypeScript and all 312 native tests pass, along with both Release builds. This is
path-selection and creation evidence, not visual acceptance of the entire form.
Large catalogs, project/per-launch layers, repository trust, screen readers,
reconnect behavior and the form's spacing remain open under MOB-007/MOB-001.
Android UI automation returned a transient null root during one observation;
a subsequent read and explicit Find directories action recovered without an app
restart. The initial opening was not counted as successful picker evidence.


## Project launch settings and composer round trip

On 6 September 2026, the creation screen gained project launch settings for its
selected hub directory. The editor uses the advertised project-defaultable
schema and the existing layer editor, including collections, conflict handling
and readback. It does not write resolved effective settings into the layer.

Manual Release checks used iPhone 17 Pro/iOS 26.5 and Pixel 7/API 35 against the
owned SecondHub fixture. iOS selected Fake Alternate, opened project settings,
saved maxRounds=7 and returned with its model and directory intact. An independent
installed API client confirmed project maxRounds=7 and global maxRounds absent.
Android selected Fake Test Model, opened the same project's editor, observed
Effective value 7, restored inheritance, saved and returned with its model and
directory intact. Independent AppWire readback confirmed both overrides absent.
These operations used no production hub and made no provider requests.

The initial native round trip reproduced a model-selection loss: blur unbound
the creation service, clearing its model and reasoning. The service now lives
with the mounted screen and connection. Focus refreshes metadata and the catalog;
matching model/reasoning choices survive only when still advertised. Creation is
blocked during the explicit catalog refresh so an in-flight read cannot silently
replace the intended model with a hub default. Tests cover the preserved choice,
removed model, removed reasoning level and blocked submission during refresh.
The project controller test also checks exact cwd/layer scope, rejection of a
global-only option and retention of unrelated layer fields.

The native suite has 314 passing tests and TypeScript passes. Both Release builds
pass. This establishes the ordinary scalar project-settings round trip; collection
editing at project scope, concurrent writers, reconnects during this flow, full
prompt preservation and accessibility still need native qualification. The form's
spacing and per-launch/trust workflows remain open.


## Repository configuration review

Project settings now expose the current web/server repository trust flow. A
review sheet shows hub identity, path, actual preview and the reviewed hash.
Only untrusted/changed/rejected files with a preview can be trusted. Unsaved
project edits must be saved or discarded first. The controller sends cwd/hash
and independently resolves after either success or a lost mutation reply;
confirmation requires the same hash to be trusted. A newer hash stays outside
the open review and requires reopening. Closing the sheet works while offline.

Manual Release evidence on 6 September 2026 used the owned skills directory on
SecondHub. iOS reviewed max_rounds=11 and trusted it; independent SDK readback
confirmed trusted status and effective maxRounds=11. Android opened a changed
max_rounds=12 preview. The fixture changed to 13 while the review stayed open.
Trust was rejected; the old preview remained visible with a changed-file message
and disabled Trust button. Closing/reopening showed 13. Android then trusted
that revision, independently confirmed as trusted with effective maxRounds=13.
The temporary launch.toml was removed after testing; the isolated hub retains
its trust history for the two reviewed hashes. No production config was touched.

Android uiautomator stalled during an observation before the final approval.
The same process remained alive, a screenshot showed the open review, and a
later semantic read recovered without restarting. The first unconfirmed action
was not counted as approval evidence. Full offline/reconnect and screen-reader
qualification remains open. The two controller tests cover exact hash/scope,
unsaved-draft fencing, lost-reply readback and a changed file after mutation.
The native suite now has 316 tests; both Release builds and TypeScript pass.


## Per-session launch options

The new-session form now keeps a launchOverrides draft separate from saved
layers. Session options use the same perLaunch/driver-support filter as the web
spawn form. Scalar, environment, fallback, path-list and MCP editors are shared
with project/global settings. A failed effective-value resolve retains the
schema controls for correction. Plugin selection remains a separate unfinished
flow, matching the web's separation from generic advanced fields.

The submit path reuses the web resolveScalars helper: advanced model/reasoning
values take precedence and are hoisted into top-level thread/start fields. A
qualified advanced model must not retain the earlier modelProvider chip. Empty
overrides are omitted; explicit false, zero and empty collection/map values are
preserved. Drafts survive failed creation and connection changes and remain
isolated between hubs. There is no launch/setLayer call in this path.

Manual native checks on 6 September 2026 created empty sessions using the owned
SecondHub fixture. iOS chose Fake Alternate and maxRounds=7, creating
034KNWkmyxCslgXC3kYIpD. Android chose Fake Test Model and maxRounds=8, creating
034KNaBKRJLBZ9QyGZ9Dh4. Independent thread/list located both, and each session's
persisted config.max_tool_rounds_per_input matched its selected value. Independent
getLayer reads confirmed no maxRounds override in either saved global or project
layer. No provider request or production mutation was needed.

318 native tests, TypeScript and both Release builds pass. The per-session row
list is a functional baseline, not visual acceptance: density, model/reasoning
presentation, large catalogs, keyboard and screen-reader behavior remain open.
Native collection/per-launch precedence and reconnect/error cases still need
expanded manual coverage. Creation images and plugin selection are not yet done.


## Composer space and model selection

The opening prompt now owns the full composer width. Model, supported reasoning
and Create share the footer; the model catalog opens in a searchable native
sheet instead of consuming the creation form. Harness choices are collapsed
until requested. Recent projects show their directory basename while retaining
the full path as the accessible label. Models and reasoning levels still come
from the hub catalog. A composer choice replaces the corresponding advanced
model/reasoning override, preserving unrelated per-session options. Choosing
hub default clears both explicit model and reasoning. The submission test checks
that reasoning chosen for an advanced model reaches thread/start.

Manual Release checks on 6 September 2026 used SecondHub and an existing owned
fixture directory. Both initial forms show the composer without scrolling at
the tested default text size. On iOS, focusing the prompt initially left the
footer under the keyboard: shrinking the ScrollView did not reveal its end.
The form now scrolls the focused composer into view on keyboard presentation
and viewport layout. iOS typed “keep this draft”, selected Fake Alternate in the
model sheet and returned with the exact draft and visible footer. Android
selected Fake Test Model and retained the same draft; dismissing and reopening
the keyboard also left the footer visible. These interactions did not submit a
prompt or issue a provider request.

Evidence:

- [Android before](assets/creation/composer-before-android.png) and
  [after](assets/creation/composer-after-android.png).
- [iOS initial form](assets/creation/composer-after-ios.png) and
  [model-picker return](assets/creation/composer-picker-return-ios.png).
- [Android model-picker return](assets/creation/composer-picker-return-android.png)
  and [keyboard refocus](assets/creation/composer-refocus-android.png).

Qualification remains incomplete. At the start of the follow-up, an Android
[screenshot showed a partially covered footer](assets/creation/composer-overlap-observation-android.png)
after earlier interrupted automation. The same app process remained alive;
scrolling exposed the controls. The subsequent picker round trip and keyboard
refocus did not reproduce the overlap. This observation is retained rather than
claimed fixed or attributed to a specific cause. Uiautomator also timed out and
returned a transient null root; screenshots and later semantic reads recovered
without an app restart. A preceding slow Android launch remains unprofiled.

Independent code review caught catalog refresh clearing composer reasoning for
an advanced model. The refresh now validates reasoning against the effective
model; a regression test fails before the fix, preserves supported effort after
refresh and clears it if the refreshed catalog removes that level. A separate
request test was also confirmed to fail under the former submit lookup.

321 native tests and TypeScript pass. Both Release builds succeeded after the
review fix. The screenshots precede that state-only refresh correction. Native reasoning-sheet checks need a fixture that advertises
reasoning levels; the current fake models do not. Small screens, long catalogs,
large text, multiline drafts and screen readers remain acceptance work under
MOB-001/MOB-007/MOB-010/MOB-011. This is an improvement in space usage, not final
visual acceptance.


## Opening images

The creation composer now uses the existing ImageSelection/nativeImagePicker
pipeline, source type/count/size limits and ImageAttachments previews. The plus
button shares the footer with model/reasoning/Create. Processing disables Create;
leaving the route cancels pending results. Removing a settled image removes its
editing marker. The form owns image bytes, preserving them through failed starts
and connection changes. Like the existing creation text/settings form, this is
memory-only: app termination or leaving creation loses it. Durable creation-draft
storage remains required before reliability acceptance.

Submission uses the shared buildComposerInput helper: an optional untrimmed text
item with translated image references, followed by image items. Whitespace-only
text can be omitted while images remain. Image identities and marker numbers are
local editing state, not wire fields. Mutating an input object after adding it
cannot alter the staged bytes; removal is blocked during submission.

Manual Release checks on 6 September 2026 used the bundled 1024×1024 icon in the
system photo libraries and the owned SecondHub skills directory. iOS selected,
removed and reselected the icon (marker 2), chose Fake Alternate, and created
local:034KOW69eSxSIzzMhenc3e. Android selected two copies, removed marker 1,
chose Fake Test Model and created local:034KOXqTdi9Z7NJp85m0TP. Both remaining
images survived model selection. The scripted turns were stopped from each app.
Both native conversation viewers then opened the persisted image.

A separate installed SDK consumer called thread/read for each session. Each
contained exactly one user message and one image/png item, with the translated
marker-2 text. Decoded bytes had the PNG signature and 1024×1024 IHDR dimensions:
iOS 747473 bytes, Android 409093 bytes. Different platform encoders need not
produce byte-identical PNGs. No production hub or provider was used.

- [iOS staged image](assets/creation/images-staged-ios.png) and
  [created-session viewer](assets/creation/created-image-viewer-ios.png).
- [Android staged image](assets/creation/images-staged-android.png) and
  [created-session viewer](assets/creation/created-image-viewer-android.png).

324 native tests, TypeScript, touched-file Biome and both Release builds pass.
Tests cover image-only wire input, translated markers, retained bytes after an
uncertain response/disconnect, per-form ownership, immutable staging, blocked
removal during submit, and the real selection controller canceling late results.
Independent review found no actionable issue in this slice.

## Live image delivery

The missing live tile was a client projection bug: full snapshots generated a
companion attachment row, but item lifecycle notifications replaced only the
text/activity row. Both paths now share attachment mapping. Live replacements
update the whole row group, remove obsolete images, and preserve source order.
Snapshot and older-page reconciliation respect newer live replacements and also
accept authoritative snapshot removal without retaining stale image ownership.

Release simulator checks sent a second image from the existing sessions above.
iOS sent icon.png and Android sent 1000000267.png; both showed the new thumbnail
while Stop was available and opened it in the native viewer before stopping.
Independent installed-SDK thread/read calls confirmed each thread was active,
turn_m2 was inProgress, and item_user_25 contained one image. These are real hub
and native UI checks with the isolated scripted provider, not model-quality tests.
The final recovery refinement was exercised by deterministic tests and rebuilt
on both platforms after the live-send checks.

- [iOS image visible during execution](assets/creation/live-image-ios.jpg).
- [Android image visible during execution](assets/creation/live-image-android.png).

331 native tests, 339 shared conversation/projection tests, TypeScript, touched
Biome checks and both Release builds pass. Seven regression tests cover live
input/output images, replacement/removal, stale snapshot and page overlap,
source-adjacent ordering, and authoritative removal. Independent review's
snapshot-removal finding was reproduced, fixed, and rechecked.

This closes the observed live-item projection gap, not all of MOB-008 or image
qualification. New-session opening-window races, image-only native submission,
limit/failure cases, long catalogs, keyboard attachment round trips, large text
and screen readers still need their own acceptance evidence.

## Per-session plugins

The creation form offers a searchable native plugin sheet for Evener-kind
harnesses, using the web's preview hook and selection helpers. Defaults omit
`enabledPlugins`; toggles create an explicit name list; None retains `[]`.
The sheet presents server descriptions, source, version, component counts,
diagnostics and unavailable selections. Selected missing names can be removed.
Plugin/launch update notifications refresh the preview. Choices affect this
session only; they do not change installed enablement or saved launch layers.

Explicit selections receive a fresh preview immediately before Start. Missing
names and structured selection errors block creation. Preview failure/disconnect
keeps the draft and cannot dispatch Start; it is distinct from an uncertain
response after Start. Unsupported harnesses omit/reset the plugin allow-list.
Review corrected failed-preview rows to permit deselection while blocking new
additions, matching the web flow.

Manual simulator Release evidence on the isolated SecondHub:

- iOS selected None, toggled native-tools on through its native checkbox, closed
  the sheet, chose Fake Alternate, and created local:034KPPuZNEP4HYLw7Gf4af.
  Independent installed-SDK thread/read returned native-tools 1.0.0 in runtime
  plugin diagnostics.
- Android toggled native-tools off through its native checkbox, closed the sheet,
  chose Fake Test Model, and created local:034KPS05mdSuC9o1z0OsXv. Independent
  runtime readback reported no loaded plugins. Both empty sessions remained idle.
- [iOS explicit selection](assets/creation/plugins-ios.jpg) and
  [Android explicit none](assets/creation/plugins-android.png).

338 native tests, 11 shared new-session service tests, 17 shared web selection/
preview tests, TypeScript, touched-file Biome and both Release builds pass.
The canonical make test-web gate also passes (typecheck, unit tests, Biome).
Seven added tests exercise explicit lists/none, unavailable names, preview
failure, disconnect before dispatch, honest uncertainty, and unsupported harnesses.
The final error-state checkbox refinement was rebuilt after the native creation
checks. Independent review found no remaining actionable issue in this slice.

The packed SDK's plugins.mjs recipe separately passed against the isolated hub:
default selection, explicit none, one named plugin and unavailable-name errors.
No session is created by that recipe. The fixture has no plugin components;
these checks prove selection/loading, not skill, hook, agent or MCP execution.
Failed-preview UI, long catalogs/search keyboard, accessibility, two-hub return
and creation-draft process-death qualification remain open.


## Durable creation drafts

Each hub has one SQLite creation draft containing the project, prompt, harness,
model/reasoning, launch overrides and image metadata. Immutable image bytes are
stored separately and are not rewritten on each text edit. Restored model IDs
are validated against the current catalog before creation. Removing a hub clears
its creation metadata and images without affecting another hub's draft.

Before thread/start, the form commits an unconfirmed-creation checkpoint. A
failed checkpoint blocks dispatch; a lost reply preserves the draft and warns
on reopening that a session may already exist. Creation is never replayed
automatically. A confirmed response clears the draft. Local storage failures
remain visible, with explicit retry; a failed load blocks editing and picking
images. A later failed checkpoint preserves any earlier uncertainty.

Manual Release evidence, 2026-09-06, on the isolated scripted SecondHub:

- iOS 26.5 / iPhone 17 Pro: chose the skills fixture directory, entered a prompt,
  selected Fake Alternate and attached icon.png. Stopped and relaunched the app
  process, reopened New session, and verified all four values. Created
  local:034KQ2UxvoMu5J5bCcfCsa, observed the image and selected model in the
  conversation, then stopped the scripted turn. Reopened New session and found
  the project, prompt, model and attachment cleared.
- Android API 35 / Pixel 7: entered a distinct prompt, selected the same directory,
  Fake Test Model and 1000000267.png. Force-stopped and relaunched the app and
  verified the restored values. Created local:034KQ6hXtTT2cqDKXM2QKf and observed
  the image and selected model in the running conversation, then stopped it.
  Reopening New session showed a cleared project, prompt, model and attachment.
- Independent installed @evener/appwire-client thread/read calls confirmed both
  session refs, directory, entered text and one image/png attachment each.
- [iOS restored draft](assets/creation/draft-restored-ios.jpg) and
  [Android restored draft](assets/creation/draft-restored-android.png).

349 native tests, TypeScript, touched-file Biome checks and both Release builds
pass. Eleven SQLite-backed tests cover hub isolation/removal, complete form
restoration, model revalidation, rollback, missing images/corrupt metadata,
uncertain dispatch/reply loss, creation cleanup, failed checkpoints and picker
blocking before load. Two review findings were reproduced as failing tests and
fixed: losing earlier uncertainty on checkpoint failure, and permitting image
picking before a saved draft could load.

This does not qualify every persistence state on devices. Storage exhaustion,
corrupt-draft recovery/discard UX, explicit plugin/reasoning restoration through
native controls, two-hub navigation and large image/text performance remain open.
The creation screen still needs the planned density, keyboard and accessibility
work; these screenshots are reliability evidence, not final visual acceptance.


## Configuration density

Group directory controls with 4-unit spacing and configuration controls without
extra inter-row gaps. Project settings and harness selection share a wrapping
row; plugins and session options share another. Expanded options take the full
width, and harness choices remain directly below their control. The visible
Project settings label retains Project launch settings as its accessible name.
The prompt stays full-width with model/reasoning and Create in its footer.
Touch targets retain the shared 44-point iOS / 48-dp Android minimums.

Both final Release builds passed, as did TypeScript, touched Biome and all 349
native tests. Manual iPhone 17 Pro and Pixel 7 checks show the four configuration
controls in two rows at default text size, and the complete prompt/footer above
the software keyboard. Session options expanded to full width on both platforms and
collapsed back; Android also exercised this with its software keyboard open. No prompt was submitted during this
layout check; the isolated-hub test drafts remain saved.

Captures: [iOS default](assets/creation/density-ios.jpg),
[iOS keyboard](assets/creation/density-ios-keyboard.jpg),
[Android default](assets/creation/density-android.png), and
[Android keyboard](assets/creation/density-android-keyboard.png).

This is a spacing improvement. Long harness/plugin labels, large accessibility
text, smaller screens, screen-reader traversal and expanded-option keyboard
behavior still require qualification; it does not establish final visual quality.


## Creation at accessibility text sizes

The largest iOS Dynamic Type check exposed an unbounded opening prompt and a
three-row footer: attachment, model and Create each occupied a separate row.
The prompt could push its actions away from the editing viewport. Creation now
uses the conversation composer's bounded-input approach: 96 units at font scale
above 1.6, otherwise a 120–160-unit input. Text scrolls inside that viewport.
Prompt content-size changes keep a focused composer in view without scrolling
past configuration when it opens. Above scale 1.4, model/reasoning precede the
attachment/submit row. The visible action is Create; its accessible name remains
Create session. No text is truncated in the stored draft by these layout rules.

Final Release checks on iOS accessibility-extra-extra-extra-large and Android
font scale 2.0 showed a focused input, model control, attachment and Create above
the software keyboard. On iOS, switching from normal text to the largest size
while editing kept the composer in view automatically. Android recreated its
activity on text-size changes; reopening creation restored the saved draft.
Settings rows wrapped to full width instead of overflowing horizontally.
Both simulator text settings were restored and read back (iOS large, Android 1.0).

- [iOS failure before bounding](assets/creation/large-before-ios.jpg).
- [iOS focused composer after live text-size change](assets/creation/large-ios.jpg).
- [Android focused composer at 2x text](assets/creation/large-android.png).

Both Release builds, TypeScript, touched Biome checks and 349 native tests pass.
This pass submitted no session. The iOS automation's replace-existing operation
inserted into the fixture instead of reliably replacing it; therefore these
captures establish geometry and focus, not exact long-text entry fidelity.
Native selection/autocorrection, screen readers, attachment-heavy creation,
long reasoning/model labels and smaller screens remain to be qualified.


## Installed client lifecycle recipe

The packaged session-lifecycle.mjs example now exercises a fresh idle session,
subscription, unique-ID input submission, start and interrupt receipts,
turn/started and turn/completed pushes, authoritative text/terminal-state
readback and unsubscribe. It deliberately requires a held-open scripted
provider and leaves the resulting idle session for inspection. It does not
establish natural completion, transcript delta reconstruction or reconnect
qualification.

A separate npm tarball consumer ran the final recipe against the authenticated
isolated SecondHub and created local:034KQlVEVwWYsH1nuXXy6t. The script verified
turn_m1 interruption and its exact unique input. A separate client connection
confirmed that session remained idle. Missing mutation opt-in and an invalid
catalog model both failed before creation; the newest session stayed unchanged.
The package build, touched Biome checks and canonical make test-web gate pass
(typecheck, tests, lint); independent source review found
no concrete protocol or cleanup issues. The coverage report now lists 14 of 88
methods and 3 of 35 notifications across five recipes. These counts describe
recipe presence, not exhaustive branches, supported-feature parity or release
readiness. The current protocol/client guide documents the lifecycle and its
uncertainty/cleanup limits.

## Creation draft isolation across saved hubs

Manual checks on the installed c9419a06a Release builds switched iOS from
SecondHu to Playground and Android from SecondHub to Android test hub.
Each destination connected and showed its own session list. Opening creation
there showed empty project and prompt fields, without the source hub's draft.
Returning to SecondHu/SecondHub and reopening creation restored the original
project path and exact previously observed prompt on both platforms. iOS also
retained Plugins · 1 selected and Session options · 1 overrides; Android
retained its launch defaults.

The check used existing saved fixture profiles and left both original drafts
unsubmitted. It establishes ordinary navigation isolation, not simultaneous
connections, late-response rejection, network fault recovery or exact
serialization of the displayed plugin/override summaries. Those remain separate
acceptance checks. Both apps were left on their restored creation forms.

## Creation during a real connection outage

On the same installed Release builds, terminating the owned SecondHub proxy
closed both native sockets while leaving the underlying isolated hub running.
Both creation screens retained their exact project and prompt, displayed
Reconnect, and disabled Create and hub-dependent configuration controls.

Android recovered automatically after the proxy resumed listening on
0.0.0.0:9200. On iOS, tapping Reconnect while the proxy was still offline
exercised a failed fresh connection. The screen retained the draft and retry
action; tapping Reconnect after the proxy returned restored the configuration
controls and enabled Create. iOS retained its displayed plugin selection and
override count. Neither draft was submitted.

The shared client's 53 deterministic tests passed separately. This native check
establishes draft retention and usable recovery for creation before submission.
It does not establish recovery during thread/start, receipt reconciliation,
stale replies after a hub switch, background execution or physical-device LAN
behavior. The proxy was left listening on all interfaces and both apps were
left on their recovered creation forms.
