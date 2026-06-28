# 8.5 — Unified wire-type → generator registry — implementation plan

**Status:** PLANNED. **Charter:** design doc §8.5 (`docs/design/fuzzing-toolkit-design.md`). **Builds on:** Phase 1 `fuzz/schemagen` (BUILT). **Portability rule:** §5 — nothing in `fuzz/` may import a serf package. **Size:** ~350–500 LoC. **Branch:** `wip/fuzzing-toolkit`.

## 1. Problem

Today schema-driven generation reaches exactly one surface: tool args, whose schemas are already JSON (`Definition.Parameters`), so `schemagen.FromJSONSchema` consumes them directly. The appwire protocol has no such luck — its wire shapes are **Go structs**, not JSON Schemas. The Phase 0 #3 target `appwire/params_fuzz_test.go` (`FuzzMethodParams`) reflects each method's `Params` and feeds it **raw fuzz bytes**, exercising the decoder but never *generating structured values*; responses (`Result`) are not fuzzed at all. Each surface that wants structured generation re-invents the reflection.

The charter asks for ONE registry: every wire type (all 46 `appwire.Methods` params + their response types) → a `rapid.Generator` (valid + adjacent), so a single table-driven harness covers the whole protocol uniformly, reusing `schemagen` for the value generation and a new reflect bridge for the structs-without-schemas.

## 2. The catalog (grounded)

`appwire.Methods` is the single source of truth — `appwire/protocol.go:85`:

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

- **Count: 46** methods (verified: `awk` over the `Methods` literal returns 46; charter and the `FuzzMethodParams` doc-comment both say 46). The companion `Notifications` catalog (`protocol.go:140`) has 18 entries (several with `nil` payload) — out of scope for this item unless §6-Q5 says otherwise.
- Each spec carries a **zero value** of the concrete param/result type, expressly "so the doc generator can reflect their JSON fields" (`protocol.go:57–60`). That is the same reflection seam this registry consumes.
- **92 (Params + Result) slots across 46 methods**, deduping to ~70 distinct named structs: `EmptyParams{}`/`EmptyResponse{}` recur (e.g. `MethodPing`, the five `ModelSet`/`EffortSet`/`Compact`/`Shutdown` methods), `InstanceListResponse{}` is the result of all five `SerfInstance*` methods, `LaunchConfigResolved{}` of three `SerfLaunch*` methods, `AuthStatusResponse{}` of two.

### How `params_fuzz_test.go` reflects today (what to reuse vs. replace)

`appwire/params_fuzz_test.go` (`FuzzMethodParams`):
- **Reuse:** the catalog-as-table idiom — `idx := methodIndex % len(Methods)`, `spec := Methods[idx]`, `paramsType := reflect.TypeOf(spec.Params)`, `reflect.New(paramsType)` for a fresh non-shared copy, then `json.Unmarshal` (lines 33–47). The round-trip-fixed-point oracle (decode→marshal→decode→marshal, `bytes.Equal`, lines 51–69) is the right oracle and carries over verbatim.
- **Ad-hoc / to subsume:** it only ever sees `spec.Params` (never `spec.Result`), and its input is **raw fuzz bytes** — it cannot *construct* a structurally-valid-but-adversarial params value, so it never reaches the "decoded clean, handler misbehaves" divergence the toolkit is really after (research §3). The reflection-over-the-catalog is copy-pasted here and would be copy-pasted again for responses and for any other catalog-driven target. That duplication is what the registry removes.

## 3. Design

### 3.1 Where it lives — sibling `fuzz/typegen/`, depending on `schemagen`

`schemagen`'s own doc-comment scopes it tightly: "turns a JSON Schema … into a rapid generator of values" and "imports only the standard library and pgregory.net/rapid". Reflecting over arbitrary Go structs is a different job, so it gets a sibling package rather than bloating schemagen:

```
fuzz/typegen/                 # NEW; reflect→schema bridge + the wire-type registry
  typegen.go                  #   SchemaFromType, GeneratorForType
  registry.go                 #   Registry: name → generator (valid+adjacent)
  typegen_test.go             #   serf-free unit tests over hand-built reflect.Types
```

`typegen` imports `schemagen` + stdlib `reflect`/`encoding/json` only. It stays in module `primeradiant.com/serf/fuzz`, whose `go.mod` (`fuzz/go.mod`) has **no serf dependency** — so the module simply will not compile if the boundary is crossed. That structural fact *is* the portability test (§5; design doc, schemagen.go:8–11).

> Decision to confirm (§6-Q1): sibling `typegen` vs. an extra file in `schemagen`. Recommendation: sibling, to keep `schemagen`'s "schema in, values out" single responsibility intact.

### 3.2 How serf-side types reach a serf-free registry (the load-bearing seam)

The registry **never imports appwire**. Go types cross the boundary as `reflect.Type` and `map[string]any` schemas — both stdlib. The serf side (a `_test.go` in package `appwire`, which *may* import both appwire and the fuzz module) builds the registry by reflecting the catalog:

```go
// appwire/wiretypes_fuzz_test.go  (package appwire — serf side, the only place
// that knows about both appwire AND typegen)
reg := typegen.NewRegistry()
for _, m := range Methods {
	if t := reflect.TypeOf(m.Params); t != nil {
		reg.RegisterType(m.Name+"#params", t)
	}
	if t := reflect.TypeOf(m.Result); t != nil {
		reg.RegisterType(m.Name+"#result", t)
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
| `[]byte` | `{"type":"string"}` | encoding/json base64s it (e.g. `InputItem.Data`, types.go:362) |
| `json.RawMessage` | `{}` (untyped → arbitrary JSON) | raw passthrough, NOT base64 — detect by exact type, before the `[]byte` rule |
| `map[string]T` | `{"type":"object","additionalProperties":true}` + value schema | open object |
| `interface{}`/`any` | `{}` (untyped) | e.g. `TurnError.CodexErrorInfo` (types.go:317), `TaskListResponse.Data` (types.go:594) |
| implements `json.Marshaler` | `{}` (untyped) + flag (see §3.3.2) | e.g. `LaunchConfigLayer` (types.go:946) |

#### 3.3.1 Struct field walk

For each **exported** field: parse the `json` tag. Skip `json:"-"`. Name = tag name or field name. A field is **required** iff it has no `omitempty` AND is not a pointer/slice/map/interface (those naturally encode to null/absent). Set `additionalProperties:false` so **Valid** mode never invents a key that `encoding/json` would drop on re-marshal (which would break the round-trip oracle); **Adjacent** mode's existing `add_unknown` lever (schemagen.go:109) then deliberately probes unknown-key tolerance. Embedded (anonymous) struct fields: flatten promoted fields into the parent (appwire has **none** today — verified — so this is forward-proofing, low risk).

#### 3.3.2 Bounded recursion

appwire types form a tree but nest deeply (`Turn`→`[]ThreadItem`→`[]InputItem`; `LaunchConfigResolved`→`map[string]LaunchConfigLayer`). Carry a `visited map[reflect.Type]int` / depth bound in `SchemaFromType` so a (hypothetical) self-referential type collapses to `{}` past the bound rather than expanding forever. schemagen already bounds *value* depth (`maxDepth=4`, schemagen.go:32); this bounds *schema* expansion.

### 3.4 The registry

```go
// registry.go — serf-free. name → its valid+adjacent generator.
type Registry struct{ entries map[string]map[string]any } // name → schema

func NewRegistry() *Registry
func (r *Registry) RegisterType(name string, t reflect.Type)              // via SchemaFromType
func (r *Registry) RegisterSchema(name string, schema map[string]any)     // for surfaces that already have JSON (tool args)
func (r *Registry) Generator(name string, mode schemagen.Mode) *rapid.Generator[any]  // schemagen.Generator(schema, mode)
func (r *Registry) Mixed(name string) *rapid.Generator[any]               // schemagen.FromJSONSchema(schema)
func (r *Registry) Schema(name string) (map[string]any, bool)
func (r *Registry) Names() []string                                       // sorted, deterministic
```

The registry is a thin index: it stores schemas (extracted once, reflection is the only non-trivial step) and hands each one to `schemagen.Generator`/`FromJSONSchema`. **All value generation stays in schemagen** — the registry adds the *catalog* dimension (many named types in one table) and the *reflect intake* path. Both `RegisterType` (Go structs) and `RegisterSchema` (tool args' JSON) coexist, so the same registry can hold the whole protocol *and* the tool surface behind one uniform `Generator(name, mode)`.

## 4. Relationship to `schemagen` (the boundary)

- **schemagen** = schema → value. Unchanged. Pure stdlib + rapid. Already handles enum/required/types/bounds/additionalProperties and both Valid/Adjacent modes.
- **typegen** = type → schema (`SchemaFromType`) + the named-catalog index (`Registry`). It produces schemagen's *input* and delegates every value to schemagen.
- **Reuse vs. new path:** where a JSON Schema already exists (tool args), feed `RegisterSchema` → straight into `FromJSONSchema`. Where only a Go struct exists (appwire params/responses), `RegisterType` → `SchemaFromType` → `FromJSONSchema`. The reflect path is the *only* net-new generation logic; everything downstream is reused.

## 5. What it replaces / subsumes, what it adds

**Subsumes:** the catalog-reflection idiom currently inlined in `FuzzMethodParams` becomes `Registry.RegisterType` called in a loop over `Methods` — one place, reused for params and responses and (later) any other `MethodSpec`/`NotificationSpec`-shaped catalog. A single new harness `appwire/wiretypes_fuzz_test.go` covers all 46 methods' params **and** results, replacing what would otherwise be per-surface targets.

**Adds:**
1. **Responses.** `spec.Result` types get structured generation + a round-trip oracle for the first time (today: zero coverage).
2. **Structured valid+adjacent input**, not just raw bytes — reaching the "decodes clean, then misbehaves" class of bug the byte-level target can't construct.

**Keeps (does NOT delete):** the existing byte-level `FuzzMethodParams`. Structured generation and byte-garbage fuzzing are complementary — the latter still catches tokenizer / custom-`UnmarshalJSON` panics that structured values never reach (`Message.UnmarshalJSON`, `ID.UnmarshalJSON`). Recommendation §6-Q2: retain both; the new harness is additive.

**The new harness (sketch).** Most natural as a `rapid.Check` property test (schemagen yields rapid generators), with failures routed through the Phase-3 promoter like other rapid surfaces:

```go
func TestWireTypesRoundTrip(t *testing.T) {
	reg := buildRegistry()              // RegisterType over Methods params+results
	names := reg.Names()
	rapid.Check(t, func(rt *rapid.T) {
		name := rapid.SampledFrom(names).Draw(rt, "wiretype")
		mode := schemagen.Valid
		if rapid.Bool().Draw(rt, "adjacent") { mode = schemagen.Adjacent }
		val := reg.Generator(name, mode).Draw(rt, "value")

		raw, err := json.Marshal(val)   // generated any → JSON
		if err != nil { rt.Fatalf("%s: marshal generated value: %v", name, err) }
		typ := typeFor(name)            // serf-side map name → reflect.Type
		p := reflect.New(typ).Interface()
		err = json.Unmarshal(raw, p)
		// Oracle 1 (floor): no panic (decode of Adjacent input may legitimately error).
		// Oracle 2 (Valid only, non-custom-marshaler types): re-marshal is a fixed point.
		if mode == schemagen.Valid && err == nil && !customMarshaler(typ) {
			assertRoundTripStable(rt, name, p)  // the FuzzMethodParams oracle, lifted
		}
	})
}
```

## 6. Build steps

1. **`fuzz/typegen/typegen.go` — `SchemaFromType`** (~150–200 LoC): the kind switch + struct walk + json-tag parsing + the `json.RawMessage`/`[]byte`/`json.Marshaler`/pointer/map/interface special cases + bounded recursion. Plus `GeneratorForType(t, mode)` = `schemagen.Generator(SchemaFromType(t), mode)`.
2. **`fuzz/typegen/registry.go` — `Registry`** (~60–100 LoC): the index + `RegisterType`/`RegisterSchema`/`Generator`/`Mixed`/`Schema`/`Names`.
3. **`fuzz/typegen/typegen_test.go`** (~120–180 LoC, serf-free): hand-built `reflect.Type`s (structs with the same shapes as appwire — pointers, slices, `[]byte`, `json.RawMessage`, `map`, `any`, custom-marshaler) asserting (a) `SchemaFromType` produces the expected schema subset; (b) Valid-mode values round-trip through `json.Marshal`→`json.Unmarshal` into the source struct; (c) determinism under fixed seed (mirrors schemagen_test.go's `TestDeterminism`); (d) no serf import (a `go list -deps` / `import`-grep guard).
4. **`appwire/wiretypes_fuzz_test.go`** (~80–120 LoC, serf side): `buildRegistry()` over `Methods` (params+results), the `rapid.Check` harness above, and a `typeFor` map name→`reflect.Type`. Lift the round-trip-fixed-point assertion from `FuzzMethodParams`.
5. **Coverage knob:** add the registry harness to `make fuzz`'s `-run '^Fuzz'`/rapid sweep (already covered by `go test ./...` in the `fuzz`/appwire modules; no Makefile change beyond what Phase 0 wired — confirm `appwire` is reached by the gate, which it is via the root module).

## 7. Dependencies, risks, acceptance

**Dependencies:** `fuzz/schemagen` (BUILT) for all value generation; `pgregory.net/rapid v1.3.0` (already in `fuzz/go.mod`); Go 1.25. No new third-party deps.

**Risks (reflection edge cases — each has a stated handling above):**
- **`json.RawMessage` vs `[]byte`** — both are `[]uint8`; must detect `json.RawMessage` by exact type *first* (untyped/raw) or it gets mis-mapped to a base64 string. Present in `ThreadItem.Raw` (types.go:354) and the jsonrpc envelope.
- **Custom `json.Marshaler`** — `LaunchConfigLayer` (types.go:946) reshapes its own JSON (moves `modelFallbacks`), reached transitively by 3+ methods (`LaunchConfigResolved`, `LaunchConfigSetLayerParams.Config`). A naive struct-walk schema would generate fields the marshaler reshapes → false round-trip failures. Handling: detect `json.Marshaler`, treat as untyped `{}`, and **skip the round-trip-stability oracle** for those types (keep no-panic).
- **`interface{}`/`any` fields** (`TurnError.CodexErrorInfo`, `TaskListResponse.Data`) → untyped `{}` → `genArbitraryJSON`; bounded by `maxDepth`.
- **Enums-as-consts are invisible to reflect.** appwire has **no** `type X string` enum types (verified) and even if it did, Go consts aren't attached to the type, so `SchemaFromType` cannot recover an allowed-value set — fields like `ThreadStatus`/`turn.Status` generate arbitrary strings. The JSON-Schema path (tool args, where enums are explicit) keeps full enum fidelity. If a specific field's enum proves load-bearing, inject it via `RegisterSchema`/a per-field override (§6-Q3) — don't build a general enum-recovery mechanism (YAGNI).
- **Pointers & recursion** — handled by the nullable-union mapping and the visited/depth bound.

**Acceptance:**
- `Registry` built from `appwire.Methods` exposes a generator for **all 46 methods' params and all 46 results** (a test asserts `len(reg.Names()) == <params+results registered>` and that every method name has both `#params` and `#result` entries where the type is non-nil).
- The single `appwire/wiretypes_fuzz_test.go` harness generates Valid + Adjacent values for every registered type, marshals + decodes them into the concrete Go type with **no panic**; Valid values (non-custom-marshaler) round-trip to a fixed point.
- **Portability holds:** `fuzz/typegen` and `fuzz/schemagen` import no serf package — enforced structurally by `fuzz/go.mod` (no serf require) and asserted by an explicit import-guard test. The serf↔registry coupling lives entirely in `appwire/wiretypes_fuzz_test.go`, which passes only `reflect.Type` across the boundary.
- `make fuzz` green; a deliberately-broken type mapping (e.g. mis-handling `json.RawMessage`) turns a typegen unit test red.

## 8. Open questions for Jesse

1. **Location:** sibling `fuzz/typegen` (recommended) vs. an extra file in `schemagen`?
2. **Keep the byte-level `FuzzMethodParams`?** (Recommended: yes — complementary tokenizer/UnmarshalJSON panic-hunt that structured generation can't reach.)
3. **Enum fidelity on the reflect path:** accept the gap (reflect can't see consts) and add a per-field `RegisterSchema` override only where an enum is load-bearing? Or invest in a const-scanning mechanism (heavier, probably YAGNI)?
4. **Custom-marshaler oracle:** downgrade to no-panic for `json.Marshaler` types (recommended), or hand-author schema overrides for the few that exist (just `LaunchConfigLayer` today)?
5. **Scope of "wire types":** params + results only (the charter's wording), or also ingest the 18 `Notifications` payloads (several `nil`) for full protocol coverage?
6. **Harness style:** `rapid.Check` property test (recommended — matches schemagen's generator output and the promoter's rapid path) vs. a seeded `testing.F` corpus target?
