# Native voice owner

Opt-in iOS Talk implementation, not release or acoustic qualification. The app subscriber owns the service independently of screens and JS foreground sockets. Wake activation is deliberately unavailable.

## Boundaries

- `core/VoiceCore.swift`: admission buffering, endpoint policy, generation fencing, rendered per-item timeline. `speech_end` closes one provider item. `delivery_end` closes generation of the whole response. Neither means played.
- `ios/VoiceAudioDevice.swift`: one voice-processed AVAudioEngine, real hardware formats, bounded preallocated capture buffers, serial inference, actual player clock and `.dataPlayedBack` receipts. Only built-in routes are accepted. Route transitions stop output before rebuilding and require consent after a private route is lost.
- `ios/SileroVAD.swift`: real recurrent Silero V5 inference, 512 new 16 kHz samples and 64 context samples per call. No amplitude-threshold substitute.
- `ios/VoiceSocket.swift`: narrow in-memory grant, bounded ordered JSON transport, reconnect fencing and explicit revocation. Plaintext hostname addresses must all resolve to loopback/Tailscale ranges. Issuance pins one literal address for socket reconnect and recovery disarm. The configured/displayed hostname remains unchanged. TLS keeps ordinary hostname certificate validation. Redirects are refused.
- `ios/VoiceAuthorization.swift`: one-shot foreground Arm/Disarm HTTP only. Broad auth exists transiently in the request, never the voice service, observable state or storage. The native boundary is necessary because React Native fetch's `redirect` option does not prove redirect rejection.
- `ios/EvenerVoiceModule.swift`: Expo bridge/application subscriber. General SecureStore accessibility is unchanged. The installation label is not speaker identity.
- `src/voice` in the native app: consent, explicit manager selection, configurable provider/model/voice, Arm/Disarm/Talk/Stop and custody/canonical reply display. Capability-absent hubs gain no controls or guessed RPCs.

Quiet microphone audio stays in bounded RAM on the phone. Capture loss/overflow abandons a partial input rather than submitting a truncation. No PCM enters a user-input record, outbox or diagnostics. Speech onset and Stop flush output and advance the local fence before network traffic. Background grant renewal is native; removal/logout retires the binding.

## Model and dependency packaging

Run from `mobile-native`:

```sh
node modules/evener-voice/ensure-model.mjs
```

The same check runs during `npm ci` postinstall. It downloads only the pinned Silero artifact when absent, verifies SHA-256 before writing, refuses to replace a bad existing file and verifies every existing file. The 2,313,101-byte model is ignored in Git because the repository limits tracked file size.

- Model: `https://raw.githubusercontent.com/snakers4/silero-vad/5cd2ba54db059f961e7545d4f211b44f005a6dfb/files/silero_vad.onnx`
- SHA-256: `6b99cbfd39246b6706f98ec13c7c50c6b299181f2474fa05cbc8046acc274396`
- Runtime: CocoaPods `onnxruntime-objc` **1.20.0**, which pins `onnxruntime-c` **1.20.0**. Both are MIT, with upstream iOS minimum 13.0. This app/module stays at 16.4.
- Silero is MIT. Notices live in `resources/` and are included with the model in CocoaPods' `EvenerVoiceModels.bundle`. The runtime checks the model checksum before inference. The app module itself does not introduce an open-source license grant.

A controlled `Podfile.lock` update remains required through the normal full-app prebuild/pod installation flow. No hand-written dependency lock was substituted.

## Checks

From `mobile-native`:

```sh
npm run check
npm run check:scripts
npm test -- App.test.tsx src/voice/voiceClient.test.ts src/voice/VoiceControls.test.tsx src/cameraPermission.test.ts
npm run test:voice-core
```

Swift Package tests run the portable controller/timeline. On macOS they also run the actual Foundation transport against a real loopback HTTP peer, including HTTP and WebSocket redirects and hostname pinning. The Python peer is an OS/network boundary, not a substitute voice controller. Removing HTTP redirect rejection makes the test observe a request at the forbidden destination.

On an Apple Silicon Mac, unpack the published ONNX 1.20.0 archives into a private directory (containing `objectivec/`, `Headers/`, `onnxruntime.xcframework/`) and run:

```sh
bash modules/evener-voice/check-apple.sh /absolute/extracted-runtime /absolute/new-output
```

Public archives:

- `https://download.onnxruntime.ai/pod-archive-onnxruntime-objc-1.20.0.zip`
- `https://download.onnxruntime.ai/pod-archive-onnxruntime-c-1.20.0.zip`

The script compiles the actual Objective-C runtime and links the production voice runtime for arm64 iOS device and simulator, then runs actual model loading plus 100 recurrent silence windows on macOS. It creates a new output directory and never generates an app, signs, installs or uploads anything. **It excludes the Expo bridge** and does not replace an app build. Swift 5 mode currently emits two Swift 6 Sendable migration warnings for the serial-queue service.

## Still unqualified

The full Expo module/app build, CocoaPods resource embedding in the final app, physical iPhone microphone/playout, leading-phoneme retention, endpoint latency, barge-in/AEC, route transitions, interruption recovery, extended locked operation and provider-to-manager end-to-end behavior require their own checks. The runtime link and synthetic timeline tests cannot prove these. No wake engine is shipped. Locked listening remains a requirement, not a claimed result.
