package hostops

import (
	"encoding/json"
	"os"
	"testing"
)

// Shared seeding helpers for the quarantine refusal tables. The intent-arms,
// epoch-sidecar and fault-ladder tests share the custody and intent body
// builders, and the intent and epoch rows share the deferred artifact seeder.

// validCustodyBody is a complete custody file for the store at path: one fence
// import at the high-water mark, exactly the seven-key schema the writing boot
// emits. The mutate hook lets a row bend one field without re-stating the
// whole document.
func validCustodyBody(t *testing.T, path string, mutate func(map[string]any)) []byte {
	t.Helper()
	boundary := []any{map[string]any{"kind": "local-markerless", "platform": "linux", "nonce": "n"}}
	body := map[string]any{
		"quarantineEpoch":        2,
		"quarantinedFile":        path,
		"custodiedAt":            "2026-09-26T00:00:00Z",
		"recordIds":              []any{map[string]any{"recordId": "00000000000000000009", "host": "hB"}},
		"allocatorHighWaterMark": 9,
		"fences":                 []any{map[string]any{"recordId": "00000000000000000009", "host": "hB", "kind": "restart", "clientOperationId": "op-hB", "generation": 3, "incarnationId": "inc-hB", "quarantine": false, "boundary": boundary}},
		"ownership":              []any{map[string]any{"quarantineRecordId": "00000000000000000009", "host": "hB", "kind": "restart", "clientOperationId": "op-hB", "generation": 3, "highWaterMark": 3, "incarnationId": "inc-hB"}},
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal custody body: %v", err)
	}
	return raw
}

// validIntentBody is the pending-work record the writing boot leaves for the
// store at path, naming the stamp's custody and aside artifacts.
func validIntentBody(t *testing.T, path, stamp string, mutate func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"corruptFile":     path,
		"custodyFile":     quarantineCustodyPath(path, stamp),
		"asideFile":       quarantineAsidePath(path, stamp),
		"quarantinedAt":   "2026-09-26T00:00:00Z",
		"quarantineEpoch": 2,
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal intent body: %v", err)
	}
	return raw
}

// artifactSeed prepares one store directory for a refusal row. path is the row's
// own store path, so parallel rows never share state.
type artifactSeed func(t *testing.T, path string)

// seedArtifactDeferred defers a fixed body to a path derived from the row's
// store path.
func seedArtifactDeferred(derive func(path string) string, body []byte) artifactSeed {
	return func(t *testing.T, path string) {
		t.Helper()
		dest := derive(path)
		if err := os.WriteFile(dest, body, 0o600); err != nil {
			t.Fatalf("write %s: %v", dest, err)
		}
	}
}
