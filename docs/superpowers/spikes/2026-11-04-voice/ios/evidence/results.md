# iOS voice spike results — 2026-11-04

**Both native targets compiled, and the disarmed simulator control test ran.
The audio graph was not initialized on a simulator or a phone.** This does not
qualify locked listening, AEC, wake words, audible barge-in, or persistent voice.

## Deliverables

- `../Sources/AudioProbe.swift`: real AVAudioSession/AVAudioEngine graph source,
  capture-count telemetry, synthesized PCM playback, stop fence, notification
  instrumentation, and JSON export without microphone samples.
- `../Sources/VoiceProbeApp.swift`: app-delegate-owned service and SwiftUI
  Arm/Disarm/Play/Stop/export controls.
- `../Sources/PlaybackFence.swift` and `../Tests/PlaybackFenceCheck.swift`:
  shared generation fence and executable assertions.
- `../Info.plist`, `../build.sh`, `../run-simulator-smoke.sh`: isolated,
  unprovisioned build and disarmed simulator runner.
- `../README.md`: graph explanation, reproduction commands, privacy boundaries,
  and the remaining physical-device acceptance checklist.

All paths below are relative to this evidence directory unless stated otherwise.
Only source and text/JSON evidence are retained, not binaries or microphone data.
No production files, signing identity, Apple registration, or TestFlight release
were changed. The simulator-only bundle is
`dev.evener.experimental.voiceprobe20261104`, never the production identifier.

## Measured host and setup limitation

`prerequisites.txt` records Darwin 27.0.0 arm64, Xcode 27.2 build 27B52,
Swift 6.4.2, iOS/simulator SDK 27.2, and available iOS 27.0/27.1 simulators.

`xcodebuild -checkFirstLaunchStatus` exited **69**. Device and simulator tools,
including their direct executable paths, printed:

```text
Install Started
Install Failed: Authorization is required to install the packages.
```

Read-only package inspection found a missing CoreTypes receipt and older
installed MobileDevice/XcodeSystemResources receipts than Xcode's bundled
packages. The receipts were unchanged after the builds and initial smoke.
No installation, `sudo`, authorization repair, or microphone-permission change
was performed. The existing compiler and isolated simulator worked despite
this unresolved setup failure. **General device installation/signing readiness
is not established.** The initially listed physical iPhone was unavailable;
that state is not proven to be caused by the package warning. No later physical
discovery or device test was attempted.

## Final native compile and link

The final source was copied into a unique remote scratch tree, created with
`mktemp -d /tmp/evener-voice-ios.XXXXXX`:
`m5max:/tmp/evener-voice-ios.3G5Ols`.

The final bounded qualification used these commands on m5max:

```sh
root=/tmp/evener-voice-ios.3G5Ols
bash "$root/source-reviewed/build.sh" simulator "$root/build-simulator-reviewed"
bash "$root/source-reviewed/build.sh" device-compile "$root/build-device-reviewed"
/Applications/Xcode.app/Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/bin/vtool \
  -show-build "$root/build-simulator-reviewed/VoiceProbe.app/VoiceProbe"
/Applications/Xcode.app/Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/bin/vtool \
  -show-build "$root/build-device-reviewed/VoiceProbe-UNSIGNED-NOT-INSTALLABLE"
bash "$root/source-reviewed/run-simulator-smoke.sh" \
  "$root/build-simulator-reviewed/VoiceProbe.app" "$root/smoke-reviewed"
```

The combined fail-fast qualification command exited **0**. `build.log` contains
the real `swiftc` invocations and `vtool` output, not a simulated build summary:

| Target | Compiler mode | Linked platform | Minimum OS | Linked SDK |
| --- | --- | --- | --- | --- |
| `arm64-apple-ios17.0-simulator` | Swift 6 | IOSSIMULATOR | 17.0 | 27.2 |
| `arm64-apple-ios17.0` | Swift 6 | IOS | 17.0 | 27.2 |

All three Swift source files compiled and linked for both targets. Each build
also compiled and ran the shared fence check as a macOS executable. Its five
`precondition` assertions exercised current, invalidated, and replacement
generations. Both executions printed:

```text
PASS: current accepted, stopped rejected, replacement accepted, previous rejected
```

The device output was unprovisioned and not an installable app bundle. It was
never run on a phone. Compilation proves API and Swift 6 compatibility and the
presence of the intended graph-construction code, not successful hardware setup.

## Disarmed simulator runtime

`simulator-smoke.log` is the final real run log. It includes the unresolved
package-setup warning rather than hiding it. A new private **iPhone 17 simulator
on iOS 27.0** booted, installed the probe, launched the actual SwiftUI app,
executed its disarmed control check, wrote JSON, exited, and was shut down.
The runner read that JSON and asserted the runtime, disarmed state, entered
view, generation change, stale callback rejection, and zero capture/playback.
It printed:

```text
PASS: SwiftUI view entered, stop advanced fence, stale callback rejected, no audio graph or capture
```

The app's evidence, copied unchanged to `disarmed-smoke.json`, contains:

```text
runtime=ios_simulator, os=27.0, phase=disarmed
engineRunning=false, voiceProcessingEnabled=false
generation=1, staleCallbacks=1, scheduled=0, completed=0
capture.buffers=0, capture.frames=0
```

The real service's stop path advanced the generation. A deliberately delayed
old-generation synthesis callback was rejected by the real handler. The test
did not create a microphone tap, start the engine, synthesize audible output,
exercise real queued playback, or measure stop latency. `sessionSampleRate`
is an inactive session value here, not a measured capture format. Process-relative
`ContinuousClock` timestamps are not evidence of locked duration or sleep
continuity. The JSON encoder/file output ran, but the interactive export sheet
and visual layout were not tested.

## Source identity and offline checks

`compiled-input-sha256.txt` records the final remote Swift, test, and plist
inputs. They were independently compared with the delivered local bytes after
the run. `source-sha256.txt` records the delivered inputs plus both scripts.
From the `ios/` directory, verify both with:

```sh
sha256sum -c evidence/compiled-input-sha256.txt
sha256sum -c evidence/source-sha256.txt
```

After the Mac qualification, only early `--help`/`-h` branches were added to
both scripts at the parent's request. Their build/runtime branches did not
change, and no compiled input changed. Both help paths and Bash syntax were
then checked on Linux with exit **0**; they require no Apple SDK or network:

```sh
bash build.sh --help
bash run-simulator-smoke.sh --help
bash -n build.sh run-simulator-smoke.sh
```

## Problems corrected during the spike

- Used the SDK's `.AVAudioEngineConfigurationChange` notification name rather
  than a nonexistent `AVAudioEngine.configurationChangeNotification` member.
- Passed typed scalar notification snapshots across actor boundaries rather
  than a non-Sendable `Notification`, keeping Swift 6 checking enabled.
- Supplied the explicit macOS SDK to the standalone fence-check compilation.
- Created the private device-set directory before `simctl create`.
- Set `SDKROOT` for the link step as well as `swiftc -sdk`. Independent `vtool`
  inspection initially found SDK 17.0 metadata and confirmed SDK 27.2 after
  that correction.
- Replaced device uptime with process-relative `ContinuousClock` telemetry,
  then rebuilt both targets and reran the simulator smoke on those exact inputs.

No assertions were weakened. The global first-launch setup failure remains.

## What remains unproved

The graph requests `playAndRecord`/`voiceChat`, enables voice processing on a
stopped engine before configuring formats/tap/player, and routes synthesized
PCM through the same engine. **`voiceChat` is not itself AEC**, and none of that
source has been hardware-qualified. The service's ownership outside SwiftUI
scenes and background-audio declaration are prerequisites, not persistence proof.

The probe deliberately disarms on interruption, route change, media reset, or
engine reconfiguration and requires foreground re-arm. That is a conservative
probe choice, not sufficient recovery for persistent voice v1. Apple speech
synthesis is only a local stimulus, not provider TTS. Whole-utterance buffering,
main-actor conversion, and lock-protected counters are probe shortcuts.

Wake recognition, ASR, automatic speech-driven barge-in, provider transport,
locked reconnection/auth refresh, and the required production recovery policy
are absent. No fake detector or RMS-based speech claim substitutes for them.
The README's physical checklist still requires foreground graph initialization,
5/30-minute locked no-playback capture, real wake/respond/barge-in and self-output
negatives, headset/speaker routes, interruption/recovery, locked network/auth
recovery, force-quit/re-arm, and unplugged no-debugger soak. Rows depending on
absent wake/ASR/network components remain blocked until those exist.

A physical continuation needs an available target phone and a parent-approved
isolated test project/signing identity. The experimental simulator identifier
must not be registered or used to overwrite the production app. TestFlight
archive/export/upload/install and real-device acceptance remain separate,
unperformed work. No App Store-policy conclusion follows from this spike.

## Cleanup

After source and evidence were copied back, all three private simulators were
independently confirmed `Shutdown`, then deleted by exact UUID within their
own device sets. Each private set subsequently listed no devices. The unique
remote scratch directory `/tmp/evener-voice-ios.3G5Ols` was removed and its
absence checked. The three matching simulator log directories under
`~/Library/Logs/CoreSimulator/` were also removed and their absence checked.
No shared simulator, existing app, or unrelated log directory was deleted.

Local scratch logs, JSON copies, and hash intermediates were removed. Only
Evener's pre-existing `.evener-session.lock` remains in this session's scratch.
The delivered source and evidence remain intact. The final Linux validation
exited **0** after checking both SHA-256 manifests, both offline help paths with
`PATH=/nonexistent`, Bash syntax, isolated plist settings, the simulator JSON,
and UTF-8/text-only contents of every retained file.
