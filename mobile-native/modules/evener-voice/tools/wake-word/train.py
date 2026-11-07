#!/usr/bin/env python3
"""Train a small openWakeWord classifier on frozen streaming embeddings."""
import argparse
import json
from pathlib import Path
import time

import numpy as np
import onnx
from onnx import TensorProto, helper, numpy_helper
import onnxruntime as ort
from sklearn.neural_network import MLPClassifier
from sklearn.preprocessing import StandardScaler
from threadpoolctl import threadpool_limits

from features import windows
from prepare import ASSETS, HOP, SEED, digest, write_json


def session(path):
    options = ort.SessionOptions()
    options.intra_op_num_threads = 1
    options.inter_op_num_threads = 1
    return ort.InferenceSession(str(path), options, providers=["CPUExecutionProvider"])


def export(model, scaler, path):
    nodes = [helper.make_node("Flatten", ["embeddings"], ["flat"], axis=1)]
    tensors = []
    value = "flat"
    for i, (weights, bias) in enumerate(zip(model.coefs_, model.intercepts_)):
        if i == 0:
            weights = weights / scaler.scale_[:, None]
            bias = bias - scaler.mean_ @ weights
        tensors.extend([numpy_helper.from_array(weights.astype(np.float32), f"w{i}"),
                        numpy_helper.from_array(bias.astype(np.float32), f"b{i}")])
        nodes.append(helper.make_node("Gemm", [value, f"w{i}", f"b{i}"], [f"linear{i}"]))
        value = "score" if i == len(model.coefs_) - 1 else f"hidden{i}"
        nodes.append(helper.make_node("Sigmoid" if value == "score" else "Relu", [f"linear{i}"], [value]))
    graph = helper.make_graph(nodes, "hey_evener", [helper.make_tensor_value_info(
        "embeddings", TensorProto.FLOAT, [None, 16, 96])],
        [helper.make_tensor_value_info("score", TensorProto.FLOAT, [None, 1])], tensors)
    output = helper.make_model(graph, opset_imports=[helper.make_opsetid("", 13)],
                               producer_name="Evener wake-word trainer", ir_version=8)
    onnx.checker.check_model(output)
    onnx.save(output, path)


def check_features(work, assets):
    metadata = json.loads((work / "features.json").read_text())
    if metadata["corpus_sha256"] != digest(work / "corpus.jsonl"):
        raise ValueError("Features do not belong to this corpus")
    for name, expected in metadata["models"].items():
        if digest(assets / name) != expected:
            raise ValueError(f"Features were made with a different frontend: {name}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--assets", type=Path, default=ASSETS)
    args = parser.parse_args()
    check_features(args.work, args.assets)
    started = time.monotonic()
    records = [json.loads(line) for line in (args.work / "corpus.jsonl").read_text().splitlines()]
    train = [record for record in records if record["split"] == "train"]
    positives, negatives = [], []
    for record in train:
        frames = windows(np.load(args.work / "features" / (record["id"] + ".npy")))
        if record["positive"]:
            end_times = (np.arange(len(frames)) + 1) * HOP
            end = record["phrase_end_sample"]
            selected = frames[(end_times >= end) & (end_times <= end + 5 * HOP)]
            if not len(selected):
                raise ValueError(f"Positive has no labelled end windows: {record['id']}")
            positives.append(selected.reshape(-1, 1536))
        else:
            negatives.append(frames[::4].reshape(-1, 1536))
    x_positive, x_negative = np.concatenate(positives), np.concatenate(negatives)
    x = np.concatenate([x_positive, x_negative])
    y = np.concatenate([np.ones(len(x_positive)), np.zeros(len(x_negative))])
    print("training_windows", len(x_positive), len(x_negative), flush=True)
    scaler = StandardScaler()
    x_scaled = scaler.fit_transform(x).astype(np.float32)
    model = MLPClassifier(hidden_layer_sizes=(64, 32), activation="relu", solver="adam",
        batch_size=512, learning_rate_init=0.001, alpha=0.01, max_iter=40,
        random_state=SEED, shuffle=True, tol=0.0001, n_iter_no_change=10, verbose=True)
    with threadpool_limits(limits=2):
        model.fit(x_scaled, y)
    initial_epochs = model.n_iter_
    args.assets.mkdir(parents=True, exist_ok=True)
    path = args.assets / "hey_evener.onnx"
    export(model, scaler, path)
    runtime = session(path)
    # Mine only training negatives at every hop, never calibration/test examples.
    hard = []
    for record in train:
        if record["positive"]:
            continue
        frames = windows(np.load(args.work / "features" / (record["id"] + ".npy")))
        score = runtime.run(None, {"embeddings": frames})[0].ravel()
        selected = frames[score >= 0.05]
        if len(selected):
            hard.append(selected.reshape(-1, 1536))
    hard_count = sum(len(batch) for batch in hard)
    if hard_count:
        hard_scaled = scaler.transform(np.concatenate(hard)).astype(np.float32)
        mined_x = np.concatenate([x_scaled, hard_scaled, hard_scaled])
        mined_y = np.concatenate([y, np.zeros(hard_count * 2)])
        with threadpool_limits(limits=2):
            for _ in range(10):
                model.partial_fit(mined_x, mined_y)
        export(model, scaler, path)
    runtime = session(path)
    rng = np.random.default_rng(SEED)
    probe = x[rng.choice(len(x), 512, replace=False)]
    with threadpool_limits(limits=2):
        expected = model.predict_proba(scaler.transform(probe))[:, 1]
    actual = runtime.run(None, {"embeddings": probe.reshape(-1, 16, 96)})[0].ravel()
    max_error = float(np.max(np.abs(expected - actual)))
    if max_error > 0.0001:
        raise ValueError(f"ONNX export differs from trained model: {max_error}")
    report = {"seed": SEED, "train_positive_windows": len(x_positive),
        "train_negative_windows": len(x_negative), "hard_negative_windows": hard_count,
        "hidden_layers": [64, 32], "initial_epochs": initial_epochs,
        "hard_negative_epochs": 10 if hard_count else 0, "loss": float(model.loss_),
        "export_max_absolute_error": max_error, "model_sha256": digest(path),
        "seconds": time.monotonic() - started}
    write_json(args.work / "training.json", report)
    print(json.dumps(report, indent=2), flush=True)


if __name__ == "__main__":
    main()
