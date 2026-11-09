#!/usr/bin/env python3
"""Throwaway, explicit-opt-in hosted voice protocol probe. Never a manager client."""
import argparse
import base64
import hashlib
import json
import logging
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

logging.disable(logging.CRITICAL)  # Transport traces must never include Authorization.
MODELS = {"live": "gpt-live-1", "realtime": "gpt-realtime-2.1-mini"}
INVENTORY_MODELS = ["gpt-live-1", "gpt-realtime-2.1", "gpt-realtime-2.1-mini"]
HARD_SECONDS = 60
MAX_RESPONSES = 4
MAX_OUTPUT_TOKENS = 192
BUDGET_USD = 1.0
RESERVATION_USD = {"live": 0.05, "realtime": 0.25}
KEY_PATTERN = re.compile(r"[A-Za-z0-9_-]{16,512}\Z")
SAFE_CODE = re.compile(r"[a-z0-9_]{1,80}\Z")
RATE = 24000
FRAME_SECONDS = 0.02
FRAME_BYTES = 960
MAX_CONTEXT_TEXT_BYTES = 3000
CLIP_TEXT = {
    "request": "Please look up the status of project Alpha. Give me a brief status report.",
    "correction": "Wait. I meant project Beta, not Alpha. Please check Beta instead.",
}
INSTRUCTIONS = (
    "This is a synthetic protocol test, not a real manager. Speak English. "
    "For every project status request, use the backend and never invent a status. "
    "After a result, say its summary. Keep replies under forty words. "
    "If the user corrects a project, look up the corrected project instead. "
    "Do not take any action except read-only status lookup."
)
TOOL = {
    "type": "function", "name": "read_status",
    "description": "Read a synthetic local status fixture, not Evener. No side effects.",
    "parameters": {"type": "object", "properties": {
        "project": {"type": "string", "enum": ["Alpha", "Beta"]}},
        "required": ["project"], "additionalProperties": False},
}


class StopProbe(Exception):
    """Safe local failure, never populated with provider errors."""


def require_opt_in(env):
    if env.get("EVENER_VOICE_LIVE") != "1":
        raise StopProbe("explicit_opt_in_required")


def parse_key(data):
    keys = []
    for line in data.decode("utf-8").splitlines():
        match = re.match(r"^\s*(?:export\s+)?OPENAI_API_KEY\s*=\s*(.*?)\s*$", line)
        if not match:
            continue
        value = match[1]
        if len(value) >= 2 and value[0] in "\"'" and value[-1] == value[0]:
            value = value[1:-1]
        if not KEY_PATTERN.fullmatch(value):
            raise StopProbe("key_not_a_literal")
        keys.append(value)
    if len(keys) != 1:
        raise StopProbe("one_literal_key_required")
    return keys[0]


def copied_key(source, scratch):
    # The first and only read of the non-regenerable source is this exact copy.
    fd, name = tempfile.mkstemp(prefix="private-key-copy-", dir=scratch)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "wb") as target, open(source, "rb") as original:
            shutil.copyfileobj(original, target)
        assert stat.S_IMODE(os.stat(name).st_mode) == 0o600
        return parse_key(Path(name).read_bytes())
    finally:
        os.unlink(name)


def safe_code(value):
    return value if isinstance(value, str) and SAFE_CODE.fullmatch(value) else None


def safe_error(payload):
    err = payload.get("error", payload) if isinstance(payload, dict) else {}
    if not isinstance(err, dict):
        return {"code": None, "message_omitted": True}
    message = str(err.get("message", "")).lower()
    return {"code": safe_code(err.get("code")), "type": safe_code(err.get("type")),
            "message_omitted": True,
            "billing_or_quota": any(s in message for s in ("billing", "quota", "credit")),
            "access_denied": any(s in message for s in ("permission", "access", "not found"))}


def safe_exception(exc):
    # Never str(exc), repr(exc), traceback, headers, or response body.
    result = {"exception_class": type(exc).__name__, "detail_omitted": True}
    status = getattr(exc, "status_code", None)
    if isinstance(status, int):
        result["http_status"] = status
    body = getattr(exc, "resp_body", None)
    if body:
        try:
            result["error"] = safe_error(json.loads(body))
        except (ValueError, TypeError):
            pass
    return result


def redact(value, key):
    if isinstance(value, dict):
        return {k: redact(v, key) for k, v in value.items()}
    if isinstance(value, list):
        return [redact(v, key) for v in value]
    if isinstance(value, str):
        return re.sub(r"sk-[A-Za-z0-9_-]+", "[REDACTED]", value.replace(key, "[REDACTED]"))
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def inventory(key):
    request = urllib.request.Request("https://api.openai.com/v1/models",
                                     headers={"Authorization": "Bearer " + key})
    opener = urllib.request.build_opener(NoRedirect, urllib.request.ProxyHandler({}))
    try:
        with opener.open(request, timeout=15) as response:
            payload = json.loads(response.read(2_000_000))
            names = {item["id"] for item in payload.get("data", [])}
            return {"http_status": response.status, "model_count": len(names),
                    "requested_models": {name: name in names for name in INVENTORY_MODELS},
                    "inference_performed": False}
    except urllib.error.HTTPError as exc:
        try:
            error = safe_error(json.loads(exc.read(32_000)))
        except (ValueError, TypeError):
            error = {"message_omitted": True}
        return {"http_status": exc.code, "error": error, "inference_performed": False}
    except Exception as exc:
        return {"failure": safe_exception(exc), "inference_performed": False}


def reserve(results, kind, corrective):
    attempts = results.setdefault("attempts", [])
    if len(attempts) >= 4:
        raise StopProbe("four_attempt_limit")
    prior = [a for a in attempts if a["kind"] == kind]
    if bool(prior) != corrective:
        raise StopProbe("retry_requires_prior_attempt_and_flag")
    if corrective and sum(a["corrective_retry"] for a in attempts) >= 2:
        raise StopProbe("two_corrective_retry_limit")
    if any((a.get("error") or a.get("failure", {}).get("error") or {}).get("billing_or_quota")
           for a in attempts):
        raise StopProbe("stop_all_candidates_after_billing_or_quota_block")
    if prior and prior[-1].get("access_or_billing_blocked"):
        raise StopProbe("no_retry_after_access_or_billing_block")
    spent = sum(a["reserved_usd"] for a in attempts)
    if spent + RESERVATION_USD[kind] > BUDGET_USD:
        raise StopProbe("dollar_budget_exhausted")
    attempt = {"kind": kind, "model_requested": MODELS[kind],
               "corrective_retry": corrective, "reserved_usd": RESERVATION_USD[kind],
               "hard_seconds": HARD_SECONDS, "status": "reserved"}
    attempts.append(attempt)
    return attempt


def save(path, results, key):
    text = json.dumps(redact(results, key), indent=2, sort_keys=True) + "\n"
    assert key not in text
    path.write_text(text)


def synthesize(scratch):
    clips, metadata = {}, {}
    for name, text in CLIP_TEXT.items():
        path = scratch / (name + ".txt")
        path.write_text(text)
        command = ["ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
                   "-f", "lavfi", "-i", f"flite=textfile={path}:voice=slt",
                   "-ar", str(RATE), "-ac", "1", "-f", "s16le", "pipe:1"]
        try:
            completed = subprocess.run(command, capture_output=True, timeout=15)
            if completed.returncode:
                raise StopProbe("local_synthesis_failed")
            audio = completed.stdout
        finally:
            path.unlink()
        if not audio or len(audio) % 2 or len(audio) > RATE * 2 * 12:
            raise StopProbe("synthetic_clip_invalid_or_over_12_seconds")
        clips[name] = audio
        metadata[name] = {"script": text, "voice": "slt", "bytes": len(audio),
                          "duration_s": len(audio) / (RATE * 2),
                          "sha256": hashlib.sha256(audio).hexdigest(),
                          "command": [p.replace(str(scratch), "$SCRATCH") for p in command]}
    return clips, metadata


def realtime_config():
    return {"type": "realtime", "model": MODELS["realtime"],
            "instructions": INSTRUCTIONS + " Use read_status and read the returned summary verbatim.",
            "output_modalities": ["audio"], "max_output_tokens": MAX_OUTPUT_TOKENS,
            "audio": {"input": {"format": {"type": "audio/pcm", "rate": RATE},
                                "turn_detection": {"type": "server_vad", "threshold": 0.5,
                                                   "prefix_padding_ms": 300, "silence_duration_ms": 500,
                                                   "create_response": False, "interrupt_response": False}},
                      "output": {"format": {"type": "audio/pcm", "rate": RATE}, "voice": "marin"}},
            "tools": [TOOL], "tool_choice": "auto"}


class SimulatedPlayer:
    """Arrival-driven FIFO playout clock only. No sound device, audibility or AEC."""
    def __init__(self):
        self.items = {}
        self.queue = []
        self.last = None

    def append(self, item_id, size, now):
        self.advance(now)
        item = self.items.setdefault(item_id, {"played_ms": 0.0, "queued_ms": 0.0,
                                               "discarded": False})
        if not item["discarded"]:
            item["queued_ms"] += size / (RATE * 2) * 1000
            if item_id not in self.queue:
                self.queue.append(item_id)

    def advance(self, now):
        elapsed_ms = 0 if self.last is None else max(0, now - self.last) * 1000
        self.last = now
        while self.queue and elapsed_ms > 0:
            item = self.items[self.queue[0]]
            played = min(item["queued_ms"], elapsed_ms)
            item["played_ms"] += played
            item["queued_ms"] -= played
            elapsed_ms -= played
            if item["queued_ms"] == 0:
                self.queue.pop(0)

    def interrupt(self, now):
        self.advance(now)
        interrupted = []
        for item_id in self.queue:
            item = self.items[item_id]
            interrupted.append({"item_id": item_id, "audio_end_ms": int(item["played_ms"]),
                                "discarded_queue_ms": round(item["queued_ms"], 1),
                                "physical_playback": False})
            item["queued_ms"] = 0
            item["discarded"] = True
        self.queue.clear()
        for item in self.items.values():
            item["discarded"] = True
        return interrupted


class Measurement:
    def __init__(self, kind, attempt, clips):
        self.kind, self.data, self.clips = kind, attempt, clips
        self.start = time.monotonic()
        self.ws = None
        self.ready = False
        self.next_frame = None
        self.active_clip = None
        self.clip_offset = 0
        self.request_end = None
        self.correction_start = None
        self.correction_end = None
        self.first_audio = None
        self.first_result_audio = None
        self.commentary_sent = False
        self.close_sent = None
        self.finished = False
        self.responses_requested = 0
        self.active_response = None
        self.completed_after_correction = None
        self.delegations = 0
        self.tool_calls = 0
        self.text_bytes = 0
        self.last_frame_time = None
        self.max_send_gap_ms = 0
        self.player = SimulatedPlayer()
        self.output_hasher = hashlib.sha256()
        self.signal_active = False
        self.data.update({"events": [], "event_counts": {}, "audio_received_bytes": 0,
                          "input_audio_bytes": 0, "input_frames": 0, "usage": [],
                          "transcripts": {"input": "", "output": ""}, "fixture_reads": [],
                          "response_limit": MAX_RESPONSES if kind == "realtime" else None,
                          "max_output_tokens": MAX_OUTPUT_TOKENS if kind == "realtime" else None,
                          "input_transcription_enabled": False if kind == "realtime" else "integrated",
                          "physical_playback": False, "real_microphone": False})

    def now(self):
        return time.monotonic() - self.start

    def event(self, event_type, **fields):
        self.data["events"].append({"at_ms": round(self.now() * 1000, 1),
                                    "type": event_type, **fields})

    def send(self, payload, *, text_context=False):
        encoded = json.dumps(payload)
        if text_context:
            self.text_bytes += len(encoded.encode())
            if self.kind == "realtime" and self.text_bytes > MAX_CONTEXT_TEXT_BYTES:
                raise StopProbe("context_text_budget")
        self.ws.send(encoded)
        if "audio" not in payload:
            # Deliberate allowlist, never dump a request or transport header.
            fields = {k: payload[k] for k in ("event_id", "delegation_id", "response_id",
                                              "item_id", "content_index", "audio_end_ms") if k in payload}
            self.event("client." + payload["type"], **fields)

    def respond(self):
        if self.responses_requested >= MAX_RESPONSES:
            self.event("probe.response_limit_reached")
            self.finished = True
            return
        self.responses_requested += 1
        self.send({"type": "response.create", "response": {"max_output_tokens": MAX_OUTPUT_TOKENS}})

    def fixture(self, project, reason):
        if project not in ("Alpha", "Beta") or self.tool_calls >= 2:
            raise StopProbe("unexpected_fixture_request")
        path = Path(__file__).with_name("fixture-status.json")
        raw = path.read_bytes()  # An actual file read, not a canned claimed manager result.
        value = json.loads(raw)[project]
        self.tool_calls += 1
        self.data["fixture_reads"].append({"project": project, "reason": reason,
                                           "path": "fixture-status.json", "at_ms": round(self.now() * 1000, 1),
                                           "fixture_sha256": hashlib.sha256(raw).hexdigest(), "result": value})
        self.event("probe.fixture_read", project=project, reason=reason)
        return value

    def begin_clip(self, name, trigger):
        self.active_clip, self.clip_offset = name, 0
        self.event("probe.clip_start", clip=name, trigger=trigger,
                   input_stream_offset_ms=self.data["input_audio_bytes"] / (RATE * 2) * 1000)
        if name == "correction":
            self.correction_start = self.now()
            self.data["correction_trigger"] = trigger

    def pump_audio(self):
        now = self.now()
        if not self.ready or self.close_sent is not None or now < self.next_frame:
            return
        if self.active_clip is None and self.request_end is None:
            self.begin_clip("request", "session_ready")
        if self.request_end is not None and self.correction_start is None:
            if self.kind == "live" and "Alpha" in self.data["transcripts"]["output"]:
                self.begin_clip("correction", "alpha_in_output_transcript_not_audio_packet")
            elif self.kind == "realtime" and self.first_result_audio is not None and now >= self.first_result_audio + 0.12:
                self.begin_clip("correction", "120ms_after_first_result_audio_arrival")
            elif now > self.request_end + 12:
                self.begin_clip("correction", "fallback_no_result_audio_after_12s")
        audio = bytes(FRAME_BYTES)
        if self.active_clip:
            clip = self.clips[self.active_clip]
            audio = clip[self.clip_offset:self.clip_offset + FRAME_BYTES]
            self.clip_offset += len(audio)
            if self.clip_offset == len(clip):
                self.event("probe.clip_end", clip=self.active_clip)
                if self.active_clip == "request":
                    self.request_end = now + len(audio) / (RATE * 2)
                else:
                    self.correction_end = now + len(audio) / (RATE * 2)
                self.active_clip = None
            audio += bytes(FRAME_BYTES - len(audio))
        if self.last_frame_time is not None:
            self.max_send_gap_ms = max(self.max_send_gap_ms, (now - self.last_frame_time) * 1000)
        self.last_frame_time = now
        self.next_frame = now + FRAME_SECONDS  # Never catch up with an unpaced burst.
        event_type = "session.input_audio.append" if self.kind == "live" else "input_audio_buffer.append"
        self.send({"type": event_type, "audio": base64.b64encode(audio).decode("ascii")})
        self.data["input_frames"] += 1
        self.data["input_audio_bytes"] += len(audio)

    def receive(self, event):
        typ = event.get("type", "unknown")
        counts = self.data["event_counts"]
        counts[typ] = counts.get(typ, 0) + 1
        now = self.now()
        if typ in ("session.output_audio.delta", "response.output_audio.delta"):
            pcm = base64.b64decode(event["delta"], validate=True)
            size = len(pcm)
            self.output_hasher.update(pcm)
            peak = max((abs(s) for (s,) in struct.iter_unpack("<h", pcm)), default=0)
            active = peak >= 185  # About -45 dBFS peak, a signal heuristic, NOT a speech detector.
            if active != self.signal_active:
                self.event("probe.pcm_signal_change", active=active, peak=peak, threshold=185)
                self.signal_active = active
            if active and "first_signal_arrival_ms" not in self.data:
                self.data["first_signal_arrival_ms"] = round(now * 1000, 1)
            self.data["audio_received_bytes"] += size
            if self.data["audio_received_bytes"] > RATE * 2 * HARD_SECONDS:
                raise StopProbe("received_audio_cap")
            if self.first_audio is None:
                self.first_audio = now
                self.event("probe.first_audio", bytes=size)
            if self.first_result_audio is None and self.commentary_sent:
                self.first_result_audio = now
                self.event("probe.first_audio_after_fixture_submission", bytes=size)
            self.data["last_audio_arrival_ms"] = round(now * 1000, 1)
            if self.kind == "realtime":
                self.player.append(event["item_id"], size, now)
            return
        fields = {k: event[k] for k in ("event_id", "client_event_id", "item_id", "response_id",
                                       "start_ms", "end_ms", "offset_ms", "audio_start_ms", "audio_end_ms",
                                       "content_index") if k in event}
        if typ == "error":
            fields["error"] = safe_error(event)
            self.data["error"] = fields["error"]
            error = fields["error"]
            self.data["access_or_billing_blocked"] = error.get("billing_or_quota") or error.get("access_denied")
            self.event(typ, **fields)
            self.data["status"] = "provider_error"
            self.finished = True
            return
        if typ.endswith("transcript.delta") or typ == "conversation.item.input_audio_transcription.delta":
            direction = "input" if "input" in typ else "output"
            delta = event.get("delta", "")
            self.data["transcripts"][direction] += delta
            fields["delta"] = delta
        if typ in ("session.started", "session.created", "session.updated"):
            session = event.get("session", {})
            fields["resolved_model"] = session.get("model")
            self.data["model_resolved"] = session.get("model")
            if typ in ("session.started", "session.updated"):
                if self.kind == "realtime" and session.get("max_output_tokens") != MAX_OUTPUT_TOKENS:
                    raise StopProbe("server_did_not_confirm_output_limit")
                self.ready, self.next_frame = True, now + .3
                self.data["session_ready_ms"] = round(now * 1000, 1)
                fields["max_output_tokens"] = session.get("max_output_tokens")
                if self.kind == "realtime":
                    vad = session.get("audio", {}).get("input", {}).get("turn_detection", {})
                    if vad.get("create_response") is not False or vad.get("interrupt_response") is not False:
                        raise StopProbe("server_did_not_confirm_manual_response_controls")
                    fields["turn_detection"] = vad
        if typ == "session.delegation.created":
            self.delegations += 1
            fields["delegation"] = event.get("delegation")
            fields["top_level_fields"] = sorted(event)
        if typ == "response.created":
            self.active_response = event["response"]["id"]
            fields["response_id"] = self.active_response
            if counts[typ] > MAX_RESPONSES:
                raise StopProbe("unexpected_automatic_response")
        if typ == "response.done":
            response = event["response"]
            fields["status"] = response.get("status")
            fields["response_id"] = response.get("id")
            fields["status_reason"] = safe_code((response.get("status_details") or {}).get("reason"))
            self.active_response = None
            usage = response.get("usage")
            if usage:
                self.data["usage"].append(usage)
            fields["output_types"] = [item.get("type") for item in response.get("output", [])]
            if self.correction_end is not None and "message" in fields["output_types"]:
                self.completed_after_correction = now
        if typ == "session.closed":
            self.data["usage"] = event.get("usage")
            fields["usage"] = event.get("usage")
            self.data["final_usage_confirmed"] = True
            self.finished = True
        # Argument deltas and rate limits are counted, not persisted. Completed calls below are authoritative.
        if typ not in ("response.function_call_arguments.delta", "rate_limits.updated"):
            self.event(typ, **fields)
        if typ == "session.delegation.created":
            delegation_id = event["delegation"]["id"]
            # Deliberately NOT derived from transcript deltas. This is test-driver task selection,
            # not a solution to Live's missing durable intent/task-text boundary.
            project = "Beta" if self.correction_start is not None else "Alpha"
            result = self.fixture(project, "scripted_schedule_not_vendor_task_arguments")
            self.send({"type": "session.thinking.append", "event_id": f"thinking_{self.delegations}",
                       "delegation_id": delegation_id, "content": ""})
            self.send({"type": "session.commentary.append", "event_id": f"commentary_{self.delegations}",
                       "delegation_id": delegation_id, "content": result["summary"]})
            self.commentary_sent = True
        elif typ == "input_audio_buffer.speech_started" and self.correction_start is not None:
            if self.active_response:
                self.send({"type": "response.cancel", "response_id": self.active_response})
            for interrupted in self.player.interrupt(now):
                self.event("probe.simulated_playout_interrupted", **interrupted)
                self.send({"type": "conversation.item.truncate", "item_id": interrupted["item_id"],
                           "content_index": 0, "audio_end_ms": interrupted["audio_end_ms"]})
        elif typ == "input_audio_buffer.committed":
            self.respond()
        elif typ == "response.done":
            for item in event["response"].get("output", []):
                if item.get("type") == "function_call":
                    if item.get("status") != "completed" or item.get("name") != "read_status":
                        raise StopProbe("unexpected_function_call")
                    args = json.loads(item["arguments"])
                    self.event("probe.completed_tool_call", name=item["name"], call_id=item["call_id"],
                               arguments=args)
                    result = self.fixture(args.get("project"), "vendor_completed_function_call")
                    self.send({"type": "conversation.item.create", "item": {"type": "function_call_output",
                               "call_id": item["call_id"], "output": json.dumps(result)}}, text_context=True)
                    self.commentary_sent = True
                    self.respond()

    def run(self, key):
        import websocket
        websocket.enableTrace(False)
        url = ("wss://api.openai.com/v1/live/sessions" if self.kind == "live" else
               "wss://api.openai.com/v1/realtime?model=" + MODELS[self.kind])
        def deadline(_signum, _frame):
            raise StopProbe("hard_session_deadline")
        previous = signal.signal(signal.SIGALRM, deadline)
        signal.setitimer(signal.ITIMER_REAL, HARD_SECONDS)
        try:
            self.ws = websocket.create_connection(url, header={"Authorization": "Bearer " + key},
                                                  timeout=10, redirect_limit=0, http_no_proxy=["*"],
                                                  suppress_origin=True)
            self.data["http_status"] = self.ws.getstatus()
            if self.ws.getstatus() != 101:
                raise StopProbe("websocket_not_upgraded")
            self.ws.settimeout(.005)
            if self.kind == "live":
                self.send({"type": "session.start", "event_id": "start", "session": {
                    "model": MODELS[self.kind], "store": False,
                    "instructions": INSTRUCTIONS + " Delegate status requests to the client.",
                    "audio": {"format": {"type": "audio/pcm", "rate": RATE}, "output": {"voice": "marin"}},
                    "delegation": {"type": "client"}}}, text_context=True)
            else:
                self.send({"type": "session.update", "event_id": "configure", "session": realtime_config()},
                          text_context=True)
            self.data["status"] = "running"
            while not self.finished:
                now = self.now()
                should_finish = now > 50
                if self.kind == "live" and self.correction_end is not None:
                    should_finish |= now > self.correction_end + 12
                if self.kind == "realtime" and self.completed_after_correction is not None:
                    should_finish |= now > self.completed_after_correction + 2
                if should_finish and self.close_sent is None:
                    if self.kind == "live" and self.ready:
                        self.send({"type": "session.close"})
                        self.close_sent = now
                    else:
                        self.finished = True
                        break
                if self.close_sent is not None and now > self.close_sent + 8:
                    self.data["status"] = "finalization_timeout"
                    break
                self.pump_audio()
                try:
                    raw = self.ws.recv()
                except websocket.WebSocketTimeoutException:
                    continue
                if not raw:
                    self.data["status"] = "transport_closed"
                    break
                self.receive(json.loads(raw))
            if self.data["status"] == "running":
                self.data["status"] = "completed"
        except StopProbe as exc:
            self.data["status"] = str(exc)
        except Exception as exc:
            self.data["status"] = "failed"
            self.data["failure"] = safe_exception(exc)
            failure = self.data["failure"]
            err = failure.get("error", {})
            self.data["access_or_billing_blocked"] = (failure.get("http_status") in (401, 403, 404, 429)
                                                       or err.get("billing_or_quota") or err.get("access_denied"))
        finally:
            # shutdown does not wait for a WebSocket close handshake or emit further requests.
            if self.ws is not None:
                self.ws.shutdown()
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, previous)
            self.data["elapsed_s"] = round(self.now(), 3)
            self.data["max_input_send_gap_ms"] = round(self.max_send_gap_ms, 2)
            self.data["injected_text_bytes"] = self.text_bytes
            self.data["responses_requested"] = self.responses_requested
            self.data["input_stream_seconds"] = self.data["input_audio_bytes"] / (RATE * 2)
            self.data["received_audio_seconds"] = self.data["audio_received_bytes"] / (RATE * 2)
            self.data["output_pcm_sha256"] = self.output_hasher.hexdigest()
            self.data["first_audio_arrival_ms"] = None if self.first_audio is None else round(self.first_audio * 1000, 1)
            self.data["first_audio_minus_request_end_ms"] = (
                None if self.first_audio is None or self.request_end is None else
                round((self.first_audio - self.request_end) * 1000, 1))
            self.data["cost"] = calculate_cost(self.kind, self.data)


def calculate_cost(kind, data):
    if kind == "live":
        usage = data.get("usage")
        seconds = usage.get("seconds") if isinstance(usage, dict) else None
        return {"reported_usage_usd": None if seconds is None else seconds * .05 / 60,
                "wall_clock_ceiling_usd": HARD_SECONDS * .05 / 60,
                "rate_usd_per_minute": .05, "backend_inference_usd": 0}
    total = 0
    for usage in data["usage"]:
        inp = usage.get("input_token_details", {})
        out = usage.get("output_token_details", {})
        # No cached discount claimed. If detail is missing, use the expensive audio rate.
        audio_in = inp.get("audio_tokens", usage.get("input_tokens", 0))
        text_in = inp.get("text_tokens", 0)
        audio_out = out.get("audio_tokens", usage.get("output_tokens", 0))
        text_out = out.get("text_tokens", 0)
        total += (audio_in * 10 + text_in * .6 + audio_out * 20 + text_out * 2.4) / 1_000_000
    return {"reported_usage_uncached_upper_usd": round(total, 8),
            "complete_usage": data["event_counts"].get("response.done", 0) == data["responses_requested"],
            "reservation_usd": RESERVATION_USD[kind], "transcription_inference_usd": 0}


def self_check():
    checks = []
    try:
        require_opt_in({})
        raise AssertionError("opt in not enforced")
    except StopProbe:
        checks.append("opt_in_denied_without_network_or_key_read")
    require_opt_in({"EVENER_VOICE_LIVE": "1"})
    scratch = Path(os.environ["EVENER_SCRATCH_DIR"])
    with tempfile.TemporaryDirectory(prefix="voice-selfcheck-", dir=scratch) as tmp:
        source = Path(tmp) / "fake.env"
        fake = "not-a-real-key-0123456789"
        contents = ("UNRELATED=not-sourced\nOPENAI_API_KEY='" + fake + "'\n").encode()
        source.write_bytes(contents)
        assert copied_key(source, tmp) == fake
        assert source.read_bytes() == contents
        assert list(Path(tmp).iterdir()) == [source]
        checks.append("private_copy_removed_source_unchanged_literal_only")
        assert fake not in json.dumps(redact({"value": fake}, fake))
        assert "sk-examplecredential" not in json.dumps(redact("sk-examplecredential", fake))
        checks.append("exact_key_and_token_redaction")
    result = {"attempts": []}
    for kind, retry in [("live", False), ("realtime", False), ("realtime", True), ("realtime", True)]:
        reserve(result, kind, retry)
    assert sum(a["reserved_usd"] for a in result["attempts"]) <= BUDGET_USD
    try:
        reserve(result, "live", True)
        raise AssertionError("attempt limit not enforced")
    except StopProbe:
        checks.append("four_attempt_and_two_retry_budget_bound")
    blocked = {"attempts": [{"kind": "live", "corrective_retry": False,
                             "reserved_usd": .05, "access_or_billing_blocked": True}]}
    try:
        reserve(blocked, "live", True)
        raise AssertionError("blocked access retried")
    except StopProbe:
        checks.append("blocked_access_not_retried")
    billed = {"attempts": [{"kind": "live", "corrective_retry": False, "reserved_usd": .05,
                            "error": {"billing_or_quota": True}}]}
    try:
        reserve(billed, "realtime", False)
        raise AssertionError("billing block tried another candidate")
    except StopProbe:
        checks.append("billing_or_quota_blocks_all_candidates")
    assert HARD_SECONDS <= 90 and MAX_RESPONSES * MAX_OUTPUT_TOKENS == 768
    checks.append("hard_session_and_output_caps")
    config = realtime_config()
    vad = config["audio"]["input"]["turn_detection"]
    assert vad["create_response"] is False and vad["interrupt_response"] is False
    assert config["max_output_tokens"] == MAX_OUTPUT_TOKENS
    # Worst modality rates, no cache discount, 1024-token protocol margin, byte/token upper assumption.
    input_bound = MAX_CONTEXT_TEXT_BYTES + HARD_SECONDS * 10 + MAX_RESPONSES * MAX_OUTPUT_TOKENS + 1024
    cost_bound = MAX_RESPONSES * (input_bound * 10 + MAX_OUTPUT_TOKENS * 20) / 1_000_000
    assert cost_bound < RESERVATION_USD["realtime"]
    checks.append("manual_responses_and_conservative_cost_reservation")
    player = SimulatedPlayer()
    player.append("a", 48000, 0)
    interrupted = player.interrupt(.1239)[0]
    assert interrupted["audio_end_ms"] == 123 and interrupted["physical_playback"] is False
    player.append("a", 48000, .2)
    assert player.items["a"]["queued_ms"] == 0
    checks.append("simulated_playout_uses_elapsed_received_audio_and_discards_late_packets")
    player = SimulatedPlayer()
    player.append("spoken-preface", 48000, 0)
    player.append("status-result", 48000, .2)
    interrupted = player.interrupt(.4)
    assert len(interrupted) == 2
    assert interrupted[0]["audio_end_ms"] == 400
    assert interrupted[1]["audio_end_ms"] == 0
    checks.append("single_fifo_does_not_play_preface_and_status_simultaneously")
    player = SimulatedPlayer()
    player.append("drained", 4800, 0)
    assert player.interrupt(.2) == []
    player.append("drained", 4800, .3)
    assert player.items["drained"]["queued_ms"] == 0
    class Sink:
        def send(self, _payload):
            pass
    sample = Measurement("live", {}, {"correction": bytes(FRAME_BYTES)})
    sample.ws, sample.ready, sample.next_frame = Sink(), True, 0
    sample.request_end, sample.first_result_audio = 1, 1
    sample.now = lambda: 2
    sample.pump_audio()
    assert sample.correction_start is None
    sample.now = lambda: 2.1
    sample.data["transcripts"]["output"] = "Alpha"
    sample.pump_audio()
    assert sample.correction_start == 2.1
    sample.receive({"type": "session.output_audio.delta", "delta": base64.b64encode(bytes(960)).decode()})
    assert "first_signal_arrival_ms" not in sample.data
    sample.receive({"type": "session.output_audio.delta", "delta": base64.b64encode(struct.pack("<h", 200)).decode()})
    assert sample.data["first_signal_arrival_ms"] == 2100
    checks.append("live_correction_requires_result_text_and_silence_is_not_signal")
    print(json.dumps({"self_check": "passed", "checks": checks}, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--self-check", action="store_true")
    parser.add_argument("--mode", choices=["inventory", "live", "realtime"])
    parser.add_argument("--key-source", type=Path)
    parser.add_argument("--scratch", type=Path)
    parser.add_argument("--out", type=Path)
    parser.add_argument("--corrective-retry", action="store_true")
    args = parser.parse_args()
    if args.self_check:
        self_check()
        return
    require_opt_in(os.environ)
    if not all((args.mode, args.key_source, args.scratch, args.out)):
        raise StopProbe("mode_key_source_scratch_out_required")
    scratch = args.scratch.resolve()
    if not scratch.is_relative_to(Path(os.environ["EVENER_SCRATCH_DIR"]).resolve()):
        raise StopProbe("scratch_must_be_under_session_scratch")
    scratch.mkdir(mode=0o700, parents=True, exist_ok=True)
    results = json.loads(args.out.read_text()) if args.out.exists() else {
        "schema": "evener-hosted-voice-spike-v1", "sample_count_per_candidate": 1,
        "budget_usd": BUDGET_USD, "attempts": []}
    if results.get("schema") != "evener-hosted-voice-spike-v1":
        raise StopProbe("unknown_results_file")
    key = copied_key(args.key_source, scratch)
    if args.mode == "inventory":
        results["inventory"] = inventory(key)
        save(args.out, results, key)
        print(json.dumps(results["inventory"], indent=2))
        return
    access = results.get("inventory", {})
    if access.get("http_status") != 200 or not access.get("requested_models", {}).get(MODELS[args.mode]):
        raise StopProbe("listed_model_inventory_required")
    clips, metadata = synthesize(scratch)
    results["synthetic_clips"] = metadata
    attempt = reserve(results, args.mode, args.corrective_retry)
    attempt["utc_started"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    attempt["probe_sha256"] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    save(args.out, results, key)  # Reserve persistently before opening a billable session.
    measurement = Measurement(args.mode, attempt, clips)
    measurement.run(key)
    save(args.out, results, key)
    summary = {k: v for k, v in attempt.items() if k not in ("events", "usage")}
    print(json.dumps(redact(summary, key), indent=2))
    if attempt["status"] != "completed":
        raise SystemExit(2)


if __name__ == "__main__":
    try:
        main()
    except StopProbe as exc:
        print(json.dumps({"stopped": str(exc)}))
        raise SystemExit(2)
    except Exception as exc:
        print(json.dumps({"failure": safe_exception(exc)}))
        raise SystemExit(2)
