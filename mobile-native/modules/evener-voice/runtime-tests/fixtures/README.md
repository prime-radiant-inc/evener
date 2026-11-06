# Wake detector audio

Unmodified mono PCM16, 16 kHz spoken WAVs from openWakeWord v0.6.0:

- `alexa.wav`: `tests/data/alexa_test.wav`, SHA-256
  `915dba88a2b936f3ba3ee91affcdbe9041ffbe532613d3aca58d8a15447fbd81`.
- `negative-hey-jane.wav`: `tests/data/hey_jane.wav`, SHA-256
  `cf1b938bd8ca1a7e9bb042665b9cf92a772a6673c6b98e264b27176ea4d3f443`.

Source: https://github.com/dscripka/openWakeWord/tree/v0.6.0/tests/data
Copyright David Scripka / openWakeWord contributors. The repository is Apache-2.0
and its pretrained models are CC BY-NC-SA 4.0, see the bundled notices in
`../../resources/`. These tiny upstream fixtures prove inference, not acoustic
accuracy, speaker identification, or physical iPhone qualification.

Tests add three seconds of digital silence before and after the original audio
to settle real streaming context. They do not replace the spoken samples or
inject classifier results.

`wake-activation.json` and `wake-trials.json` contain actual Swift/ONNX 1.20.0
detector, controller and ledger observations from `check-apple.sh` on m5max,
2026-11-06, projected into the bridge shape. They do not run VoiceService or the
microphone. The five runtime tests write new receipts
under the check's `macos/` output; copy them here when deliberately refreshing
the JS bridge replay inputs. They include a positive attempt, a negative attempt
interrupted by reconstruction, and a completed negative attempt. UUIDs identify
test attempts only. The JS tests change manual labels/cohort metadata to exercise
counter rules, not the recorded detector scores or counts.
