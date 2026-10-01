package agent

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

func TestSessionActivityReportPreviewReadsBoundedPrefix(t *testing.T) {
	// A surrogate pair occupies twelve encoded bytes but one Unicode code point.
	message := json.RawMessage(`"` + strings.Repeat(`\ud83d\ude00`, 100000) + `"`)
	aggregate := &delegatestore.Aggregate{DelegateID: "dlg", Generation: 1, Phase: delegatestore.PhaseIdle, LatestOutcome: &delegatestore.Outcome{Status: delegatestore.OutcomeCompleted}, LatestPacket: &delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: message}}
	state := delegatestore.State{"dlg": aggregate}
	candidate := captureSessionActivityDelegate("root", state, aggregate, time.Unix(100, 0))
	maxPrefixBytes := 1 + 12*(activityMaxDelegateProseRunes+1)
	t.Run("retained prefix", func(t *testing.T) {
		if len(candidate.report) > maxPrefixBytes {
			t.Fatalf("preview retains %d report bytes, limit %d", len(candidate.report), maxPrefixBytes)
		}
	})
	row := candidate.project()
	if row.ReportPreview != strings.Repeat("😀", activityMaxDelegateProseRunes-1)+"…" || !row.ReportPreviewTruncated {
		t.Fatalf("surrogate preview: runes=%d truncated=%v", utf8.RuneCountInString(row.ReportPreview), row.ReportPreviewTruncated)
	}
	if len(aggregate.LatestPacket.Message) != len(message) {
		t.Fatal("preview changed the durable full report")
	}
	// The allocation ceiling is the existing public page budget, independent
	// of the much larger durable tail. It includes capture and rendering.
	measurement := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			captureSessionActivityDelegate("root", state, aggregate, time.Unix(100, 0)).project()
		}
	})
	t.Logf("bounded preview allocation: %d bytes per row", measurement.AllocedBytesPerOp())
	if measurement.AllocedBytesPerOp() > sessionActivityPageBytes {
		t.Fatalf("preview allocated %d bytes per row, page budget %d", measurement.AllocedBytesPerOp(), sessionActivityPageBytes)
	}
}

func TestSessionActivityReportPreviewEncodedBoundaries(t *testing.T) {
	t.Parallel()
	runeLimit := activityMaxDelegateProseRunes
	for _, tc := range []struct {
		name      string
		message   json.RawMessage
		want      string
		truncated bool
	}{
		{"exact cap", mustReportJSON(t, strings.Repeat("界", runeLimit)), strings.Repeat("界", runeLimit), false},
		{"one beyond cap", mustReportJSON(t, strings.Repeat("界", runeLimit+1)), strings.Repeat("界", runeLimit-1) + "…", true},
		{"utf8 prefix cut", mustReportJSON(t, "a"+strings.Repeat("😀", runeLimit*10)), "a" + strings.Repeat("😀", runeLimit-2) + "…", true},
		{"unicode escape prefix cut", json.RawMessage(`"a` + strings.Repeat(`\u754c`, runeLimit*10) + `"`), "a" + strings.Repeat("界", runeLimit-2) + "…", true},
		{"surrogate prefix cut", json.RawMessage(`"a` + strings.Repeat(`\ud83d\ude00`, runeLimit*10) + `"`), "a" + strings.Repeat("😀", runeLimit-2) + "…", true},
		{"simple escape prefix cut", json.RawMessage(`"a` + strings.Repeat(`\n`, runeLimit*20) + `"`), "a" + strings.Repeat("\n", runeLimit-2) + "…", true},
		{"leading whitespace before huge report", json.RawMessage(strings.Repeat(" ", 100) + `"` + strings.Repeat(`\ud83d\ude00`, runeLimit*10) + `"`), strings.Repeat("😀", runeLimit-1) + "…", true},
		{"surrounding whitespace", json.RawMessage(" \n\t\"report\"\r\n "), "report", false},
		{"large trailing whitespace", json.RawMessage(`"report"` + strings.Repeat(" ", runeLimit*20)), "report", false},
		{"bounded leading whitespace", json.RawMessage(strings.Repeat(" ", runeLimit*20) + `"report"`), "", false},
		{"structured result", json.RawMessage(`{"report":"text"}`), "", false},
		{"malformed escape", json.RawMessage(`"\q` + strings.Repeat("x", runeLimit*20) + `"`), "", false},
		{"malformed unicode", json.RawMessage(`"\uZZZZ` + strings.Repeat("x", runeLimit*20) + `"`), "", false},
		{"unterminated short string", json.RawMessage(`"short`), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aggregate := &delegatestore.Aggregate{DelegateID: "dlg", Generation: 1, Phase: delegatestore.PhaseIdle, LatestOutcome: &delegatestore.Outcome{Status: delegatestore.OutcomeCompleted}, LatestPacket: &delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: tc.message}}
			row := captureSessionActivityDelegate("root", delegatestore.State{"dlg": aggregate}, aggregate, time.Unix(100, 0)).project()
			if row.ReportPreview != tc.want || row.ReportPreviewTruncated != tc.truncated || !utf8.ValidString(row.ReportPreview) {
				t.Fatalf("preview runes=%d truncated=%v, want runes=%d truncated=%v", utf8.RuneCountInString(row.ReportPreview), row.ReportPreviewTruncated, utf8.RuneCountInString(tc.want), tc.truncated)
			}
		})
	}
}

func TestSessionActivityReportPreviewHugeRetainedReport(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
	at := time.Unix(100, 0).UTC()
	id := "dlg_huge_report"
	message := json.RawMessage(`"` + strings.Repeat(`\ud83d\ude00`, 100000) + `"`)
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: message}
	seedStableReadonlyFinish(t, s, id, stableToolDescriptor(s, id, ""), at, delegateFinish{outcome: delegatestore.OutcomeCompleted, disposition: delegatestore.DispositionReported, endedAt: at.Add(time.Second), packet: &packet}, true)
	params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID())}
	live, err := s.ListActivityDelegates(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	// Drop this fixture's live cache so the retained read reconstructs the journal.
	key := s.stateDir + "\x00" + s.ID()
	sessionActivityIndexes.Lock()
	if cached := sessionActivityIndexes.entries[key]; cached != nil {
		delete(sessionActivityIndexes.entries, key)
		sessionActivityIndexes.order.Remove(cached.element)
	}
	sessionActivityIndexes.Unlock()
	retained, err := LoadSessionActivityDelegates(t.Context(), s.stateDir, s.ID(), params)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []appwire.SessionDelegatesResponse{live, retained} {
		if len(page.Delegates) != 1 {
			t.Fatalf("rows=%d", len(page.Delegates))
		}
		row := page.Delegates[0]
		if row.RunGeneration != 1 || row.ReportPreview != strings.Repeat("😀", activityMaxDelegateProseRunes-1)+"…" || !row.ReportPreviewTruncated {
			t.Fatalf("generation=%d preview runes=%d truncated=%v", row.RunGeneration, utf8.RuneCountInString(row.ReportPreview), row.ReportPreviewTruncated)
		}
		encoded, err := json.Marshal(page)
		if err != nil || len(encoded) > sessionActivityPageBytes {
			t.Fatalf("page bytes=%d err=%v", len(encoded), err)
		}
	}
	events, err := s.delegateController.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := delegatestore.Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	durable := replayed[id]
	if durable == nil || durable.Generation != 1 || durable.LatestPacket == nil || !bytes.Equal(durable.LatestPacket.Message, message) {
		t.Fatal("durable full report lost its generation or tail")
	}
}
