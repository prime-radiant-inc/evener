package appwire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/serf/fuzz/schemagen"
	"primeradiant.com/serf/fuzz/typegen"
)

var jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

// launchConfigLayerSchema hand-authors LaunchConfigLayer's JSON shape (types.go).
// LaunchConfigLayer has a custom MarshalJSON, so the reflect bridge cannot infer
// its shape — but the marshaler only relocates modelFallbacks (an empty
// non-nil slice is emitted as [] rather than omitted); the field SET is the
// struct's. Every field is optional (omitempty); pointer-backed scalars are
// nullable. additionalProperties:false keeps Valid generation from inventing a
// key encoding/json would drop, so the round-trip-fixed-point oracle stays on.
var launchConfigLayerSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{},
	"properties": map[string]any{
		"schema":                      map[string]any{"type": []string{"integer", "null"}},
		"model":                       map[string]any{"type": "string"},
		"fastCheapModel":              map[string]any{"type": "string"},
		"agent":                       map[string]any{"type": "string"},
		"reasoningEffort":             map[string]any{"type": "string"},
		"contextStrategy":             map[string]any{"type": "string"},
		"openAIResponsesContinuation": map[string]any{"type": "string"},
		"maxRounds":                   map[string]any{"type": []string{"integer", "null"}},
		"maxSubagentDepth":            map[string]any{"type": []string{"integer", "null"}},
		"noProjectPrompts":            map[string]any{"type": []string{"boolean", "null"}},
		"nonInteractive":              map[string]any{"type": []string{"boolean", "null"}},
		"appReplaySize":               map[string]any{"type": []string{"integer", "null"}},
		"skillsDirs":                  stringArraySchema,
		"pluginDirs":                  stringArraySchema,
		"mcpConfigs":                  stringArraySchema,
		"systemPromptMode":            map[string]any{"type": "string"},
		"systemPromptFile":            map[string]any{"type": "string"},
		"systemPromptText":            map[string]any{"type": "string"},
		"systemPromptAppendMode":      map[string]any{"type": "string"},
		"systemPromptAppendFile":      map[string]any{"type": "string"},
		"systemPromptAppendText":      map[string]any{"type": "string"},
		"systemPromptAppend":          stringArraySchema,
		"modelFallbacks":              stringArraySchema,
		"mcps": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"name", "command"},
				"properties": map[string]any{
					"name":    map[string]any{"type": "string"},
					"command": map[string]any{"type": "string"},
					"args":    stringArraySchema,
				},
			},
		},
		"env": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
		},
		"verbose":                   map[string]any{"type": []string{"boolean", "null"}},
		"rawHTTPLogging":            map[string]any{"type": []string{"boolean", "null"}},
		"traceFile":                 map[string]any{"type": "string"},
		"cpuProfile":                map[string]any{"type": "string"},
		"exportATIFPath":            map[string]any{"type": "string"},
		"exportATIFProviderHandles": map[string]any{"type": "string"},
	},
}

var stringArraySchema = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}

// buildRegistry reflects the whole AppWire catalog into a serf-free typegen
// Registry: a #params and #result entry for every method, and a #payload entry
// for every typed notification (the 11 nil-payload notifications are skipped —
// no Go type to reflect). The returned typeFor maps a registry name back to its
// concrete reflect.Type, the only serf↔registry coupling, crossing the boundary
// as a stdlib reflect.Type.
func buildRegistry() (*typegen.Registry, func(string) reflect.Type) {
	reg := typegen.NewRegistry()
	reg.RegisterTypeSchema(reflect.TypeOf(LaunchConfigLayer{}), launchConfigLayerSchema)

	types := map[string]reflect.Type{}
	add := func(name string, v any) {
		t := reflect.TypeOf(v)
		if t == nil {
			return
		}
		reg.RegisterType(name, t)
		types[name] = t
	}
	for _, m := range Methods {
		add(m.Name+"#params", m.Params)
		add(m.Name+"#result", m.Result)
	}
	for _, n := range Notifications {
		add(n.Name+"#payload", n.Payload)
	}
	return reg, func(name string) reflect.Type { return types[name] }
}

// FuzzWireTypes is roadmap item 8.5: one coverage-guided target over the WHOLE
// AppWire protocol. A fuzzed (sel, adjacent, data) selects a registered wire
// type, generates a structured Valid/Adjacent value by feeding data to a byte
// Source, marshals it, and decodes it into the concrete Go type. The byte
// Source means go's fuzzer steers the STRUCTURED search and persists crashers
// for free. Oracles:
//   - Floor: decode never panics (an Adjacent value may legitimately error).
//   - Valid (round-trippable types): decode→encode is a fixed point after one
//     normalization pass — the FuzzMethodParams oracle, now over params, results,
//     and typed notification payloads alike.
//
// It complements (does not replace) the byte-level FuzzMethodParams, which still
// hunts tokenizer / custom-UnmarshalJSON panics that structured values never
// reach.
//
// Focus note: the whole-package focus is low by construction — appwire is almost
// entirely var-declaration catalogs and struct-tag types whose (un)marshaling is
// stdlib reflection, so there are few executable decode statements to credit
// (LaunchConfigLayer.MarshalJSON is the lone marshaler). The value is the
// catalog-wide no-panic + fixed-point oracles, not appwire line coverage.
func FuzzWireTypes(f *testing.F) {
	reg, typeFor := buildRegistry()
	names := reg.Names()
	if len(names) == 0 {
		f.Fatal("wire-type registry is empty")
	}

	f.Add(0, false, []byte{})
	f.Add(1, true, []byte{0x01, 0x02})
	f.Add(5, false, []byte{0xff, 0x00, 0x7f, 0x42, 0x13})
	f.Add(9, true, []byte("structured-but-adversarial"))

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

		raw, err := json.Marshal(val)
		if err != nil {
			t.Fatalf("%s: marshal generated value: %v\n value=%#v", name, err, val)
		}

		typ := typeFor(name)
		p := reflect.New(typ).Interface()
		err = json.Unmarshal(raw, p)
		if mode == schemagen.Valid && err == nil && roundTrippable(typ) {
			assertRoundTripStable(t, name, p)
		}
	})
}

// assertRoundTripStable is the FuzzMethodParams decode→encode fixed-point oracle,
// lifted to run over any decoded wire value: the first marshal normalizes (key
// order, number formatting, UTF-8), and a second decode+marshal must reproduce
// it byte-for-byte.
func assertRoundTripStable(t *testing.T, name string, p any) {
	t.Helper()
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("%s: decoded value failed to re-marshal: %v\n value=%#v", name, err, p)
	}
	p2 := reflect.New(reflect.TypeOf(p).Elem()).Interface()
	if err := json.Unmarshal(encoded, p2); err != nil {
		t.Fatalf("%s: re-marshaled value failed to re-decode: %v\n encoded=%s", name, err, encoded)
	}
	encoded2, err := json.Marshal(p2)
	if err != nil {
		t.Fatalf("%s: re-decoded value failed to re-marshal: %v\n encoded=%s", name, err, encoded)
	}
	if !bytes.Equal(encoded, encoded2) {
		t.Fatalf("%s: encode not idempotent after normalization:\n once=%s\n twice=%s",
			name, encoded, encoded2)
	}
}

// roundTrippable reports whether the fixed-point oracle applies to typ. It is
// dropped only for a custom json.Marshaler with no hand-authored override (whose
// JSON shape the bridge cannot model) — today the sole marshaler in the catalog,
// LaunchConfigLayer, carries an override, so the oracle stays on for everything.
func roundTrippable(typ reflect.Type) bool {
	if typ.Implements(jsonMarshalerType) || reflect.PointerTo(typ).Implements(jsonMarshalerType) {
		return typ == reflect.TypeOf(LaunchConfigLayer{})
	}
	return true
}

// TestWireTypeRegistryCoverage is the acceptance check: every method exposes a
// #params and #result generator, every typed notification a #payload generator,
// and the 12 nil-payload notifications none.
func TestWireTypeRegistryCoverage(t *testing.T) {
	reg, typeFor := buildRegistry()

	for _, m := range Methods {
		for _, suffix := range []string{"#params", "#result"} {
			name := m.Name + suffix
			if _, ok := reg.Schema(name); !ok {
				t.Errorf("missing registry entry %s", name)
			}
			if typeFor(name) == nil {
				t.Errorf("missing reflect.Type for %s", name)
			}
		}
	}

	typed, nilPayload := 0, 0
	for _, n := range Notifications {
		name := n.Name + "#payload"
		_, ok := reg.Schema(name)
		if n.Payload != nil {
			typed++
			if !ok {
				t.Errorf("typed notification %s missing #payload entry", n.Name)
			}
		} else {
			nilPayload++
			if ok {
				t.Errorf("nil-payload notification %s should have no #payload entry", n.Name)
			}
		}
	}
	if typed != 7 {
		t.Errorf("typed notifications = %d, want 7", typed)
	}
	if nilPayload != 12 {
		t.Errorf("nil-payload notifications = %d, want 12", nilPayload)
	}
	if got, want := len(reg.Names()), 2*len(Methods)+typed; got != want {
		t.Errorf("registry has %d names, want %d (2×%d methods + %d typed payloads)",
			got, want, len(Methods), typed)
	}
}
