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

Live transcript qualification is incomplete: the initial running-session native
views showed the user text without its image tile. The tile appeared after Stop,
while independent thread/read already contained the image during the Android
run. Trace the initial snapshot/item notifications and native projection before
claiming live image delivery works. This is tracked with the running-work matrix
under MOB-008. Image-only native submission, limit/failure cases, long catalogs,
keyboard attachment round trips, large text and screen readers remain unqualified.
