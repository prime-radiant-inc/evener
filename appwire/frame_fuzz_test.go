package appwire

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"testing"

	"primeradiant.com/serf/fuzz/schemagen"
	"primeradiant.com/serf/fuzz/typegen"
)

// generateFrame builds a valid-but-adversarial AppWire frame from a byte Source
// and returns its JSON encoding. It is the structure-aware counterpart to the
// raw-byte FuzzMessageDecode corpus: where random bytes almost always die at the
// first json.Unmarshal, this synthesizes a frame the decoder ACCEPTS — one of
// the four kinds, with a real catalog method name, a valid-or-edge id, and a
// schema-generated params/result/payload body — so the fuzzer spends its budget
// inside Message.UnmarshalJSON, ID.UnmarshalJSON, and the per-kind decoders
// rather than in the JSON tokenizer.
//
// The envelope (kind selection, method name, id, error code/message types) is
// always structurally valid so the frame decodes; adversarial content lives in
// the opaque regions the frame decoder does NOT type-check (Request/Notification
// Params is json.RawMessage, Response.Result and WireError.Data are `any`),
// where it round-trips regardless. That keeps decode success near 100% while
// still exploring weird bodies, large/nested ids, and every error code.
//
// Determinism: the only entropy is the byte Source, drawn through schemagen's
// deterministic primitives; no map iteration drives control flow (maps are only
// marshaled, where encoding/json sorts keys), and no time/rand is consulted. The
// same bytes always yield the same frame, so go's fuzzer can persist crashers.
func generateFrame(s schemagen.Source, reg *typegen.Registry) ([]byte, error) {
	mode := schemagen.Valid
	if s.Bool("adjacent_body") {
		mode = schemagen.Adjacent
	}

	var frame map[string]any
	switch s.Intn(4, "frame_kind") {
	case 0: // request: id + method + optional params
		m := Methods[s.Intn(len(Methods), "req_method")]
		frame = map[string]any{"id": generateID(s), "method": m.Name}
		if s.Bool("req_with_params") {
			if params, ok := reg.Value(m.Name+"#params", mode, s); ok {
				frame["params"] = params
			}
		}
	case 1: // notification: method + optional params, never an id
		n := Notifications[s.Intn(len(Notifications), "notif")]
		frame = map[string]any{"method": n.Name}
		if s.Bool("notif_with_params") {
			if n.Payload != nil {
				if payload, ok := reg.Value(n.Name+"#payload", mode, s); ok {
					frame["params"] = payload
				}
			} else {
				// nil-payload notifications carry an inline object on the wire; an
				// arbitrary schemagen body stands in for it.
				frame["params"] = schemagen.Value(s, nil, mode)
			}
		}
	case 2: // response: id + a non-empty result (any-typed, so any JSON decodes)
		m := Methods[s.Intn(len(Methods), "resp_method")]
		result, ok := reg.Value(m.Name+"#result", mode, s)
		if !ok {
			result = schemagen.Value(s, nil, mode)
		}
		frame = map[string]any{"id": generateID(s), "result": result}
	default: // error: id + a WireError-shaped error object
		frame = map[string]any{"id": generateID(s), "error": generateError(s, mode)}
	}

	return json.Marshal(frame)
}

// frameIDInts are the integer id magnitudes the generator draws from: zero, the
// units, and values straddling the int32 and float64-exact-integer boundaries
// that the ID accessors (Int64 via json) must survive.
var frameIDInts = []int64{0, 1, -1, 42, 1 << 31, -(1 << 31), 1 << 53}

// generateID returns a value that marshals to a valid (non-null) AppWire id. It
// spans the id shapes the decoder accepts: integers (incl. boundary magnitudes),
// strings, an integer too large for any Go int (kept as a raw token, the
// {"id":9999999999999999999999} seed shape), and a nested object id.
func generateID(s schemagen.Source) any {
	switch s.Intn(4, "id_kind") {
	case 0:
		return frameIDInts[s.Intn(len(frameIDInts), "id_int")]
	case 1:
		return s.String("id_str")
	case 2:
		return json.RawMessage("9999999999999999999999")
	default:
		return map[string]any{"nested": "object"}
	}
}

// frameErrorCodes are the codes the generated error frames draw from: every
// defined AppWire code plus a few off-catalog ints (unknown, zero, positive).
var frameErrorCodes = []int{
	CodeParseError, CodeInvalidRequest, CodeMethodNotFound, CodeInvalidParams,
	CodeInternalError, CodeConflict, CodeUnavailable, 0, 1, 65535,
}

// frameErrorInfos are the serfErrorInfo discriminants a generated error data
// blob draws from.
var frameErrorInfos = []ErrorInfo{
	ErrorInvalidParams, ErrorMethodNotFound, ErrorProviderUnavailable,
	ErrorSessionUnavailable, ErrorConflict, ErrorActionUnavailable,
	ErrorHubLaunch, ErrorQueuedDrainPartial, ErrorInternal,
}

// generateError builds a WireError-shaped object: an int code and string message
// (kept well-typed so the error frame decodes) plus an optional data blob —
// either the structured ErrorData shape or, in Adjacent mode, an arbitrary
// `any` body, since WireError.Data is untyped and round-trips either way.
func generateError(s schemagen.Source, mode schemagen.Mode) any {
	e := map[string]any{
		"code":    frameErrorCodes[s.Intn(len(frameErrorCodes), "err_code")],
		"message": s.String("err_message"),
	}
	if s.Bool("err_with_data") {
		if mode == schemagen.Adjacent {
			e["data"] = schemagen.Value(s, nil, mode)
		} else {
			info := frameErrorInfos[s.Intn(len(frameErrorInfos), "err_info")]
			e["data"] = map[string]any{"serfErrorInfo": string(info)}
		}
	}
	return e
}

// frameStructuredSeeds steer the structured generator into each kind from the
// first bytes: kind selection reads one byte, so 0x00/0x04/0x08/0x0c land on
// request/notification/response/error respectively after the leading
// adjacent-body bool byte.
var frameStructuredSeeds = [][]byte{
	{},
	{0x00, 0x00},
	{0x00, 0x01},
	{0x00, 0x02},
	{0x00, 0x03},
	{0x01, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
	[]byte("structured-but-adversarial-frame"),
}

// FuzzMessageDecodeStructured is roadmap lane 8.4: a structure-aware sibling of
// FuzzMessageDecode. It consumes fuzz bytes through generateFrame to synthesize a
// valid-but-adversarial frame, then asserts the IDENTICAL oracle as the raw-byte
// target (checkMessageDecode): never panic, and a decode→encode fixed point for
// any frame that decodes. Because the frames are structurally valid, this target
// reaches the per-kind decoders and ID/accessor paths that random bytes almost
// never construct — see TestStructuredFrameReachesDecoder for the decode-success
// gap it opens over the raw-byte target.
func FuzzMessageDecodeStructured(f *testing.F) {
	reg, _ := buildRegistry()
	for _, seed := range frameStructuredSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		raw, err := generateFrame(schemagen.NewByteSource(data), reg)
		if err != nil {
			// A generated frame that won't even marshal is a generator defect.
			t.Fatalf("generateFrame: %v", err)
		}
		checkMessageDecode(t, raw)
	})
}

// TestStructuredFrameReachesDecoder is the evidence + determinism check for lane
// 8.4. Over a fixed pseudo-random byte sample it measures how often each target's
// bytes survive the frame decoder: the structured generator must turn nearly
// every input into an accepted frame, while feeding the same bytes raw to the
// decoder (as FuzzMessageDecode does) is rejected the overwhelming majority of
// the time. It also asserts generateFrame is deterministic given identical bytes.
func TestStructuredFrameReachesDecoder(t *testing.T) {
	reg, _ := buildRegistry()
	const samples = 4000

	rng := rand.New(rand.NewSource(1)) // fixed seed: reproducible sample
	var rawAccepted, structuredAccepted int
	for i := 0; i < samples; i++ {
		data := make([]byte, rng.Intn(48))
		rng.Read(data)

		var m Message
		if json.Unmarshal(data, &m) == nil {
			rawAccepted++
		}

		frame, err := generateFrame(schemagen.NewByteSource(data), reg)
		if err != nil {
			t.Fatalf("generateFrame(%x): %v", data, err)
		}
		// Determinism: identical bytes must yield identical frames.
		frame2, err := generateFrame(schemagen.NewByteSource(data), reg)
		if err != nil {
			t.Fatalf("generateFrame(%x) second call: %v", data, err)
		}
		if !bytes.Equal(frame, frame2) {
			t.Fatalf("generateFrame not deterministic for %x:\n once=%s\n twice=%s", data, frame, frame2)
		}

		var fm Message
		if json.Unmarshal(frame, &fm) == nil {
			structuredAccepted++
		}
	}

	rawRate := float64(rawAccepted) / samples
	structuredRate := float64(structuredAccepted) / samples
	t.Logf("decode-success over %d random inputs: raw-bytes=%.1f%% (%d), structured=%.1f%% (%d)",
		samples, rawRate*100, rawAccepted, structuredRate*100, structuredAccepted)

	if structuredRate < 0.99 {
		t.Errorf("structured generator should decode nearly always, got %.1f%%", structuredRate*100)
	}
	if rawRate > 0.05 {
		t.Errorf("raw random bytes should rarely decode as a frame, got %.1f%%", rawRate*100)
	}
	if structuredRate <= rawRate {
		t.Errorf("structured (%.1f%%) must reach the decoder more than raw bytes (%.1f%%)",
			structuredRate*100, rawRate*100)
	}
}
