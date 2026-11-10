# HEY EVENER bounded follow-up

**Result: the wake gate remains unresolved.** The original 10 positives still miss,
as do two positives from a fresh synthetic voice. Canonical upstream tokenization
matches the baseline. Removing only the confidence cutoff does not recover a hit.
The evidence points toward phrase/model/decoder-path fit, but does not identify
which internal path loses the keyword. No shipping engine was selected.

This is Linux synthetic diagnostic evidence, recorded 2026-11-05. It is **not**
human accuracy, speech-quality, AEC or locked-iPhone qualification. The original
[README.md](README.md), [probe.py](probe.py) and [results.json](results.json) are
unchanged. Only this report, [followup.py](followup.py) and
[followup-results.json](followup-results.json) are new repository artifacts.

## Counts, with inputs and operating points kept separate

| Input set | Clips | Keyword | Threshold | Triggered clips / hits |
| --- | ---: | --- | ---: | ---: |
| Original wake alone | 5 | HEY EVENER | 0.25 | 0 / 0 |
| Original wake plus command | 5 | HEY EVENER | 0.25 | 0 / 0 |
| Original negatives, actually decoded | 42 | HEY EVENER | 0.25 | 0 / 0 |
| Original duplicate playback labels | 5 | HEY EVENER | 0.25 | 0 / 0 |
| One local reduced-schwa input | 1 | HEY EVENER | 0.25 | 0 / 0 |
| Fresh cedar wake alone and plus command | 2 | HEY EVENER | 0.25 | 0 / 0 |
| Fresh cedar negatives | 3 | HEY EVENER | 0.25 | 0 / 0 |
| Same original SLT wake, alternate-phrase calibration | 1 | HEY EVEN HER | 0.25 | 1 / 1 |
| Local Flite LIGHT UP calibration | 1 | LIGHT UP | 0.25 | 0 / 0 |
| Fresh cedar LIGHT UP calibration | 1 | LIGHT UP | 0.25 | 1 / 1 |
| Official reference calibration | 1 | Official keyword list | 0.25 | 1 / 1 |
| Official reference concatenated twice | 1 stream | Official keyword list | 0.25 | 1 / 2 |
| Original 57 plus five fresh target/negative clips | 62 | HEY EVENER | **0.0** | 0 / 0 |

The last row is a separate, deliberately extreme diagnostic, not a proposed
operating point or 62 additional independent samples. Its 62 clips account for
491 decode calls. Its intended user positives are the original 10 and fresh two,
not the five playback duplicates. Zero triggers on 42 original and three fresh
negatives are not a useful false-accept result while all intended positives miss.
The two new LIGHT UP WAVs also produced zero HEY EVENER hits when fed to the target
detector, recorded separately from their LIGHT UP calibration results.

All target runs retain int8 zipformer-gigaspeech 3.3M, sherpa-onnx/core 2.0.1,
score **1**, maximum active paths **4**, trailing blanks **1**, CPU **one thread**,
16-kHz mono PCM16, 80 features, 1,280-sample chunks and immediate reset after a hit.
Synthetic WAVs retain 0.3 seconds leading and 1.0 second trailing silence. Only
the last experiment changes threshold, from **0.25 to 0.0**. No weights, scores,
beam widths or target spellings were searched.

## Ordered hypotheses, recorded before diagnostic changes

1. **Synthetic pronunciation / acoustic fit.** Flite's pronunciation of the uncommon
   word “Evener” may not fit the model's expected sounds. Compare the original input
   with a local, explicitly pronounced control, retaining the canonical keyword and
   all decoder settings. Compare synthetic LIGHT UP with the official recording.
2. **Keyword tokenization differs from upstream.** Compare the original SentencePiece
   pieces with the pinned upstream helper and vocabulary IDs, without guessing
   alternate spellings or changing the keyword.
3. **The fixed decoding point rejects an otherwise plausible phrase.** If the first
   two checks do not resolve it, inspect the same model's unbiased transcription
   before considering a single isolated settings diagnostic. No threshold sweep.
4. **A phrase-specific stream/framing defect.** Check upstream padding, chunking,
   sample-rate and reset use against the original loop, with the official positive
   calibration. Do not infer this from empty detections alone.
5. **The small model lacks usable support for this phrase.** This remains plausible
   if canonical tokens and a fresh synthetic pronunciation still miss. Stop rather
   than select a shipping engine or fit many variants to the synthetic corpus.

### Reproduction and source checks

The full unchanged baseline ran, not just a representative miss. An independent
standard-library audit compared all 57 WAV/PCM hashes, samples, decode counts and
hit counts against the retained original. All matched. Official LIGHT UP still
hit once and the continuous reference hit twice. The baseline missing-model
subprocess still exited 1, not a silent fallback.

The actual installed `sherpa_onnx.text2token` function, with its real pinned
`pypinyin==0.55.0` dependency, returned:

```text
pieces: ▁HE Y ▁EVEN ER
IDs:    49 17 274 38
file:   ▁HE Y ▁EVEN ER @HEY_EVENER
```

This agrees with the pinned upstream helper's BPE branch, which calls
SentencePiece and checks `tokens.txt`. It rules out the original probe's direct
SentencePiece call differing from that helper. It does **not** prove that the
acoustic decoder will emit those pieces for every pronunciation.

Unbiased `OnlineRecognizer.from_transducer` decoding used the **same three int8
model files**, greedy search, no hotwords/language-model file, no endpoint reset,
and the same rate/chunks/padding. These are **model outputs, not human transcripts**:

| Input | Greedy output |
| --- | --- |
| Original awb wake | HE NORTH |
| Original kal wake | THEY EVEN ARE |
| Original kal16 wake | I EVEN |
| Original rms wake | HE EVEN EARTH |
| Original slt wake | HEY EVEN HER |
| Fresh cedar wake | HEY EVEN A |
| Fresh cedar wake plus command | HEY EVEN A TELL ME MY NEXT MEETING |

The greedy decoder differs from the constrained KWS decoder. These outputs are
clues about acoustic/token competition, not a trace of the losing KWS beam.

### Experiment 1: one local pronunciation intervention, failed

Before synthesis, Flite's real segment relation returned
`pau hh ey iy v ax n er pau` for both “Hey Evener” and “Hey even er”. Merely inserting
spaces would not establish a different phone sequence. The predeclared control
used SLT direct-phone synthesis of **`hh ey iy v n er`**, removing the unstressed
schwa, with unchanged target keyword/settings. It missed; greedy output was
**NAIVE**. Direct-phone synthesis also changes prosody, so this failed control is
not clean proof about the vowel or the quality of the original pronunciation.

Two separate calibrations ran at the same settings. Flite “Light up” missed
LIGHT UP and decoded as LIKE UP. The original `slt_wake.wav`, without changing a
sample, detected **HEY EVEN HER** once at 1.76 seconds fed when configured with
`▁HE Y ▁EVEN ▁HER @HEY_EVEN_HER`. This demonstrates that the SLT waveform and loop
can produce a phrase hit, but **HEY EVEN HER is not the requested wake**. That
keyword was selected from observed model output, so it is an explanatory
calibration, not an independently selected candidate or measured accuracy.

### Experiment 2: one fresh synthetic voice, failed

The local controls did not settle the target. The next experiment was declared
before requests: one voice, two target positives, three negatives, one calibration.
OpenAI's documented `POST /v1/audio/speech`, model
**`gpt-4o-mini-tts-2025-12-15`**, voice **`cedar`**, `response_format: pcm` were used.
`GET /v1/models/gpt-4o-mini-tts-2025-12-15` returned the exact model ID before
synthesis. **Six speech requests completed, no retries**, below the eight-request
limit. No transcription API was called.

Instruction on every request:

> Speak naturally and clearly in American English. Pronounce Evener as EE-vuh-ner. Say only the supplied text.

| Input | Role | Returned 24-kHz PCM bytes |
| --- | --- | ---: |
| Hey Evener | Positive | 98,400 |
| Hey Evener tell me my next meeting | Positive | 172,800 |
| Hey everyone | Negative | 196,800 |
| Hey even | Negative | 69,600 |
| Please list my upcoming meetings | Negative | 127,200 |
| Light up | Calibration | 103,200 |

The response bytes were saved, then FFmpeg converted 24-kHz signed little-endian
PCM16 to 16 kHz and added the fixed padding. Raw/converted hashes and provider
request IDs are retained. The fresh LIGHT UP control hit, but both fresh target
positives missed. The model returned HEY EVERYONE twice for its negative clip;
these short synthetic fixtures were not human-audited for pronunciation or
speech quality. New synthesis with the same request is not byte-deterministic.
This TTS service generated diagnostic inputs only, not a proposed runtime service.

The authorized dotenv was copied exactly to owned scratch before processing.
Only the needed key was loaded in process, never printed or retained in results.
The copy was deleted by an EXIT trap, and its absence was checked. Billing was
not queried, so no dollar-cost claim is made.

### Experiment 3: remove only the confidence cutoff, failed, then stop

Only global `keywords_threshold` changed to **0.0**. All original 57 and the five
fresh target/negative WAVs ran unchanged. None of the 62 triggered. The two input experiments
and this settings experiment exhaust the declared three-failure budget.

The pinned decoder source explains why “just lower the threshold” need not help:

```mermaid
flowchart LR
  A[PCM and acoustic model] --> B[Top four token paths]
  B --> C[Choose best path]
  C --> D[Require keyword match and trailing blanks]
  D --> E[Compare mean token probability with threshold]
  E --> F[Emit keyword]
```

`TransducerKeywordDecoder::Decode` prunes at lines 109–110, chooses the best path
at 147, checks a matched context at 149–153, then requires trailing blanks
**greater than** the configured 1 and mean token probability at least threshold
at 159–160. `ContextGraph` stores a passed global zero, and the Python wrapper
passes it unchanged. Zero was not substituted back to 0.25.

Thus the default confidence cutoff is **not the sole blocker on these inputs**.
Canonical tokens are correct, and multiple non-target calibrations work. The
remaining distinction—phrase/model fit, path pruning, or matching/blank timing—
requires a KWS internal trace or representative human audio. Neither was obtained.
No fourth intervention, wider beam, score boost, alternate-spelling sweep or new
voice was tried. **Judgment call:** stop here and hand the choice of the next
bounded investigation to the parent rather than tune this tiny synthetic corpus.

## Reproduce and check

Read both scripts before execution. Run from the repository root with Python
3.12, `uv`, FFmpeg 6.1.1/Flite 2.2 on Linux x86_64, as in the baseline. `prepare`
checks the original download hashes, then installs the additional pinned wheel
needed to call the actual upstream helper. No downloaded example is executed.

```bash
set -e
: "${EVENER_SCRATCH_DIR:?Use owned scratch}"
DIR=docs/superpowers/spikes/2026-11-04-voice/wake
WORK="$EVENER_SCRATCH_DIR/wake-followup"
test ! -e "$WORK"
export PYTHONDONTWRITEBYTECODE=1 OPENBLAS_NUM_THREADS=1 OMP_NUM_THREADS=1
timeout 180 /usr/bin/python3.12 "$DIR/followup.py" prepare --workspace "$WORK"
timeout 180 "$WORK/venv/bin/python" "$DIR/probe.py" run --workspace "$WORK" \
  --output "$WORK/baseline-reproduction.json"
timeout 120 "$WORK/venv/bin/python" "$DIR/followup.py" run --workspace "$WORK" \
  --output "$WORK/local-results.json"
```

To replay the exact six fresh inputs without another paid request, restore the
retained archive below into `$WORK`. Check its hash **before** extraction.

```bash
FRESH_ARCHIVE=/tmp/evener-scratch-034rn5pK6xRVceogYz0b3x/034s9evGkE96Q1Uazr0oaq/wake-followup-fresh.tar.gz
printf '%s  %s\n' 8c648284f8547f1da0c0e440db731d6e189a7ad7aa99caaadd4c9238c375806c "$FRESH_ARCHIVE" | sha256sum --check
tar -xzf "$FRESH_ARCHIVE" -C "$WORK"
timeout 120 "$WORK/venv/bin/python" "$DIR/followup.py" run --workspace "$WORK" \
  --fresh --threshold-zero --output "$WORK/final-results.json"
```

This final command ran twice on the same inputs with identical hashes, greedy
outputs and hit results. Timings vary. The retained JSON is the final replay,
not a claim that repeated inference creates independent samples. The external
archive is 738,476 bytes with exactly six raw PCM files, six WAVs and one manifest;
it contains no credentials. Its members were independently checked against the
manifest. It is a scratch evidence artifact, not a permanent repository asset.
If it is unavailable, exact fresh-input replay is unavailable; new TTS output is
a new experiment and may not share these hashes or outcomes.

For provenance, this was the **one paid generation command**, run before the
fresh-input measurements. Do not run it to perform an ordinary replay. It refuses
an existing `fresh` directory and does not retry requests.

```bash
(
  set -e
  KEY_COPY="$EVENER_SCRATCH_DIR/openai-env-copy"
  test ! -e "$KEY_COPY"
  (umask 077; cp /home/jesse/git/terminal-bench/.env "$KEY_COPY")
  trap 'rm -f "$KEY_COPY"' EXIT
  timeout 420 "$WORK/venv/bin/python" "$DIR/followup.py" synthesize \
    --workspace "$WORK" --credential-copy "$KEY_COPY"
)
```

### Observed verification and exit semantics

- `prepare`, original `probe.py run`, local/fresh/final diagnostic runs, and
  `--help` exited **0**. Python AST parsing succeeded. These are real inference
  executions, not printed test summaries. No Go, race, native build or heavy gate ran.
- Missing `--output`, an existing output path, and synthesis without an explicit
  credential-copy argument exited **2**, with argparse errors. Missing model
  assets exited **1**, with the missing encoder path. No silent fallback ran.
- A successful diagnostic run exits **0 even when every intended wake misses**.
  Calibration asserts come from the original probe. New phrase controls record
  observations rather than assert that an unqualified wake must work.
- A separate standard-library audit, importing neither script, recomputed WAV
  headers, sample counts, WAV/PCM hashes and nonzero RMS for all speech inputs.
  It also independently checked all five model/tokenizer hashes and four original
  wheel hashes against the immutable baseline. Fresh speech minimum RMS was
  **1395.24** PCM16 units. Counts were recomputed from per-clip observations:

```text
BASELINE: 10 positives / 0 hits; 42 negatives / 0 hits; all 57 WAV/PCM hashes match
FRESH: 6 raw PCM + WAV hashes verified; 2 positives/0 hits, 3 negatives/0 hits, LIGHT UP 1 hit
DIAGNOSTICS: 13 replayed rows, identical ASR and hit results, both local WAV hashes verified
THRESHOLD ZERO: 62 clips, 491 decode calls, 0 hits
CALIBRATION: alternate phrase 1, Flite LIGHT UP 0, fresh LIGHT UP 1, official single 1 / continuous 2
```

Repository artifact SHA-256:

```text
a9145a892f87254f0f8fe8456ecc8eb6195079bc9121ef8fe44bf175a6c84d02  followup.py
e843f6161baac67975baa2ed8b271e1117cc7e1ed6759ae9f870951bfd70d881  followup-results.json
```

## Native packaging and remaining gates

A **native packaging experiment has a documented source path**, not a proven
build here. At pinned commit `af2d8d81dbfa61c4cea4c568da78f3982edca005`,
[`build-ios.sh`](https://github.com/k2-fsa/sherpa-onnx/blob/af2d8d81dbfa61c4cea4c568da78f3982edca005/build-ios.sh)
builds a static C-API XCFramework for arm64 devices and arm64/x86_64 simulators,
with an iOS 13.0 deployment target. The same revision's
[C API](https://github.com/k2-fsa/sherpa-onnx/blob/af2d8d81dbfa61c4cea4c568da78f3982edca005/sherpa-onnx/c-api/c-api.h)
exposes keyword-spotter create/decode/result/reset and these decoder settings.
Those sources were read, not executed. No SDK was integrated or native package
built. This source-level feasibility does not make these weights a viable
HEY EVENER wake candidate on the evidence above.

A useful target-phrase detector, human positive/negative recordings, sustained
false accepts, physical playback/AEC and double-talk remain open. No iPhone was
used. Locked/background audio-session survival, interruptions, routes, permissions,
battery/thermal behavior and App Store/platform constraints remain device gates.
Digital playback labels are still not AEC evidence.

Licenses remain **unresolved for product distribution**. The baseline records
Apache-2.0 runtime/model metadata, not commercial clearance. GigaSpeech/source
media terms, native ONNX Runtime and bundled dependency notices, Flite/FFmpeg
provenance, the added pypinyin package, and generated TTS/service-use rights still
need the applicable review. Nothing in this diagnostic authorizes shipping them.
The next research choice is internal beam tracing versus representative human
phone inputs before further candidate/model evaluation; it is not deployment.

## Focused source evidence

- Pinned [BPE helper](https://github.com/k2-fsa/sherpa-onnx/blob/af2d8d81dbfa61c4cea4c568da78f3982edca005/sherpa-onnx/python/sherpa_onnx/utils.py),
  lines 107–108 and 160–178. Runtime-source fingerprints are in the result JSON.
  The extra `pypinyin==0.55.0` wheel SHA-256 is
  `d53b1e8ad2cdb815fb2cb604ed3123372f5a28c6f447571244aca36fc62a286f`.
- Pinned [keyword decoder](https://github.com/k2-fsa/sherpa-onnx/blob/af2d8d81dbfa61c4cea4c568da78f3982edca005/sherpa-onnx/csrc/transducer-keyword-decoder.cc),
  source SHA-256 `872a9e074e626ce66914a4a1bb48948790851c281907340ea319d9817f888edb`;
  [context graph](https://github.com/k2-fsa/sherpa-onnx/blob/af2d8d81dbfa61c4cea4c568da78f3982edca005/sherpa-onnx/csrc/context-graph.h)
  preserves the global threshold. Pinned wheel bytes are verified, not independently
  rebuilt/attested against this native source.
- OpenAI [speech API guide](https://developers.openai.com/api/docs/guides/text-to-speech)
  and [model snapshots](https://developers.openai.com/api/docs/models/gpt-4o-mini-tts)
  verified endpoint, PCM format, voice and snapshot before calls. The older
  `platform.openai.com/docs/api-reference/audio/createSpeech` URL returned 403;
  no API inference was made from that failed fetch.
