package appwire

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// This file is serf's first DIFFERENTIAL fuzz oracle: a golden/snapshot of what
// each decode target produces for its committed seed corpus. The fuzz targets
// (FuzzMessageDecode et al.) catch inputs that PANIC or break the round-trip
// fixed point; they say nothing about a refactor that silently changes a clean
// decode's OUTPUT — wrong field, dropped value, reordered shape — with no crash.
// The snapshot is that missing oracle: replay the corpus, canonically re-encode
// every decoded value, and fail the gate on any diff against the committed
// golden. Regenerate intentionally with `make fuzz-goldens`.

// updateGoldens rewrites the decode snapshots instead of checking them. Wired to
// `make fuzz-goldens`; the check side runs under plain `make test`/`make fuzz`.
var updateGoldens = flag.Bool("update-goldens", false,
	"rewrite the appwire decode golden snapshots from the current decoders")

// goldenDir holds the committed decode snapshots, one file per decode target.
const goldenDir = "testdata/golden"

// indexedSeed is a corpus item for a decode target whose fuzz signature is
// (shapeIndex, bytes): shape selects the concrete decode type, raw is the JSON
// fed to it. Shared by the params/codex-item fuzz targets and the golden
// snapshot so both replay exactly one corpus.
type indexedSeed struct {
	shape int
	raw   string
}

// goldenRecord is one corpus item's canonical decode outcome. Decoded reports
// whether the decoder accepted the input; Output is the decoded value re-encoded
// with encoding/json (which sorts map keys, so the bytes are stable run-to-run).
// Detail names the decode variant (method name / concrete item type) so a drift
// diff reads cleanly. A decode ERROR records only Decoded=false, never the error
// text: stdlib error strings are not part of our decoder's contract and churn
// across Go toolchains, so snapshotting them would flake on a compiler upgrade
// instead of flagging a real behavior change.
type goldenRecord struct {
	Input   string          `json:"input"`
	Detail  string          `json:"detail,omitempty"`
	Decoded bool            `json:"decoded"`
	Output  json.RawMessage `json:"output,omitempty"`
}

// canonicalRecord captures one decode's outcome. value is the decoded value to
// re-encode; accepted reports whether the decoder took the input. When accepted,
// re-marshal must succeed (an accepted-but-unmarshalable value is itself a
// defect, exactly as the fuzz fixed-point oracle treats it).
func canonicalRecord(t *testing.T, input, detail string, value any, accepted bool) goldenRecord {
	t.Helper()
	rec := goldenRecord{Input: input, Detail: detail, Decoded: accepted}
	if !accepted {
		return rec
	}
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("decoded value failed to re-marshal: %v\n input=%q value=%#v", err, input, value)
	}
	rec.Output = out
	return rec
}

// checkOrUpdateGolden compares records against testdata/golden/<name>.json, or
// rewrites it under -update-goldens. The file is pretty-printed so a drift diff
// reads line-by-line in review.
func checkOrUpdateGolden(t *testing.T, name string, records []goldenRecord) {
	t.Helper()
	want, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	want = append(want, '\n')
	path := filepath.Join(goldenDir, name+".json")

	if *updateGoldens {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `make fuzz-goldens` to create it)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decode golden drift for %s.\n"+
			"A decoder changed what it produces for the committed corpus. If the change is\n"+
			"INTENDED, run `make fuzz-goldens` and commit the updated snapshot; otherwise it is\n"+
			"a regression.\n--- committed snapshot ---\n%s\n--- current decoders ---\n%s",
			name, got, want)
	}
}

// TestMessageDecodeGolden snapshots FuzzMessageDecode's corpus: each seed frame
// decoded and canonically re-encoded. It catches a change to what the AppWire
// frame decoder produces that nothing in the fixed-point oracle would notice.
func TestMessageDecodeGolden(t *testing.T) {
	records := make([]goldenRecord, 0, len(messageDecodeSeeds))
	for _, raw := range messageDecodeSeeds {
		var m Message
		accepted := json.Unmarshal([]byte(raw), &m) == nil
		records = append(records, canonicalRecord(t, raw, "", m, accepted))
	}
	checkOrUpdateGolden(t, "FuzzMessageDecode", records)
}

// TestMethodParamsGolden snapshots FuzzMethodParams's corpus: each seed decoded
// into its method's concrete Params type and canonically re-encoded. A method
// whose Params type declares nil records Decoded=false (no decode performed).
func TestMethodParamsGolden(t *testing.T) {
	records := make([]goldenRecord, 0, len(methodParamsSeeds))
	for _, seed := range methodParamsSeeds {
		idx := ((seed.shape % len(Methods)) + len(Methods)) % len(Methods)
		spec := Methods[idx]
		var value any
		accepted := false
		if paramsType := reflect.TypeOf(spec.Params); paramsType != nil {
			p := reflect.New(paramsType).Interface()
			if json.Unmarshal([]byte(seed.raw), p) == nil {
				value, accepted = p, true
			}
		}
		records = append(records, canonicalRecord(t, seed.raw, spec.Name, value, accepted))
	}
	checkOrUpdateGolden(t, "FuzzMethodParams", records)
}

// TestCodexItemDecodeGolden snapshots FuzzCodexItemDecode's corpus: each seed
// decoded into its selected codex item/thread/turn type and canonically
// re-encoded.
func TestCodexItemDecodeGolden(t *testing.T) {
	records := make([]goldenRecord, 0, len(codexItemDecodeSeeds))
	for _, seed := range codexItemDecodeSeeds {
		idx := ((seed.shape % len(codexItemTypes)) + len(codexItemTypes)) % len(codexItemTypes)
		itemType := reflect.TypeOf(codexItemTypes[idx])
		v := reflect.New(itemType).Interface()
		accepted := json.Unmarshal([]byte(seed.raw), v) == nil
		var value any
		if accepted {
			value = v
		}
		records = append(records, canonicalRecord(t, seed.raw, itemType.String(), value, accepted))
	}
	checkOrUpdateGolden(t, "FuzzCodexItemDecode", records)
}
