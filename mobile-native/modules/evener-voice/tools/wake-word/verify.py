#!/usr/bin/env python3
"""Check asset contracts, data isolation and upstream streaming parity."""
import argparse
import json
from pathlib import Path
import sys
import unittest

import numpy as np
import onnx
from openwakeword.utils import AudioFeatures
import soundfile as sf

from corpus import init_worker, render
from evaluate import detector, predict
from features import windows
from prepare import ASSETS, HOP, RATE, SEED, digest, write_json
from train import session


class WakeWordChecks(unittest.TestCase):
    work = None
    assets = None
    evidence = {}

    @classmethod
    def setUpClass(cls):
        cls.manifest = json.loads((cls.assets / "manifest.json").read_text())
        cls.records = [json.loads(line) for line in (cls.work / "corpus.jsonl").read_text().splitlines()]

    def test_model_hashes_and_tensor_contract(self):
        for name, sha in self.manifest["models"].items():
            path = self.assets / name
            self.assertEqual(digest(path), sha)
            self.assertLessEqual(path.stat().st_size, 1048576)
            onnx.checker.check_model(onnx.load(path))
        classifier = session(self.assets / "hey_evener.onnx")
        self.assertEqual(classifier.get_inputs()[0].shape, [None, 16, 96])
        self.assertEqual(classifier.get_outputs()[0].shape, [None, 1])
        score = classifier.run(None, {"embeddings": np.zeros((3, 16, 96), np.float32)})[0]
        self.assertEqual(score.shape, (3, 1))
        self.assertTrue(np.isfinite(score).all())
        self.assertTrue(((score >= 0) & (score <= 1)).all())

    def test_speakers_and_positive_audio_are_disjoint(self):
        for source in ("piper", "librispeech"):
            speakers = {split: {str(record["speaker"]) for record in self.records
                        if record["source"] == source and record["split"] == split}
                        for split in ("train", "calibration", "test")}
            for left, right in (("train", "calibration"), ("train", "test"), ("calibration", "test")):
                self.assertTrue(speakers[left])
                self.assertFalse(speakers[left] & speakers[right])
        voice = json.loads((self.work / "downloads" / "en_US-libritts_r-medium.onnx.json").read_text())
        real_speakers = {record["speaker"] for record in self.records if record["source"] == "librispeech"}
        self.assertFalse(set(voice["speaker_id_map"]) & real_speakers)
        hashes = [record["sha256"] for record in self.records if record["positive"]]
        self.assertEqual(len(hashes), 5424)
        self.assertEqual(len(hashes), len(set(hashes)))

    def test_piper_is_deterministic(self):
        init_worker(str(self.work), "Hey Evener.")
        first = render("Hey Evener.", 0, 1.0)
        second = render("Hey Evener.", 0, 1.0)
        self.assertGreater(len(first), RATE // 4)
        np.testing.assert_array_equal(first, second)

    def test_compact_frontend_stays_close_to_original(self):
        np.random.seed(SEED)
        original = AudioFeatures(melspec_model_path=str(self.work / "downloads" / "melspectrogram.onnx"),
            embedding_model_path=str(self.work / "downloads" / "embedding_model.onnx"),
            inference_framework="onnx", ncpu=1)
        np.random.seed(SEED)
        compact = AudioFeatures(melspec_model_path=str(self.assets / "melspectrogram.onnx"),
            embedding_model_path=str(self.assets / "embedding_model.onnx"),
            inference_framework="onnx", ncpu=1)
        # Compare independent input signals: real speech, a confuser and target.
        probes = [next(record for record in self.records if record["source"] == source
                       and record["positive"] == positive)
                  for source, positive in (("librispeech", False), ("piper", False), ("piper", True))]
        errors = []
        for record in probes:
            audio, _ = sf.read(self.work / record["path"], dtype="int16")
            left, right = original._get_embeddings(audio), compact._get_embeddings(audio)
            relative_error = float(np.linalg.norm(left - right) / np.linalg.norm(left))
            self.assertLess(relative_error, 0.01)
            errors.append(relative_error)
        self.evidence["frontend_relative_l2_errors"] = errors

    def test_cached_training_features_match_independent_model_predict(self):
        runtime = session(self.assets / "hey_evener.onnx")
        live = detector(self.assets)
        probes = [next(record for record in self.records if record["split"] == "test"
                       and record["source"] == source and record["positive"] == positive)
                  for source, positive in (("librispeech", False), ("piper", False),
                                            ("piper", True), ("procedural", False))]
        errors = []
        for record in probes:
            audio, _ = sf.read(self.work / record["path"], dtype="int16")
            independent, warmup = predict(live, audio)
            self.assertLess(warmup, self.manifest["threshold"])
            features = np.load(self.work / "features" / (record["id"] + ".npy"))
            cached = runtime.run(None, {"embeddings": windows(features)})[0].ravel()
            self.assertEqual(len(independent), (len(audio) + HOP - 1) // HOP)
            np.testing.assert_allclose(independent, cached, rtol=0, atol=0.00001)
            errors.append(float(np.max(np.abs(independent - cached))))
        self.evidence["streaming_parity_max_absolute_errors"] = errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--assets", type=Path, default=ASSETS)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    WakeWordChecks.work, WakeWordChecks.assets = args.work, args.assets
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(WakeWordChecks)
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    write_json(args.output, {"tests_run": result.testsRun, "failures": len(result.failures),
        "errors": len(result.errors), **WakeWordChecks.evidence})
    sys.exit(not result.wasSuccessful())


if __name__ == "__main__":
    main()
