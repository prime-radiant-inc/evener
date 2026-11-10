# Wake detector audio

## Upstream preview fixtures

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
microphone. The six runtime tests write new receipts
under the check's `macos/` output; copy them here when deliberately refreshing
the JS bridge replay inputs. They include a positive attempt, a negative attempt
interrupted by reconstruction, and a completed negative attempt. UUIDs identify
test attempts only. The JS tests change manual labels/cohort metadata to exercise
counter rules, not the recorded detector scores or counts.

## Custom Hey Evener fixture

- `hey-evener.wav`: **test split**, Piper speaker **263** (LibriTTS-R speaker **7933**), source `en_US-libritts_r-medium` revision `e7229f7d33a10e145e109b886d661984201ac3c4`, seed **20261106** (speaker RNG seed **20261369**), one “Hey Evener.” utterance, clean positive variant 0, length scale 0.85.

This is synthetic speech, not a human wake recording or phone qualification.
The WAV is mono, signed 16-bit PCM at 16,000 Hz: **29,095 samples / 1.8184375 s /
58,234 bytes**. SHA-256:
`5eeec03e53907f81c8de18f046f0043219faa2be0479b89e79ae05aad8a9988d`.

Regeneration uses `tools/wake-word/corpus.py`: initialize the pinned Piper voice,
call `render("Hey Evener.", 263, 0.85)`, then
`augment(raw, np.random.default_rng(20261369), 0)`. Save the returned PCM array
from sample **14,080** onward as WAV PCM_16. This removes only 11 leading silent
80 ms hops; no speech or trailing samples are removed. Speaker 263 is listed
only in the test split of `tools/wake-word/results/corpus-summary.json`.
Dependency and download pins are in the [trainer README](../../tools/wake-word/README.md).
The Piper model card identifies LibriTTS-R as CC BY 4.0; attribution and source
notices are in [NOTICE.md](../../resources/wake-word/NOTICE.md).

### Trainer's packaged-model check

The exact saved WAV scored **0.9999995231628418** at 0.88 s using the packaged
models, above the pinned **0.99** threshold. Verification used the trainer's
actual streaming `evaluate.predict` path: 25 silent warmup hops, then 1,280
samples per call. The warmup peak was 0.00009760260581970215.

From the repository root, with `PY` pointing to the pinned trainer environment:

```sh
PYTHONPATH=mobile-native/modules/evener-voice/tools/wake-word "$PY" - <<'PY'
import soundfile as sf
from evaluate import detector, predict
from prepare import ASSETS

audio, rate = sf.read("mobile-native/modules/evener-voice/runtime-tests/fixtures/hey-evener.wav", dtype="int16")
assert rate == 16000 and audio.ndim == 1 and len(audio) <= 32000
scores, warmup = predict(detector(ASSETS), audio)
print("peak", float(scores.max()), "warmup", warmup)
assert scores.max() >= 0.99 and warmup < 0.99
PY
```

The classifier SHA-256 used was
`a519522a2fa56b8a1c761fc71a17ff38e9c8d980c0584610214f61d42cf6851d`.
This Python CPU check does not claim a Swift or physical-device result.
