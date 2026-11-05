#!/usr/bin/env python3
"""Bounded synthetic KWS diagnostics, never iPhone or shipping qualification.

prepare adds one pinned dependency to probe.py's scratch environment.
run needs probe.py run --output WORK/baseline-reproduction.json first.
synthesize makes six paid requests, only with an explicit copied credential file.
Exit 0 means diagnostics executed, NOT that HEY EVENER worked.
"""

import argparse
import ctypes as C
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.error
import urllib.request
import wave

sys.dont_write_bytecode = True
import probe

PYPINYIN_URL = "https://files.pythonhosted.org/packages/b9/7b/4cabc76fcc21c3c7d5c671d8783984d30ac9d3bb387c4ba784fca3cdfa3a/pypinyin-0.55.0-py2.py3-none-any.whl"
PYPINYIN_SHA = "d53b1e8ad2cdb815fb2cb604ed3123372f5a28c6f447571244aca36fc62a286f"
PHONES = "hh ey iy v n er"  # One predeclared reduced-schwa input, no spelling sweep.
BASELINE_SHA = "68fb8c6f0193b5b2da1ceadb3d7dccd559d21e00abc07e390a014ec73717fa8b"
TTS_MODEL = "gpt-4o-mini-tts-2025-12-15"
TTS_INPUTS = [
    ("wake", "positive", "Hey Evener"),
    ("command", "positive", "Hey Evener tell me my next meeting"),
    ("everyone", "negative", "Hey everyone"),
    ("even", "negative", "Hey even"),
    ("ordinary", "negative", "Please list my upcoming meetings"),
    ("light_up", "calibration", "Light up"),
]


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def pcm(path):
    with wave.open(str(path)) as w:
        if (w.getnchannels(), w.getsampwidth(), w.getframerate()) != (1, 2, 16000):
            raise ValueError(f"Expected mono 16-kHz PCM16 WAV: {path.name}")
        return w.readframes(w.getnframes())


def prepare(root):
    probe.prepare(root)
    wheel = root / "downloads" / PYPINYIN_URL.rsplit("/", 1)[1]
    probe.fetch(PYPINYIN_URL, wheel, PYPINYIN_SHA)
    subprocess.run(["uv", "pip", "install", "--no-index", "--no-deps", "--python",
                    str(root / "venv/bin/python"), str(wheel)], check=True, timeout=60,
                   env=dict(os.environ, UV_CACHE_DIR=str(root / "uv-cache")))


def synthesize(root, credential_copy):
    """No retries. Never print headers, credentials, or provider error bodies."""
    key = None
    for line in credential_copy.read_text().splitlines():
        name, sep, value = line.removeprefix("export ").partition("=")
        if sep and name.strip() == "OPENAI_API_KEY":
            key = value.strip().strip("\"'")
    if not key:
        raise ValueError("OPENAI_API_KEY not found in credential copy")
    folder = root / "fresh"
    folder.mkdir()  # Existing folder blocks accidental extra paid requests.
    headers = {"Authorization": f"Bearer {key}", "Content-Type": "application/json"}

    def request(url, body=None, cap=480_000):
        req = urllib.request.Request(url, headers=headers,
                                     data=json.dumps(body).encode() if body else None)
        try:
            with urllib.request.urlopen(req, timeout=60) as response:
                data = response.read(cap + 1)
                if len(data) > cap:
                    raise ValueError("TTS response exceeded ten seconds of PCM, stopping")
                return data, response.headers.get("x-request-id")
        except urllib.error.HTTPError as error:
            raise ValueError(f"OpenAI HTTP {error.code}, no retries") from None

    model_data, _ = request(f"https://api.openai.com/v1/models/{TTS_MODEL}")
    assert json.loads(model_data)["id"] == TTS_MODEL
    manifest = dict(model=TTS_MODEL, voice="cedar", endpoint="https://api.openai.com/v1/audio/speech",
                    input_format="24000 Hz mono signed little-endian PCM16",
                    instructions="Speak naturally and clearly in American English. Pronounce Evener as EE-vuh-ner. Say only the supplied text.",
                    requests=[])
    for name, category, text in TTS_INPUTS:
        payload = dict(model=TTS_MODEL, voice="cedar", input=text, response_format="pcm",
                       instructions=manifest["instructions"])
        data, request_id = request(manifest["endpoint"], payload)
        raw = folder / f"{name}.pcm"
        raw.write_bytes(data)
        path = folder / f"{name}.wav"
        subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "s16le",
                        "-ar", "24000", "-ac", "1", "-i", str(raw), "-ar", "16000",
                        "-c:a", "pcm_s16le", str(path)], check=True, timeout=30)
        padded = bytes(9600) + pcm(path) + bytes(32000)
        probe.write_wav(path, padded)
        manifest["requests"].append(dict(id=name, category=category, text=text, request_id=request_id,
                                         raw_sha256=sha(raw), raw_bytes=len(data), wav_sha256=sha(path),
                                         pcm_sha256=probe.digest(padded), samples=len(padded) // 2))
        (folder / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        print(f"TTS {name}: {len(data)} PCM bytes")


def flite(root):
    """Trace actual Flite segment labels, then synthesize exactly one phone control."""
    lib = C.CDLL("/lib/x86_64-linux-gnu/libflite.so.1", mode=C.RTLD_GLOBAL)
    voice_lib = C.CDLL("/lib/x86_64-linux-gnu/libflite_cmu_us_slt.so.1")
    lib.flite_init()
    voice_lib.register_cmu_us_slt.argtypes = [C.c_char_p]
    voice_lib.register_cmu_us_slt.restype = C.c_void_p
    voice = voice_lib.register_cmu_us_slt(None)
    for name, args, result in [
        ("flite_synth_text", [C.c_char_p, C.c_void_p], C.c_void_p),
        ("utt_relation", [C.c_void_p, C.c_char_p], C.c_void_p),
        ("relation_head", [C.c_void_p], C.c_void_p),
        ("item_next", [C.c_void_p], C.c_void_p),
        ("item_feat_string", [C.c_void_p, C.c_char_p], C.c_char_p),
        ("delete_utterance", [C.c_void_p], None),
        ("flite_phones_to_speech", [C.c_char_p, C.c_void_p, C.c_char_p], C.c_float),
    ]:
        fn = getattr(lib, name)
        fn.argtypes, fn.restype = args, result
    traces = {}
    for text in ["Hey Evener", "Hey even er", "Hey evening", "Light up"]:
        utterance = lib.flite_synth_text(text.encode(), voice)
        item = lib.relation_head(lib.utt_relation(utterance, b"Segment"))
        phones = []
        while item:
            phones.append(lib.item_feat_string(item, b"name").decode())
            item = lib.item_next(item)
        traces[text] = phones
        lib.delete_utterance(utterance)
    raw = root / "diagnostics" / "reduced.raw.wav"
    seconds = lib.flite_phones_to_speech(PHONES.encode(), voice, os.fsencode(raw))
    if seconds <= 0:
        raise ValueError("Flite direct-phone synthesis produced no audio")
    return traces, raw


def local_inputs(root):
    folder = root / "diagnostics"
    folder.mkdir(exist_ok=True)
    traces, raw = flite(root)
    light = folder / "light_up.raw.wav"
    subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi",
                    "-i", "flite=text='Light up':voice=slt", "-ac", "1", "-ar", "16000",
                    "-c:a", "pcm_s16le", str(light)], check=True, timeout=30)
    inputs = []
    for name, source, label in [("reduced_schwa", raw, PHONES), ("flite_light_up", light, "Light up")]:
        converted = folder / f"{name}.wav"
        subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", str(source),
                        "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", str(converted)],
                       check=True, timeout=30)
        data = bytes(9600) + pcm(converted) + bytes(32000)
        probe.write_wav(converted, data)
        inputs.append((name, converted, label))
    return traces, inputs


def transcribe(recognizer, data):
    import numpy as np
    samples = np.frombuffer(data, dtype="<i2").astype(np.float32) / 32768.0
    stream = recognizer.create_stream()
    decodes = 0
    for start in range(0, len(samples), 1280):
        stream.accept_waveform(16000, samples[start:start + 1280])
        while recognizer.is_ready(stream):
            recognizer.decode_stream(stream)
            decodes += 1
    stream.input_finished()
    while recognizer.is_ready(stream):
        recognizer.decode_stream(stream)
        decodes += 1
    assert decodes > 0
    return dict(decode_calls=decodes, samples_fed=len(samples),
                result=json.loads(recognizer.get_result_as_json_string(stream)))


def run(root, output, fresh, threshold_zero):
    import sherpa_onnx
    for name, version, _, _ in probe.WHEELS:
        assert importlib.metadata.version(name) == version, name
    assert importlib.metadata.version("pypinyin") == "0.55.0"
    for name, expected in probe.ASSETS.items():
        assert sha(root / "model" / name) == expected, name
    baseline = json.loads((root / "baseline-reproduction.json").read_text())
    assert baseline["settings"]["keyword_file"] == "▁HE Y ▁EVEN ER @HEY_EVENER\n"
    assert all(baseline["settings"][k] == v for k, v in probe.SETTINGS.items())
    original = Path(__file__).with_name("results.json")
    assert sha(original) == BASELINE_SHA
    prior = json.loads(original.read_text())
    baseline_rows = []
    for old, new in zip(prior["clips"], baseline["clips"], strict=True):
        for key in ["id", "wav_sha256", "pcm_sha256", "samples_fed", "decode_calls", "observed_hits"]:
            assert old[key] == new[key], (old["id"], key)
        path = root / "audio" / f"{old['id']}.wav"
        assert sha(path) == old["wav_sha256"]
        assert hashlib.sha256(pcm(path)).hexdigest() == old["pcm_sha256"]
        baseline_rows.append({k: new[k] for k in ["id", "category", "expected_hits", "observed_hits",
                                                "samples_fed", "decode_calls", "wav_sha256", "pcm_sha256"]})
    kwargs = dict(tokens=str(root / "model/tokens.txt"), tokens_type="bpe",
                  bpe_model=str(root / "model/bpe.model"))
    tokens = sherpa_onnx.text2token(["HEY EVENER"], **kwargs)[0]
    ids = sherpa_onnx.text2token(["HEY EVENER"], **kwargs, output_ids=True)[0]
    assert tokens == ["▁HE", "Y", "▁EVEN", "ER"] and ids == [49, 17, 274, 38]
    probe.keyword_file(root)
    kws = probe.create_spotter(root)
    traces, local = local_inputs(root)
    paths = {p: str(root / "model" / f"{p}-epoch-12-avg-2-chunk-16-left-64.int8.onnx")
             for p in ["encoder", "decoder", "joiner"]}
    recognizer = sherpa_onnx.OnlineRecognizer.from_transducer(
        tokens=str(root / "model/tokens.txt"), **paths, num_threads=1, sample_rate=16000,
        feature_dim=80, provider="cpu", decoding_method="greedy_search", enable_endpoint_detection=False)
    inputs = [(f"{v}_wake", root / "audio" / f"{v}_wake.wav", "Hey Evener") for v in probe.VOICES] + local
    manifest = None
    if fresh:
        manifest = json.loads((root / "fresh/manifest.json").read_text())
        assert len(manifest["requests"]) == len(TTS_INPUTS)
        for entry in manifest["requests"]:
            path = root / "fresh" / f"{entry['id']}.wav"
            assert sha(path) == entry["wav_sha256"]
            inputs.append(("fresh_" + entry["id"], path, entry["text"]))
    rows = []
    for name, path, text in inputs:
        data = pcm(path)
        rows.append(dict(id=name, input=text, wav_sha256=sha(path), pcm_sha256=probe.digest(data),
                         target_kws=probe.stream_audio(kws, data), unbiased_asr=transcribe(recognizer, data)))
    controls = {}
    control_inputs = [("HEY EVEN HER", "alternate_phrase", root / "audio/slt_wake.wav"),
                      ("LIGHT UP", "synthetic_light_up", root / "diagnostics/flite_light_up.wav")]
    if fresh:
        control_inputs.append(("LIGHT UP", "fresh_light_up", root / "fresh/light_up.wav"))
    for phrase, label, path in control_inputs:
        keyword = " ".join(sherpa_onnx.text2token([phrase], **kwargs)[0]) + " @" + phrase.replace(" ", "_") + "\n"
        file = root / "diagnostics" / f"{label}.txt"
        file.write_text(keyword)
        control = probe.create_spotter(root, keywords_file=file)
        controls[label] = dict(keyword_file=keyword, input_wav_sha256=sha(path),
                               measurement=probe.stream_audio(control, pcm(path)))
    zero = None
    if threshold_zero:
        settings = dict(probe.SETTINGS, keywords_threshold=0.0)
        control = sherpa_onnx.KeywordSpotter(tokens=str(root / "model/tokens.txt"),
                                            keywords_file=str(root / "keywords.txt"), **paths, **settings)
        zero_inputs = [(r["id"], r["category"], root / "audio" / f"{r['id']}.wav") for r in baseline_rows]
        if fresh:
            zero_inputs += [("fresh_" + r["id"], r["category"], root / "fresh" / f"{r['id']}.wav")
                            for r in manifest["requests"] if r["category"] != "calibration"]
        measurements = []
        for name, category, path in zero_inputs:
            measurements.append(dict(id=name, category=category, wav_sha256=sha(path),
                                     **probe.stream_audio(control, pcm(path))))
        zero = dict(purpose="Threshold-only rejection diagnostic, NOT a proposed operating point",
                    settings=settings, clips=measurements)
    result = dict(label="SYNTHETIC DIAGNOSTICS, NOT WAKE RELIABILITY OR iPHONE QUALIFICATION",
                  followup_sha256=sha(Path(__file__)), baseline_sha256=BASELINE_SHA,
                  baseline_reproduction_sha256=sha(root / "baseline-reproduction.json"),
                  settings=probe.SETTINGS, chunk_samples=1280,
                  canonical=dict(tokens=tokens, ids=ids), flite_phones=traces,
                  phonetic_input=PHONES, baseline_clips=baseline_rows,
                  fresh_synthesis=manifest, diagnostics=rows, controls=controls,
                  threshold_zero=zero, official_calibration=probe.reference_control(root),
                  runtime_sources_sha256={name: sha(Path(sherpa_onnx.__file__).parent / name)
                                         for name in ["utils.py", "online_recognizer.py", "keyword_spotter.py"]})
    with output.open("x") as handle:
        json.dump(result, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
    print(json.dumps(dict(output=str(output), target_hits={r['id']: len(r['target_kws']['result_observations']) for r in rows},
                          control_hits={k: len(v['measurement']['result_observations']) for k, v in controls.items()},
                          threshold_zero_hits=sum(len(r['result_observations']) for r in zero['clips']) if zero else None), indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=["prepare", "synthesize", "run"])
    parser.add_argument("--workspace", type=Path, required=True, help="Owned scratch prepared with probe.py")
    parser.add_argument("--output", type=Path, help="New JSON path, existing files are never overwritten")
    parser.add_argument("--fresh", action="store_true", help="Include the six already synthesized fresh WAVs")
    parser.add_argument("--threshold-zero", action="store_true", help="One isolated threshold=0 diagnostic, no sweep")
    parser.add_argument("--credential-copy", type=Path, help="synthesize only, caller deletes this exact .env copy")
    args = parser.parse_args()
    root = args.workspace.resolve()
    if args.mode == "run" and (args.output is None or args.output.exists()):
        parser.error("run requires --output naming a new file")
    if args.mode == "synthesize" and args.credential_copy is None:
        parser.error("synthesize requires --credential-copy, makes six paid requests")
    try:
        if args.mode == "prepare":
            prepare(root)
        elif args.mode == "synthesize":
            synthesize(root, args.credential_copy)
        else:
            run(root, args.output, args.fresh, args.threshold_zero)
    except (OSError, ValueError, AssertionError, subprocess.SubprocessError) as error:
        parser.exit(1, f"followup: {error}\n")


if __name__ == "__main__":
    main()
