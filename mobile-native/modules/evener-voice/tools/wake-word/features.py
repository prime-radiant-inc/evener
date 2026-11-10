#!/usr/bin/env python3
"""Cache the real streaming frontend, one 1,280-sample call at a time."""
import argparse
from concurrent.futures import ProcessPoolExecutor
import json
from pathlib import Path
import time

import numpy as np
from openwakeword.utils import AudioFeatures
import soundfile as sf

from prepare import ASSETS, HOP, RATE, SEED, digest, write_json

FRONTEND = None
ROOT = None


def frontend(assets):
    np.random.seed(SEED)
    return AudioFeatures(melspec_model_path=str(assets / "melspectrogram.onnx"),
                         embedding_model_path=str(assets / "embedding_model.onnx"),
                         inference_framework="onnx", ncpu=1)


def stream_embeddings(processor, audio):
    np.random.seed(SEED)
    processor.reset()
    # Replace openWakeWord's randomized initial history with real silent frames.
    for _ in range(25):
        processor(np.zeros(HOP, dtype=np.int16))
    embeddings = [row.copy() for row in processor.feature_buffer[-15:]]
    for start in range(0, len(audio), HOP):
        frame = audio[start:start + HOP]
        if len(frame) < HOP:
            frame = np.pad(frame, (0, HOP - len(frame)))
        processor(frame)
        embeddings.append(processor.feature_buffer[-1].copy())
    return np.asarray(embeddings, dtype=np.float32)


def windows(embeddings):
    return np.lib.stride_tricks.sliding_window_view(embeddings, 16, axis=0).transpose(0, 2, 1)


def init_worker(work, assets):
    global FRONTEND, ROOT
    ROOT = Path(work)
    FRONTEND = frontend(Path(assets))


def extract(record):
    path = ROOT / record["path"]
    if digest(path) != record["sha256"]:
        raise ValueError(f"Changed corpus audio: {path}")
    audio, rate = sf.read(path, dtype="int16")
    if rate != RATE or audio.ndim != 1 or len(audio) != record["samples"]:
        raise ValueError(f"Unexpected audio format: {path}")
    embedding = stream_embeddings(FRONTEND, audio)
    if not np.isfinite(embedding).all():
        raise ValueError(f"Frontend produced nonfinite features: {path}")
    np.save(ROOT / "features" / (record["id"] + ".npy"), embedding, allow_pickle=False)
    return record["id"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--assets", type=Path, default=ASSETS)
    parser.add_argument("--workers", type=int, default=4)
    args = parser.parse_args()
    started = time.monotonic()
    (args.work / "features").mkdir(parents=True, exist_ok=True)
    records = [json.loads(line) for line in (args.work / "corpus.jsonl").read_text().splitlines()]
    with ProcessPoolExecutor(args.workers, initializer=init_worker,
                             initargs=(str(args.work), str(args.assets))) as pool:
        for i, _ in enumerate(pool.map(extract, records, chunksize=8)):
            if (i + 1) % 500 == 0:
                print(f"streamed {i + 1}/{len(records)} clips", flush=True)
    write_json(args.work / "features.json", {
        "corpus_sha256": digest(args.work / "corpus.jsonl"),
        "models": {name: digest(args.assets / name) for name in ("melspectrogram.onnx", "embedding_model.onnx")},
        "seconds": time.monotonic() - started, "clips": len(records), "hop_samples": HOP,
    })
    print("feature_seconds", time.monotonic() - started, flush=True)


if __name__ == "__main__":
    main()
