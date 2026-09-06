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
