package appwire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// codexItemTypes is the single source of truth for FuzzCodexItemDecode's reflect
// table: the response/notification-shape item types that no Methods-catalog
// Params struct reaches (the coverage gap FuzzMethodParams leaves open).
var codexItemTypes = []any{
	ThreadItem{},
	Thread{},
	Turn{},
	InputItem{},
}

// FuzzCodexItemDecode drives the codex-compat item/thread/turn shapes through
// stock json.Unmarshal. shapeIndex%4 selects the concrete type from
// codexItemTypes; a fresh zero value is allocated by reflection (mirroring
// FuzzMethodParams) so the table is never mutated. The oracle is floor "no
// panic" plus a decode→encode→decode fixed point for any cleanly-decoding input
// (these are plain structs: the first marshal normalizes, then re-decode and
// re-marshal must be byte-stable).
//
// Focus note: ThreadItem/Thread/Turn/InputItem are plain struct-tag types with
// no custom (un)marshalers, so the focus file types.go (whose only func is the
// unrelated LaunchConfigLayer.MarshalJSON) carries no executable decode
// statements to credit. The focus-set % sits at the floor by construction — the
// value is the no-panic + fixed-point oracles over the codex item shapes, not
// types.go line coverage.
// codexItemDecodeSeeds is FuzzCodexItemDecode's committed corpus, shared with
// the snapshot oracle (TestCodexItemDecodeGolden). shape selects the concrete
// type from codexItemTypes; the trailing four are degenerate shapes.
var codexItemDecodeSeeds = []indexedSeed{
	{0, `{"type":"userMessage","id":"i","turnId":"t","text":"hi","status":"completed"}`},
	{0, `{"type":"commandExecution","id":"i","command":"git status","cwd":"/w","aggregatedOutput":"","status":"inProgress"}`},
	{0, `{"type":"dynamicToolCall","id":"i","tool":"web_search","status":"inProgress","arguments":{"query":"x"}}`},
	{1, `{"id":"thr","sessionId":"thr","status":{"type":"active","activeFlags":["waitingOnApproval"]},"turns":[]}`},
	{2, `{"id":"turn","status":"inProgress","items":[],"error":null}`},
	{3, `{"type":"input_image","data":"aGVsbG8=","mediaType":"image/png"}`},
	{3, `{"type":"text","text":"x","metadata":{"k":"v"}}`},
	{0, `null`},
	{0, `{}`},
	{0, `not json`},
	{1, `{"turns":[{"items":[{"type":"x"}]}]}`},
}

func FuzzCodexItemDecode(f *testing.F) {
	for _, s := range codexItemDecodeSeeds {
		f.Add(s.shape, []byte(s.raw))
	}

	f.Fuzz(func(t *testing.T, shapeIndex int, raw []byte) {
		idx := shapeIndex % len(codexItemTypes)
		if idx < 0 {
			idx += len(codexItemTypes)
		}
		itemType := reflect.TypeOf(codexItemTypes[idx])

		v := reflect.New(itemType).Interface()
		if err := json.Unmarshal(raw, v); err != nil {
			return // rejected input
		}

		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: decoded value failed to re-marshal: %v\n input=%q\n value=%#v",
				itemType, err, raw, v)
		}
		v2 := reflect.New(itemType).Interface()
		if err := json.Unmarshal(encoded, v2); err != nil {
			t.Fatalf("%s: re-marshaled value failed to re-decode: %v\n encoded=%q",
				itemType, err, encoded)
		}
		encoded2, err := json.Marshal(v2)
		if err != nil {
			t.Fatalf("%s: re-decoded value failed to re-marshal: %v\n encoded=%q\n value=%#v",
				itemType, err, encoded, v2)
		}
		if !bytes.Equal(encoded, encoded2) {
			t.Fatalf("%s: encode is not idempotent after normalization:\n input=%q\n once=%q\n twice=%q",
				itemType, raw, encoded, encoded2)
		}
	})
}
