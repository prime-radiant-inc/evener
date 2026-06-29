package appwire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// FuzzMethodParams drives every method in the Methods catalog through its typed
// Params decoder. For a fuzzed (methodIndex, paramsBytes) it allocates a fresh
// value of the method's concrete Params type (via reflection, so the shared
// catalog is never mutated) and unmarshals the bytes into it. The oracle is
// floor "no panic" plus a decode→encode→decode fixed point for any input that
// decodes cleanly — this exercises all 46 methods' Params structs (field types,
// tags, any custom UnmarshalJSON) through one harness, not the generic decoder.
//
// Focus note: the Params decode is stdlib struct-tag reflection, so the focus
// file protocol.go (var-declaration catalog plus three catalog helpers this
// target never calls) carries no executable decode statements to credit. The
// focus-set % therefore sits at the floor by construction — the value here is
// the no-panic + fixed-point oracles over every Params type, not protocol.go
// line coverage.
func FuzzMethodParams(f *testing.F) {
	// Seed with a couple of method/params pairs so coverage starts inside the
	// real structs rather than at the JSON tokenizer.
	f.Add(0, []byte(`{"protocolVersion":"1","clientInfo":{"name":"x"}}`))
	f.Add(1, []byte(`{}`))
	f.Add(2, []byte(`{"includeArchived":true}`))
	f.Add(3, []byte(`{"threadId":"abc","subscribe":true}`))
	f.Add(7, []byte(`{"threadId":"t","turnLimit":5}`))
	f.Add(0, []byte(`null`))
	f.Add(0, []byte(`not json`))
	f.Add(0, []byte(`{"unknownField":[1,2,3]}`))

	f.Fuzz(func(t *testing.T, methodIndex int, paramsBytes []byte) {
		if len(Methods) == 0 {
			t.Fatal("Methods catalog is empty")
		}
		idx := methodIndex % len(Methods)
		if idx < 0 {
			idx += len(Methods)
		}
		spec := Methods[idx]
		paramsType := reflect.TypeOf(spec.Params)
		if paramsType == nil {
			return // method declares no params type
		}

		// Fresh, addressable copy of the concrete Params type.
		p := reflect.New(paramsType).Interface()
		if err := json.Unmarshal(paramsBytes, p); err != nil {
			return // rejected input
		}

		// First marshal normalizes (key order, number formatting, UTF-8
		// replacement); compare the normalized forms, not the raw input.
		encoded, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("method %s: decoded params failed to re-marshal: %v\n input=%q\n value=%#v",
				spec.Name, err, paramsBytes, p)
		}
		p2 := reflect.New(paramsType).Interface()
		if err := json.Unmarshal(encoded, p2); err != nil {
			t.Fatalf("method %s: re-marshaled params failed to re-decode: %v\n encoded=%q",
				spec.Name, err, encoded)
		}
		encoded2, err := json.Marshal(p2)
		if err != nil {
			t.Fatalf("method %s: re-decoded params failed to re-marshal: %v\n encoded=%q\n value=%#v",
				spec.Name, err, encoded, p2)
		}
		if !bytes.Equal(encoded, encoded2) {
			t.Fatalf("method %s: encode is not idempotent after normalization:\n input=%q\n once=%q\n twice=%q",
				spec.Name, paramsBytes, encoded, encoded2)
		}
	})
}
