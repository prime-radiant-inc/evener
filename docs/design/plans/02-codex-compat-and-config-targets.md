# 8.2 — Codex-compat input/item + config decode fuzz targets — implementation plan

**Status:** PLANNED. **Charter:** design doc §8.2. **Branch:** `wip/fuzzing-toolkit`.
**Pattern:** identical to Phase-0 (`appwire/jsonrpc_fuzz_test.go`, `appwire/params_fuzz_test.go`) — single-input `testing.F`, Go-native corpus auto-promotion, no promoter. Each target lives next to the code it tests, in that code's module, so `make fuzz` (the `go test -run '^Fuzz'` loop over `GO_MODULES`) picks it up.

**This plan now carries one production refactor (§1.0) as a hard prerequisite** before its launchconfig fuzz target. Sequence: refactor (TDD, gate green) → then the fuzz targets.

## 0. What the charter named vs. what the code actually has

Charter seams: "appwire `InputItem`/item parsing (the codex-compat path), `providers.toml`, plugin manifests, session config." Verified against code:

- **There is no custom `UnmarshalJSON` on any appwire item type.** `InputItem` (`appwire/types.go:357`), `ThreadItem` (`:337`), `Thread` (`:139`), `Turn` (`:303`) are plain structs decoded by stock `encoding/json`. The "codex-compat path" is exactly the wire shapes in `appwire/codex_compat_test.go` / `appwire/item_type_test.go` flowing through `json.Unmarshal`. The only custom codec in the package is `LaunchConfigLayer.MarshalJSON` (`appwire/types.go:946`) — marshal-only, no matching `UnmarshalJSON`.
- **`InputItem` is already partially fuzzed** by `FuzzMethodParams`: it is a field of `ThreadStartParams`/`TurnStartParams`/`TurnSteerParams`/`TurnQueueParams`/`TurnDrainAsSteerParams`, all of which are `Methods`-catalog Params. So `InputItem` decode (incl. the `Data []byte` base64 path) is reached today.
- **`ThreadItem`/`Thread`/`Turn` are NOT reached** by any existing target: they appear only in *response* and *notification* types (`ThreadReadResponse`, `TurnStartResponse`, `item/started` params, `SerfSubagentPreviewResponse`, `ThreadTurnItemsListResponse`), never in a `Methods` Params struct. **This is the real coverage gap** the appwire half of 8.2 closes.

Config seams found (all real, all distinct decoders):
- `providers.toml` → `providercfg.Load([]byte) (Config, error)` (`llm/providercfg/load.go:46`), TOML via `github.com/BurntSushi/toml`. File wrapper `LoadFile` (`:124`). Module: `llm`.
- plugin manifest → `plugin.ParseManifest([]byte) (Manifest, error)` (`agent/plugin/plugin.go:55`), JSON. File-reading wrapper `Load(dir)` (`:181`) does the `.claude-plugin`→`.codex-plugin` fallback + component-dir walk. Module: `agent`.
- session/launch config → `launchconfig.tomlDecode([]byte, *Layer)` (`cmd/serf-hub/internal/launchconfig/io.go:90`, in-package) behind `LoadLayer(path)` (`:15`) and also called by `resolver.go:107`; and `toml.Decode` into `Meta` behind `LoadMeta(path)` (`:73`). TOML. Types `Layer` (`types.go:12`), `Meta` (`:102`). Module: root (`.`).
- credentials store → in-package `toml.Decode(string(raw), &s.data)` into the unexported `fileShape` (`internal/credentials/store.go:79`), behind `LoadStore(path)` (`:63`) which gates on `os.Stat` + a 0600-mode check (`:65-77`). TOML. Module: root (`.`).

Net: **one production refactor (prerequisite) + 6 fuzz targets** — one appwire item/response decode target, four config decode targets (providers.toml, launchconfig, plugin manifest, credentials store), and one plugin `Load(dir)` filesystem-walk target.

## 1. Targets

All fuzz targets are single-input `testing.F`. Common oracle vocabulary:
- **Floor:** never panic (the point of the exercise).
- **Round-trip fixed point** (for types that re-serialize): `decode → encode → decode → encode` is byte-stable after the first normalizing marshal (same discipline as `FuzzMessageDecode`/`FuzzMethodParams` — first marshal normalizes key order / number format / UTF-8, compare normalized forms). For TOML, compare decoded *values* via `reflect.DeepEqual`, not bytes (TOML byte-equality is fragile across encode; map key order is non-deterministic).
- **Structured-error / no-partial** (for config loaders that validate): a rejected input yields a non-nil error **and** the zero value of the result type — never a panic, never a half-populated value. On `err == nil`, the loader's own post-conditions (documented invariants) must hold.

### 1.0 PREREQUISITE — launchconfig `ModelFallbacks *[]string` production refactor (module `.`)

This is real production work, sequenced **first**, landed gate-green, before the 1.3 fuzz target. It replaces the two-field three-state encoding (`ModelFallbacks []string` + companion `ModelFallbacksSet bool`) with a single pointer:

- `nil` → unset / inherit
- non-nil empty (`&[]string{}`) → explicit clear ("use no fallbacks"; overrides lower layers)
- non-nil non-empty → set

Today the explicit-clear state survives on disk only via two duplicated string-prepend hacks (`io.go:48`, `trust.go:27`) that hand-write `model_fallbacks = []\n` because the TOML encoder drops empty arrays. The pointer change lets the 1.3 fuzz round-trip oracle go through the REAL encode/decode path and assert the three-state survives — instead of replicating the prepend quirk inside the test.

**CRITICAL pre-step — verify the encoder BEFORE committing to `*[]string`.** Write a throwaway check (scratch `go test` in the package) that exercises BurntSushi/toml against a `struct{ MF *[]string \`toml:"model_fallbacks,omitempty"\` }`:
1. Encode with `MF: &[]string{}` → output **must contain** `model_fallbacks = []`.
2. Encode with `MF: nil` → output **must omit** `model_fallbacks`.
3. `toml.Decode("model_fallbacks = []\n", &v)` → `v.MF != nil && len(*v.MF) == 0`.
4. `toml.Decode("", &v)` → `v.MF == nil`.

**If all four hold**, do the full pointer refactor and DELETE both prepend hacks. **If any fails** (most likely #1 — the prepend hacks exist precisely because the encoder historically dropped empty arrays), DO NOT switch to `*[]string`. Fall back: keep `ModelFallbacks []string` + `ModelFallbacksSet bool`, but **centralize the duplicated prepend into one helper** (e.g. `func encodeLayerTOML(Layer) (string, error)`) called by BOTH `SaveLayer` (io.go) and `CanonicalHashTOML` (trust.go), killing the duplication, and have 1.3's round-trip use that one helper. **Report which branch you took before continuing.**

**Call sites (all module `.`; verified by grep of non-test `ModelFallbacks` refs in the hub package):**
1. `types.go:35-36` — `ModelFallbacks []string` + `ModelFallbacksSet bool` → `ModelFallbacks *[]string \`toml:"model_fallbacks,omitempty"\``. Delete the `ModelFallbacksSet` field entirely.
2. `io.go:48-50` (`SaveLayer`) — DELETE the `if layer.ModelFallbacksSet && len(...)==0 { data = "model_fallbacks = []\n" + data }` prepend.
3. `io.go:90-99` (`tomlDecode`, shared by `LoadLayer` io.go:24 and `resolver.go:107`) — DELETE the `layer.ModelFallbacksSet = meta.IsDefined("model_fallbacks")` post-processing block; the `*[]string` is populated directly by decode. Keep the `tomlDecode` function (two callers) — it just becomes a thin `toml.Decode` wrapper.
4. `trust.go:22` (`CanonicalHashTOML`) — DELETE `l.ModelFallbacksSet = meta.IsDefined(...)`.
5. `trust.go:27-31` — DELETE the `model_fallbacks = []` prepend branch; encode `l` and hash the encoder output directly.
6. `merge.go:211-213` — `if len(l.ModelFallbacks) > 0 || l.ModelFallbacksSet` → `if l.ModelFallbacks != nil`; deep-copy into a fresh pointer: `cp := append([]string{}, (*l.ModelFallbacks)...); eff.ModelFallbacks = &cp`. (Semantics unchanged: higher-precedence layers REPLACE; an explicit-clear `&[]string{}` at a layer wins over a lower set chain.)
7. `wire.go:29-30` (`FromWire`) — replace `ModelFallbacks: in.ModelFallbacks` + `ModelFallbacksSet: in.ModelFallbacks != nil` with one nil-preserving wrap: `nil []string → nil *[]string`, non-nil (incl empty) → `&copy`. The appwire DTO field `ModelFallbacks` is `[]string` with a custom `MarshalJSON` (`appwire/types.go:935`/`946`) that already carries absent/`[]`/`[...]` three-state via JSON nil-ness, so the wrap is exact.
8. `wire.go:72` (`ToWire`) — deref: `nil *[]string → nil []string`, `&[]string{} → []string{}`, `&[a] → [a]`.
9. `wire.go:81-82` (`ToWire`) — DELETE the `if in.ModelFallbacksSet && out.ModelFallbacks == nil { out.ModelFallbacks = []string{} }` patch; the deref in #8 produces the non-nil empty slice directly.
10. `args.go:84` (`ToArgs`) — `for _, m := range e.ModelFallbacks` won't compile on a pointer; guard nil: `if e.ModelFallbacks != nil { for _, m := range *e.ModelFallbacks { add("--model-fallback", m) } }`. (Effective argv carries no three-state: explicit-clear emits no `--model-fallback` flags, same as unset — correct, this is the final resolved config.)
11. **`spawn.go:260-261`** — NOT in the decision list; found by grep. `resumeResolved.Effective.ModelFallbacks = nil` stays (nil pointer = unset). DELETE line 261 `resumeResolved.Effective.ModelFallbacksSet = false`.

**Existing tests to migrate (must stay green; semantics unchanged — these are the TDD anchor):**
- `cmd/serf-hub/internal/launchconfig/io_test.go` — `TestLoadLayer_TracksExplicitEmptyModelFallbacks`, `TestSaveLayer_PersistsExplicitEmptyModelFallbacks`: drop `ModelFallbacksSet` refs; `ModelFallbacks: []string{}` → `&[]string{}`; assert explicit-clear is `got.ModelFallbacks != nil && len(*got.ModelFallbacks) == 0` and unset is `== nil`.
- `cmd/serf-hub/internal/launchconfig/wire_test.go` — `TestToWirePreservesExplicitEmptyModelFallbacks`, `TestWireOmitsUnsetModelFallbacks`, `TestLaunchConfigLayer_ConfigPlumbingRoundtrip`: same migration (these directly assert the three-state across FromWire/ToWire).
- `cmd/serf-hub/internal/launchconfig/args_test.go:35` — `ModelFallbacks: []string{...}` → `&[]string{...}`.
- `cmd/serf-hub/spawn_test.go:102` — `launchconfig.Layer{... ModelFallbacks: []string{"openai/gpt-fallback"} ...}` → `&[]string{...}`.

TDD discipline: flip a test to the pointer shape (red — won't compile), make it compile + pass via the field change, repeat per call site; run `go test ./cmd/serf-hub/...` green before starting the 1.3 fuzz target.

**Out of scope — separate types, do NOT touch:**
- `agent/session_config.go:132` `ModelFallbacks []string` (agent `Session` config — a different struct; `cmd/serf/serve.go:219/235`, `agent/session_*.go`, `agent/job_delegate.go`, `agent/schema/config_snapshot.go` all reference this one).
- `appwire/types.go:935` (the wire DTO — its `[]string` + `MarshalJSON` three-state is the *bridge* the refactor maps onto; unchanged).
- `cmd/serf-tui/internal/launchconfig` (the TUI has its OWN `Layer`; `launch_schema.go:143`, `launch_settings_panel.go:460/473` operate on it — confirmed no import of the hub package, so leave it alone).

LoC: ~60–110 production + ~20–40 test migration.

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

**Prerequisite: §1.0 has landed.** Seam: `tomlDecode([]byte, *Layer)` (`io.go:90`, in-package) and `toml.Decode` into `Meta` (`io.go:82`). **Do not** call `LoadLayer`/`LoadMeta`/`SaveLayer` — they touch the filesystem; fuzz the in-memory decoder + an in-memory `toml.NewEncoder(&buf)` for the round-trip.

Signature:
```go
func FuzzLaunchConfigDecode(f *testing.F) // f.Fuzz(func(t, which int, raw []byte))
```
`which & 1` picks `Layer` vs `Meta`.

Seeds (TOML, fields from `types.go`):
- `model = "gpt-5.5"\nreasoning_effort = "high"\n`
- `model_fallbacks = ["a","b"]\n` and `model_fallbacks = []\n` (the three-state edge — set vs explicit-clear vs absent)
- `[env]\nFOO = "bar"\n` · `[[mcps]]\nname="x"\ncommand="y"\nargs=["-a"]\n`
- `schema = 1\ncwd = "/w"\ncreated_at = 2020-01-01T00:00:00Z\n` (Meta; exercises `time.Time` TOML decode — a classic panic/round-trip hazard)
- `[trust]\nhashes = ["abc"]\ndecision = "trusted"\n` (Meta)
- degenerate: `` · `= = =` · `max_rounds = "not an int"` (type-mismatch → structured error)

Oracle: floor + structured-error + round-trip fixed point for cleanly-decoding values, encoding via `toml.NewEncoder(&buf).Encode`, re-decode, compare with `reflect.DeepEqual` on the decoded values.

**Three-state regression guard (the payoff of §1.0):** if §1.0 took the `*[]string` branch, the `Layer` round-trip goes through the **real** encode/decode path with **no prepend quirk** — explicitly round-trip `ModelFallbacks` = `nil`, `&[]string{}`, and `&[]string{"a","b"}` and assert `reflect.DeepEqual` preserves each (nil stays nil, non-nil-empty stays non-nil-empty, set stays set). This is the end-to-end proof the refactor preserved three-state. If §1.0 took the FALLBACK branch (kept the bool), route the round-trip through the centralized encode helper instead of bare `toml.NewEncoder`; the same three-state assertion holds via that helper. ~90–140 LoC.

### 1.4 `agent/plugin/manifest_fuzz_test.go` — plugin manifest decode (module `agent`)

Seam: `plugin.ParseManifest([]byte)` (`plugin.go:55`) — the pure decode+validate seam. (`Load(dir)` is fuzzed separately in §1.6.)

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

### 1.5 `internal/credentials/store_fuzz_test.go` — credentials store decode (module `.`)

Seam: the in-package `toml.Decode(string(raw), &s.data)` into `fileShape` (`store.go:79`). `fileShape`/`providerSection` are unexported, so the fuzz test must be `package credentials`. **Bypass `LoadStore`'s `os.Stat` / 0600-mode / `os.ReadFile` gate** (`store.go:65-77`) — decode `[]byte` directly, exactly as `LoadStore` does after the file read.

Signature:
```go
func FuzzCredentialsStoreDecode(f *testing.F) // f.Fuzz(func(t, raw []byte))
```
Seeds:
- `schema = 1\n[providers.openai]\napi_key = "sk-x"\n`
- `[providers.anthropic]\napi_key = "k"\n[providers.openai]\napi_key = "j"\n`
- `schema = 2\n` (no providers — `LoadStore` nil-guards the map at `store.go:82`, but the bare decoder does not)
- degenerate / error: `` · `not toml [` · `schema = "x"` (type mismatch) · `[providers.x]\napi_key = 123\n` (api_key type mismatch) · `[providers]\nx = "y"\n` (providers as scalar, not section)

Oracle: floor + structured-error/no-partial (on `err != nil`, never panic; whatever toml left in the value is discarded by `LoadStore`) + round-trip fixed point on success: `fileShape` is a plain struct with a `map[string]providerSection`; re-encode via `toml.NewEncoder` (mirroring `Store.save`, `store.go:225`) → re-decode → `reflect.DeepEqual` on the decoded values (compare values not bytes; map key order is non-deterministic). This exercises the SAME encode/decode pair the store uses on disk. The map-nil-guard lives in `LoadStore`, not the decoder, so it is out of this oracle's scope. ~70–100 LoC.

### 1.6 `agent/plugin/load_fuzz_test.go` — plugin.Load filesystem walk (module `agent`)

Seam: `plugin.Load(dir)` (`plugin.go:181`) — the file-reading wrapper, NOT the pure `ParseManifest` (§1.4). Exercises the `.claude-plugin`→`.codex-plugin` fallback (`plugin.go:191-199` — the path behind the resume re-injection bug) + the component-dir walk (`discoverPluginSkills`/`discoverPluginAgents`/`discoverPluginHooksDiag`/`discoverPluginMCPConfigs`, plus `resolveComponentDirs` at `plugin.go:262`). This is a *filesystem* fuzz: materialize a bounded dir tree under `t.TempDir()` from fuzz bytes, then call `Load`.

Signature:
```go
func FuzzPluginLoad(f *testing.F) // f.Fuzz(func(t, layout uint16, manifest, mcpJSON []byte))
```
Driver: `dir := t.TempDir()`, then write a small FIXED-SHAPE tree gated by `layout` bits:
- bit0: write `.claude-plugin/plugin.json` = `manifest`
- bit1: write `.codex-plugin/plugin.json` = `manifest` (bit0+bit1 together exercise the Claude-preferred fallback)
- bit2: write `skills/<fixed-name>/SKILL.md`
- bit3: write `agents/a.md`
- bit4: write `.mcp.json` = `mcpJSON`
- bit5: write `hooks/hooks.json`

All path components are fixed constants. **Fuzz bytes go ONLY into file CONTENTS, never into path strings** — no traversal/symlink escape, nothing is ever written outside `dir`.

Seeds: a few `layout` values with the minimal valid `{"name":"p","version":"1.0.0"}` manifest — `(0b000001, validManifest, nil)` (claude only), `(0b000010, validManifest, nil)` (codex fallback), `(0b000011, validManifest, nil)` (both → claude wins), `(0b010001, validManifest, {"mcpServers":{}})`.

Oracle: floor (the real win — a malformed manifest/mcp/skill/hooks file on disk must yield an error, never a crash) + a fallback-correctness invariant: bit0 set → `inst.ManifestFlavor == "claude"`; only bit1 → `"codex"`; `inst.ManifestPath` is inside `dir`. On `err != nil`, `inst == Instance{}` (Load returns `Instance{}, err` on every error path). No round-trip — `Load` has no inverse. ~90–140 LoC (the tree-writer dominates).

## 2. Safety / determinism

- **No real config is ever read.** Targets 1.2/1.4/1.5 take `[]byte` directly. Target 1.3 fuzzes the in-package decoder over fuzz bytes, never `LoadLayer`/`LoadMeta`. None of these call `os.ReadFile`, `Load(dir)`, `LoadFile`, `LoadStore`, or `SaveLayer`/`SaveMeta`.
- **Target 1.6 is the one exception** and uses `t.TempDir()` + disk writes — but every path is a fixed constant and fuzz bytes go only into file contents, so writes stay inside the per-run temp dir. The "no filesystem" claim is therefore relaxed to: *the only disk activity is bounded, fixed-shape writes into a per-run `t.TempDir`*.
- Deterministic & offline: no network, no clock, no goroutines. The only nondeterminism risk is map key ordering, handled by the "compare decoded values, not bytes" discipline (TOML targets) and "normalize on first marshal, compare normalized" (JSON targets) inherited from Phase 0.

## 3. Build order, size, dependencies, risks

Order (refactor first, then cheapest/lowest-risk fuzz targets, filesystem last):
0. **§1.0 launchconfig `*[]string` refactor** — production prerequisite. Encoder-verification pre-step → refactor (or fallback) → all existing launchconfig tests green. ~80–150.
1. **1.2 providers.toml** — simplest (`[]byte`→`Load`), richest validation to assert. ~80–120.
2. **1.4 plugin manifest** — same shape, adds round-trip + kebab invariant. ~80–120.
3. **1.5 credentials store** — `[]byte`→`toml.Decode`, round-trip via `Store.save`'s encoder. ~70–100.
4. **1.3 launchconfig** — runs only after §1.0; two types + value-level round-trip + three-state regression guard. ~90–140.
5. **1.1 appwire items** — reflect-table over 4 types; closes the response/notification gap. ~70–110.
6. **1.6 plugin.Load filesystem** — `t.TempDir` tree-writer + fallback invariant; most machinery. ~90–140.

Total (refactor + six targets) ~560–880 LoC incl. seeds. **LoC budget: run over freely** (charter's ~250–450 advisory is superseded — Jesse approved running over).

Dependencies: none new. All three modules (`.`, `llm`, `agent`) are already in `GO_MODULES`; `make fuzz` picks the targets up with no Makefile change. No promoter, no `schemagen`.

Risks:
- **R1 — encoder behavior gates the refactor SHAPE.** Whether BurntSushi emits `model_fallbacks = []` for a non-nil empty `*[]string` decides `*[]string`-with-no-hacks vs keep-bool-with-centralized-helper. Resolved up front by the §1.0 pre-step; both branches kill the duplication and both yield a real round-trip for 1.3. (This was Open Question #3, now folded into §1.0.)
- **R2 — TOML `time.Time` in `Meta`** is the likeliest real crasher/round-trip-breaker; deliberately seeded in 1.3.
- **R3 — `InputItem.Data []byte`** base64: stock `encoding/json` rejects bad base64 with an error (no panic expected), but it is the highest-entropy decode path in 1.1 and is seeded.
- **R4 — over-strict success-invariants** could turn a legitimate-but-surprising accept into a false failure. Mitigation: assert only invariants the loader's own code guarantees (read directly off `load.go`/`plugin.go`/`store.go`), nothing aspirational.
- **R5 — 1.6 path safety.** A regression that lets fuzz bytes into path components could write outside the temp dir. Mitigation: path components are compile-time constants; assert in review that no fuzz-derived string is ever passed to `filepath.Join` for a write path.

## 4. Acceptance (per design doc §6 style)

- **§1.0 refactor:** `go test ./cmd/serf-hub/...` green (all migrated launchconfig + spawn tests), `make lint` clean, gate green. The branch taken (pointer vs centralized-helper) is reported with the encoder-verification result.
- `make fuzz` green with all six targets present (seed corpus + any saved crashers run as ordinary tests).
- End-to-end free-loop demonstration on **one** target (per §6 Phase-0 acceptance): inject a deliberate decode bug (e.g. make `providercfg.Load` index `names[0]` before the empty-check), confirm `go test -fuzz=FuzzProvidersTOMLLoad` finds it, the crasher lands in `testdata/fuzz/`, `make fuzz` goes red, and reverting the bug makes it green.
- Each target either surfaces ≥1 real defect or is argued clean to its stated oracle depth.

## 5. Per-target oracle summary

| Target | File (module) | Seam | Oracle |
|---|---|---|---|
| 1.0 refactor | `cmd/serf-hub/internal/launchconfig/*.go` (`.`) | `ModelFallbacks *[]string` (prereq) | existing launchconfig tests green; encoder verified |
| 1.1 codex items | `appwire/item_fuzz_test.go` (`.`) | `json.Unmarshal` → ThreadItem/Thread/Turn/InputItem | floor + round-trip fixed point |
| 1.2 providers.toml | `llm/providercfg/load_fuzz_test.go` (`llm`) | `providercfg.Load` | floor + structured-error/no-partial + success-invariants |
| 1.3 session config | `cmd/serf-hub/internal/launchconfig/io_fuzz_test.go` (`.`) | `tomlDecode`→Layer, `toml.Decode`→Meta | floor + structured-error + value round-trip + three-state guard |
| 1.4 plugin manifest | `agent/plugin/manifest_fuzz_test.go` (`agent`) | `plugin.ParseManifest` | floor + structured-error/no-partial + kebab invariant + round-trip |
| 1.5 credentials store | `internal/credentials/store_fuzz_test.go` (`.`) | `toml.Decode` → `fileShape` | floor + structured-error/no-partial + round-trip |
| 1.6 plugin.Load | `agent/plugin/load_fuzz_test.go` (`agent`) | `plugin.Load(dir)` filesystem walk | floor + flavor-fallback invariant + no-partial on error |

## 6. Notes for the implementer

- **Do §1.0 first and report the encoder-verification outcome before writing any fuzz target.** 1.3 depends on it.
- Mirror the existing two appwire targets verbatim for structure: reflect-`New` over a catalog table (1.1), normalize-then-compare round-trip, `t.Fatalf` with `input=%q` for reproducibility.
- 1.3's decode seam `tomlDecode` is unexported but in-package — the fuzz test is `package launchconfig`, so it can call it directly (do this, not `LoadLayer`).
- 1.5's `fileShape`/`providerSection` are unexported — the fuzz test is `package credentials` and constructs them directly.
- 1.6 writes file CONTENTS from fuzz bytes only; path components are fixed constants — never `filepath.Join` a fuzz-derived string into a write path.
- Keep seeds in the test files (`f.Add`); no `fuzz/corpus/` entries are needed for 8.2 (that's 8.4's job).

## 7. Open questions — RESOLVED

1. **Credentials store** — **ADD as the 5th target (§1.5).** Fuzz the in-package `toml.Decode` into `fileShape`, bypassing the 0600 mode check.
2. **File-reading wrappers / `plugin.Load(dir)`** — **FOLD IN (§1.6).** A `t.TempDir`-based `Load` target that generates a directory tree and exercises the `.claude-plugin`→`.codex-plugin` fallback + the component-dir walk.
3. **Round-trip on `launchconfig.Layer` / the `model_fallbacks=[]` asymmetry** — **DO THE PRODUCTION REFACTOR (§1.0).** Change to `ModelFallbacks *[]string`, delete both prepend hacks, route the 1.3 round-trip through the real encode/decode path. The encoder-verification pre-step decides pointer-vs-centralized-helper, but either way the duplication dies and the round-trip is real.
4. **LoC budget** — **run over freely.** Six targets + the refactor land at ~560–880 LoC; the charter's ~250–450 advisory is superseded.
