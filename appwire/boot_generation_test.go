package appwire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompareBootGeneration(t *testing.T) {
	for _, tc := range []struct {
		held, incoming string
		want           BootGenerationAction
	}{
		{"3", "3", BootGenerationApply},
		{DaemonlessBootGeneration, DaemonlessBootGeneration, BootGenerationApply},
		{"3", "2", BootGenerationIgnore},
		{"10", "9", BootGenerationIgnore},
		{"3", "4", BootGenerationReplace},
		// Numeric, not lexical: 10 is higher than 9.
		{"9", "10", BootGenerationReplace},
		{"3", DaemonlessBootGeneration, BootGenerationReplace},
		{DaemonlessBootGeneration, "1", BootGenerationReplace},
		// Nothing held yet: whatever arrives replaces.
		{"", "1", BootGenerationReplace},
		{"", DaemonlessBootGeneration, BootGenerationReplace},
		// A descendant's token, qualified by its root: same owner compares
		// numerically.
		{"3@root1", "3@root1", BootGenerationApply},
		{"3@root1", "2@root1", BootGenerationIgnore},
		{"3@root1", "4@root1", BootGenerationReplace},
		{"9@root1", "10@root1", BootGenerationReplace},
		// Different owners never compare.
		{"3@root1", "2@root2", BootGenerationReplace},
		{"3@root1", "4@root2", BootGenerationReplace},
		// Qualified against unqualified, either way, replaces.
		{"3@root1", "2", BootGenerationReplace},
		{"3", "2@root1", BootGenerationReplace},
		{"3", "4@root1", BootGenerationReplace},
		// daemonless against a qualified token, either way, replaces.
		{"3@root1", DaemonlessBootGeneration, BootGenerationReplace},
		{DaemonlessBootGeneration, "1@root1", BootGenerationReplace},
		// A malformed token is some other token: it replaces.
		{"3@", "2@", BootGenerationReplace},
		{"3", "x", BootGenerationReplace},
	} {
		if got := CompareBootGeneration(tc.held, tc.incoming); got != tc.want {
			t.Errorf("CompareBootGeneration(%q, %q) = %v, want %v", tc.held, tc.incoming, got, tc.want)
		}
	}
}

func TestBootGenerationFormatsTheCounter(t *testing.T) {
	if got := BootGeneration(12); got != "12" {
		t.Fatalf("BootGeneration(12) = %q", got)
	}
	if got := DescendantBootGeneration("12", "root1"); got != "12@root1" {
		t.Fatalf("DescendantBootGeneration = %q", got)
	}
}

// TestBootGenerationRidesEveryHistoryMessage pins the field on each message
// the spec names: history/updated, the resync push, and both read responses.
func TestBootGenerationRidesEveryHistoryMessage(t *testing.T) {
	for name, v := range map[string]any{
		"history/updated":   HistoryUpdatedParams{BootGeneration: "7"},
		"resync":            ThreadResyncParams{BootGeneration: "7"},
		"thread/read":       ThreadReadResponse{BootGeneration: "7"},
		"thread/turns/list": ThreadTurnsListResponse{BootGeneration: "7"},
	} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"bootGeneration":"7"`) {
			t.Errorf("%s JSON %s lacks bootGeneration", name, raw)
		}
	}
}

// TestTurnsListCarriesNoRequestGeneration: request generations order
// latest-window reads only; a backfill page carries none.
func TestTurnsListCarriesNoRequestGeneration(t *testing.T) {
	var params ThreadTurnsListParams
	if err := json.Unmarshal([]byte(`{"threadId":"t","requestGeneration":3}`), &params); err == nil {
		t.Fatal("thread/turns/list params accepted requestGeneration")
	}
	raw, err := json.Marshal(ThreadTurnsListResponse{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "requestGeneration") {
		t.Fatalf("thread/turns/list response JSON %s carries requestGeneration", raw)
	}
}

func TestThreadItemCompletedAtEntry(t *testing.T) {
	raw, err := json.Marshal(ThreadItem{Type: "toolCall", ID: "i", CompletedAtEntry: 9})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"completedAtEntry":9`) {
		t.Fatalf("ThreadItem JSON %s lacks completedAtEntry", raw)
	}
	raw, err = json.Marshal(ThreadItem{Type: "toolCall", ID: "i"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "completedAtEntry") {
		t.Fatalf("ThreadItem JSON %s carries a zero completedAtEntry", raw)
	}
}

// TestHistoryReadErrorCarriesGenerationAndEpoch: a failed history read names
// the entry and carries the boot generation and epoch, and nothing else a
// client could adopt (no snapshot identity, no items).
func TestHistoryReadErrorCarriesGenerationAndEpoch(t *testing.T) {
	err := WithHistoryReadIdentity(TranscriptHistoryFailed(7), "4", 2)
	if err.Code != CodeInternalError || err.Message != "thread history failed at entry 7" {
		t.Fatalf("error = %+v", err)
	}
	data, ok := err.Data.(HistoryReadErrorData)
	if !ok {
		t.Fatalf("Data = %T, want HistoryReadErrorData", err.Data)
	}
	if data.EvenerErrorInfo != ErrorTranscriptHistoryFailed || data.BootGeneration != "4" || data.Epoch != 2 {
		t.Fatalf("data = %+v", data)
	}
	raw, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, want := range []string{`"evenerErrorInfo":"transcriptHistoryFailed"`, `"bootGeneration":"4"`, `"epoch":2`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("error JSON %s lacks %s", raw, want)
		}
	}
	for _, unwanted := range []string{"snapshot", "incarnation", "items"} {
		if strings.Contains(string(raw), unwanted) {
			t.Errorf("error JSON %s carries %s", raw, unwanted)
		}
	}
}

// TestHistoryReadIdentityKeepsTheErrorsOwnData: stamping a read error keeps
// its discriminant and retry disposition.
func TestHistoryReadIdentityKeepsTheErrorsOwnData(t *testing.T) {
	err := WithHistoryReadIdentity(TranscriptItemCursorStale(), DaemonlessBootGeneration, 0)
	data, ok := err.Data.(HistoryReadErrorData)
	if !ok {
		t.Fatalf("Data = %T", err.Data)
	}
	if data.EvenerErrorInfo != ErrorTranscriptItemCursorStale || data.RetryDisposition != RetryDispositionAutomatic || data.BootGeneration != DaemonlessBootGeneration {
		t.Fatalf("data = %+v", data)
	}
}
