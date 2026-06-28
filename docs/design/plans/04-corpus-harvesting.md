# 8.4 — Corpus harvesting from recorded traffic — implementation plan

**Status:** PLANNED. **Charter:** design doc §8.4 (`docs/design/fuzzing-toolkit-design.md`). **Branch:** `wip/fuzzing-toolkit`. **Size:** ~250–400 LoC (sanitization rigor pushes it to the upper bound).

serf already records real provider traffic and full conversation transcripts. A tool that scrubs those into seed corpora beats hand-written `f.Add` seeds by orders of magnitude — real provider framing quirks, real tool-argument shapes, for free. This is the highest-leverage tooling item: it multiplies the yield of every existing single-input target without writing a single new target.

## 0. What the charter named vs. what the code actually has

Charter: "serf already records `RawRequestBody`/`RawResponseBody` and full transcripts. A tool that sanitizes those into `fuzz/corpus/` seeds…" Verified against code — with two corrections the implementer must not skip:

- **`RawRequestBody`/`RawResponseBody` are NOT persisted as those fields.** They are `Response` fields tagged `json:"-"` (`llm/types.go:471-472`) — deliberately excluded from `api.jsonl` to avoid bloat. They are persisted **only** when `SERF_LOG_RAW_HTTP=1` (`llm.RawBodyEnabled`, `llm/apilog.go:196-209`), and then to a **separate file** `api-raw.jsonl` as `APIRawLogEntry.{RequestBody,ResponseBody}` (`llm/apilog.go:177-192`, written by `writeRaw`/`writeRawResponse` `:398-450`). For streaming calls `ResponseBody` holds the **verbatim SSE stream** (e.g. `anthropic/adapter.go:640` `rp.RawResponseBody = sseBuf.String()`). This is the gold mine for SSE seeds.
- **The default-on record is `api.jsonl` (`APILogEntry`), which carries no raw bodies** — only metadata + the parsed `Raw map[string]any` and the `Tools []ToolDefinition` schema list (`llm/apilog.go:118-174`). Useful for tool *schemas*, not for raw decode-surface bytes.

So the two harvest substrates are:

| On-disk file | Type / writer | Contains | Always present? |
|---|---|---|---|
| `<stateDir>/api-raw.jsonl` | `llm.APIRawLogEntry` (`llm/apilog.go:177`) | verbatim HTTP request body + response body (SSE for streams), `provider`, `model`, `mode` | **only if `SERF_LOG_RAW_HTTP=1`** |
| `<stateDir>/sessions/<SID>.transcript.jsonl` | `transcript.{Header,Entry,APICall}` (`agent/transcript/transcript.go:27/54/61`) | every `schema.Turn` incl. `llm.Message.Content[].ToolCall.{Name,Arguments}` | **always** (written whenever `stateDir != ""`) |

`<stateDir>` resolves via `cmdutil.DefaultStateRoot()` (`cmdutil/statedir.go:17`): `$SERF_STATE_DIR`, else `~/.serf`. Directory layout (confirmed): `api.jsonl` + `api-raw.jsonl` at the root; per session `sessions/<SID>.transcript.jsonl` (file), `sessions/<SID>.meta.json`, and `sessions/<SID>/jobs.jsonl` (`agent/jobs.go:306-311` `jobsDir`). The jobs event log (`jobstore.Event`, `agent/internal/jobstore/event.go:33`; reader `ReadEvents` `read_events.go:16`) is **not** harvested here — no fuzz target decodes it today; it becomes a corpus source for the 8.1 persistence targets, noted in §7.

## 1. Artifact → target map (the honest version)

| Corpus surface | Target(s) it seeds | Recorded source | Status |
|---|---|---|---|
| **sse** | `llm.FuzzParseSSE` (`llm/sse_fuzz_test.go`); provider metamorphic decoders `openai.FuzzOpenAIResponsesMetamorphic` (`llm/providers/openai/responses_fuzz_test.go`) + the `anthropic`/`google`/`openaicompat` `*_stream_fuzz_test.go` | `api-raw.jsonl` `response_body` where `mode=="stream"`, routed by the `provider` field | **RICH** — primary value |
| **toolargs** | `agent.FuzzToolArgsValidate` (`agent/tool_args_fuzz_test.go`), input `(nameIndex int, argsBytes []byte)` | transcript `Entry.Turn.Message.Content[].ToolCall.{Name,Arguments}` (`llm/types.go:159-164`); secondarily `api-raw.jsonl` request bodies | **RICH** |
| **appwire** | `appwire.FuzzMessageDecode`, `appwire.FuzzMethodParams` | — **no recorded source** — | **GAP** (see §6) |
| **http** | `cmd/serf-hub/web_fuzz_test.go` (inbound `WebServer.Handler()`) | — **no recorded source** — | **GAP** (see §6) |

**Be explicit (charter asked us to be):** appwire frames cross the WebSocket and are **never written to disk verbatim** — no frame log, no access log (grep for a non-test `OpenFile`/frame recorder under the appwire flow returns nothing). Inbound hub HTTP is likewise unrecorded. The harvester therefore covers **sse + toolargs** — exactly the two surfaces where real provider/model quirks are the dominant bug source — and leaves appwire/http on hand-written + OWASP seeds until a recorder exists. Do not fabricate appwire seeds from transcript-derived structures; they are not wire frames.

### 1.1 SSE extraction (`sse/`)
For each `api-raw.jsonl` line: decode `APIRawLogEntry`; skip unless `Mode == "stream"` and `ResponseBody != ""`. The `ResponseBody` *is* the seed for `FuzzParseSSE` (provider-agnostic). For the provider metamorphic targets, route by `Provider`: `openai`/`openai-responses` → openai target's testdata, `anthropic` → anthropic's, etc. Error-body responses (a 4xx/5xx JSON instead of an SSE stream) are kept too — they are valid adversarial input for the decoders, just tagged `non-sse` in the dedup signature so they don't crowd out real streams.

### 1.2 toolargs extraction (`toolargs/`)
For each transcript: read the NDJSON (`transcript.Header` first line, then `kind:"entry"`→`transcript.Entry` / `kind:"api_call"`→`transcript.APICall`; we only need entries). For each `Entry.Turn.Message.Content` part with `Kind == ContentToolCall`, take `ToolCall.Name` + `ToolCall.Arguments` (a `json.RawMessage`). The target's input is `(nameIndex int, argsBytes []byte)`; map `Name` → `nameIndex` by matching against the live core-tool name list (the same `coreToolSchemas` registry the target builds, `agent/tool_args_fuzz_test.go:83`). A tool name with no registered match is dropped (it can't index the target's table). `argsBytes` = the scrubbed `Arguments` bytes.

## 2. Tool location & shape

**Location: `cmd/serf-fuzz-harvest` (a serf-module command), reusing `agent/doctor`'s session locator.** Rationale:
- The harvester must import serf types — `llm.APIRawLogEntry`, `agent/transcript.{Header,Entry,APICall}`, `llm.ToolCallData`, the core-tool registry. So it **cannot** live in the dep-free `fuzz/` module (§5 portability rule: "nothing in `fuzz/` imports serf"). The promoter/schemagen stay pure; the harvester is serf-coupled by nature.
- `serf-doctor` already walks sessions/transcripts/jobs via `agent/doctor` (`agent/doctor/locate.go`) and reads recorded state; reuse its locator for multi-bucket discovery instead of re-implementing path-walking. The transcript types in `agent/transcript` are exported, so the harvester reads the NDJSON directly with stock `encoding/json` (no unexported reader needed).

Surface:
```
serf-fuzz-harvest \
  [--state-dir DIR ...]   # default: cmdutil.DefaultStateRoot(); repeatable for multi-bucket
  [--out-root DIR]        # default: repo root; seeds land under each target's testdata/fuzz/<Name>/
  [--surface sse,toolargs]# default: all harvestable
  [--keep-values]         # opt-in: skip the shape-scrub (local-only corpora; still regex+entropy scanned)
  [--max-per-surface N]   # default 200
  [--max-seed-bytes N]    # default 32768
  [--dry-run]             # report counts + would-write paths, write nothing
```
Output: a one-line-per-surface summary (`sse: scanned 412 stream bodies → 37 unique seeds (kept) / 18 oversized / 9 redacted-out`) — context-managed per the automation rules; full per-seed provenance to a `--log` file on request.

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

### 3.3 Verification — must be airtight
A seed leaks nothing only if we *prove* it. Two gates:
1. **In-process self-check (write-time):** before writing each seed, re-run the Layer-B detectors in *detect* mode over the final bytes; a single hit aborts that seed (drop + log), and any hit at all fails the whole run with a non-zero exit. Redaction and detection share one regex set so they cannot drift.
2. **Repo gate (`make fuzz-corpus-scan`):** an external scanner (`gitleaks detect --no-git --source fuzz/corpus <surface testdata dirs>`, or `trufflehog filesystem`) over the entire committed corpus, wired into the gate so a leak can never land. The implementer adds the target and the CI hook; the canonical scanner is an open question (§7).

Both gates are tested adversarially: a fixture transcript/raw-log seeded with a planted `sk-…` key must (a) make the scanner red **before** redaction and (b) be absent from the written seed **and** green after — proving the pipeline actually strips, not just that clean input stays clean.

## 4. Determinism & corpus file format

**Format (Go-native, verified against the two files already in-tree).** A Go fuzz corpus file is the literal text:
```
go test fuzz v1
[]byte("data:\a")
```
— a `go test fuzz v1` header line, then **one Go-literal line per fuzz argument** in declaration order. Single-`[]byte` targets (`FuzzParseSSE`, `FuzzMessageDecode`, the provider SSE targets) get one `[]byte(...)` line. Two-arg targets (`FuzzToolArgsValidate(nameIndex int, argsBytes []byte)`, `FuzzMethodParams`) get two lines — `int(N)` then `[]byte(...)`. Existing in-tree examples: `llm/testdata/fuzz/FuzzParseSSE/f17d1670fa834b2b` and `llm/providers/openai/testdata/fuzz/FuzzOpenAIChatCompletionsMetamorphic/b4da5124609623d8`.

**Where to write — write straight into each target's `testdata/fuzz/<FuzzName>/`** (same module as the target), **not** `fuzz/corpus/<surface>/`. Two reasons: (1) Go's toolchain auto-loads `testdata/fuzz/<FuzzName>/` as **seed corpus** under plain `go test` (no `-fuzz`), so harvested seeds run in `make fuzz` for free with zero loader code; (2) a loader living in the `fuzz/` module could not be imported by the per-module targets anyway (go.work does not span modules — design §1). The pre-created `fuzz/corpus/<surface>/` dirs are for the module-agnostic hand-written OWASP vectors; reconciling that location with the testdata reality is an open question (§7) but does not block harvesting.

**Encoding.** Emit `[]byte(%q)` via `strconv.Quote` (which is the inverse of the loader's `strconv.Unquote`) and `int(%d)`; do not hand-roll escaping. The determinism acceptance test is simply that `go test -run '^Fuzz' ./...` loads every written file without a corpus-parse error.

**Dedup.** Filename = the lowercase hex of the SHA-256 of the encoded file body (matching Go's own content-addressed scheme); identical seeds collide to one file. An in-run set plus an on-disk `Stat` skip make re-runs idempotent (a second harvest of the same state produces an empty diff).

**Size & diversity bounds.** Drop seeds over `--max-seed-bytes` (default 32 KiB; SSE streams can be large — truncate is unsafe mid-frame, so oversize is dropped, not clipped). Cap `--max-per-surface` (default 200). To keep the committed set small *and* diverse, bucket by a **structural signature** (sorted set of SSE event types for streams; sorted JSON key-paths for tool args) and keep ≤ K (default 3) per bucket — this discards the thousands of near-identical happy-path streams while retaining the structurally distinct ones.

## 5. Build steps

1. **`cmd/serf-fuzz-harvest/main.go`** — flag parsing, state-dir resolution (`cmdutil.DefaultStateRoot`), `agent/doctor` locator reuse, dispatch per surface, summary output. (~60 LoC)
2. **`raw.go`** — walk `api-raw.jsonl`, decode `APIRawLogEntry`, filter `Mode=="stream"`, route by `Provider` → sse seeds. (~50 LoC)
3. **`transcript.go`** — walk `sessions/*.transcript.jsonl`, decode entries, pull `ToolCall.{Name,Arguments}`, map name→index → toolargs seeds. (~55 LoC)
4. **`sanitize.go`** — Layer A shape-scrub (JSON walker + SSE frame splitter) + Layer B regex/entropy + the write-time self-check. (~120 LoC; the bulk and the load-bearing part)
5. **`emit.go`** — corpus-literal encoder, content-hash filenames, dedup, size/diversity bounds, write to `testdata/fuzz/<Name>/`. (~55 LoC)
6. **Makefile** — add `fuzz-corpus-scan` (external scanner over the corpus dirs) and wire it into the gate; document `serf-fuzz-harvest` in `fuzz/README.md`. (~20 LoC)
7. **Tests** — unit tests per file + the adversarial planted-secret end-to-end test (§3.3) + a determinism test that harvests a fixture and asserts `go test -run '^Fuzz'` loads the output clean.

## 6. Dependencies & risks

**Dependencies.** `SERF_LOG_RAW_HTTP=1` must have been set when the traffic was recorded, or `api-raw.jsonl` does not exist and the sse harvest yields nothing — **the toolargs harvest works regardless** (transcripts are always written). Recommend the nightly/capture environment runs with raw logging on. External secret scanner (`gitleaks`/`trufflehog`) available in CI for the gate.

**Risks.**
- *Sanitization false-negative* (an unknown secret format) → mitigated structurally: under the default shape-scrub no original value survives at all, so Layer-B/entropy is a backstop, not the primary defense. The external scanner gate is the third independent check.
- *Shape-scrub erases the quirks we want to fuzz* → accepted for the committed corpus; `--keep-values` (local-only, scanned) preserves them when a human is driving.
- *appwire/http have no recorded source* → out of scope here; flagged loudly (§1) rather than faked.
- *testdata bloat / slower `go test`* → size + per-bucket diversity caps keep it small; dedup keeps re-runs from growing it.
- *Internal-package boundaries* → avoided by harvesting only the two substrates whose types are exported (`llm`, `agent/transcript`); `jobstore` (internal) is deliberately not a source here.
- *PII in developers' personal `~/.serf`* → the scrub addresses content, but harvesting personal state at all is a policy question (§7).

## 7. Acceptance

- `serf-fuzz-harvest` over a fixture `~/.serf` writes seeds; `go test -run '^Fuzz' ./...` in `llm`, `llm/providers/...`, and `agent` loads and runs every harvested seed **green** (determinism + format correctness).
- A **known recorded edge case appears as a seed**: e.g. a captured `response.completed` stream with `status:"incomplete"` / `max_output_tokens` (the openai target's own seed shape) harvested from a fixture transcript/raw-log shows up in `llm/providers/openai/testdata/fuzz/FuzzOpenAIResponsesMetamorphic/`.
- **Secret-scan passes:** `make fuzz-corpus-scan` is green on the committed corpus, and the adversarial planted-secret test proves the pipeline strips a real `sk-…` key (red before, absent + green after).
- Re-running the harvest over unchanged state produces **no diff** (idempotent dedup).

## 8. Open questions (for Jesse)
1. **Commit harvested corpus, or generate on demand in nightly?** Recommendation: commit a *small, scrubbed, diversity-capped* set as durable seeds; let `fuzz-nightly` regenerate a larger **local-only** (`--keep-values`) set that is never committed.
2. **Reconcile `fuzz/corpus/<surface>/` (design §1, module-agnostic, cross-module-import problem) vs. writing seeds into per-target `testdata/fuzz/<FuzzName>/` (native, free load).** Recommendation: harvester → `testdata/fuzz/`; keep `fuzz/corpus/` for hand-written OWASP vectors with whatever loader those need.
3. **Default shape-scrub (airtight, loses value quirks) vs. keep-values+scan for the *committed* corpus.** Recommendation: shape-scrub default; `--keep-values` local-only.
4. **appwire + http have no recorded traffic source.** Accept the gap (harvest sse+toolargs only), or first build an opt-in WebSocket frame recorder / hub access log as a separate item?
5. **Canonical secret scanner for the gate** — `gitleaks` vs `trufflehog`, and is it already in CI?
6. **May the harvester run over a developer's personal `~/.serf`** (PII risk remains even after scrub), or only a designated capture environment?
7. **jobs.jsonl as a corpus source** for the 8.1 persistence/replay targets (`jobstore.Event` decode + Fold) — fold into this tool later, or own it under the 8.1 plan?
