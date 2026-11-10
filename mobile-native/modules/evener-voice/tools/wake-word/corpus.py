#!/usr/bin/env python3
"""Build disjoint-speaker Piper positives, confusers, real speech and noise."""
import argparse
from concurrent.futures import ProcessPoolExecutor
import json
from pathlib import Path
import time

import numpy as np
import onnxruntime as ort
from piper import PiperVoice, SynthesisConfig
from piper.config import PiperConfig
from scipy.signal import lfilter, resample_poly
import soundfile as sf

from prepare import RATE, SEED, digest, write_json

# Authored here, not scraped from a restricted speech corpus. Near-misses are
# deliberately shared across splits; voices and rendered waveforms are not.
CONFUSERS = [
    "Hey Evan.", "Hey Eva.", "Hey everyone.", "Hey Evelyn.", "Hey Steven.",
    "Hey Kevin.", "Hey Eleanor.", "Hey ever.", "Hey never.", "Hey seven.",
    "Hey evening.", "Hey there.", "Hey you.", "Hey Siri.", "Hey Alexa.",
    "Hey Google.", "Hey computer.", "Hey engineer.", "Hey editor.",
    "Evener.", "Even her.", "Even now.", "Good evening.", "Hey.",
    "Have you ever been there?", "We can leave her alone.",
    "The weather is even better today.", "Every evening we take a walk.",
    "I haven't even heard the news.", "Please open the other window.",
    "Set a timer for seven minutes.", "Stop talking and listen.",
    "What is the next item on the list?", "The package arrived this morning.",
    "Please turn down the television.", "I need to finish the report.",
    "Can you send that message tomorrow?", "I will call you after dinner.",
    "Read the latest message.", "We should check the test results.",
    "Heaven and earth.", "An even number.", "Hey Oliver.", "Hey Emerson.",
    "The river was flowing very fast.", "There is nothing else to do.",
    "Let me know when the job is complete.", "A little earlier than expected.",
]
VOICE = None
ROOT = None
PHRASE = None


def init_worker(work, phrase):
    global VOICE, ROOT, PHRASE
    ROOT, PHRASE = Path(work), phrase
    path = ROOT / "downloads" / "en_US-libritts_r-medium.onnx"
    options = ort.SessionOptions()
    options.intra_op_num_threads = 1
    options.inter_op_num_threads = 1
    VOICE = PiperVoice(
        session=ort.InferenceSession(str(path), options, providers=["CPUExecutionProvider"]),
        config=PiperConfig.from_dict(json.loads(Path(str(path) + ".json").read_text())),
    )


def render(text, speaker, speed):
    config = SynthesisConfig(speaker_id=speaker, length_scale=speed,
                             noise_scale=0.0, noise_w_scale=0.0)
    chunks = [chunk.audio_float_array for chunk in VOICE.synthesize(text, config)]
    audio = resample_poly(np.concatenate(chunks), 320, 441).astype(np.float32)
    # Remove TTS boundary silence so the positive-window labels mark the phrase,
    # not Piper's padding. No clip is discarded based on detector scores.
    active = np.flatnonzero(np.abs(audio) > np.max(np.abs(audio)) * 0.015)
    return audio[max(0, active[0] - 160):min(len(audio), active[-1] + 321)]


def augment(audio, rng, variant):
    prefix = int(rng.uniform(0.8, 1.6) * RATE)
    out = np.pad(audio, (prefix, int(0.96 * RATE)))
    end = prefix + len(audio)
    out *= rng.uniform(0.2, 0.85)
    if variant % 2:
        delay = int(rng.uniform(0.02, 0.12) * RATE)
        out[delay:] += rng.uniform(0.05, 0.25) * out[:-delay].copy()
        noise = lfilter([1], [1, -0.85], rng.normal(size=len(out)))
        signal_rms = np.sqrt(np.mean(out[prefix:end] ** 2))
        noise *= signal_rms / (10 ** (rng.uniform(15, 30) / 20) * np.std(noise))
        out += noise
    return np.round(np.clip(out, -1, 1) * 32767).astype(np.int16), end


def save_clip(audio, identity, split, source, positive, **fields):
    path = ROOT / "audio" / split / (identity + ".wav")
    sf.write(path, audio, RATE, subtype="PCM_16")
    return dict(id=identity, path=str(path.relative_to(ROOT)), split=split,
                source=source, positive=positive, samples=len(audio),
                sha256=digest(path), **fields)


def speaker_clips(task):
    speaker, split = task
    rng = np.random.default_rng(SEED + speaker)
    records = []
    for speed_index, speed in enumerate((0.85, 1.0, 1.15)):
        raw = render(PHRASE, speaker, speed)
        for variant in range(2):
            index = 2 * speed_index + variant
            audio, end = augment(raw, rng, variant)
            records.append(save_clip(audio, f"piper-{speaker:03}-positive-{index}",
                split, "piper", True, speaker=speaker, text=PHRASE,
                phrase_end_sample=end, length_scale=speed, variant=variant))
    for index, text_index in enumerate(rng.choice(len(CONFUSERS), 8, replace=False)):
        text = CONFUSERS[text_index]
        speed = float(rng.uniform(0.85, 1.15))
        audio, _ = augment(render(text, speaker, speed), rng, index)
        records.append(save_clip(audio, f"piper-{speaker:03}-negative-{index}",
            split, "piper", False, speaker=speaker, text=text, length_scale=speed))
    return records


def procedural_noise(rng, index):
    n = RATE * 60
    t = np.arange(n) / RATE
    noise = rng.normal(size=n)
    kind = index % 5
    if kind == 0:
        noise *= 0.01  # white
    elif kind == 1:
        noise = lfilter([1], [1, -0.98], noise) * 0.008  # colored
    elif kind == 2:
        noise = np.sin(2 * np.pi * 60 * t) * 0.05 + noise * 0.002  # hum
    elif kind == 3:
        noise = np.sin(2 * np.pi * (220 * t + 8 * t * t)) * 0.03  # swept tone
    else:
        noise *= 0  # silence is counted explicitly, not called ambient audio
    return np.round(np.clip(noise, -1, 1) * 32767).astype(np.int16)


def main():
    global ROOT
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--phrase", default="Hey Evener.")
    parser.add_argument("--workers", type=int, default=4)
    args = parser.parse_args()
    ROOT = args.work
    started = time.monotonic()
    for split in ("train", "calibration", "test"):
        (ROOT / "audio" / split).mkdir(parents=True, exist_ok=True)
    rng = np.random.default_rng(SEED)
    speakers = rng.permutation(904)
    split_ids = {"train": speakers[:704], "calibration": speakers[704:804], "test": speakers[804:]}
    tasks = [(int(speaker), split) for split, ids in split_ids.items() for speaker in ids]
    records = []
    with ProcessPoolExecutor(args.workers, initializer=init_worker,
                             initargs=(str(ROOT), args.phrase)) as pool:
        for i, result in enumerate(pool.map(speaker_clips, tasks)):
            records.extend(result)
            if (i + 1) % 50 == 0:
                print(f"Piper speakers {i + 1}/904", flush=True)
    speech_root = ROOT / "librispeech" / "LibriSpeech" / "test-clean"
    speech_speakers = sorted(path.name for path in speech_root.iterdir() if path.is_dir())
    if len(speech_speakers) != 40:
        raise ValueError("Expected the complete 40-speaker LibriSpeech test-clean archive")
    speech_speakers = list(rng.permutation(speech_speakers))
    for i, speaker in enumerate(speech_speakers):
        split = "train" if i < 24 else "calibration" if i < 32 else "test"
        for transcript_path in sorted((speech_root / speaker).glob("*/*.trans.txt")):
            for line in transcript_path.read_text().splitlines():
                identity, text = line.split(" ", 1)
                if "evener" in text.lower():
                    raise ValueError(f"Review potential wake phrase in {identity}")
                path = transcript_path.parent / (identity + ".flac")
                info = sf.info(path)
                if info.samplerate != RATE or info.channels != 1:
                    raise ValueError(f"Unexpected speech format: {path}")
                records.append(dict(id="librispeech-" + identity, path=str(path.relative_to(ROOT)),
                    split=split, source="librispeech", positive=False,
                    samples=info.frames, sha256=digest(path), speaker=speaker, text=text))
    for split, count in (("train", 30), ("calibration", 10), ("test", 20)):
        for i in range(count):
            records.append(save_clip(procedural_noise(rng, i), f"noise-{split}-{i:02}",
                split, "procedural", False, kind=("white", "colored", "hum", "sweep", "silence")[i % 5]))
    records.sort(key=lambda record: record["id"])
    manifest = ROOT / "corpus.jsonl"
    manifest.write_text("".join(json.dumps(record, sort_keys=True) + "\n" for record in records))
    summary = {"seed": SEED, "phrase": args.phrase, "manifest_sha256": digest(manifest),
               "piper_speaker_ids": {split: [int(x) for x in ids] for split, ids in split_ids.items()},
               "groups": {}, "seconds": time.monotonic() - started}
    for record in records:
        key = f"{record['split']}/{record['source']}/{'positive' if record['positive'] else 'negative'}"
        group = summary["groups"].setdefault(key, {"clips": 0, "samples": 0})
        group["clips"] += 1
        group["samples"] += record["samples"]
    for group in summary["groups"].values():
        group["hours"] = group["samples"] / RATE / 3600
    write_json(ROOT / "corpus-summary.json", summary)
    print(json.dumps(summary["groups"], indent=2), flush=True)
    print("corpus_seconds", summary["seconds"], flush=True)


if __name__ == "__main__":
    main()
