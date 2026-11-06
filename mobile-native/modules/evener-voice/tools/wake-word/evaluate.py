#!/usr/bin/env python3
"""Evaluate real openWakeWord.Model streaming inference, with no network calls."""
import argparse
from concurrent.futures import ProcessPoolExecutor
import json
import math
from pathlib import Path
import time

import numpy as np
from openwakeword.model import Model
import soundfile as sf

from prepare import ASSETS, HOP, RATE, SEED, digest, write_json

MODEL = None
ROOT = None


def detector(assets):
    np.random.seed(SEED)
    return Model(wakeword_models=[str(assets / "hey_evener.onnx")],
                 melspec_model_path=str(assets / "melspectrogram.onnx"),
                 embedding_model_path=str(assets / "embedding_model.onnx"),
                 inference_framework="onnx", ncpu=1)


def predict(model, audio):
    np.random.seed(SEED)
    model.reset()
    warmup = [float(model.predict(np.zeros(HOP, dtype=np.int16))["hey_evener"]) for _ in range(25)]
    scores = []
    for start in range(0, len(audio), HOP):
        frame = audio[start:start + HOP]
        if len(frame) < HOP:
            frame = np.pad(frame, (0, HOP - len(frame)))
        scores.append(float(model.predict(frame)["hey_evener"]))
    if not np.isfinite(scores).all() or not np.isfinite(warmup).all():
        raise ValueError("Detector produced nonfinite scores")
    return np.asarray(scores, dtype=np.float32), max(warmup)


def init_worker(work, assets):
    global ROOT, MODEL
    ROOT = Path(work)
    MODEL = detector(Path(assets))


def score_clip(record):
    path = ROOT / record["path"]
    if digest(path) != record["sha256"]:
        raise ValueError(f"Changed corpus audio: {path}")
    audio, rate = sf.read(path, dtype="int16")
    if rate != RATE or audio.ndim != 1 or len(audio) != record["samples"]:
        raise ValueError(f"Expected mono 16 kHz PCM16: {path}")
    scores, warmup_max = predict(MODEL, audio)
    np.save(ROOT / "scores" / (record["id"] + ".npy"), scores, allow_pickle=False)
    result = {key: record[key] for key in ("id", "positive", "source", "samples")}
    result.update(peak=float(scores.max()), warmup_peak=warmup_max)
    return result


def summarize(work, results, threshold):
    groups = {}
    for result in results:
        name = result["source"] + ("/positive" if result["positive"] else "/negative")
        group = groups.setdefault(name, {"clips": 0, "samples": 0, "detected_clips": 0,
                                        "activation_events": 0, "peak": 0.0, "minimum_clip_peak": 1.0})
        group["clips"] += 1
        group["samples"] += result["samples"]
        scores = np.load(work / "scores" / (result["id"] + ".npy"))
        active = scores >= threshold
        group["detected_clips"] += int(np.any(active))
        group["activation_events"] += int(np.count_nonzero(active & ~np.r_[False, active[:-1]]))
        group["peak"] = max(group["peak"], result["peak"])
        group["minimum_clip_peak"] = min(group["minimum_clip_peak"], result["peak"])
    for group in groups.values():
        group["hours"] = group["samples"] / RATE / 3600
        group["detected_fraction"] = group["detected_clips"] / group["clips"]
        group["events_per_hour"] = group["activation_events"] / group["hours"]
    positive = [result for result in results if result["positive"]]
    negative = [result for result in results if not result["positive"]]
    negative_hours = sum(result["samples"] for result in negative) / RATE / 3600
    false_events = sum(group["activation_events"] for key, group in groups.items() if key.endswith("/negative"))
    return {"threshold": threshold, "groups": groups,
        "positive_clips": len(positive), "detected_positive_clips": sum(result["peak"] >= threshold for result in positive),
        "negative_clips": len(negative), "negative_hours": negative_hours,
        "false_accept_events": false_events, "false_accepts_per_hour": false_events / negative_hours,
        "warmup_peak": max(result["warmup_peak"] for result in results)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--assets", type=Path, default=ASSETS)
    parser.add_argument("--split", choices=("calibration", "test"), required=True)
    parser.add_argument("--calibrate", action="store_true")
    parser.add_argument("--workers", type=int, default=4)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.calibrate and args.split != "calibration":
        parser.error("Threshold selection may only use the calibration split")
    hashes = {name: digest(args.assets / name) for name in (
        "hey_evener.onnx", "melspectrogram.onnx", "embedding_model.onnx")}
    manifest_path = args.assets / "manifest.json"
    if not args.calibrate:
        manifest = json.loads(manifest_path.read_text())
        if manifest["models"] != hashes:
            raise ValueError("Models differ from the calibrated manifest")
        threshold = manifest["threshold"]
    started = time.monotonic()
    records = [json.loads(line) for line in (args.work / "corpus.jsonl").read_text().splitlines()]
    records = [record for record in records if record["split"] == args.split]
    if not any(record["positive"] for record in records) or not any(not record["positive"] for record in records):
        raise ValueError("Evaluation requires both positive and negative examples")
    (args.work / "scores").mkdir(exist_ok=True)
    with ProcessPoolExecutor(args.workers, initializer=init_worker,
                             initargs=(str(args.work), str(args.assets))) as pool:
        results = []
        for i, result in enumerate(pool.map(score_clip, records, chunksize=8)):
            results.append(result)
            if (i + 1) % 500 == 0:
                print(f"evaluated {i + 1}/{len(records)}", flush=True)
    if args.calibrate:
        maximum = max(result["peak"] for result in results if not result["positive"])
        threshold = max(0.5, math.ceil((maximum + 0.01) * 100) / 100)
        if threshold >= 1:
            raise ValueError("Calibration negatives leave no threshold margin, improve training")
        phrase = json.loads((args.work / "corpus-summary.json").read_text())["phrase"]
        write_json(manifest_path, {"phrase": phrase, "threshold": threshold,
            "sample_rate": RATE, "hop_samples": HOP, "feature_frames": 16,
            "embedding_dimensions": 96, "warmup_hops": 25, "comparison": ">=",
            "patience": 1, "vad": False, "noise_suppression": False,
            "models": hashes, "seed": SEED,
            "calibration_rule": "max(0.50, ceil((maximum calibration negative score + 0.01) * 100) / 100)",
            "calibration_corpus_sha256": digest(args.work / "corpus.jsonl")})
    report = summarize(args.work, results, threshold)
    report.update(split=args.split, models=hashes, corpus_sha256=digest(args.work / "corpus.jsonl"),
                  seconds=time.monotonic() - started)
    report["threshold_sweep"] = []
    for candidate in sorted(set((0.25, 0.5, 0.75, 0.9, 0.95, threshold))):
        summary = summarize(args.work, results, candidate)
        report["threshold_sweep"].append({key: summary[key] for key in (
            "threshold", "detected_positive_clips", "positive_clips", "false_accept_events", "false_accepts_per_hour")})
    args.output.parent.mkdir(parents=True, exist_ok=True)
    write_json(args.output, report)
    write_json(args.work / f"{args.split}-clip-scores.json", results)
    print(json.dumps(report, indent=2), flush=True)
    # This is the offline lane gate, not the physical-device qualification gate.
    if report["detected_positive_clips"] / report["positive_clips"] < 0.95 or report["false_accept_events"] or report["warmup_peak"] >= threshold:
        raise SystemExit("Offline gate failed: need >=95% positives, zero negatives, no warmup trigger")


if __name__ == "__main__":
    main()
