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
