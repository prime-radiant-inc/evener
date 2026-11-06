# Native voice owner

Opt-in iOS Talk and local openWakeWord detection, not release or acoustic qualification. The app subscriber owns the service independently of screens and JS foreground sockets. The default shipped phrase is **Hey Evener.**, using the custom classifier at threshold **0.99**. Pretrained Alexa remains an optional personal-use preview, not the shipped default.

## Boundaries

- `core/VoiceCore.swift`: admission buffering, endpoint policy, generation fencing, rendered per-item timeline. `speech_end` closes one provider item. `delivery_end` closes generation of the whole response. Neither means played.
- `ios/VoiceAudioDevice.swift`: one voice-processed AVAudioEngine, real hardware formats, bounded preallocated capture buffers, serial inference, actual player clock and `.dataPlayedBack` receipts. Only built-in routes are accepted. Route transitions stop output before rebuilding and require consent after a private route is lost.
- `ios/SileroVAD.swift`: real recurrent Silero V5 inference, 512 new 16 kHz samples and 64 context samples per call. No amplitude-threshold substitute.
- `core/WakeWordDetector.swift`: replaceable normalized mono 16 kHz input/result protocol, configured score/hop/holdoff and hysteresis. `ios/OpenWakeWordDetector.swift` implements the three ONNX models with PCM16 scaling, mel `/10 + 2`, 76×32 mel windows and 96-wide embeddings. It waits for real startup context instead of inventing random features.
- `core/WakeWordStream.swift`: separate serial inference queue with a non-waiting two-block limit. Overflow resets and fences old context, never waits on the audio callback or blocks VAD/playback work. `VoiceAudioDevice` feeds the existing 16 kHz resampler branch only while armed and quiet. Wake shares `VoiceController.talk` admission through `wake`, with no extra hub endpoint or JS activation dependency.
- `core/WakeTrialLedger.swift`: native UserDefaults attempt records, scores, detections, model/settings and scored sample counts. `src/voice/WakeTrialControls.tsx` projects these under **Debug · Wake trials**. Trials disable normal wake activation/Talk and retire incoming speech, keeping the trial local even during JS suspension. Start while armed and quiet, wait for Listening, then mark positive, negative or false activation. Positive means intended, including a missed wake. Restarted incomplete attempts are marked interrupted, not silently resumed.
- `ios/VoiceSocket.swift`: narrow in-memory grant, bounded ordered JSON transport, reconnect fencing and explicit revocation. Plaintext hostname addresses must all resolve to loopback/Tailscale ranges. Issuance pins one literal address for socket reconnect and recovery disarm. The configured/displayed hostname remains unchanged. TLS keeps ordinary hostname certificate validation. Redirects are refused.
- `ios/VoiceAuthorization.swift`: one-shot foreground Arm/Disarm HTTP only. Broad auth exists transiently in the request, never the voice service, observable state or storage. The native boundary is necessary because React Native fetch's `redirect` option does not prove redirect rejection.
- `ios/EvenerVoiceModule.swift`: Expo bridge/application subscriber. General SecureStore accessibility is unchanged. The installation label is not speaker identity.
- `src/voice` in the native app: consent, explicit manager selection, configurable provider/model/voice, Arm/Disarm/Talk/Stop and custody/canonical reply display. Capability-absent hubs gain no controls or guessed RPCs.

Playback accepts nonempty, sample-aligned PCM16LE packets up to the hub's 64 KiB
decoded-byte limit. Packet size is separate from the outstanding playback budget
of 720,000 provider samples and the timeline's 8,192-chunk/128-ended-item bounds.

Quiet microphone audio stays in bounded RAM on the phone. Capture loss/overflow abandons a partial input rather than submitting a truncation. No PCM enters a user-input record, outbox or diagnostics. Speech onset and Stop flush output and advance the local fence before network traffic. Background grant renewal is native; removal/logout retires the binding.

The maximum-utterance policy is a normal endpoint: admitted speech ends within its
24 kHz sample budget, including buffered input, with a sample-aligned final block.
Silent input is abandoned, not submitted. Each accepted reply replaces the previous
follow-up deadline; its next window starts only when playback drains.

While speaker consent is pending, an incoming speech delivery is retired with its
own generation and no played-sample credit. Later consent permits fresh deliveries,
not replay of rejected speech. Retirement leaves admitted input and manager work alone.

Replies arriving during audio recovery are also retired, without leaving the
recovering state. They are cancelled rather than held for replay. Once capture is
ready, fresh speech can be admitted without another user input.

## Model and dependency packaging

Run from `mobile-native`:

```sh
node modules/evener-voice/ensure-model.mjs
```

The same check runs during `npm ci` postinstall. It downloads pinned Silero when absent and copies the custom wake assets from the tracked `resources/wake-word/` export. It verifies SHA-256 before writing, refuses to replace a bad existing file and verifies every selected file. Installed root-level ONNX copies and the generated `resources/wake-models.json` are ignored in Git. The three trainer originals are tracked and each fits the repository's 1 MiB file limit.

Wake manifests must have non-empty IDs/phrases and satisfy the Swift settings
limits before any assets are installed. Generated JSON is replaced atomically;
a truncated output from an earlier install does not prevent recovery.

- Model: `https://raw.githubusercontent.com/snakers4/silero-vad/5cd2ba54db059f961e7545d4f211b44f005a6dfb/files/silero_vad.onnx`
- SHA-256: `6b99cbfd39246b6706f98ec13c7c50c6b299181f2474fa05cbc8046acc274396`
- Runtime: CocoaPods `onnxruntime-objc` **1.20.0**, which pins `onnxruntime-c` **1.20.0**. Both are MIT, with upstream iOS minimum 13.0. This app/module stays at 16.4.
- Silero is MIT. Notices live in `resources/` and are included with the model in CocoaPods' `EvenerVoiceModels.bundle`. The runtime checks the model checksum before inference. The app module itself does not introduce an open-source license grant.
- `wake-models.custom.json` is the default installer manifest. It pins the custom classifier and the trainer's paired mel/embedding models, including their modified FP16 weight storage with FP32 operators. Do not substitute the upstream feature assets for these. Streaming feature semantics follow openWakeWord v0.6.0, and the runtime verifies all three hashes. The first-party classifier and Apache-2.0 feature models retain their training-data notices in `resources/wake-word/NOTICE.md`.
- `wake-models.pretrained.json` retains the upstream Alexa option and its original feature assets. That classifier is CC BY-NC-SA 4.0, personal-use preview only. Notices and license texts are bundled.

`Podfile.lock` records the module and both ONNX pods. Update it only through the
normal [full-app prebuild and pod installation flow](../../../docs/design/mobile/ios-build-distribution.md).

### Swapping the wake model

No change to the training lane's export path is required. Copy
`wake-models.custom.json` to a local manifest and keep the classifier's paired feature assets.
Set `id` to a unique model revision, `phrase` to the trained phrase, and `classifier`
to an object containing `file` (a new bundle basename ending in `.onnx`),
`sha256`, and `source` (an absolute path or a path relative to that manifest).
`url` is also supported for pinned downloaded assets. `source` is read-only;
the installer copies it into `resources/` under `file`, never moves the export.
Use a new filename when changing bytes; mismatched existing files are refused.

```sh
# Run after npm ci, which packages Hey Evener by default.
node modules/evener-voice/ensure-model.mjs --wake-manifest /absolute/custom-wake.json
# Optional personal-use Alexa preview:
node modules/evener-voice/ensure-model.mjs --wake-manifest modules/evener-voice/wake-models.pretrained.json
# Restore the shipped custom model:
node modules/evener-voice/ensure-model.mjs
```

Rebuild the app with the normal pod/prebuild flow. `settings.embeddingFrames`
must match the binary classifier's `[1, frames, 96]` input. Default settings are
threshold **0.99**, release threshold **0.5**, hop **1280** samples, holdoff
**2500 ms**, and **16** embedding frames. The release threshold is deliberately
well below detection: another detection requires both expired holdoff and a
score having fallen below 0.5, not just fluctuations near 0.99. This conservative
hysteresis choice is not a newly calibrated detection threshold.
`hopSamples` batches multiples of the trained 1280-sample/80 ms stride, not a
change to the model's time scale. The detector can also load an explicit manifest
URL, so its boundary is independent of the bundle convention. Record a new model
ID/settings cohort for qualification, update model notices and rerun real WAV
checks with matching positive audio. Do not treat the Alexa tests as Hey Evener
accuracy evidence.

The installer does not delete earlier models. CocoaPods selects only Silero,
the generated manifest's three ONNX files, and notices. Cached Alexa/upstream
ONNX copies and duplicate trainer originals are not included in the default app
bundle. Selecting a different manifest requires the normal pod/prebuild flow.

### Evidence and remaining gates

The trainer's [held-out evaluation](tools/wake-word/results/evaluation.json)
recorded **598/600 positive clips (99.67%)** and **zero false accepts over
2.089803 hours (2.09 h) of negatives** at 0.99. Positives are synthetic Piper
speech with speaker-disjoint training/calibration/test splits, not held-out
human recordings. Negatives mix Piper confusers, real LibriSpeech speech and
procedural noise. These offline numbers do not establish room, microphone,
locked-phone or long-duration accuracy.

Trial detections/labels save immediately, ongoing scored duration every five
seconds. No PCM is stored. The ledger retains the **latest 256 attempts**, pruning
oldest records on load/save without discarding the current unmarked attempt.
Debug totals cover retained records only, exclude incomplete/interrupted attempts
and keep model/settings cohorts separate. Warmup and gaps do not count as scored
audio. Record qualification evidence before older attempts age out. Targets are
≥60 intended trials, ≥95% detection and 30 h negative audio; none are claimed by
this runtime implementation. Periodic bridge updates run only during an active
trial or on a changed wake state or wake error. A classifier score that moves
while merely armed does not republish the whole voice state, so the debug
panel's live score comes from trials and quiet listening stays off the bridge.

## Checks

From `mobile-native`:

```sh
npm run check
npm run check:scripts
npm test -- App.test.tsx src/voice/voiceClient.test.ts src/voice/VoiceControls.test.tsx src/cameraPermission.test.ts
npm run test:voice-core
```

Swift Package tests run the portable controller/timeline. On macOS they also run the actual Foundation transport against a real loopback HTTP peer, including HTTP and WebSocket redirects and hostname pinning. The Python peer is an OS/network boundary, not a substitute voice controller. Removing HTTP redirect rejection makes the test observe a request at the forbidden destination.

The dependency-free core tests also cover wake trigger hysteresis/holdoff and
trial start/finish/interruption/retention. A semaphore-controlled slow/throwing
detector stub proves the stream's two-block bound, stale-epoch fencing and reset
after capture gaps or inference errors. Those mechanics tests do not claim model
accuracy; the separate WAV/ONNX checks below supply real inference evidence.

Consent tests exercise the production admission policy used by `VoiceService`.
The loopback WebSocket test observes the denied generation's interrupt before
offering a fresh delivery after consent, and checks that stale deliveries stay fenced.
These checks do not run the audio device or a live manager.

Packet tests exercise the validation and signed-sample conversion used by
`VoiceAudioDevice.enqueue`, including wire-sized packets through the Foundation
socket. They do not exercise AVAudioEngine scheduling or hardware-rate conversion.

On an Apple Silicon Mac, unpack the published ONNX 1.20.0 archives into a private directory (containing `objectivec/`, `Headers/`, `onnxruntime.xcframework/`) and run:

```sh
# Keep the five original preview regression cases available as test-only assets.
node modules/evener-voice/ensure-model.mjs --wake-manifest modules/evener-voice/wake-models.pretrained.json
node modules/evener-voice/ensure-model.mjs
bash modules/evener-voice/check-apple.sh /absolute/extracted-runtime /absolute/new-output
```

Public archives:

- `https://download.onnxruntime.ai/pod-archive-onnxruntime-objc-1.20.0.zip`
- `https://download.onnxruntime.ai/pod-archive-onnxruntime-c-1.20.0.zip`

The script compiles the actual Objective-C runtime and links the production voice runtime for arm64 iOS device and simulator, then runs actual model loading plus 100 recurrent silence windows on macOS. It builds an Apple XCTest bundle and runs six real wake-model WAV tests: the packaged custom model must detect the held-out Hey Evener fixture once and the negative fixture zero times at 0.99; the five original preview cases retain their positive/negative, state/Talk admission, chunk/reset/repeated-phrase, configured batch/threshold and trial persistence assertions. Preview assets are isolated in a test-only directory, not the app's model bundle. The script creates a new output directory and never generates an app, signs, installs or uploads anything. **It excludes the Expo bridge** and does not replace an app build.

The WAVs and provenance live in `runtime-tests/fixtures/`. The same test run writes
`macos/wake-activation.json` and `macos/wake-trials.json`; the committed copies are
replayed by JS activation, counter and UI tests, not generated detector scores.
Those receipts retain the original Alexa regression inputs. On 2026-11-06,
m5max/ONNX 1.20.0 measured **one custom Hey Evener detection, peak 0.9999996**,
and **zero** detections for spoken “Hey Jane”, peak **0.0004506111**, at 0.99.
The five preview regressions also passed (Alexa peak 0.9999999, negative peak
0.00028333068). The custom fixture is 58,234 bytes of held-out synthetic speech,
not a physical microphone recording. Do not lower 0.99 to make this check pass.
`make test-native` runs the JS/Metro/typecheck/script gate, **not Swift**; run
`swift test --package-path modules/evener-voice` and `check-apple.sh` separately.

The full-app check must build the Expo bridge and original share extension,
verify the embedded model checksum, and verify normal Simulator signing. Follow
the [iOS build procedure](../../../docs/design/mobile/ios-build-distribution.md);
a successful standalone runtime link does not cover these boundaries.

## Still unqualified

Physical iPhone microphone/playout, leading-phoneme retention, endpoint latency,
barge-in/AEC, route transitions, interruption recovery, extended locked operation,
battery and the complete phone-to-manager journey require their own checks. App
builds, scripted hub journeys and synthetic timeline tests cannot prove these.
The custom runtime is not an acoustically qualified Hey Evener release. Locked listening remains a requirement, not a claimed result.
