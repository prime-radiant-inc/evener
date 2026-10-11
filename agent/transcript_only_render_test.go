package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/agent/transcript"
)

// The transcript tool renders through publicTranscriptData, which already
// drops transcript-only entries; the renderers also pass over one directly,
// the way they pass over an attention resolution.
func TestRenderersPassOverTranscriptOnlyEntries(t *testing.T) {
	for _, sample := range entriesOf(schematest.TranscriptOnlySamples()) {
		one := []transcript.Entry{sample}
		idx := buildResultIndex(one, 0)
		if line, ok := outlineLine(one, 0, &idx); ok {
			t.Errorf("outline gave %s a line: %q", sample.Turn.Kind, line)
		}
		var b strings.Builder
		writeEntry(&b, 0, sample, "communicate", &idx, renderOpts{})
		if b.Len() != 0 {
			t.Errorf("markdown rendered %s: %q", sample.Turn.Kind, b.String())
		}
	}
	plainTurns := transcriptOnlyFixtureTurns()
	plain := publicTranscriptData(transcriptDataOf(t, entriesOf(plainTurns)))
	interleaved := publicTranscriptData(transcriptDataOf(t, entriesOf(schematest.InterleaveTranscriptOnly(plainTurns))))
	wantOutline, _, _ := renderOutline(plain.Entries, 0, len(plain.Entries)-1)
	if got, _, _ := renderOutline(interleaved.Entries, 0, len(interleaved.Entries)-1); got != wantOutline {
		t.Fatalf("outline differs:\n got %q\nwant %q", got, wantOutline)
	}
	want := renderMarkdown(plain.Header, plain.Entries, 0, renderOpts{})
	if got := renderMarkdown(interleaved.Header, interleaved.Entries, 0, renderOpts{}); got != want {
		t.Fatalf("markdown differs:\n got %q\nwant %q", got, want)
	}
}
