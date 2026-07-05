package schema

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// TestSessionMeta_CumulativeUsageOmitzero proves CumulativeUsage and WorkMillis
// marshal onto SessionMeta when set, and are both omitted (via omitzero) on a
// zero-valued legacy SessionMeta, so old metas without these fields round-trip
// unchanged (WS2 working-state-metrics).
func TestSessionMeta_CumulativeUsageOmitzero(t *testing.T) {
	meta := SessionMeta{
		CumulativeUsage: CumulativeUsage{
			InputTokens:     100,
			OutputTokens:    200,
			CacheReadTokens: 50,
			TotalTokens:     300,
		},
		WorkMillis: 45000,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const wantUsage = `"cumulative_usage":{"input_tokens":100,"output_tokens":200,"cache_read_tokens":50,"total_tokens":300}`
	if !strings.Contains(string(data), wantUsage) {
		t.Errorf("marshal = %s, want substring %s", data, wantUsage)
	}
	const wantWorkMillis = `"work_millis":45000`
	if !strings.Contains(string(data), wantWorkMillis) {
		t.Errorf("marshal = %s, want substring %s", data, wantWorkMillis)
	}

	zeroData, err := json.Marshal(SessionMeta{})
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	if strings.Contains(string(zeroData), "cumulative_usage") {
		t.Errorf("zero-valued SessionMeta marshal = %s, want no cumulative_usage key", zeroData)
	}
	if strings.Contains(string(zeroData), "work_millis") {
		t.Errorf("zero-valued SessionMeta marshal = %s, want no work_millis key", zeroData)
	}
}

// saveSessionMetaFS round-trips through an in-memory fs and surfaces a mkdir
// failure on a read-only fs.
func TestCov_SaveSessionMetaFS(t *testing.T) {
	mem := afero.NewMemMapFs()
	meta := SessionMeta{ID: "01SESSIONXXXXXXXXXXXXXXXXXX"}
	if err := saveSessionMetaFS(mem, "/state", meta); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadSessionMetaFS(mem, "/state", meta.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.ID != meta.ID {
		t.Errorf("round-trip ID = %q, want %q", got.ID, meta.ID)
	}

	// Read-only fs → MkdirAll fails → save surfaces an error.
	ro := afero.NewReadOnlyFs(afero.NewMemMapFs())
	if err := saveSessionMetaFS(ro, "/state", meta); err == nil {
		t.Fatal("save on a read-only fs should error")
	}
}

// listSessionMetasFS returns nil for a missing sessions dir and lists saved metas.
func TestCov_ListSessionMetasFS(t *testing.T) {
	mem := afero.NewMemMapFs()
	metas, err := listSessionMetasFS(mem, "/nowhere")
	if err != nil || metas != nil {
		t.Errorf("missing dir should yield (nil,nil), got %v %v", metas, err)
	}
	if err := saveSessionMetaFS(mem, "/state", SessionMeta{ID: "01AAA"}); err != nil {
		t.Fatal(err)
	}
	metas, err = listSessionMetasFS(mem, "/state")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != "01AAA" {
		t.Errorf("list = %+v, want one meta 01AAA", metas)
	}
}
