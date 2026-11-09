#!/usr/bin/env python3
"""Download hash-locked inputs and compact the frozen openWakeWord frontend."""
import argparse
import hashlib
import json
from pathlib import Path
import tarfile
import urllib.request

import numpy as np
import onnx
from onnx import TensorProto, helper, numpy_helper

HERE = Path(__file__).resolve().parent
ASSETS = HERE.parents[1] / "resources" / "wake-word"
SEED = 20261106
RATE = 16000
HOP = 1280


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def write_json(path, value):
    Path(path).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def compact(source, destination):
    """Pack matrix/convolution weights, preserving FP32 scalars and operator API."""
    model = onnx.load(source)
    casts = []
    for tensor in model.graph.initializer:
        # The mel log floor is 1e-10, which would become zero in FP16. Keep
        # scalars, biases and normalization vectors in their original precision.
        if tensor.data_type == TensorProto.FLOAT and len(tensor.dims) >= 2:
            name = tensor.name
            packed = numpy_helper.from_array(
                numpy_helper.to_array(tensor).astype(np.float16), name + "__fp16"
            )
            tensor.CopyFrom(packed)
            casts.append(helper.make_node("Cast", [tensor.name], [name], to=TensorProto.FLOAT))
    original = list(model.graph.node)
    del model.graph.node[:]
    model.graph.node.extend(casts + original)
    onnx.checker.check_model(model)
    onnx.save(model, destination)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--assets", type=Path, default=ASSETS)
    args = parser.parse_args()
    downloads = args.work / "downloads"
    downloads.mkdir(parents=True, exist_ok=True)
    args.assets.mkdir(parents=True, exist_ok=True)
    sources = json.loads((HERE / "sources.json").read_text())
    for name, source in sources.items():
        target = downloads / name
        if not target.exists():
            temporary = target.with_suffix(target.suffix + ".partial")
            print("download", name, flush=True)
            urllib.request.urlretrieve(source["url"], temporary)
            if digest(temporary) != source["sha256"]:
                raise ValueError(f"Checksum mismatch: {temporary}")
            temporary.rename(target)
        if digest(target) != source["sha256"]:
            raise ValueError(f"Checksum mismatch: {target}")
    for name in ("melspectrogram.onnx", "embedding_model.onnx"):
        compact(downloads / name, args.assets / name)
        print(name, (args.assets / name).stat().st_size, digest(args.assets / name))
    # The pinned archive's data filter also rejects paths/links outside the destination.
    with tarfile.open(downloads / "test-clean.tar.gz") as archive:
        archive.extractall(args.work / "librispeech", filter="data")


if __name__ == "__main__":
    main()
