# 8.4 — Corpus harvesting from recorded traffic — implementation plan

**Status:** PLANNED, decisions locked (see §8). **Charter:** design doc §8.4 (`docs/design/fuzzing-toolkit-design.md`). **Branch:** `wip/fuzzing-toolkit`. **Size:** ~500–700 LoC — the two new recorders (decision 4), jobs harvesting (decision 7), and the gitleaks gate (decision 5) push it well past the original ~250–400 estimate; sanitization rigor stays the load-bearing part.

serf already records real provider traffic and full conversation transcripts. A tool that scrubs those into seed corpora beats hand-written `f.Add` seeds by orders of magnitude — real provider framing quirks, real tool-argument shapes, for free. This is the highest-leverage tooling item: it multiplies the yield of every existing single-input target without writing a single new target.

## Ordering & dependency (read first)

- **8.4 runs BEFORE 8.1.** 8.1 (persistence round-trip + replay-idempotence targets, `docs/design/plans/01-persistence-roundtrip-targets.md`) depends on *this* item's seeds — specifically the `jobs.jsonl` (`jobstore.Event`) seeds this harvester now emits (decision 7, §1.4). This reverses the tentative sequencing in the design doc's build-order note (§"Build order"), which had 8.1 first; the decisions below take precedence. Build 8.4, land its seeds, then build 8.1 on top of them.
- **The recorders are a prerequisite sub-deliverable of 8.4 (decision 4).** appwire frames and inbound hub HTTP are *never written to disk today*, so before the harvester can produce real seeds for the `appwire` and `http` surfaces, this item first builds two opt-in, default-off recorders (§1.3). The recorders ship first within 8.4; the harvester's appwire/http surfaces consume what they record. The recorder is new plumbing 8.4 now owns — it is not deferred to a separate item.

## Decisions folded in (verbatim intent — do not re-litigate; see §8 for the resolved questions)

1. **Commit everything harvested**, shape-scrubbed so it is airtight, as durable seeds — not a small hand-picked subset. Shape-scrub + content-hash dedup (§4) collapses near-identical happy-path traffic to one file per distinct scrubbed shape, so "everything" stays naturally bounded.
2. **Seeds land in native `testdata/fuzz/<FuzzName>/`** (Go auto-loads them under `make fuzz`); `fuzz/corpus/` is reserved for hand-written OWASP vectors only.
3. **Shape-scrub is the default** (every string/number leaf → synthetic placeholder, structure/framing/enum values preserved — destroys PII/secrets by construction). `--keep-values` (real values) is **LOCAL-ONLY, never committed**, and is gated (decision 6).
4. **Build the recorders first** — an opt-in WebSocket frame recorder + a hub HTTP request recorder (§1.3), both default-off, so the appwire and http surfaces get real recorded seeds.
5. **Secret scanner = gitleaks**, introduced repo-wide here (none exists in CI today): a `make` secret-scan target plus a write-time self-check the harvester runs with the same engine, aborting on any hit.
6. **Personal `~/.serf` is an allowed source, but shape-scrub is FORCED on it.** `--keep-values` is permitted only in a designated capture environment (§2, §3.4).
7. **Fold `jobs.jsonl` harvesting into this tool** — the harvester also emits `jobstore.Event` seeds for 8.1's jobstore targets (§1.4).

## 0. What the charter named vs. what the code actually has

Charter: "serf already records `RawRequestBody`/`RawResponseBody` and full transcripts. A tool that sanitizes those into `fuzz/corpus/` seeds…" Verified against code — with two corrections the implementer must not skip:

- **`RawRequestBody`/`RawResponseBody` are NOT persisted as those fields.** They are `Response` fields tagged `json:"-"` (`llm/types.go:471-472`) — deliberately excluded from `api.jsonl` to avoid bloat. They are persisted **only** when `SERF_LOG_RAW_HTTP=1` (`llm.RawBodyEnabled`, `llm/apilog.go:196-209`), and then to a **separate file** `api-raw.jsonl` as `APIRawLogEntry.{RequestBody,ResponseBody}` (`llm/apilog.go:177-192`, written by `writeRaw`/`writeRawResponse` `:398-450`). For streaming calls `ResponseBody` holds the **verbatim SSE stream** (e.g. `anthropic/adapter.go:640` `rp.RawResponseBody = sseBuf.String()`). This is the gold mine for SSE seeds.
- **The default-on record is `api.jsonl` (`APILogEntry`), which carries no raw bodies** — only metadata + the parsed `Raw map[string]any` and the `Tools []ToolDefinition` schema list (`llm/apilog.go:118-174`). Useful for tool *schemas*, not for raw decode-surface bytes.

So the two harvest substrates are:

| On-disk file | Type / writer | Contains | Always present? |
|---|---|---|---|
| `<stateDir>/api-raw.jsonl` | `llm.APIRawLogEntry` (`llm/apilog.go:177`) | verbatim HTTP request body + response body (SSE for streams), `provider`, `model`, `mode` | **only if `SERF_LOG_RAW_HTTP=1`** |
| `<stateDir>/sessions/<SID>.transcript.jsonl` | `transcript.{Header,Entry,APICall}` (`agent/transcript/transcript.go:27/54/61`) | every `schema.Turn` incl. `llm.Message.Content[].ToolCall.{Name,Arguments}` | **always** (written whenever `stateDir != ""`) |

`<stateDir>` resolves via `cmdutil.DefaultStateRoot()` (`cmdutil/statedir.go:17`): `$SERF_STATE_DIR`, else `~/.serf`. Directory layout (confirmed): `api.jsonl` + `api-raw.jsonl` at the root; per session `sessions/<SID>.transcript.jsonl` (file), `sessions/<SID>.meta.json`, and `sessions/<SID>/jobs.jsonl` (`agent/jobs.go:306` `jobsDir` returns the `sessions/<SID>` dir; `newJobManager` appends `jobs.jsonl`). The jobs event log is `jobstore.Event`, one JSON object per line (`agent/internal/jobstore/event.go:33`, written/read via `ReadEvents` `read_events.go:16`, replayed by `Fold` `fold.go:12`). Per **decision 7** it **is** harvested here now (§1.4) — as raw NDJSON lines, no typed decode needed — to seed 8.1's jobstore-Event decode/Fold targets.

Two new substrates are introduced by **decision 4** (the recorders, §1.3): `<stateDir>/appwire-frames.jsonl` and `<stateDir>/hub-http.jsonl`, both written only when their opt-in recorder is enabled (default off, exactly like `api-raw.jsonl`). They are what let the previously-dead `appwire` and `http` surfaces produce real seeds.

**Module-boundary note (verified).** `agent/internal/jobstore` is internal to the `agent` module and cannot be imported from `cmd/serf-fuzz-harvest` (root module) — but the harvester never imports it: a decode-target seed is just raw `[]byte`, so `jobs.jsonl` is read line-by-line as opaque JSON and scrubbed structurally, the same treatment `api-raw.jsonl` and the appwire/http recorder logs get. Only the `toolargs` surface needs typed access, and it uses the *exported* `agent/transcript` types.

## 1. Artifact → target map

| Corpus surface | Target(s) it seeds | Recorded source | Status |
|---|---|---|---|
| **sse** | `llm.FuzzParseSSE` (`llm/sse_fuzz_test.go:52`); provider metamorphic decoders `openai.FuzzOpenAIResponsesMetamorphic` (`llm/providers/openai/responses_fuzz_test.go:101`) + `openai.FuzzOpenAIChatCompletionsMetamorphic`, `anthropic.FuzzAnthropicStreamMetamorphic`, `google.FuzzGeminiStreamMetamorphic`, `openaicompat.FuzzOpenAICompatStreamMetamorphic` (`*/stream_fuzz_test.go`) | `api-raw.jsonl` `response_body` where `mode=="stream"`, routed by the `provider` field | **RICH** — primary value, available today |
| **toolargs** | `agent.FuzzToolArgsValidate` (`agent/tool_args_fuzz_test.go:29`), input `(nameIndex int, argsBytes []byte)` | transcript `Entry.Turn.Message.Content[].ToolCall.{Name,Arguments}` (`llm/types.go:159-164`); secondarily `api-raw.jsonl` request bodies | **RICH** — available today |
| **appwire** | `appwire.FuzzMessageDecode` (`appwire/jsonrpc_fuzz_test.go:14`), `appwire.FuzzMethodParams` (`appwire/params_fuzz_test.go:17`) | `appwire-frames.jsonl` — produced by the new WS frame recorder (decision 4, §1.3) | **SUPPORTED once the recorder is enabled** |
| **http** | `FuzzWebHandler` (`cmd/serf-hub/web_fuzz_test.go:143`), input `(routeIdx uint8, suffix string)` | `hub-http.jsonl` — produced by the new HTTP recorder (decision 4, §1.3), reverse-mapped to `(routeIdx, suffix)` | **SUPPORTED once the recorder is enabled** |
| **jobs** | 8.1's jobstore-Event decode + `Fold` targets (name TBD by 8.1) | `sessions/<SID>/jobs.jsonl` (`jobstore.Event`, one JSON object/line), read as raw NDJSON (decision 7, §1.4) | **SUPPORTED — extractor ships here; 8.1 adds the target** |

**Be explicit (the previous honest gap, now closed by decision 4):** appwire frames cross the WebSocket and inbound hub HTTP requests were **never written to disk verbatim** — no frame log, no access log existed (a grep for any non-test `OpenFile`/frame recorder under the appwire flow or hub handler returns nothing). Rather than fabricate wire frames from transcript-derived structures (they are *not* wire frames), this item **builds the missing recorders** (§1.3) so the appwire and http surfaces get real, recorded seeds. Hand-written + OWASP vectors in `fuzz/corpus/` remain the floor; the recorder-fed seeds add the real-traffic layer on top.

### 1.1 SSE extraction (`sse/`)
For each `api-raw.jsonl` line: decode `APIRawLogEntry`; skip unless `Mode == "stream"` and `ResponseBody != ""`. The `ResponseBody` *is* the seed for `FuzzParseSSE` (provider-agnostic). For the provider metamorphic targets, route by `Provider`: `openai`/`openai-responses` → openai target's testdata, `anthropic` → anthropic's, etc. Error-body responses (a 4xx/5xx JSON instead of an SSE stream) are kept too — they are valid adversarial input for the decoders, just tagged `non-sse` in the dedup signature so they don't crowd out real streams.

### 1.2 toolargs extraction (`toolargs/`)
For each transcript: read the NDJSON (`transcript.Header` first line, then `kind:"entry"`→`transcript.Entry` / `kind:"api_call"`→`transcript.APICall`; we only need entries). For each `Entry.Turn.Message.Content` part with `Kind == ContentToolCall`, take `ToolCall.Name` + `ToolCall.Arguments` (a `json.RawMessage`). The target's input is `(nameIndex int, argsBytes []byte)`; map `Name` → `nameIndex` by matching against the live core-tool name list (the same `coreToolSchemas` registry the target builds, `agent/tool_args_fuzz_test.go:83`). The target indexes its table as `nameIndex % len(names)`, so the harvester must emit the *exact* index of the matched name (not just any value); a tool name with no registered match is dropped (it can't address the target's table). `argsBytes` = the scrubbed `Arguments` bytes.

### 1.3 The recorders (decision 4 — prerequisite sub-deliverable)
Two opt-in, default-off recorders write the verbatim wire bytes the appwire/http fuzz targets consume. They follow the exact precedent of `api-raw.jsonl`: gated by an env var, write raw (unscrubbed) bytes to a file under `<stateDir>`, never on the request hot path's correctness, and never committed. Scrubbing happens later, in the harvester (the single scrub chokepoint), not in the recorder.

**(a) WebSocket frame recorder → `<stateDir>/appwire-frames.jsonl`.** Hook point verified: `appwire.WSTransport` is the single decode/encode boundary for every frame, used by both the client (`DialWebSocket`) and the server-accept side (`internal/appserver/websocket.go:47` wraps the accepted conn via `appwire.NewWSTransport`; its recv loop is `ServeWebSocket` `:64-65`).
  - **Inbound (the seed gold):** in `WSTransport.Recv` (`appwire/ws_transport.go:46-55`), the raw frame is `data` at `:47` (`_, data, err := t.conn.Read(ctx)`), *before* `json.Unmarshal(data, &msg)` at `:52`. Record `data` here: it is byte-for-byte what `FuzzMessageDecode` feeds to `json.Unmarshal(raw, &Message{})` (→ `Message.UnmarshalJSON`, `appwire/jsonrpc.go:145`). On the server this captures client→server request/notification frames — the richest decode surface.
  - **Outbound:** in `WSTransport.Send` (`appwire/ws_transport.go:38-44`), record the marshaled `data` at `:39` before `t.conn.Write`. These are server→client responses/notifications — also valid `Message` frames.
  - **Per-method params (`FuzzMethodParams`):** no separate capture needed — the recorded inbound request frame already carries the `params` object; the harvester decodes the frame's method name + `params` and emits `(methodIndex, paramsBytes)` against the `appwire.Methods` catalog (`appwire/protocol.go:85`).
  - **Gating:** off unless a new env var (proposed `SERF_RECORD_APPWIRE=1`) is set when the transport is constructed. One small recorder type (open-append a JSONL file, one line per frame: `{"dir":"recv"|"send","frame":<raw bytes as a JSON string>}`), wired in `NewWSTransport`.

**(b) Hub HTTP recorder → `<stateDir>/hub-http.jsonl`.** Hook point verified: `WebServer.Handler()` (`cmd/serf-hub/web.go:150`) returns `auth(httpsec.CSPMiddleware(mux))` at `:208-209`; there is **no** existing access-log/middleware. Wrap the whole stack in one `func(http.Handler) http.Handler` recorder middleware: `return recorder(auth(httpsec.CSPMiddleware(mux)))`. It captures method, path, query, headers, and a (size-capped) body copy per inbound request, off unless a new env var (proposed `SERF_RECORD_HTTP=1`) is set.
  - **Mapping constraint (important — verified against the target):** `FuzzWebHandler` does **not** consume raw HTTP bytes. Its input is `(routeIdx uint8, suffix string)`; `buildFuzzRequest` (`cmd/serf-hub/web_fuzz_test.go:107-121`) builds a GET against `fuzzReadOnlyRoutes[routeIdx % len]` (the allowlist at `:34-52`) with `suffix` appended (or, for `/doc/file`, as the `?path=` query). So the http harvester must **reverse-map** each recorded request: longest-prefix-match the recorded path against `fuzzReadOnlyRoutes` → `routeIdx`, the path remainder (or `?path=`) → `suffix`; **drop** any recorded request whose method is not GET or whose path matches no allowlisted route (the target cannot drive it). This is the honest seed shape — the recorder logs everything, the harvester keeps only what the target can replay.

### 1.4 jobs extraction (`jobs/`, decision 7)
For each `sessions/<SID>/jobs.jsonl`: read it line by line; each non-empty line is one `jobstore.Event` as JSON. The seed for 8.1's jobstore-Event decode target is the **raw scrubbed line bytes** — no typed decode and no `jobstore` import (it is `internal` to the `agent` module; §0 module-boundary note). The harvester also concatenates a whole session's scrubbed event sequence as a seed for the `Fold` / replay-idempotence target (a sequence seed, written once 8.1 defines the target). Destination `testdata/fuzz/<FuzzName>/` is registered in the harvester's surface→target table when 8.1 names its targets; until then `--surface jobs` emits to `--out-root`'s staging path and logs the count, so the extractor is exercisable now (acceptance §7) and 8.1 only has to point it at the real testdata dir.

## 2. Tool location & shape

**Location: `cmd/serf-fuzz-harvest` (a serf-module command), reusing `agent/doctor`'s session locator.** Rationale:
- The harvester must import serf types — `llm.APIRawLogEntry`, `agent/transcript.{Header,Entry,APICall}`, `llm.ToolCallData`, the core-tool registry. So it **cannot** live in the dep-free `fuzz/` module (§5 portability rule: "nothing in `fuzz/` imports serf"). The promoter/schemagen stay pure; the harvester is serf-coupled by nature.
- `serf-doctor` already walks sessions/transcripts/jobs via `agent/doctor` (`agent/doctor/locate.go`) and reads recorded state; reuse its locator for multi-bucket discovery instead of re-implementing path-walking. The transcript types in `agent/transcript` are exported, so the harvester reads the NDJSON directly with stock `encoding/json` (no unexported reader needed).

Surface:
```
serf-fuzz-harvest \
  [--state-dir DIR ...]            # default: cmdutil.DefaultStateRoot(); repeatable for multi-bucket
  [--out-root DIR]                 # default: repo root; seeds land under each target's testdata/fuzz/<Name>/
  [--surface sse,toolargs,appwire,http,jobs]  # default: all surfaces with a present source
  [--keep-values]                  # GATED: skip shape-scrub (real values). Refused unless the
                                   #   designated-capture-env marker is set, AND forced off for any
                                   #   personal ~/.serf source (decision 6, §3.4). Local-only; never committed.
  [--max-seed-bytes N]             # default 32768 (oversize dropped, not truncated — §4)
  [--dry-run]                      # report counts + would-write paths, write nothing
```
**No `--max-per-surface` cap, no per-bucket diversity cap (decision 1):** commit *everything* harvested. Shape-scrub + content-hash dedup (§4) already collapses the thousands of near-identical happy-path streams/requests into one file per distinct scrubbed shape, so "everything" is self-bounding without discarding structurally distinct inputs.

Output: a one-line-per-surface summary (`sse: scanned 412 stream bodies → 37 unique scrubbed seeds / 18 oversized / 0 secret-aborts`) — context-managed per the automation rules; full per-seed provenance to a `--log` file on request.

## 3. Sanitization — the crux

Recorded traffic carries secrets and PII: request bodies embed the full prompt (user text, pasted credentials, file contents read by `read_file`, shell output that may include tokens); response bodies echo model output; tool arguments are model-generated but often quote user/file content. Auth headers are **not** a concern in this substrate — `api-raw.jsonl` stores HTTP **bodies only** (`RawRequestBody = string(b)` over the marshaled request body, e.g. `openai/adapter.go:541`), never headers, and Google's URL-embedded API key is stripped to host+path before logging (`StampEndpointURL`, `llm/apilog.go:587-602`, and that lives in `api.jsonl`, not the raw file). The risk is **inside the JSON/SSE payloads.**

Two layers, applied in order, on every byte before it is written:

### 3.1 Layer A — shape-preserving value scrub (default; the airtight one)
The fuzz targets exercise **framing and structure**, not the semantic content of string leaves. So the strongest sanitization is to **destroy all original free-text by construction** while preserving everything the parser cares about:
- Parse the payload structurally (JSON for tool args & most bodies; for SSE, parse frame-by-frame and scrub each `data:` JSON payload, leaving the SSE framing — `event:`, blank-line boundaries, comments, `[DONE]` — byte-for-byte intact).
- Walk the JSON: **keep every key, every type, every array length, every structural nesting.** Replace each *string leaf* with a synthetic placeholder of the same length-class (e.g. `"xxxx…"` sized to a bucket: ≤8, ≤64, ≤512, else 512) and each *number leaf* with a fixed sentinel of the same kind (int→`0`, float→`0.0`), **except** a small allowlist of structural enum keys that the decoders branch on (`type`, `role`, `finish_reason`, `status`, `kind`, `object`, SSE event names) whose *values* are framing, not content, and are preserved verbatim.
- Net: no original user/model free-text survives, so PII and secrets cannot leak — yet the decode/round-trip/accumulate oracles still see a structurally faithful stream.

Trade-off (state it plainly): shape-scrub also erases value-level provider quirks (a weird unicode escape inside a delta, a malformed number). That is the cost of an airtight committed corpus. `--keep-values` opts out for **local-only** campaigns and still runs Layer B + the external scanner; its output is never committed.

### 3.2 Layer B — pattern redaction + entropy quarantine (always on, both modes)
Even under shape-scrub (belt-and-suspenders, and the only net under `--keep-values`), run every output byte through:
- **Known-secret regexes**, replaced with `REDACTED`: OpenAI `sk-[A-Za-z0-9_-]{20,}` / `sk-ant-…`; Google `AIza[0-9A-Za-z_-]{35}`; AWS `AKIA[0-9A-Z]{16}`; GitHub `gh[pousr]_[A-Za-z0-9]{36}` / `github_pat_…`; Slack `xox[baprs]-…`; JWTs `eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`; PEM `-----BEGIN [A-Z ]*PRIVATE KEY-----`…`-----END…-----`; bearer-token and `api[_-]?key`/`authorization`/`x-api-key` field values; email addresses.
- **Entropy quarantine:** any surviving string leaf with length ≥ 20 and Shannon entropy > 4.0 bits/char is treated as a probable secret → the **whole seed is dropped** (quarantined, logged), never written. Under shape-scrub this should essentially never fire (leaves are synthetic); under `--keep-values` it is the real safety net.

### 3.3 Verification — must be airtight (scanner = gitleaks, decision 5)
A seed leaks nothing only if we *prove* it. **gitleaks is the canonical scanner** — none exists in CI today, so this item introduces repo-wide secret scanning. Three gates:
1. **In-process self-check (write-time):** before writing each seed, re-run the Layer-B detectors in *detect* mode over the final bytes; a single hit aborts that seed (drop + log), and any hit at all fails the whole run with a non-zero exit. Redaction and detection share one regex set so they cannot drift, and that set is kept in lockstep with the gitleaks ruleset so the two engines agree (the harvester shells out to `gitleaks detect --no-git` over the staged output dir as the final pre-write barrier, so the *same engine* that gates the repo also gates the writer — no drift possible).
2. **Corpus gate (`make fuzz-corpus-scan`):** `gitleaks detect --no-git` over every committed corpus dir (the per-target `testdata/fuzz/` trees the harvester writes + `fuzz/corpus/`), wired into `make lint`/the gate so a leak can never land.
3. **Repo gate (`make secret-scan`):** `gitleaks detect --no-git` over the whole repo — the new repo-wide secret scan decision 5 calls for, wired into the gate. (`fuzz-corpus-scan` is the corpus-scoped subset for fast harvester feedback.)

The implementer adds a committed `.gitleaks.toml` (the shared ruleset), the two `make` targets, and the CI hook. All three gates are tested adversarially: a fixture transcript/raw-log/recorder-log seeded with a planted `sk-…` key must (a) make gitleaks red **before** redaction and (b) be absent from the written seed **and** green after — proving the pipeline actually strips, not just that clean input stays clean.

### 3.4 Source policy — personal state vs. capture environment (decision 6)
- **Personal `~/.serf` is an allowed source, but shape-scrub is FORCED on it.** Any `--state-dir` that resolves to the personal default state root (`cmdutil.DefaultStateRoot()` with no `$SERF_STATE_DIR` override, i.e. `~/.serf`) is harvested with Layer A shape-scrub unconditionally; `--keep-values` is **ignored** (and logged as ignored) for that source. PII/secrets in a developer's own state can never become real-value seeds.
- **`--keep-values` is permitted only in a designated capture environment.** It is refused (non-zero exit, no output) unless a designated-capture-env marker is present — a new opt-in signal (proposed env var `SERF_FUZZ_CAPTURE_ENV=1`, set only on the dedicated capture box). Even there, output is local-only and never committed, and still runs Layer B + the gitleaks self-check. This makes the airtight committed corpus the default everywhere and real-value capture a deliberate, isolated act.

## 4. Determinism & corpus file format

**Format (Go-native, verified against the two files already in-tree).** A Go fuzz corpus file is the literal text:
```
go test fuzz v1
[]byte("data:\a")
```
— a `go test fuzz v1` header line, then **one Go-literal line per fuzz argument** in declaration order. Single-`[]byte` targets (`FuzzParseSSE`, `FuzzMessageDecode`, the provider SSE targets) get one `[]byte(...)` line. Two-arg targets (`FuzzToolArgsValidate(nameIndex int, argsBytes []byte)`, `FuzzMethodParams`) get two lines — `int(N)` then `[]byte(...)`. Existing in-tree examples: `llm/testdata/fuzz/FuzzParseSSE/f17d1670fa834b2b` and `llm/providers/openai/testdata/fuzz/FuzzOpenAIChatCompletionsMetamorphic/b4da5124609623d8`.

**Where to write — write straight into each target's `testdata/fuzz/<FuzzName>/`** (same module as the target), **not** `fuzz/corpus/<surface>/` (decision 2). Two reasons: (1) Go's toolchain auto-loads `testdata/fuzz/<FuzzName>/` as **seed corpus** under plain `go test` (no `-fuzz`), so harvested seeds run in `make fuzz` for free with zero loader code; (2) a loader living in the `fuzz/` module could not be imported by the per-module targets anyway (go.work does not span modules — design §1). The pre-created `fuzz/corpus/<surface>/` dirs (`http`, `toolargs`, `sse`, `appwire` exist today) are kept **only** for the module-agnostic hand-written OWASP vectors; the harvester never writes there. Destination dirs the harvester targets (verified present or auto-created): `llm/testdata/fuzz/FuzzParseSSE/`, `llm/providers/openai/testdata/fuzz/FuzzOpenAIResponsesMetamorphic/` (and the sibling provider targets), `agent/testdata/fuzz/FuzzToolArgsValidate/`, `appwire/testdata/fuzz/{FuzzMessageDecode,FuzzMethodParams}/`, `cmd/serf-hub/testdata/fuzz/FuzzWebHandler/`, and 8.1's jobstore-Event target dir.

**Encoding.** Emit `[]byte(%q)` via `strconv.Quote` (which is the inverse of the loader's `strconv.Unquote`) and `int(%d)`; do not hand-roll escaping. The determinism acceptance test is simply that `go test -run '^Fuzz' ./...` loads every written file without a corpus-parse error.

**Dedup is the diversity reducer (decision 1).** Filename = the lowercase hex of the SHA-256 of the encoded file body (matching Go's own content-addressed scheme); identical seeds collide to one file. This is what makes "commit everything" tractable: under shape-scrub, two streams/requests that differ only in their free-text/number leaves scrub to **byte-identical** output and therefore hash to the same filename — so the thousands of near-identical happy-path captures collapse to one committed file *per distinct structural shape*, with no signature-bucketing heuristic and no information-losing per-bucket cap. Structurally distinct inputs are all kept. An in-run set plus an on-disk `Stat` skip make re-runs idempotent (a second harvest of the same state produces an empty diff).

**Size bound (the only drop rule).** Drop seeds over `--max-seed-bytes` (default 32 KiB; SSE streams can be large — truncation is unsafe mid-frame, so oversize is dropped, not clipped). No count cap and no diversity cap (decision 1).

## 5. Build steps

**Phase A — the recorders (prerequisite, decision 4; build and land first).**
1. **WS frame recorder** — a small recorder type (open-append JSONL, one line per frame), wired into `appwire.NewWSTransport` (`appwire/ws_transport.go:33`) so `Recv` records `data` at `:47` and `Send` records `data` at `:39`; gated on `SERF_RECORD_APPWIRE=1`; writes `<stateDir>/appwire-frames.jsonl`. Default-off, no behavior change when unset. (~50 LoC + tests)
2. **Hub HTTP recorder** — a `func(http.Handler) http.Handler` middleware wrapping the stack at `cmd/serf-hub/web.go:208-209` (`recorder(auth(httpsec.CSPMiddleware(mux)))`); gated on `SERF_RECORD_HTTP=1`; captures method/path/query/headers/body (size-capped) to `<stateDir>/hub-http.jsonl`. Default-off. (~50 LoC + tests)

**Phase B — the harvester (`cmd/serf-fuzz-harvest`).**
3. **`main.go`** — flag parsing, state-dir resolution (`cmdutil.DefaultStateRoot`), capture-env / personal-source policy enforcement (§3.4), `agent/doctor` locator reuse, dispatch per surface, summary output. (~80 LoC)
4. **`raw.go`** — walk `api-raw.jsonl`, decode `APIRawLogEntry`, filter `Mode=="stream"`, route by `Provider` → sse seeds. (~50 LoC)
5. **`transcript.go`** — walk `sessions/*.transcript.jsonl`, decode entries via exported `agent/transcript` types, pull `ToolCall.{Name,Arguments}`, map name→exact index → toolargs seeds. (~55 LoC)
6. **`appwire.go`** — walk `appwire-frames.jsonl` → frame seeds for `FuzzMessageDecode`; decode each frame's method+`params` against `appwire.Methods` → `(methodIndex, paramsBytes)` seeds for `FuzzMethodParams`. (~55 LoC)
7. **`http.go`** — walk `hub-http.jsonl`; reverse-map each GET request's path to `(routeIdx, suffix)` against `fuzzReadOnlyRoutes`, drop non-GET/non-allowlisted → http seeds. (~55 LoC)
8. **`jobs.go`** — walk `sessions/<SID>/jobs.jsonl` as raw NDJSON → per-line `jobstore.Event` decode seeds + per-session sequence seed for the `Fold` target; emit to the 8.1 target dir (or staging until 8.1 registers it). (~45 LoC)
9. **`sanitize.go`** — Layer A shape-scrub (JSON walker + SSE frame splitter) + Layer B regex/entropy + the write-time gitleaks self-check (§3.3). (~130 LoC; the bulk and the load-bearing part)
10. **`emit.go`** — corpus-literal encoder, content-hash filenames, dedup, size bound, write to `testdata/fuzz/<Name>/`. (~50 LoC)

**Phase C — gates & docs.**
11. **`.gitleaks.toml` + Makefile** — the shared gitleaks ruleset; add `make secret-scan` (whole repo) and `make fuzz-corpus-scan` (corpus dirs), wire both into the gate; document `serf-fuzz-harvest` + the two recorder env vars in `fuzz/README.md`. (~30 LoC)
12. **Tests** — unit tests per file + recorder tests (frame/request round-trips to the JSONL) + the adversarial planted-secret end-to-end test against gitleaks (§3.3) + a determinism test that harvests a fixture and asserts `go test -run '^Fuzz'` loads the output clean.

## 6. Dependencies & risks

**Dependencies.**
- *Source recording must have been on when the traffic flowed:* `SERF_LOG_RAW_HTTP=1` for `api-raw.jsonl` (sse); the new `SERF_RECORD_APPWIRE=1` / `SERF_RECORD_HTTP=1` for the appwire/http substrates. **toolargs and jobs harvest regardless** — transcripts and `jobs.jsonl` are always written. Recommend the nightly/capture environment runs with all four enabled.
- *gitleaks* must be available in CI for the three gates (§3.3); it is **new to this repo** (decision 5 introduces it).
- *8.1 depends on this item's jobs seeds* (§"Ordering"): build/land 8.4 first.

**Risks.**
- *Sanitization false-negative* (an unknown secret format) → mitigated structurally: under the default shape-scrub no original value survives at all, so Layer-B/entropy is a backstop, not the primary defense. The gitleaks gate is the third independent check.
- *Shape-scrub erases the value-level quirks we want to fuzz* → accepted for the committed corpus; `--keep-values` (capture-env-only, scanned, never committed) preserves them when a human is driving (§3.4).
- *http reverse-map drops real traffic* → `FuzzWebHandler` only drives an allowlist of GET routes, so recorded POSTs and off-allowlist paths cannot become seeds (§1.3b). Accepted: the alternative (a raw-bytes HTTP target) is an 8.x retarget, not this item.
- *Recorder is new code on a hot path* → kept default-off and side-effect-only (append a line; never alter the frame/request or its error path); a failed record write is logged and swallowed, never propagated.
- *testdata bloat / slower `go test`* → size bound + shape-scrub-driven dedup (§4) collapse near-duplicates; re-runs are idempotent so the set does not grow without genuinely new shapes.
- *Internal-package boundary* (`agent/internal/jobstore`) → sidestepped: jobs are harvested as raw NDJSON `[]byte`, no `jobstore` import (§0, §1.4). typed access is needed only for `toolargs`, which uses exported `agent/transcript`.
- *PII in personal `~/.serf`* → resolved by decision 6: personal sources are allowed but shape-scrub is forced and `--keep-values` ignored (§3.4).

## 7. Acceptance

- **Recorders (Phase A):** with `SERF_RECORD_APPWIRE=1` / `SERF_RECORD_HTTP=1` set, a driven hub/daemon session writes `appwire-frames.jsonl` and `hub-http.jsonl`; with them unset, neither file appears and there is no behavior change (a recorder-off regression test asserts byte-identical handler/transport behavior).
- **Harvest + load:** `serf-fuzz-harvest` over a fixture `~/.serf` (incl. fixture recorder logs + a `jobs.jsonl`) writes seeds; `go test -run '^Fuzz' ./...` in `llm`, `llm/providers/...`, `agent`, `appwire`, and the root module (`cmd/serf-hub`) loads and runs every harvested seed **green** (determinism + format correctness).
- **Known recorded edge case appears as a seed:** a captured `response.completed` stream with `status:"incomplete"` / `max_output_tokens` harvested from a fixture raw-log shows up in `llm/providers/openai/testdata/fuzz/FuzzOpenAIResponsesMetamorphic/`; a captured appwire request frame shows up in `appwire/testdata/fuzz/FuzzMessageDecode/`; an allowlisted GET shows up reverse-mapped in `cmd/serf-hub/testdata/fuzz/FuzzWebHandler/`.
- **jobs surface (decision 7):** `--surface jobs` over a fixture `jobs.jsonl` emits scrubbed `jobstore.Event` seeds (to staging until 8.1 registers its target dir), exercised in a test so 8.1 can consume them.
- **Secret-scan passes (gitleaks, decision 5):** `make secret-scan` and `make fuzz-corpus-scan` are green, and the adversarial planted-secret test proves the pipeline strips a real `sk-…` key (gitleaks red before, key absent from the written seed + gitleaks green after).
- **Commit-everything is idempotent:** re-running the harvest over unchanged state produces **no diff** (content-hash dedup), confirming "commit everything" does not churn the tree.

## 8. Resolved decisions (was: open questions)
All seven are settled; this section records the resolution so the rest of the plan is turnkey.
1. **Commit harvested corpus?** → **RESOLVED: commit everything**, shape-scrubbed (airtight), as durable seeds — not a small subset. Dedup keeps it bounded (§1 decision 1, §4). `--keep-values` regenerates a larger local-only set, never committed.
2. **`fuzz/corpus/` vs. per-target `testdata/fuzz/`?** → **RESOLVED: harvester writes native `testdata/fuzz/<FuzzName>/`** (free auto-load); `fuzz/corpus/` keeps hand-written OWASP vectors only (§4, decision 2).
3. **Shape-scrub vs. keep-values for the committed corpus?** → **RESOLVED: shape-scrub is the default; `--keep-values` is local-only** and gated (§3, §3.4, decision 3).
4. **appwire/http have no recorded source.** → **RESOLVED: build the recorders here** (decision 4). Two opt-in, default-off recorders (§1.3) are a prerequisite sub-deliverable of 8.4, not a separate item; hook points verified (`appwire/ws_transport.go:47/39`, `cmd/serf-hub/web.go:208-209`).
5. **Canonical scanner + is it in CI?** → **RESOLVED: gitleaks**, and it is **not** in CI today — this item introduces repo-wide secret scanning (`make secret-scan` + `make fuzz-corpus-scan` + write-time self-check, §3.3, decision 5).
6. **Personal `~/.serf` as a source?** → **RESOLVED: allowed, but shape-scrub is forced**; `--keep-values` only in a designated capture environment (§3.4, decision 6).
7. **jobs.jsonl as a corpus source?** → **RESOLVED: folded into this tool** (decision 7). The harvester emits `jobstore.Event` seeds for 8.1's targets as raw NDJSON (§1.4); **8.1 runs after 8.4 because it depends on these seeds** (§"Ordering").
