# 8.2 — Codex-compat input/item + config decode fuzz targets — implementation plan

**Status:** PLANNED. **Charter:** design doc §8.2. **Branch:** `wip/fuzzing-toolkit`.
**Pattern:** identical to Phase-0 (`appwire/jsonrpc_fuzz_test.go`, `appwire/params_fuzz_test.go`) — single-input `testing.F`, Go-native corpus auto-promotion, no promoter. Each target lives next to the code it tests, in that code's module, so `make fuzz` (the `go test -run '^Fuzz'` loop over `GO_MODULES`) picks it up.

## 0. What the charter named vs. what the code actually has

Charter seams: "appwire `InputItem`/item parsing (the codex-compat path), `providers.toml`, plugin manifests, session config." Verified against code:

- **There is no custom `UnmarshalJSON` on any appwire item type.** `InputItem` (`appwire/types.go:357`), `ThreadItem` (`:337`), `Thread` (`:139`), `Turn` (`:303`) are plain structs decoded by stock `encoding/json`. The "codex-compat path" is exactly the wire shapes in `appwire/codex_compat_test.go` / `appwire/item_type_test.go` flowing through `json.Unmarshal`. The only custom codec in the package is `LaunchConfigLayer.MarshalJSON` (`appwire/types.go:946`) — marshal-only, no matching `UnmarshalJSON`.
- **`InputItem` is already partially fuzzed** by `FuzzMethodParams`: it is a field of `ThreadStartParams`/`TurnStartParams`/`TurnSteerParams`/`TurnQueueParams`/`TurnDrainAsSteerParams`, all of which are `Methods`-catalog Params. So `InputItem` decode (incl. the `Data []byte` base64 path) is reached today.
- **`ThreadItem`/`Thread`/`Turn` are NOT reached** by any existing target: they appear only in *response* and *notification* types (`ThreadReadResponse`, `TurnStartResponse`, `item/started` params, `SerfSubagentPreviewResponse`, `ThreadTurnItemsListResponse`), never in a `Methods` Params struct. **This is the real coverage gap** the appwire half of 8.2 closes.

Config seams found (all real, all distinct decoders):
- `providers.toml` → `providercfg.Load([]byte) (Config, error)` (`llm/providercfg/load.go:46`), TOML via `github.com/BurntSushi/toml`. File wrapper `LoadFile` (`:124`). Module: `llm`.
- plugin manifest → `plugin.ParseManifest([]byte) (Manifest, error)` (`agent/plugin/plugin.go:55`), JSON. File-reading wrapper `Load(dir)` (`:181`) does the `.claude-plugin`→`.codex-plugin` fallback + component-dir walk. Module: `agent`.
- session/launch config → `launchconfig.tomlDecode([]byte, *Layer)` (`cmd/serf-hub/internal/launchconfig/io.go:90`, in-package) behind `LoadLayer(path)` (`:15`); and `toml.Decode` into `Meta` behind `LoadMeta(path)` (`:73`). TOML. Types `Layer` (`types.go:12`), `Meta` (`:102`). Module: root (`.`).

Bonus seam found, NOT in charter (raise in §7, do not build now): credentials store `LoadStore(path)` → `toml.Decode` into an unexported `fileShape` (`internal/credentials/store.go:79`).

Net: **4 targets** — one appwire item/response decode target + three config decode targets.

## 1. Targets

All four are single-input `testing.F`. Common oracle vocabulary:
- **Floor:** never panic (the point of the exercise).
- **Round-trip fixed point** (for types that re-serialize): `decode → encode → decode → encode` is byte-stable after the first normalizing marshal (same discipline as `FuzzMessageDecode`/`FuzzMethodParams` — first marshal normalizes key order / number format / UTF-8, compare normalized forms).
- **Structured-error / no-partial** (for config loaders that validate): a rejected input yields a non-nil error **and** the zero value of the result type — never a panic, never a half-populated value. On `err == nil`, the loader's own post-conditions (documented invariants) must hold.

### 1.1 `appwire/item_fuzz_test.go` — codex item/thread/turn decode (module `.`)

Seam: stock `json.Unmarshal` into `ThreadItem` / `Thread` / `Turn` / `InputItem` (`appwire/types.go:337/139/303/357`). Closes the response/notification-shape gap left by `FuzzMethodParams`.

Signature:
```go
func FuzzCodexItemDecode(f *testing.F) // f.Fuzz(func(t, shapeIndex int, raw []byte))
```
`shapeIndex % 4` selects the concrete type; allocate a fresh zero value by reflection (mirror `FuzzMethodParams`'s `reflect.New`) over a small `[]any{ThreadItem{}, Thread{}, Turn{}, InputItem{}}` table so the table stays the single source of truth.

Seeds (from `codex_compat_test.go` / `item_type_test.go`, verified real shapes):
- `0, {"type":"userMessage","id":"i","turnId":"t","text":"hi","status":"completed"}`
- `0, {"type":"commandExecution","id":"i","command":"git status","cwd":"/w","aggregatedOutput":"","status":"inProgress"}`
- `0, {"type":"dynamicToolCall","id":"i","tool":"web_search","status":"inProgress","arguments":{"query":"x"}}` (exercises `ThreadItem.Raw` passthrough of unknown fields — note `arguments`/`tool` are not struct fields, so they land nowhere; that is itself worth pinning)
- `1, {"id":"thr","sessionId":"thr","status":{"type":"active","activeFlags":["waitingOnApproval"]},"turns":[]}`
- `2, {"id":"turn","status":"inProgress","items":[],"error":null}`
- `3, {"type":"input_image","data":"aGVsbG8=","mediaType":"image/png"}` (exercises `InputItem.Data []byte` base64 decode — invalid base64 is a prime crasher seed)
- `3, {"type":"text","text":"x","metadata":{"k":"v"}}`
- degenerate: `0,null` · `0,{}` · `0,not json` · `1,{"turns":[{"items":[{"type":"x"}]}]}` (nested item slice)

Oracle: floor + round-trip fixed point. (These are plain structs; round-trip holds for every cleanly-decoding input, exactly as in the two Phase-0 appwire targets.) ~70–110 LoC incl. seeds + the reflect table + a `messageIDs`-style helper.

### 1.2 `llm/providercfg/load_fuzz_test.go` — providers.toml decode (module `llm`)

Seam: `providercfg.Load([]byte)` (`load.go:46`).

Signature:
```go
func FuzzProvidersTOMLLoad(f *testing.F) // f.Fuzz(func(t, raw []byte))
```
Seeds:
- `[instances.openai]\ntype = "openai"\n`
- `default = "a"\n[instances.a]\ntype = "anthropic"\n[instances.b]\ntype = "kimi-anthropic"\n`
- `[instances.x]\ntype = "openai"\napi_style = "responses"\n`
- error shapes (must produce structured errors, not panics): `[instances.X]\ntype="openai"\n` (uppercase name) · `[instances.a]\ntype="bogus"\n` (unknown type) · `[instances.a]\ntype="anthropic"\napi_style="responses"\n` (api_style on non-openai) · `default="nope"\n[instances.a]\ntype="openai"\n` (dangling default) · `` (empty → "no instances") · `not = toml = [` (parse error) · `[instances.a/b]\ntype="openai"\n` ("/" in name)

Oracle: floor + **structured-error/no-partial + success-invariants**. On `err == nil` assert Load's own guarantees (read off `load.go`): `cfg.Default` names some `cfg.Instances[i].Name`; every instance `Type` is in `KnownTypeNames()`; every name is lowercase and `/`-free; `api_style` set only when `Type=="openai"`. On `err != nil` assert `cfg == Config{}` (no partial). No round-trip — Load has no inverse encoder. ~80–120 LoC.

### 1.3 `cmd/serf-hub/internal/launchconfig/io_fuzz_test.go` — session config decode (module `.`)

Seam: `tomlDecode([]byte, *Layer)` (`io.go:90`, in-package; sets `ModelFallbacksSet` from `meta.IsDefined`) and `toml.Decode` into `Meta` (`io.go:82`). **Do not** call `LoadLayer`/`LoadMeta`/`SaveLayer` — they touch the filesystem; fuzz the in-memory decoder + an in-memory `toml.NewEncoder(&buf)` for the round-trip.

Signature:
```go
func FuzzLaunchConfigDecode(f *testing.F) // f.Fuzz(func(t, which int, raw []byte))
```
`which & 1` picks `Layer` vs `Meta`.

Seeds (TOML, fields from `types.go`):
- `model = "gpt-5.5"\nreasoning_effort = "high"\n`
- `model_fallbacks = ["a","b"]\n` and `model_fallbacks = []\n` (the `ModelFallbacksSet` edge — empty-but-present vs absent)
- `[env]\nFOO = "bar"\n` · `[[mcps]]\nname="x"\ncommand="y"\nargs=["-a"]\n`
- `schema = 1\ncwd = "/w"\ncreated_at = 2020-01-01T00:00:00Z\n` (Meta; exercises `time.Time` TOML decode — a classic panic/round-trip hazard)
- `[trust]\nhashes = ["abc"]\ndecision = "trusted"\n` (Meta)
- degenerate: `` · `= = =` · `max_rounds = "not an int"` (type-mismatch → structured error)

Oracle: floor + structured-error + round-trip fixed point for cleanly-decoding values, encoding via `toml.NewEncoder(&buf).Encode`, re-decode, compare with `reflect.DeepEqual` on the decoded values (TOML byte-equality is fragile across encode; compare values, not bytes). **Carry the `model_fallbacks = []` prefix quirk** from `SaveLayer` (`io.go:48`) into the round-trip helper, or the empty-set case will fail spuriously — pin it as a known asymmetry, not a bug. ~90–140 LoC.

### 1.4 `agent/plugin/manifest_fuzz_test.go` — plugin manifest decode (module `agent`)

Seam: `plugin.ParseManifest([]byte)` (`plugin.go:55`) — the pure decode+validate seam. (`Load(dir)` reads files and walks component dirs; out of scope here, see §7.)

Signature:
```go
func FuzzPluginManifestParse(f *testing.F) // f.Fuzz(func(t, raw []byte))
```
Seeds:
- `{"name":"my-plugin","version":"1.0.0"}`
- `{"name":"x","mcpServers":{"s":{"command":"c"}},"hooks":{"PreToolUse":[]},"agents":["a.md"]}` (the `json.RawMessage` polymorphic fields — string/array/object)
- `{"name":"x","author":{"name":"y"}}` and `{"name":"x","author":"y"}` (author both shapes)
- error shapes: `{}` (empty name) · `{"name":"Bad_Name"}` (not kebab-case) · `{"name":"-x"}` (leading hyphen) · `not json` · `{"name":123}` (type mismatch)

Oracle: floor + structured-error/no-partial + **success-invariant** (`m.Name` matches `kebabCaseRe`, guaranteed by `validatePluginName`) + round-trip fixed point on the `Manifest` struct (plain JSON with `RawMessage` fields — re-marshal/re-decode is byte-stable after normalization, same as `FuzzMethodParams`). On `err != nil` assert `m == Manifest{}`. ~80–120 LoC.

## 2. Safety / determinism

- **No real config is ever read.** Targets 1.2/1.4 take `[]byte` directly. Target 1.3 fuzzes the in-package decoder over fuzz bytes, never `LoadLayer`/`LoadMeta`. No target calls `os.ReadFile`, `Load(dir)`, `LoadFile`, or `SaveLayer`/`SaveMeta`.
- No `t.TempDir()` needed — every seam under test is a pure `[]byte`→value function once the file-reading wrappers are excluded.
- Deterministic & offline: no network, no clock, no goroutines. The only nondeterminism risk is map key ordering, already handled by the "normalize on first marshal, compare normalized" discipline inherited from Phase 0.

## 3. Build order, size, dependencies, risks

Order (cheapest/lowest-risk first): **1.2 → 1.4 → 1.3 → 1.1.**
1. **1.2 providers.toml** — simplest (`[]byte`→`Load`), richest validation to assert. ~80–120.
2. **1.4 plugin manifest** — same shape, adds round-trip + kebab invariant. ~80–120.
3. **1.3 launchconfig** — two types + value-level round-trip + the `model_fallbacks=[]` asymmetry. ~90–140.
4. **1.1 appwire items** — reflect-table over 4 types; closes the response/notification gap. ~70–110.

Total ~320–490 LoC incl. seeds (charter budget ~250–450; the four-target spread runs slightly over — trim seed counts if it matters).

Dependencies: none new. All four modules (`.`, `llm`, `agent`) are already in `GO_MODULES`; `make fuzz` picks the targets up with no Makefile change. No promoter, no `schemagen`.

Risks:
- **R1 — round-trip false positives from custom codecs.** `LaunchConfigLayer.MarshalJSON` is appwire-side and not exercised here; the launchconfig `Layer` TOML round-trip has the `ModelFallbacksSet`/empty-slice asymmetry (§1.3). Mitigation: compare decoded *values* via `reflect.DeepEqual`, replicate the `model_fallbacks=[]` prefix, and if the asymmetry proves un-pinnable, drop the round-trip oracle for `Layer` and keep floor+structured-error only.
- **R2 — TOML `time.Time` in `Meta`** is the likeliest real crasher/round-trip-breaker; it is deliberately seeded.
- **R3 — `InputItem.Data []byte`** base64: stock `encoding/json` rejects bad base64 with an error (no panic expected), but it is the highest-entropy decode path in 1.1 and is seeded.
- **R4 — over-strict success-invariants** could turn a legitimate-but-surprising accept into a false failure. Mitigation: assert only invariants the loader's own code guarantees (read directly off `load.go`/`plugin.go`), nothing aspirational.

## 4. Acceptance (per design doc §6 style)

- `make fuzz` green with all four targets present (seed corpus + any saved crashers run as ordinary tests).
- End-to-end free-loop demonstration on **one** target (per §6 Phase-0 acceptance): inject a deliberate decode bug (e.g. make `providercfg.Load` index `names[0]` before the empty-check), confirm `go test -fuzz=FuzzProvidersTOMLLoad` finds it, the crasher lands in `testdata/fuzz/`, `make fuzz` goes red, and reverting the bug makes it green.
- Each target either surfaces ≥1 real defect or is argued clean to its stated oracle depth.

## 5. Per-target oracle summary

| Target | File (module) | Seam | Oracle |
|---|---|---|---|
| 1.1 codex items | `appwire/item_fuzz_test.go` (`.`) | `json.Unmarshal` → ThreadItem/Thread/Turn/InputItem | floor + round-trip fixed point |
| 1.2 providers.toml | `llm/providercfg/load_fuzz_test.go` (`llm`) | `providercfg.Load` | floor + structured-error/no-partial + success-invariants |
| 1.3 session config | `cmd/serf-hub/internal/launchconfig/io_fuzz_test.go` (`.`) | `tomlDecode`→Layer, `toml.Decode`→Meta | floor + structured-error + value round-trip |
| 1.4 plugin manifest | `agent/plugin/manifest_fuzz_test.go` (`agent`) | `plugin.ParseManifest` | floor + structured-error/no-partial + kebab invariant + round-trip |

## 6. Notes for the implementer

- Mirror the existing two appwire targets verbatim for structure: reflect-`New` over a catalog table (1.1), normalize-then-compare round-trip, `t.Fatalf` with `input=%q` for reproducibility.
- 1.3's decode seam `tomlDecode` is unexported but in-package — the fuzz test is `package launchconfig`, so it can call it directly (do this, not `LoadLayer`).
- Keep seeds in the test files (`f.Add`); no `fuzz/corpus/` entries are needed for 8.2 (that's 8.4's job).

## 7. Open questions for Jesse

1. **Credentials store (`internal/credentials/store.go:79`, `LoadStore`→TOML).** A real, untested config decoder, but not named in the §8.2 charter and gated behind a file read + a 0600 mode check. Add a fifth target (fuzzing the in-package `toml.Decode` into `fileShape`, bypassing the mode check) or leave it? I lean *add it* — it's ~40 LoC and config-shaped — but it's scope creep on the charter.
2. **File-reading wrappers (`plugin.Load(dir)`, `providercfg.LoadFile`, `launchconfig.LoadLayer`).** 8.2 fuzzes the pure decoders only. `plugin.Load` has real logic beyond decode — the `.claude-plugin`→`.codex-plugin` fallback (the thing that caused the resume re-injection bug) and the component-dir walk. Fuzzing that needs `t.TempDir()` + a generated directory tree (a *filesystem* fuzz, closer to 8.1's territory). Defer to a follow-up, or fold a `t.TempDir`-based `Load` target in here?
3. **Round-trip on `launchconfig.Layer`** carries the `ModelFallbacksSet` / `model_fallbacks=[]` asymmetry (§1.3, R1). OK to pin that quirk in the round-trip helper, or drop the round-trip oracle for `Layer` and keep floor+structured-error only?
4. **Four targets vs. the ~250–450 LoC charter budget** — estimate is ~320–490. Fine to run slightly over, or cut seed counts / merge 1.3's Layer+Meta into one fuzzed dimension to stay under?
</content>
</invoke>
