# Wake-word model notices

`hey_evener.onnx` is a first-party classifier trained by this repository's
`tools/wake-word/` pipeline. It is **not** an openWakeWord pretrained phrase
classifier. This module does not introduce a new license grant for Evener code
or its classifier. Training-data attribution is retained below.

`melspectrogram.onnx` and `embedding_model.onnx` are modified openWakeWord
v0.5.1 release assets. Copyright 2022 David Scripka. The speech embedding
backbone derives from Google's Apache-2.0 `speech_embedding/1` model.
The upstream code and shared feature extractor are Apache-2.0; see
`Apache-2.0.txt`. Their source URLs and original SHA-256 hashes are recorded in
`../../tools/wake-word/sources.json`.

Modification: matrix/convolution weights are stored as FP16 and cast back to
FP32 before the original operators. Scalars and vectors retain FP32 precision.
Input/output tensor contracts are unchanged,
but weights are rounded and these are **not byte-identical upstream files**.
The trained classifier and evaluation use these exact modified assets.

No openWakeWord CC BY-NC-SA pretrained phrase classifier is included or used.

## Training sources (downloaded, not bundled)

- Piper `en_US-libritts_r-medium`, 904 speakers, from Michael Hansen / rhasspy's
  piper-voices repository, revision
  `e7229f7d33a10e145e109b886d661984201ac3c4`.
  Its model card declares LibriTTS-R, CC BY 4.0:
  <https://www.openslr.org/141/>. See the copied `Piper-MODEL_CARD.txt`.
  LibriTTS-R: Yuma Koizumi, Heiga Zen, Shigeki Karita, Yifan Ding, Kohei Yatabe,
  Nobuyuki Morioka, Michiel Bacchiani, Yu Zhang, Wei Han and Ankur Bapna.
  The card says this voice was fine-tuned from
  English lessac medium on train-clean-360. Audio here is newly synthesized,
  resampled, gain-adjusted and sometimes mixed with synthetic noise/echo.
- LibriSpeech test-clean, CC BY 4.0, Vassil Panayotov, Guoguo Chen, Daniel Povey
  and Sanjeev Khudanpur, <https://www.openslr.org/12/>. Original 16 kHz recordings
  and transcripts are used as negative speech, partitioned by speaker. The
  archive's LICENSE.TXT remains in the regenerated work directory.
- Confuser text and procedural noise were authored/generated for this trainer.
  Procedural noise is not represented as real environmental audio.
- CC BY 4.0: <https://creativecommons.org/licenses/by/4.0/>.

Piper 1.3.0 is GPL-3.0 and is a training-only executable dependency. Its code
and voice model are not shipped to the phone. openWakeWord 0.6.0 is Apache-2.0;
ONNX Runtime 1.20.1 is MIT. See the trainer's full package version lock.

These notices do not establish acoustic qualification or speaker identity.
