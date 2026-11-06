# HEY EVENER keyword-spotting spike

**Result: custom BPE configuration works, but HEY EVENER did not trigger in this experiment.** The fixed operating point missed all 10 synthetic user positives and all 5 exact-wake playback controls. An official **LIGHT UP** recording triggered with the same model, settings and streaming loop. Do not treat this as a qualified wake engine for v1.

**Status:** throwaway research, 2026-11-04. Not production code. Linux CPU evidence does **not** qualify iPhone listening while locked/backgrounded, human accuracy, physical echo cancellation, or acoustical barge-in.

## Measured result

| Independent clip category | Clips | Triggered clips | Expected triggers |
| --- | ---: | ---: | ---: |
| HEY EVENER alone | 5 | 0 | 5 |
| Wake immediately followed by command | 5 | 0 | 5 |
| Ordinary speech | 10 | 0 | 0 |
| Phonetic near-misses | 30 | 0 | 0 |
| Exact wake labelled assistant playback | 5 | 0 | 5 |
| Silence | 1 | 0 | 0 |
| Seeded noise | 1 | 0 | 0 |

The 42 negatives **actually ran**: each has positive `samples_fed` and `decode_calls` in [results.json](results.json). Zero negative triggers are not a useful accuracy result when every intended wake also misses. The cause of the custom-phrase misses remains unresolved; synthetic pronunciation, phrase/model fit and this operating point are not separated by this test.

- **Continuous HEY EVENER stream:** 89.06075 seconds, 30 labelled segments, 277 decode calls, 0/15 intended triggers. The stream is not recreated at clip boundaries. The shorter `slt_wake → slt_ordinary1 → slt_wake` control also missed both wakes, with and without reset. **HEY EVENER retrigger behavior was not demonstrated.**
- **Pipeline calibration:** the official archive's `test_wavs/0.wav`, with its original keyword list, detected `LIGHT UP` once. Concatenating two copies detected it twice, at 3.68 and 11.36 seconds of audio fed. The correct-reset path actually called `reset_stream` twice. Omitting reset also yielded two detections in this limited control, not sticky duplicates; that does not override upstream's instruction to reset after a hit. This is calibration, not HEY EVENER or AEC evidence.
- **Playback:** each playback PCM hash equals that voice's wake-alone hash. Both missed. The planned positive self-wake control failed; **this run does not demonstrate self-wake or successful echo rejection**. Direct digital injection cannot test a speaker/microphone echo path. A working wake detector still needs a separate playback/AEC qualification.
- **Missing model:** a subprocess called the real `sherpa_onnx.KeywordSpotter` constructor with a nonexistent encoder. It exited **1** with `AssertionError: $WORKSPACE/model/deliberately-missing-encoder.onnx does not exist`. No silent fallback ran.

### Timing and size

On an AMD Ryzen 9 PRO 8945HS, CPU provider, one configured inference thread:

| Measurement | Captured value |
| --- | ---: |
| Model construction, wall / process CPU | 0.944344 / 0.936537 s |
| Independent-clip audio processed | 155.48612 s |
| Independent-clip decode calls | 432 |
| Streaming processing, wall / process CPU | 3.054827 / 3.003081 s |
| Wall / CPU real-time factor | 0.019647 / 0.019314 |
| Five selected model/tokenizer assets | 5,498,367 bytes |

RTF is measured processing seconds divided by fed audio seconds, including the documented padding. Timing covers `accept_waveform`, decoding, result checks, resets and final drain. It excludes synthesis, conversion to float samples, stream creation, downloads and model construction. Model construction excludes Python imports and tokenization; asset hashing already read the files, so this is **not cold-storage startup**. These desktop measurements say nothing about iPhone battery, thermal behavior or scheduling. `fed_seconds` is not end-to-end detection latency.

## Fixed experiment

- Runtime: `sherpa-onnx==2.0.1`, `sherpa-onnx-core==2.0.1`, `sentencepiece==0.2.1`, `numpy==2.3.4`.
- API source: tag `v2.0.1`, commit `af2d8d81dbfa61c4cea4c568da78f3982edca005`. The installed Python `KeywordSpotter` wrapper's SHA-256 matches the pinned source. Wheel bytes are pinned separately; this is not a source-build attestation for their native binaries.
- Model: `sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01`, int8 encoder/decoder/joiner, `tokens.txt`, `bpe.model`. BPE is used to prepare the keyword, not to retrain the model.
- Settings selected **before inference**: threshold **0.25**, score **1.0**, maximum active paths **4**, trailing blanks **1**, CPU provider, **1** thread, **80** features, **16,000 Hz** mono PCM16. No per-keyword overrides, threshold sweep, alternate spelling search or training.
- Keyword file, generated through SentencePiece and checked against `tokens.txt`:

  ```text
  ▁HE Y ▁EVEN ER @HEY_EVENER
  ```

- File-fed streaming uses **1,280 samples / 80 ms** per call, without sleeping. A fresh stream is used per independent clip. Continuous controls retain one stream, and the normal path resets immediately after a detection, as upstream instructs.
- FFmpeg `6.1.1-3ubuntu5`, Debian package `7:6.1.1-3ubuntu5`, and `libflite1 2.2-6build3`. All five installed voice choices were used: `awb`, `kal`, `kal16`, `rms`, `slt`. `kal` and `kal16` are related voices, not independent human speakers.
- Each voice synthesized `Hey Evener`, `Hey Evener tell me my next meeting`, `Please list my upcoming meetings`, `The train leaves in ten minutes`, `Hey everyone`, `Hey evening`, `Hey Evan`, `Hey even`, `Hey Evelyn`, `Evener`, and the duplicate `Hey Evener` playback control. The wake-plus-command is **one synthesis call**, with no inserted interphrase pause.
- Every speech clip has 0.3 seconds of leading silence and 1.0 second of trailing silence. Separate silence/noise clips are 5 seconds each. Noise is NumPy PCG64 seed `20261104`, uniform PCM16 integers from -1024 through 1024. The manifest stores exact text, voice, sample count, WAV/PCM SHA-256, expected/observed hits, decode calls and timing for each clip.

The all-miss result prompted **one bounded diagnostic**, using the archive's labelled `LIGHT UP` recording and original keyword list. The first full run and the clean reproduction both missed all custom-phrase positives. No setting changed. The successful reference control rules out a completely nonfunctional inference path, not all custom-phrase or synthetic-speech problems.

## Reproduce

These commands were run from the repository root, with `/usr/bin/python3.12`, `uv 0.10.10`, FFmpeg with Flite, Linux x86_64 and the package versions above. `prepare` downloads only pinned public files into scratch, checks hashes, extracts named archive members, and installs exact wheels into a scratch virtual environment. It does not run downloaded example scripts.

```bash
set -e
: "${EVENER_SCRATCH_DIR:?Set an owned scratch directory}"
PROBE=docs/superpowers/spikes/2026-11-04-voice/wake/probe.py
WORK="$EVENER_SCRATCH_DIR/wake-sherpa-repro"
test ! -e "$WORK"
# Read probe.py before running it.
timeout 180 /usr/bin/python3.12 "$PROBE" prepare --workspace "$WORK"
timeout 180 "$WORK/venv/bin/python" "$PROBE" run --workspace "$WORK" \
  --output docs/superpowers/spikes/2026-11-04-voice/wake/results.json
```

For a new comparison, use `--output "$WORK/results.json"` instead of overwriting this captured record. Timings vary; compare settings, hashes, clip counts and detections. `run` exits zero when execution/coverage/calibration checks complete, **not when the proposed wake phrase works**. Intended misses remain `matches_expectation: false`.

The initial smoke command was:

```bash
timeout 180 "$EVENER_SCRATCH_DIR/wake-sherpa/venv/bin/python" \
  docs/superpowers/spikes/2026-11-04-voice/wake/probe.py smoke \
  --workspace "$EVENER_SCRATCH_DIR/wake-sherpa"
```

It synthesized the corpus but inferred only `slt_wake`: 39,280 samples, seven decode calls, no hit. Early setup tried an unpublished `sherpa-onnx==1.12.17` pin and an ambiguous `uv --python python3` selection, which chose Python 3.14. Neither produced inference. Published 2.0.1 wheels and explicit Python 3.12 corrected setup before the first smoke test. No fallback engine was used.

### Verification and retained artifacts

The clean `prepare` and `run` commands both exited zero. A separate standard-library audit, without importing the probe, opened all 57 WAVs and checked their headers, frame counts, WAV hashes and PCM hashes against the manifest. Every speech clip was non-silent, with minimum RMS 477.67 PCM16 units. It also verified the 42 negative executions, duplicate playback PCM, unchanged settings, source hash, reference detections and missing-model exit.

Captured artifact SHA-256:

```text
ee622c5476ce072de64eb1d06467ed14de7b054f37c8ecd1a82322609ef9303a  probe.py
68fb8c6f0193b5b2da1ceadb3d7dccd559d21e00abc07e390a014ec73717fa8b  results.json
```

[probe.py](probe.py) contains the executable experiment and download pins. [results.json](results.json) is a 43,429-byte manifest/result record, not audio or model data. This README contains interpretation, run commands and license references. Scratch downloads, weights, audio, virtual environments and caches were removed after the audit. No product source, product tests, git configuration or existing state was changed; no commit or push was made. No microphone, private audio, paid API or inference service was used.

## Source and license references

- [Official English model instructions and custom BPE keywords](https://k2-fsa.github.io/sherpa/onnx/kws/pretrained_models/index.html#sherpa-onnx-kws-zipformer-gigaspeech-3-3m-2024-01-01-english).
- [Official model archive](https://github.com/k2-fsa/sherpa-onnx/releases/download/kws-models/sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01.tar.bz2), SHA-256 `f170013b4716e41b62b9bfd809687c207cef798ef9bc6534d524e17af9b6561a`. All five extracted asset hashes and sizes, exact wheel URLs/hashes, API-source hashes and synthesis binary hashes are in `results.json` and/or `probe.py`.
- [Pinned sherpa runtime license](https://github.com/k2-fsa/sherpa-onnx/blob/af2d8d81dbfa61c4cea4c568da78f3982edca005/LICENSE): Apache-2.0.
- [Pinned Python streaming example](https://github.com/k2-fsa/sherpa-onnx/blob/af2d8d81dbfa61c4cea4c568da78f3982edca005/python-api-examples/keyword-spotter.py), [KeywordSpotter wrapper](https://github.com/k2-fsa/sherpa-onnx/blob/af2d8d81dbfa61c4cea4c568da78f3982edca005/sherpa-onnx/python/sherpa_onnx/keyword_spotter.py), and [BPE helper](https://github.com/k2-fsa/sherpa-onnx/blob/af2d8d81dbfa61c4cea4c568da78f3982edca005/sherpa-onnx/python/sherpa_onnx/utils.py). The probe uses the helper's SentencePiece BPE branch directly to avoid its unused pypinyin dependency.
- [ModelScope model metadata](https://www.modelscope.cn/api/v1/models/pkufool/sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01) and the archive's `README.md` state **Apache License 2.0**. The archive README is hash-pinned in `reference_control`.
- [GigaSpeech repository and access instructions](https://github.com/SpeechColab/GigaSpeech): training used GigaSpeech XL. Dataset access and third-party source-media terms require **separate review**. Repository/model metadata is **not commercial clearance** for this product.
- [Flite COPYRIGHT/exception notices](https://github.com/festvox/flite/blob/master/COPYING): BSD-like CMU terms plus component exceptions. [FFmpeg licensing](https://ffmpeg.org/legal.html): this installed FFmpeg build enables GPL components. Synthetic generation is provenance evidence, not a blanket downstream rights determination.
- [SentencePiece v0.2.1 license](https://github.com/google/sentencepiece/blob/v0.2.1/LICENSE) and [NumPy v2.3.4 license](https://github.com/numpy/numpy/blob/v2.3.4/LICENSE.txt), plus bundled wheel notices, remain relevant to any future packaging. No dependency binaries are retained here.

## What remains unproved

The library accepts custom HEY EVENER tokens without retraining; **useful detection of this phrase is not established**. No human speaker, accent/noise/distance sweep, long-run false-accept rate, command transcription, physical AEC, double-talk, acoustical barge-in or playback cancellation was tested. No iOS code ran. Lock/background survival, permission and interruption handling, device-route changes, battery and thermal behavior remain required native-device work. Browser behavior was not tested. These gaps cannot be replaced by the desktop RTF or the unrelated LIGHT UP calibration.
