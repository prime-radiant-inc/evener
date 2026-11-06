#!/usr/bin/env python3
"""THROWAWAY Linux CPU KWS experiment. No microphone, network inference or iOS code."""

import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
import tarfile
import time
import urllib.request
import wave

MODEL = "sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01"
ARCHIVE_URL = f"https://github.com/k2-fsa/sherpa-onnx/releases/download/kws-models/{MODEL}.tar.bz2"
ARCHIVE_SHA = "f170013b4716e41b62b9bfd809687c207cef798ef9bc6534d524e17af9b6561a"
SOURCE_COMMIT = "af2d8d81dbfa61c4cea4c568da78f3982edca005"  # v2.0.1
ASSETS = {
    "encoder-epoch-12-avg-2-chunk-16-left-64.int8.onnx": "1e721676515bcd42a186979733981213c66c80db680e1cc582dfedf3be76e678",
    "decoder-epoch-12-avg-2-chunk-16-left-64.int8.onnx": "e40ff43297abe815e8898494c17e71bba2152d9d40fa3eb803f75d0f7533329a",
    "joiner-epoch-12-avg-2-chunk-16-left-64.int8.onnx": "eae9da0c7e1e6c6a3f4cc42d167899c388f6c6701b94cb96320e4f55df79624c",
    "tokens.txt": "fd2ded4050a55d2b1578870ba8697d02371980217806b7558bd0a5cc60f3ba53",
    "bpe.model": "c8a2a0129c4ab8e463164c142f82d25649661b122c8cd0b7aab5c9e80b90ad24",
}
# Exact CPython 3.12, Linux x86_64 wheels, rather than a moving dependency solve.
WHEELS = [
    ("sherpa-onnx", "2.0.1", "https://files.pythonhosted.org/packages/32/3f/b47ec3a66e53bb6a70391b27acc9086bba8b56986b365f239e4426868efe/sherpa_onnx-2.0.1-cp312-cp312-manylinux2014_x86_64.manylinux_2_17_x86_64.whl", "c7780d8af6921c18e8e920376b32d7707294e5cd3838f4762cf746ab919bcc40"),
    ("sherpa-onnx-core", "2.0.1", "https://files.pythonhosted.org/packages/9a/4e/d946cc081864c8c242e787a73dd2ea61f861445f3d4207787d873e4dc65d/sherpa_onnx_core-2.0.1-py3-none-manylinux2014_x86_64.whl", "f4ee0ab91c4ae596e0e49d7de32522a946295e038a652f1c1884d89014ee4ec2"),
    ("sentencepiece", "0.2.1", "https://files.pythonhosted.org/packages/04/88/14f2f4a2b922d8b39be45bf63d79e6cd3a9b2f248b2fcb98a69b12af12f5/sentencepiece-0.2.1-cp312-cp312-manylinux_2_27_x86_64.manylinux_2_28_x86_64.whl", "0cdfecef430d985f1c2bcbfff3defd1d95dae876fbd0173376012d2d7d24044b"),
    ("numpy", "2.3.4", "https://files.pythonhosted.org/packages/f9/58/30a85127bfee6f108282107caf8e06a1f0cc997cb6b52cdee699276fcce4/numpy-2.3.4-cp312-cp312-manylinux_2_27_x86_64.manylinux_2_28_x86_64.whl", "4121c5beb58a7f9e6dfdee612cb24f4df5cd4db6e8261d7f4d7450a997a65d6a"),
]
SOURCES = {
    "python-api-examples/keyword-spotter.py": "18a6425a297db4d947ac49fcb53d1cca2646be47cbe9ff5c3901c25a021f2ab8",
    "sherpa-onnx/python/sherpa_onnx/keyword_spotter.py": "6b8ed936d10e98c066a993e8dc58342aa8f4f0f04083706b178e3fe7fb5a4452",
    "scripts/text2token.py": "5c8c5ba1cceae720ee5aa574875ac1238bf55a334de454dd7a5d174ebfc1f76a",
    "LICENSE": "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30",
}
REFERENCE = {
    "test_wavs/0.wav": "6bc58a4efdf20daac252b6b1502632601a71efe0308f6757dc1eda34891a7e4f",
    "test_wavs/test_keywords.txt": "fd9c3b504ab922bc96ce8613eff02193f8a5526fcf74a14df436c7af74ad649d",
    "test_wavs/trans.txt": "55ca91f3ee53c324b2f802f63c50cacc8931da846c4aaa35f115680201ff904b",
    "README.md": "74e42d37d63acd2366b042151d576fcc0c4917a41de339414a37f1e79f4e80e2",
}
SETTINGS = dict(num_threads=1, sample_rate=16000, feature_dim=80,
                max_active_paths=4, keywords_score=1.0, keywords_threshold=0.25,
                num_trailing_blanks=1, provider="cpu")
RATE, CHUNK = 16000, 1280  # 80 ms callback, files fed faster than real time
VOICES = ["awb", "kal", "kal16", "rms", "slt"]
PHRASES = [
    ("wake", "wake_alone", "Hey Evener", 1),
    ("command", "wake_command", "Hey Evener tell me my next meeting", 1),
    ("ordinary1", "ordinary", "Please list my upcoming meetings", 0),
    ("ordinary2", "ordinary", "The train leaves in ten minutes", 0),
    ("everyone", "near_miss", "Hey everyone", 0),
    ("evening", "near_miss", "Hey evening", 0),
    ("evan", "near_miss", "Hey Evan", 0),
    ("even", "near_miss", "Hey even", 0),
    ("evelyn", "near_miss", "Hey Evelyn", 0),
    ("bare", "near_miss", "Evener", 0),
    ("playback", "assistant_playback", "Hey Evener", 1),
]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def fetch(url, target, expected):
    if not target.exists():
        with urllib.request.urlopen(url, timeout=60) as response:
            data = response.read(100_000_001)
        assert len(data) <= 100_000_000, "Download exceeded spike bound"
        assert digest(data) == expected, f"Hash mismatch: {url}"
        target.write_bytes(data)
    assert digest(target.read_bytes()) == expected, f"Hash mismatch: {target}"


def prepare(root):
    assert sys.version_info[:2] == (3, 12), "Use /usr/bin/python3.12 for pinned wheels"
    for folder in ["downloads", "model", "source", "audio", "reference"]:
        (root / folder).mkdir(parents=True, exist_ok=True)
    archive = root / "downloads" / f"{MODEL}.tar.bz2"
    fetch(ARCHIVE_URL, archive, ARCHIVE_SHA)
    with tarfile.open(archive) as tar:
        for filename, expected in ASSETS.items():
            data = tar.extractfile(f"{MODEL}/{filename}").read()
            assert digest(data) == expected, filename
            (root / "model" / filename).write_bytes(data)
        for filename, expected in REFERENCE.items():
            data = tar.extractfile(f"{MODEL}/{filename}").read()
            assert digest(data) == expected, filename
            (root / "reference" / Path(filename).name).write_bytes(data)
    wheels = []
    for _, _, url, expected in WHEELS:
        target = root / "downloads" / url.rsplit("/", 1)[1]
        fetch(url, target, expected)
        wheels.append(str(target))
    for filename, expected in SOURCES.items():
        fetch(f"https://raw.githubusercontent.com/k2-fsa/sherpa-onnx/{SOURCE_COMMIT}/{filename}",
              root / "source" / filename.replace("/", "__"), expected)
    env = dict(os.environ, UV_CACHE_DIR=str(root / "uv-cache"))
    if not (root / "venv" / "bin" / "python").exists():
        subprocess.run(["uv", "venv", "--python", sys.executable, str(root / "venv")],
                       env=env, check=True, timeout=120)
    subprocess.run(["uv", "pip", "install", "--reinstall", "--no-index", "--python",
                    str(root / "venv" / "bin" / "python"), *wheels],
                   env=env, check=True, timeout=120)
    print("Prepared pinned source, model and wheels under", root)


def create_spotter(root, missing=False, keywords_file=None):
    import sherpa_onnx
    paths = {part: str(root / "model" / f"{part}-epoch-12-avg-2-chunk-16-left-64.int8.onnx")
             for part in ["encoder", "decoder", "joiner"]}
    if missing:
        paths["encoder"] = str(root / "model" / "deliberately-missing-encoder.onnx")
        assert not Path(paths["encoder"]).exists()
    return sherpa_onnx.KeywordSpotter(tokens=str(root / "model" / "tokens.txt"),
                                    keywords_file=str(keywords_file or root / "keywords.txt"),
                                    **paths, **SETTINGS)


def write_wav(path, pcm):
    with wave.open(str(path), "wb") as handle:
        handle.setnchannels(1)
        handle.setsampwidth(2)
        handle.setframerate(RATE)
        handle.writeframes(pcm)


def corpus(root):
    import numpy as np
    listed = subprocess.run(["ffmpeg", "-hide_banner", "-f", "lavfi", "-i",
                             "flite=list_voices=1", "-f", "null", "-"],
                            capture_output=True, text=True, timeout=20, check=True)
    available = re.findall(r"\[Parsed_flite_\d+ @ [^\]]+\] (\w+)\s*$", listed.stderr, re.M)
    assert sorted(available) == sorted(VOICES), f"Voice inventory changed: {available}"
    clips = []
    for voice in VOICES:
        for name, category, text, expected in PHRASES:
            clip_id = f"{voice}_{name}"
            path = root / "audio" / f"{clip_id}.wav"
            # Single synthesis call: no synthetic pause is inserted after the wake phrase.
            command = ["ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi",
                       "-i", f"flite=text='{text}':voice={voice}", "-ac", "1", "-ar", str(RATE),
                       "-c:a", "pcm_s16le", str(path)]
            subprocess.run(command, check=True, timeout=30)
            with wave.open(str(path), "rb") as handle:
                assert (handle.getnchannels(), handle.getsampwidth(), handle.getframerate()) == (1, 2, RATE)
                speech = handle.readframes(handle.getnframes())
            # Fixed leading 0.3 s and trailing 1.0 s, represented in manifest durations/hashes.
            pcm = bytes(int(0.3 * RATE) * 2) + speech + bytes(RATE * 2)
            write_wav(path, pcm)
            clips.append(dict(id=clip_id, category=category, voice=voice, text=text,
                              expected_hits=expected, speech_samples=len(speech) // 2,
                              pcm=pcm, path=path))
    for name in ["silence", "noise"]:
        # Deterministic PCM16 uniform noise in [-1024,1024], about -35 dBFS RMS.
        pcm = (bytes(5 * RATE * 2) if name == "silence" else
               np.random.Generator(np.random.PCG64(20261104)).integers(
                   -1024, 1025, size=5 * RATE, dtype=np.int16).astype("<i2").tobytes())
        path = root / "audio" / f"{name}.wav"
        write_wav(path, pcm)
        clips.append(dict(id=name, category=name, voice=None, text=None,
                          expected_hits=0, speech_samples=0, pcm=pcm, path=path))
    return clips


def stream_audio(kws, pcm, reset=True):
    import numpy as np
    samples = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0
    stream = kws.create_stream()
    observations, decode_calls, fed = [], 0, 0
    wall, cpu = time.perf_counter(), time.process_time()

    def drain():
        nonlocal decode_calls
        while kws.is_ready(stream):
            kws.decode_stream(stream)
            decode_calls += 1
            keyword = kws.get_result(stream)
            if keyword:
                observations.append(dict(keyword=keyword, fed_seconds=round(fed / RATE, 5)))
                if reset:
                    kws.reset_stream(stream)

    for start in range(0, len(samples), CHUNK):
        piece = samples[start:start + CHUNK]
        stream.accept_waveform(RATE, piece)
        fed += len(piece)
        drain()
    stream.input_finished()
    drain()
    wall, cpu = time.perf_counter() - wall, time.process_time() - cpu
    seconds = len(samples) / RATE
    assert fed == len(samples) and decode_calls > 0, "No inference ran"
    return dict(samples_fed=fed, audio_seconds=round(seconds, 5), decode_calls=decode_calls,
                wall_seconds=round(wall, 6), cpu_seconds=round(cpu, 6),
                wall_rtf=round(wall / seconds, 6), cpu_rtf=round(cpu / seconds, 6),
                result_observations=observations, reset_after_hit=reset,
                reset_calls=len(observations) if reset else 0)


def keyword_file(root):
    import sentencepiece as spm
    sp = spm.SentencePieceProcessor(model_file=str(root / "model" / "bpe.model"))
    pieces = sp.encode("HEY EVENER", out_type=str)
    vocabulary = {line.split()[0] for line in (root / "model" / "tokens.txt").read_text().splitlines()}
    assert pieces and all(piece in vocabulary and piece != "<unk>" for piece in pieces)
    # Exactly the upstream text2token BPE branch, without its unused pypinyin import.
    content = " ".join(pieces) + " @HEY_EVENER\n"
    (root / "keywords.txt").write_text(content)
    return content


def reference_control(root):
    """One labelled official positive, added after all custom-phrase clips missed."""
    for filename, expected in REFERENCE.items():
        assert digest((root / "reference" / Path(filename).name).read_bytes()) == expected
    kws = create_spotter(root, keywords_file=root / "reference" / "test_keywords.txt")
    with wave.open(str(root / "reference" / "0.wav")) as handle:
        assert (handle.getnchannels(), handle.getsampwidth(), handle.getframerate()) == (1, 2, RATE)
        pcm = handle.readframes(handle.getnframes()) + bytes(RATE * 2)
    single = stream_audio(kws, pcm)
    repeated = stream_audio(kws, pcm + pcm)
    no_reset = stream_audio(kws, pcm + pcm, reset=False)
    assert [e["keyword"] for e in single["result_observations"]] == ["LIGHT UP"]
    assert [e["keyword"] for e in repeated["result_observations"]] == ["LIGHT UP", "LIGHT UP"]
    return dict(purpose="Pipeline calibration only, NOT HEY EVENER accuracy or AEC evidence",
                transcript=(root / "reference" / "trans.txt").read_text().splitlines()[0],
                keywords_file=(root / "reference" / "test_keywords.txt").read_text(),
                archive_members_sha256=REFERENCE, trailing_silence_seconds=1.0,
                single=single, continuous_repeat=repeated, omitted_reset_repeat=no_reset)


def run(root, output, smoke):
    import sherpa_onnx
    for name, version, _, _ in WHEELS:
        assert importlib.metadata.version(name) == version, name
    for filename, expected in ASSETS.items():
        assert digest((root / "model" / filename).read_bytes()) == expected, filename
    import inspect
    assert digest(Path(inspect.getsourcefile(sherpa_onnx.KeywordSpotter)).read_bytes()) == SOURCES[
        "sherpa-onnx/python/sherpa_onnx/keyword_spotter.py"]
    keyword = keyword_file(root)
    wall, cpu = time.perf_counter(), time.process_time()
    kws = create_spotter(root)
    load = dict(wall_seconds=round(time.perf_counter() - wall, 6),
                cpu_seconds=round(time.process_time() - cpu, 6))
    clips = corpus(root)
    if smoke:
        clip = next(clip for clip in clips if clip["id"] == "slt_wake")
        print(json.dumps(dict(keyword_file=keyword, model_load=load,
                              smoke=stream_audio(kws, clip["pcm"])), indent=2))
        return
    records = []
    for clip in clips:
        measurement = stream_audio(kws, clip["pcm"])
        count = len(measurement["result_observations"])
        record = {key: value for key, value in clip.items() if key not in ["pcm", "path"]}
        record.update(wav_sha256=digest(clip["path"].read_bytes()), pcm_sha256=digest(clip["pcm"]),
                      observed_hits=count, matches_expectation=count == clip["expected_hits"], **measurement)
        records.append(record)
    # A single uninterrupted stream across ordinary speech, repeated wakes and playback.
    by_id = {clip["id"]: clip for clip in clips}
    sequence = [f"{voice}_{name}" for voice in VOICES
                for name in ["ordinary1", "wake", "ordinary2", "command", "everyone", "playback"]]
    pcm, segments = b"", []
    for clip_id in sequence:
        clip = by_id[clip_id]
        start = len(pcm) / 2 / RATE
        pcm += clip["pcm"]
        segments.append(dict(id=clip_id, start_seconds=round(start, 5),
                             end_seconds=round(len(pcm) / 2 / RATE, 5), expected_hits=clip["expected_hits"]))
    continuous = stream_audio(kws, pcm)
    for segment in segments:
        segment["observed_hits"] = sum(segment["start_seconds"] < event["fed_seconds"] <= segment["end_seconds"]
                                       for event in continuous["result_observations"])
        segment["fresh_stream_hits"] = next(r["observed_hits"] for r in records if r["id"] == segment["id"])
    continuous.update(pcm_sha256=digest(pcm), segments=segments,
                      expected_hits=sum(s["expected_hits"] for s in segments),
                      observed_hits=len(continuous["result_observations"]))
    # Deliberate API misuse control: distinguish sticky results from genuine retriggers.
    repeated = by_id["slt_wake"]["pcm"] + by_id["slt_ordinary1"]["pcm"] + by_id["slt_wake"]["pcm"]
    state = dict(sequence=["slt_wake", "slt_ordinary1", "slt_wake"], expected_hits=2,
                 pcm_sha256=digest(repeated), correct_reset=stream_audio(kws, repeated),
                 omitted_reset_control=stream_audio(kws, repeated, reset=False))
    child = subprocess.run([sys.executable, str(Path(__file__).resolve()), "missing-model", "--workspace", str(root)],
                           capture_output=True, text=True, timeout=30)
    missing = dict(exit_code=child.returncode, stdout=child.stdout.strip(),
                   error=child.stderr.strip().splitlines()[-1].replace(str(root), "$WORKSPACE"))
    assert child.returncode != 0 and "deliberately-missing-encoder.onnx does not exist" in missing["error"], missing
    reference = reference_control(root)
    negative = [r for r in records if r["expected_hits"] == 0]
    assert len(records) == 57 and len(negative) == 42, "A planned clip did not run"
    assert all(r["decode_calls"] > 0 and r["samples_fed"] > 0 for r in negative), "Negatives were skipped"
    categories = {}
    for category in dict.fromkeys(r["category"] for r in records):
        rows = [r for r in records if r["category"] == category]
        categories[category] = dict(clips=len(rows), triggered_clips=sum(r["observed_hits"] > 0 for r in rows),
                                    hits=sum(r["observed_hits"] for r in rows),
                                    expected_hits=sum(r["expected_hits"] for r in rows))
    duration = sum(r["audio_seconds"] for r in records)
    wall = sum(r["wall_seconds"] for r in records)
    cpu = sum(r["cpu_seconds"] for r in records)
    cpu_model = next((line.split(":", 1)[1].strip() for line in Path("/proc/cpuinfo").read_text().splitlines()
                      if line.startswith("model name")), "unknown")
    synthesis_files = ["/usr/bin/ffmpeg", "/lib/x86_64-linux-gnu/libflite.so.1"] + [
        f"/lib/x86_64-linux-gnu/libflite_cmu_us_{voice}.so.1" for voice in VOICES]
    result = dict(schema=1, label="THROWAWAY SYNTHETIC LINUX CPU EVIDENCE, NOT iOS QUALIFICATION",
                  recorded_utc=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                  platform=dict(python=platform.python_version(), system=platform.platform(), cpu=cpu_model,
                                ffmpeg=subprocess.check_output(["ffmpeg", "-version"], text=True).splitlines()[0],
                                synthesis_packages=subprocess.check_output([
                                    "dpkg-query", "-W", "-f=${Package} ${Version}\\n", "ffmpeg", "libflite1"],
                                    text=True).splitlines(),
                                synthesis_sha256={name: digest(Path(name).read_bytes()) for name in synthesis_files},
                                voices=VOICES),
                  provenance=dict(probe_sha256=digest(Path(__file__).read_bytes()), model=MODEL,
                                  archive_url=ARCHIVE_URL, archive_sha256=ARCHIVE_SHA,
                                  source_commit=SOURCE_COMMIT, sources=SOURCES,
                                  wheels=[dict(package=n, version=v, url=u, sha256=h) for n, v, u, h in WHEELS],
                                  assets=[dict(file=n, bytes=(root / "model" / n).stat().st_size, sha256=h)
                                          for n, h in ASSETS.items()]),
                  settings=dict(**SETTINGS, chunk_samples=CHUNK, keyword_file=keyword,
                                leading_silence_seconds=0.3, trailing_silence_seconds=1.0,
                                noise_seed=20261104, noise_pcm16_range=[-1024, 1024]),
                  model_load=load,
                  summary=dict(categories=categories, clips=len(records), negative_clips_executed=len(negative),
                               negative_clips_triggered=sum(r["observed_hits"] > 0 for r in negative),
                               audio_seconds=round(duration, 5), wall_seconds=round(wall, 6),
                               cpu_seconds=round(cpu, 6), wall_rtf=round(wall / duration, 6),
                               cpu_rtf=round(cpu / duration, 6),
                               decode_calls=sum(r["decode_calls"] for r in records)),
                  clips=records, continuous=continuous, state_control=state, missing_model=missing,
                  reference_control=reference,
                  limitations=["Synthetic flite pronunciations, not human accuracy or false accepts per hour",
                               "kal and kal16 are related voices, not independent speakers",
                               "Digital assistant-playback injection, no speaker, mic, AEC or acoustical barge-in",
                               "No iOS, locked/background lifecycle, interruptions, battery, thermal or memory measurements",
                               "80 ms file chunks processed faster than real time, fed_seconds is not end-to-end latency",
                               "One fixed operating point, no threshold sweep or model training",
                               "Model metadata says Apache-2.0, GigaSpeech terms need separate review"])
    # Compact but auditable: one clip per line, no audio or binary artifacts.
    compact = lambda value: json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    lines = []
    for key, value in result.items():
        body = ("[\n    " + ",\n    ".join(compact(clip) for clip in value) + "\n  ]"
                if key == "clips" else compact(value))
        lines.append("  " + json.dumps(key) + ": " + body)
    output.write_text("{\n" + ",\n".join(lines) + "\n}\n")
    print(json.dumps(dict(summary=result["summary"], model_load=load,
                          continuous_hits=continuous["observed_hits"], missing_model=missing,
                          reference_hits=len(reference["single"]["result_observations"]),
                          reference_continuous_hits=len(reference["continuous_repeat"]["result_observations"]),
                          output=str(output)), indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=["prepare", "smoke", "run", "missing-model"])
    parser.add_argument("--workspace", type=Path, required=True, help="Owned scratch only, never the repository")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    root = args.workspace.resolve()
    if args.mode == "prepare":
        prepare(root)
    elif args.mode == "missing-model":
        print("Invoking sherpa_onnx.KeywordSpotter with missing encoder", flush=True)
        create_spotter(root, missing=True)  # Unhandled failure must exit nonzero, no fallback.
    else:
        if args.mode == "run" and args.output is None:
            parser.error("--output is required for run")
        run(root, args.output, smoke=args.mode == "smoke")


if __name__ == "__main__":
    main()
