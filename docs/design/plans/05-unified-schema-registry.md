# 8.5 — Unified wire-type → generator registry — implementation plan

**Status:** PLANNED (decisions locked — see §8). **Charter:** design doc §8.5 (`docs/design/fuzzing-toolkit-design.md`). **Builds on:** Phase 1 `fuzz/schemagen` (BUILT) — but first refactors it (§3.0). **Portability rule:** §5 — nothing in `fuzz/` may import a serf package. **Size:** ~500–700 LoC (the Source refactor adds ~120–180 LoC to schemagen on top of the original ~350–500). **Branch:** `wip/fuzzing-toolkit`.

## 1. Problem

Today schema-driven generation reaches exactly one surface: tool args, whose schemas are already JSON (`Definition.Parameters`), so `schemagen.FromJSONSchema` consumes them directly (the lone live consumer is `agent/registry_schemafuzz_test.go`). The appwire protocol has no such luck — its wire shapes are **Go structs**, not JSON Schemas. The Phase 0 #3 target `appwire/params_fuzz_test.go` (`FuzzMethodParams`) reflects each method's `Params` and feeds it **raw fuzz bytes**, exercising the decoder but never *generating structured values*; responses (`Result`) and notification payloads are not fuzzed at all. Each surface that wants structured generation re-invents the reflection.

The charter asks for ONE registry: every wire type (all 46 `appwire.Methods` params + their response types, **plus** the notification payloads) → a generator (valid + adjacent), so a single table-driven harness covers the whole protocol uniformly, reusing `schemagen` for the value generation and a new reflect bridge for the structs-without-schemas.

Two foundational shifts versus the original sketch (decisions §8):

- The new harness is a **coverage-guided `testing.F`** target, not `rapid.Check`. go test's fuzzer mutates a byte stream and persists crashers for free; we want that on the registry surface. This requires schemagen's generators to be drivable from a **byte stream** as well as from rapid — so schemagen is refactored to take entropy from a `Source` interface (§3.0) before anything else is built.
- Scope is **full protocol coverage**: params + results + the 18 `Notifications` payloads (decision §8.5).

## 2. The catalog (grounded)

`appwire.Methods` is the single source of truth — `MethodSpec` at `appwire/protocol.go:65`, the `var Methods` literal at `protocol.go:85`:

```go
type MethodSpec struct {
	Name    string
	Params  any   // zero value of the concrete params struct, e.g. ThreadReadParams{}
	Result  any   // zero value of the concrete result struct, e.g. ThreadReadResponse{}
	Scope   MethodScope
	Summary string
}

var Methods = []MethodSpec{ /* 46 entries, protocol.go:86–131 */ }
```

- **Count: 46** methods (verified by counting the literal's entries; charter and the `FuzzMethodParams` doc-comment both say 46).
- The companion `Notifications` catalog (`NotificationSpec` at `protocol.go:75`, `var Notifications` at `protocol.go:140`) has **18 entries**: **7 carry a payload type** (`ThreadStatusChangedParams`, `ThreadQueueChangedParams`, `TurnCompletedParams`, `AgentMessageDeltaParams`, `AgentMessageResetParams`, `ReasoningSummaryDeltaParams`, `ToolOutputDeltaParams`) and **11 have `nil` payload** (their data is inlined into the envelope, undocumented as a typed struct — nothing to reflect). The 7 typed payloads are **in scope** (decision §8.5); the 11 nil ones are skipped because there is no Go type to reflect.

```go
type NotificationSpec struct {
	Name    string
	Payload any   // zero value of the concrete payload struct, or nil
	Summary string
}
```

- Each spec carries a **zero value** of the concrete type, expressly "so the doc generator can reflect their JSON fields" (`protocol.go:57–59`). That is the same reflection seam this registry consumes.
- **92 (Params + Result) slots across 46 methods + 7 notification payloads**, deduping to ~70 distinct named structs: `EmptyParams{}`/`EmptyResponse{}` recur (e.g. `MethodPing`, the `ModelSet`/`EffortSet`/`Compact`/`Shutdown` methods), `InstanceListResponse{}` is the result of all five `SerfInstance*` methods, `LaunchConfigResolved{}` of three `SerfLaunch*` methods, `AuthStatusResponse{}` of two.

### How `params_fuzz_test.go` reflects today (what to reuse vs. replace)

`appwire/params_fuzz_test.go` (`FuzzMethodParams`):
- **Reuse:** the catalog-as-table idiom — `idx := methodIndex % len(Methods)`, `spec := Methods[idx]`, `paramsType := reflect.TypeOf(spec.Params)`, `reflect.New(paramsType)` for a fresh non-shared copy, then `json.Unmarshal` (lines 33–47). The round-trip-fixed-point oracle (decode→marshal→decode→marshal, `bytes.Equal`, lines 49–69) is the right oracle and carries over verbatim.
- **Ad-hoc / to subsume:** it only ever sees `spec.Params` (never `spec.Result`, never notification payloads), and its input is **raw fuzz bytes** — it cannot *construct* a structurally-valid-but-adversarial value, so it never reaches the "decoded clean, handler misbehaves" divergence the toolkit is really after (research §3). The reflection-over-the-catalog is copy-pasted here and would be copy-pasted again for responses and payloads. That duplication is what the registry removes.
- **KEPT, not deleted** (decision §8.2): the byte-level `FuzzMethodParams` stays as a complementary raw-bytes / `UnmarshalJSON` panic-hunt alongside the structured registry. See §5.

## 3. Design

Build order is **Source refactor (§3.0) → typegen (§3.1–3.4) → registry (§3.5) → the `testing.F` harness (§3.6)**. §3.0 is a prerequisite because it changes schemagen's internal generator API, on which everything downstream depends.

### 3.0 Prerequisite: a `Source` abstraction for `fuzz/schemagen` (FOUNDATIONAL)

**Why.** schemagen today is hard-wired to rapid: every generator pulls entropy via `*rapid.T` (`rapid.Bool().Draw(t, …)`, `rapid.IntRange(...).Draw`, `rapid.SampledFrom(...).Draw`, `rapid.Float64Range(...).Draw`, `rapid.String().Draw`), and `FromJSONSchema`/`Generator` wrap `genValue` in `rapid.Custom`. A `testing.F` harness (decision §8.6) hands the generator a **byte slice**, not a `*rapid.T`. To drive the *same generator definitions* from either entropy source, abstract the draws behind a `Source` interface.

**The interface** (serf-free, schemagen-internal — still only stdlib + rapid in the package):

```go
// Source is the entropy stream a generator draws from. Two implementations exist:
// a rapid-backed Source (reproducible, shrinking) for rapid.Check targets, and a
// byte-backed Source (consumes a []byte, deterministic, exhaustion-safe) for the
// coverage-guided testing.F targets.
type Source interface {
	Bool(label string) bool
	Intn(n int, label string) int            // [0, n); n<=0 returns 0
	IntRange(lo, hi int, label string) int    // inclusive
	Float64Range(lo, hi float64, label string) float64
	String(label string) string
}

// draw is the generic SampledFrom replacement (interfaces can't carry type params):
func draw[T any](s Source, opts []T, label string) T  // opts[s.Intn(len(opts), label)]
```

`label` is preserved so the rapid-backed Source keeps rapid's labelled draws (readable shrink output) and the byte Source can use it for nothing more than self-documentation.

**Two adapters:**

1. `rapidSource{t *rapid.T}` — each method delegates to the corresponding `rapid.X().Draw(t, label)`. This is the adapter that keeps the existing rapid targets working unchanged (decision §8.6).
2. `byteSource` — wraps `NewByteSource(data []byte)`. Consumes bytes to derive each primitive (e.g. read N bytes little-endian for an int, modulo into range; one byte's low bit for a bool; a length-prefixed/​terminated run for a string, biased toward the `adversarialStrings` corpus). When the stream is **exhausted it returns deterministic defaults** (zero/false/empty, low end of ranges, index 0) so generation always terminates — the depth bound (`maxDepth`) already caps structural recursion, so a finite byte budget yields a finite value.

**API change & migration:**
- The recursive core becomes `genValue(s Source, schema, mode, depth) any` (and every helper — `genObject`, `genArray`, `genString`, `genInteger`, `genNumber`, `genArbitraryJSON`, `genWrongType`, `genEnumViolation`, `chooseType` — takes `Source` instead of `*rapid.T`).
- **Public rapid entry points are preserved** as thin rapid-backed-Source wrappers, so their signatures don't change:
  - `FromJSONSchema(schema) *rapid.Generator[any]` → `rapid.Custom(func(t){ return genValue(rapidSource{t}, …) })`
  - `Generator(schema, mode) *rapid.Generator[any]` → same, single mode.
- **New byte entry point** for the `testing.F` path: `Value(s Source, schema map[string]any, mode Mode) any` (the exported source-driven core) + `NewByteSource(data []byte) Source`. A harness does `schemagen.Value(schemagen.NewByteSource(data), schema, mode)`.
- **Impact on existing rapid targets:** the sole live caller, `agent/registry_schemafuzz_test.go:81` (`schemagen.Generator(td.params, mode).Draw(rt, "args")`), and the in-package `schemagen_test.go` keep compiling untouched — they migrate to the rapid-backed Source *transparently* because `Generator`/`FromJSONSchema` now route through `rapidSource`. Decision §8.6: keep the existing rapid-based stateful targets working through the rapid-backed Source adapter. The only behavioral contract that must hold post-refactor is schemagen's existing `TestDeterminism` (same seed → same value) — assert the equivalent for the byte Source (same bytes → same value).

**Why a `Source` interface and not two copies of the generator:** the value-generation rules (required-present, enum membership, adjacent levers, depth bound, adversarial corpora) are the asset; they must not fork. One definition, two entropy backends.

### 3.1 Where it lives — sibling `fuzz/typegen/`, depending on `schemagen`

`schemagen`'s own doc-comment scopes it tightly: "turns a JSON Schema … into a rapid generator of values" and "imports only the standard library and pgregory.net/rapid". Reflecting over arbitrary Go structs is a different job, so it gets a sibling package rather than bloating schemagen (decision §8.1):

```
fuzz/typegen/                 # NEW; reflect→schema bridge + the wire-type registry
  typegen.go                  #   SchemaFromType, GeneratorForType, per-type overrides
  registry.go                 #   Registry: name → generator (valid+adjacent), source-driven
  typegen_test.go             #   serf-free unit tests over hand-built reflect.Types
```

`typegen` imports `schemagen` + stdlib `reflect`/`encoding/json` only. It stays in module `primeradiant.com/serf/fuzz`, whose `go.mod` (`fuzz/go.mod`) has **no serf dependency** — so the module simply will not compile if the boundary is crossed. That structural fact *is* the portability test (§5; design doc, schemagen.go:8–11).

### 3.2 How serf-side types reach a serf-free registry (the load-bearing seam)

The registry **never imports appwire**. Go types cross the boundary as `reflect.Type` and `map[string]any` schemas — both stdlib. The serf side (a `_test.go` in package `appwire`, which *may* import both appwire and the fuzz module) builds the registry by reflecting the catalog:

```go
// appwire/wiretypes_fuzz_test.go  (package appwire — serf side, the only place
// that knows about both appwire AND typegen)
reg := typegen.NewRegistry()
reg.RegisterTypeSchema(reflect.TypeOf(LaunchConfigLayer{}), launchConfigLayerSchema) // hand-authored, §3.3.2
for _, m := range Methods {
	if t := reflect.TypeOf(m.Params); t != nil {
		reg.RegisterType(m.Name+"#params", t)
	}
	if t := reflect.TypeOf(m.Result); t != nil {
		reg.RegisterType(m.Name+"#result", t)
	}
}
for _, n := range Notifications {
	if t := reflect.TypeOf(n.Payload); t != nil { // 7 typed; 11 nil skipped
		reg.RegisterType(n.Name+"#payload", t)
	}
}
```

`reflect.TypeOf` yields a `reflect.Type` — a stdlib interface that carries **no import edge** back to appwire. `typegen` receives only `reflect.Type` values and produces generators; it has no idea the types came from serf. This is exactly how `appwire.Methods` already feeds its doc generator and `FuzzMethodParams`: reflection over zero values, never a typed dependency.

### 3.3 The reflect → schema bridge (`SchemaFromType`)

```go
// SchemaFromType converts a Go type into the map[string]any JSON-Schema subset
// schemagen consumes. It mirrors encoding/json's marshalling rules so a generated
// value, marshalled and re-decoded, round-trips into the same Go type.
func SchemaFromType(t reflect.Type) map[string]any
```

Mapping (encoding/json semantics, so generated JSON decodes back cleanly):

| Go kind | schema | notes |
|---|---|---|
| `string` | `{"type":"string"}` | |
| `bool` | `{"type":"boolean"}` | |
| `int*`/`uint*` | `{"type":"integer"}` | within float64 exact range (schemagen already clamps, schema.go:16) |
| `float*` | `{"type":"number"}` | |
| `*T` (pointer) | `SchemaFromType(T)` but type set `["<T>","null"]` | nil ↔ JSON null; optional in parent |
| `struct` | `{"type":"object","properties":…,"required":…,"additionalProperties":false}` | walk exported fields; see §3.3.1 |
| `[]T` / `[N]T` | `{"type":"array","items":SchemaFromType(T)}` | |
| `[]byte` | `{"type":"string"}` | encoding/json base64s it (e.g. `InputItem.Data`) |
| `json.RawMessage` | `{}` (untyped → arbitrary JSON) | raw passthrough, NOT base64 — detect by exact type, before the `[]byte` rule |
| `map[string]T` | `{"type":"object","additionalProperties":true}` + value schema | open object |
| `interface{}`/`any` | `{}` (untyped) | e.g. `TurnError.CodexErrorInfo`, `TaskListResponse.Data` |
| has a **per-type override** | the hand-authored schema (§3.3.2) | e.g. `LaunchConfigLayer` (types.go:946) |
| other `json.Marshaler` (no override) | `{}` (untyped) + custom-marshaler flag (§3.3.3) | default fallback; none such today |

#### 3.3.1 Struct field walk

For each **exported** field: parse the `json` tag. Skip `json:"-"`. Name = tag name or field name. A field is **required** iff it has no `omitempty` AND is not a pointer/slice/map/interface (those naturally encode to null/absent). Set `additionalProperties:false` so **Valid** mode never invents a key that `encoding/json` would drop on re-marshal (which would break the round-trip oracle); **Adjacent** mode's existing `add_unknown` lever (schemagen.go:109) then deliberately probes unknown-key tolerance. Embedded (anonymous) struct fields: flatten promoted fields into the parent (appwire has **none** today — verified — so this is forward-proofing, low risk).

#### 3.3.2 Per-type schema overrides (decision §8.4 — hand-authored, full coverage)

`SchemaFromType` consults a per-`reflect.Type` override map **before** the kind switch (and before the generic `json.Marshaler` downgrade), at top level **and** at every nested occurrence. The override seam:

```go
func (r *Registry) RegisterTypeSchema(t reflect.Type, schema map[string]any) // type → hand-authored schema
```

This is the seam for a `json.Marshaler` whose JSON shape differs from a naive struct walk. **Today the only such type is `LaunchConfigLayer`** (`appwire/types.go:946`). Its `MarshalJSON` relocates `modelFallbacks` (nils it on the aliased struct, then re-adds it when non-nil) — the *field set* is unchanged versus a plain marshal; the only observable difference is that an empty-but-non-nil `ModelFallbacks` is emitted as `"modelFallbacks":[]` rather than omitted. So the hand-authored schema mirrors the struct's 33 optional fields with `additionalProperties:false`, and the round-trip-fixed-point oracle stays **ENABLED** for it (the custom marshaler is deterministic, so decode→marshal→decode→marshal is a fixed point). This is full structured coverage, **not** a downgrade-to-no-panic. The override is registered on the serf side (it needs the concrete `reflect.Type`); the hand-authored schema literal lives in `appwire/wiretypes_fuzz_test.go`. Because `LaunchConfigLayer` is reached transitively (`LaunchConfigResolved.Effective`/`.Layers`, `LaunchConfigSetLayerParams.Config`), the override must fire at nested depth — hence keyed by type, not by registry name.

#### 3.3.3 Generic custom-marshaler fallback

Any *other* type implementing `json.Marshaler` with no registered override maps to untyped `{}` and is flagged so the harness drops the round-trip-stability oracle (keeps no-panic). There are none in appwire today; this is the safe default for future additions, with the hand-authored override (§3.3.2) as the upgrade path when a marshaler becomes load-bearing.

#### 3.3.4 Bounded recursion

appwire types form a tree but nest deeply (`Turn`→`[]ThreadItem`→`[]InputItem`; `LaunchConfigResolved`→`map[string]LaunchConfigLayer`). Carry a `visited map[reflect.Type]int` / depth bound in `SchemaFromType` so a (hypothetical) self-referential type collapses to `{}` past the bound rather than expanding forever. schemagen already bounds *value* depth (`maxDepth=4`, schemagen.go:32); this bounds *schema* expansion.

### 3.5 The registry

```go
// registry.go — serf-free. name → its valid+adjacent generator, drivable from any Source.
type Registry struct {
	entries   map[string]map[string]any  // name → schema
	overrides map[reflect.Type]map[string]any // §3.3.2
}

func NewRegistry() *Registry
func (r *Registry) RegisterType(name string, t reflect.Type)              // via SchemaFromType (+ overrides)
func (r *Registry) RegisterSchema(name string, schema map[string]any)     // for surfaces that already have JSON (tool args)
func (r *Registry) RegisterTypeSchema(t reflect.Type, schema map[string]any) // per-type override (§3.3.2)
func (r *Registry) Value(name string, mode schemagen.Mode, s schemagen.Source) (any, bool) // source-driven (testing.F)
func (r *Registry) Generator(name string, mode schemagen.Mode) *rapid.Generator[any]        // rapid path (rapid.Check)
func (r *Registry) Schema(name string) (map[string]any, bool)
func (r *Registry) Names() []string                                       // sorted, deterministic
```

The registry is a thin index: it stores schemas (extracted once, reflection is the only non-trivial step) and hands each one to schemagen via whichever `Source` the caller supplies. **All value generation stays in schemagen** — the registry adds the *catalog* dimension (many named types in one table), the *reflect intake* path, and the *per-type override* table. `Value` (byte-source) powers the `testing.F` harness; `Generator` (rapid) remains for any rapid stateful target. Both `RegisterType` (Go structs) and `RegisterSchema` (tool args' JSON) coexist, so the same registry can hold the whole protocol *and* the tool surface behind one uniform interface.

### 3.6 The harness — coverage-guided `testing.F` (decision §8.6)

The new harness is a `testing.F` target driven by a byte-fed structured generator — **not** `rapid.Check`. go test's fuzzer mutates the `data []byte` (coverage-guided exploration) and persists any crasher to `testdata/fuzz` for free; the structured generator turns those bytes into valid+adjacent wire values, so the fuzzer's coverage feedback is steering the *structured* search, not just the JSON tokenizer.

```go
func FuzzWireTypes(f *testing.F) {
	reg, typeFor := buildRegistry()      // RegisterType over Methods params+results + Notifications payloads
	names := reg.Names()
	f.Add(0, false, []byte{})            // seed: first name, Valid, empty stream → defaults
	f.Add(1, true, []byte{0x01, 0x02})   // a second name, Adjacent

	f.Fuzz(func(t *testing.T, sel int, adjacent bool, data []byte) {
		name := names[((sel%len(names))+len(names))%len(names)]
		mode := schemagen.Valid
		if adjacent {
			mode = schemagen.Adjacent
		}
		val, ok := reg.Value(name, mode, schemagen.NewByteSource(data))
		if !ok {
			t.Fatalf("no generator for %s", name)
		}

		raw, err := json.Marshal(val)    // generated any → JSON
		if err != nil {
			t.Fatalf("%s: marshal generated value: %v", name, err)
		}
		typ := typeFor(name)             // serf-side map name → reflect.Type
		p := reflect.New(typ).Interface()
		err = json.Unmarshal(raw, p)
		// Oracle 1 (floor): no panic (decode of Adjacent input may legitimately error).
		// Oracle 2 (Valid only, non-custom-marshaler-without-override): re-marshal is a fixed point.
		if mode == schemagen.Valid && err == nil && roundTrippable(typ) {
			assertRoundTripStable(t, name, p)  // the FuzzMethodParams oracle, lifted
		}
	})
}
```

`roundTrippable(typ)` returns false only for types flagged by §3.3.3 (generic custom marshaler, no override) — `LaunchConfigLayer` has an override, so it stays round-trippable.

## 4. Relationship to `schemagen` (the boundary)

- **schemagen** = schema → value, now **source-driven** (§3.0). Pure stdlib + rapid. Handles enum/required/types/bounds/additionalProperties, both Valid/Adjacent modes, and both rapid and byte entropy backends. The Source refactor is the one change to this package.
- **typegen** = type → schema (`SchemaFromType` + overrides) + the named-catalog index (`Registry`). It produces schemagen's *input* and delegates every value to schemagen.
- **Reuse vs. new path:** where a JSON Schema already exists (tool args), feed `RegisterSchema` → straight into schemagen. Where only a Go struct exists (appwire params/responses/payloads), `RegisterType` → `SchemaFromType` → schemagen. The reflect path and the Source abstraction are the only net-new generation logic; everything downstream is reused.

## 5. What it replaces / subsumes, what it adds

**Subsumes:** the catalog-reflection idiom currently inlined in `FuzzMethodParams` becomes `Registry.RegisterType` called in a loop over `Methods`/`Notifications` — one place, reused for params, responses, and payloads. A single new harness `appwire/wiretypes_fuzz_test.go` covers all 46 methods' params **and** results **and** the 7 typed notification payloads, replacing what would otherwise be per-surface targets.

**Adds:**
1. **Responses + notification payloads.** `spec.Result` and the 7 typed `Notifications` payloads get structured generation + a round-trip oracle for the first time (today: zero coverage).
2. **Structured valid+adjacent input**, not just raw bytes — reaching the "decodes clean, then misbehaves" class of bug the byte-level target can't construct.
3. **Coverage-guided structured search** — the `testing.F` byte source means go's fuzzer steers the structured generator and persists crashers automatically.

**Keeps (does NOT delete) — decision §8.2:** the existing byte-level `FuzzMethodParams`. Structured generation and byte-garbage fuzzing are complementary — the latter still catches tokenizer / custom-`UnmarshalJSON` panics that structured values never reach (`Message.UnmarshalJSON`, `ID.UnmarshalJSON`). The new harness is additive.

## 6. Build steps (in dependency order)

1. **Source refactor of `fuzz/schemagen`** (~120–180 LoC; FOUNDATIONAL, §3.0): define the `Source` interface + `draw[T]` helper; add `rapidSource` and `byteSource`/`NewByteSource`; thread `Source` through `genValue` and every helper; keep `FromJSONSchema`/`Generator` as rapid-backed-Source wrappers (signatures unchanged); add the exported `Value(s, schema, mode)` byte entry point. Verify the existing `agent/registry_schemafuzz_test.go` and `schemagen_test.go` still pass; add a byte-source determinism test (same bytes → same value) mirroring `TestDeterminism`.
2. **`fuzz/typegen/typegen.go` — `SchemaFromType`** (~160–220 LoC): the kind switch + struct walk + json-tag parsing + the per-type-override check + `json.RawMessage`/`[]byte`/`json.Marshaler`/pointer/map/interface special cases + bounded recursion. Plus `GeneratorForType(t, mode)`.
3. **`fuzz/typegen/registry.go` — `Registry`** (~80–120 LoC): the index + `RegisterType`/`RegisterSchema`/`RegisterTypeSchema`/`Value`/`Generator`/`Schema`/`Names`.
4. **`fuzz/typegen/typegen_test.go`** (~140–200 LoC, serf-free): hand-built `reflect.Type`s (structs with the same shapes as appwire — pointers, slices, `[]byte`, `json.RawMessage`, `map`, `any`, a custom-marshaler with and without an override) asserting (a) `SchemaFromType` produces the expected schema subset; (b) Valid-mode values round-trip through `json.Marshal`→`json.Unmarshal` into the source struct under **both** a byte Source and a rapid Source; (c) determinism (fixed seed / fixed bytes); (d) a registered per-type override is applied at nested depth; (e) no serf import (a `go list -deps` / `import`-grep guard).
5. **`appwire/wiretypes_fuzz_test.go`** (~120–160 LoC, serf side): `buildRegistry()` over `Methods` (params+results) and `Notifications` (7 typed payloads), the hand-authored `launchConfigLayerSchema` registered via `RegisterTypeSchema`, the `FuzzWireTypes` `testing.F` harness (§3.6) with a seed corpus, the `typeFor` map name→`reflect.Type`, and the lifted round-trip-fixed-point assertion.
6. **Coverage knob:** the `testing.F` target is picked up by `make fuzz`'s seed-corpus run (`-run '^Fuzz'`) and by `make fuzz-nightly`'s coverage-guided search — confirm `appwire` is reached by the gate (it is, via the root module + `scripts/run-fuzz.sh`); no Makefile change expected beyond what Phase 0 wired.

## 7. Dependencies, risks, acceptance

**Dependencies:** `fuzz/schemagen` (BUILT) — but the Source refactor (step 1) must land first, as it changes the generator API everything downstream uses. The change is internal-plus-additive: the public `FromJSONSchema`/`Generator` signatures are preserved, so the one existing rapid consumer (`agent/registry_schemafuzz_test.go`) migrates to the rapid-backed Source transparently. `pgregory.net/rapid v1.3.0` (already in `fuzz/go.mod`); Go 1.25. No new third-party deps.

**Risks (each has a stated handling above):**
- **Source refactor regressing the existing target.** The whole generator core changes signature. Mitigation: keep the public rapid API byte-identical, and treat `TestDeterminism` (rapid) + the new byte-determinism test as the regression gate before building typegen.
- **`json.RawMessage` vs `[]byte`** — both are `[]uint8`; must detect `json.RawMessage` by exact type *first* (untyped/raw) or it gets mis-mapped to a base64 string. Present in `ThreadItem.Raw` and the jsonrpc envelope.
- **Custom `json.Marshaler` (`LaunchConfigLayer`, types.go:946)** — hand-author its schema via `RegisterTypeSchema` (decision §8.4); it is reached transitively by 3+ types so the override fires at nested depth, and the round-trip oracle stays enabled (the field set matches; the marshaler only relocates `modelFallbacks`). Any *other* future marshaler with no override falls back to untyped `{}` + no-panic-only (§3.3.3).
- **`interface{}`/`any` fields** (`TurnError.CodexErrorInfo`, `TaskListResponse.Data`) → untyped `{}` → `genArbitraryJSON`; bounded by `maxDepth`.
- **Enums-as-consts are invisible to reflect (decision §8.3).** appwire has **no** `type X string` enum types (verified), and even if it did, Go consts aren't attached to the type, so `SchemaFromType` cannot recover an allowed-value set — fields like `ThreadStatus`/`turn.Status` generate arbitrary strings. The JSON-Schema path (tool args, where enums are explicit) keeps full enum fidelity. Where a specific field's enum proves load-bearing, inject it via a per-field `RegisterSchema`/override — **no general const-scanner** (YAGNI).
- **Byte-source exhaustion** — a short/empty byte stream must still produce a well-formed value; the byte Source returns deterministic defaults past exhaustion and `maxDepth` caps structure, so generation always terminates.
- **Pointers & recursion** — handled by the nullable-union mapping and the visited/depth bound.

**Acceptance:**
- `schemagen` exposes a `Source` interface with a rapid-backed and a byte-backed adapter; `FromJSONSchema`/`Generator` are unchanged in signature; a byte-source determinism test passes; `agent/registry_schemafuzz_test.go` still passes.
- `Registry` built from `appwire.Methods` + `Notifications` exposes a generator for **all 46 methods' params, all 46 results, and the 7 typed notification payloads** (a test asserts every method name has both `#params` and `#result` entries where the type is non-nil, and every typed notification has a `#payload` entry; the 11 nil payloads are absent).
- The single `appwire/wiretypes_fuzz_test.go` `testing.F` harness generates Valid + Adjacent values for every registered type from a byte source, marshals + decodes them into the concrete Go type with **no panic**; Valid values (round-trippable types, including the overridden `LaunchConfigLayer`) round-trip to a fixed point.
- **Portability holds:** `fuzz/typegen` and `fuzz/schemagen` import no serf package — enforced structurally by `fuzz/go.mod` (no serf require) and asserted by an explicit import-guard test. The serf↔registry coupling lives entirely in `appwire/wiretypes_fuzz_test.go`, which passes only `reflect.Type` and hand-authored schemas across the boundary.
- `make fuzz` green (seed corpus); `make fuzz-nightly` exercises `FuzzWireTypes` under coverage-guided search; a deliberately-broken type mapping (e.g. mis-handling `json.RawMessage`) turns a typegen unit test red.

## 8. Decisions (locked — Jesse)

1. **Location:** sibling `fuzz/typegen`, depending on `schemagen`, serf-free (takes `reflect.Type` / JSON-schema input, no appwire import — the portability test must hold). *Resolved: sibling, not an extra file in schemagen.*
2. **Keep the byte-level `FuzzMethodParams`.** Complementary raw-bytes / `UnmarshalJSON` panic-hunt alongside the structured registry. *Resolved: keep both.*
3. **Enum fidelity on the reflect path:** accept the reflect-can't-see-consts gap; add a per-field `RegisterSchema` override only where an enum is load-bearing. *Resolved: no const-scanner.*
4. **Custom `json.Marshaler` types:** hand-author schema overrides (full structured coverage), not downgrade-to-no-panic. Today that is only `LaunchConfigLayer` (types.go:946) — author its schema to match its actual custom-`MarshalJSON` shape; keep the round-trip oracle on. *Resolved: hand-author (§3.3.2). Generic untyped `{}` remains the fallback only for future override-less marshalers.*
5. **Scope of "wire types":** params + results **and** the 18 `Notifications` payloads (7 typed in scope; 11 nil have no type to reflect) — full protocol coverage. *Resolved: include notifications.*
6. **Harness style:** `testing.F` driven by a byte-fed structured generator (coverage-guided exploration + free crasher persistence), **not** `rapid.Check`. This requires the foundational `Source` refactor of `fuzz/schemagen` (§3.0) so the same generator definitions drive both the byte-source `testing.F` and the rapid-source stateful targets; the existing rapid targets keep working through a rapid-backed `Source` adapter. *Resolved: testing.F + Source refactor as prerequisite.*
