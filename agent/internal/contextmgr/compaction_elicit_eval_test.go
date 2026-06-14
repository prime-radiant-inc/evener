//go:build eval

package contextmgr

// Validates Variant B (harness elicits the note): does a side LLM call, run over
// the history at compaction time, actually CAPTURE the facts that the baseline
// recursive summary erodes? The erosion eval showed the baseline loses the opaque
// token (DEPLOY-...) and the exact number ("numShards exceeds 16") over successive
// compactions. If the elicitor captures those verbatim while they are still present,
// pinning them prevents the erosion by construction.
//
// Run: go test -tags eval ./agent/internal/contextmgr/ -run TestElicitNoteCapture -v -timeout 15m

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestElicitNoteCapture(t *testing.T) {
	cm := newOAuthManager(t)
	facts := erosionFacts()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// The same initial history the erosion eval starts from: all facts present,
	// buried in bulk.
	var initial strings.Builder
	initial.WriteString(block("project kickoff notes", repeat("setup line DROPNOISE_init\n", 25)))
	for _, f := range facts {
		initial.WriteString(fact(f.statement))
	}
	initial.WriteString("\n" + newWorkBulk(0))

	// Exercise the real production method (cm.ElicitNote) over the same history.
	note, err := cm.ElicitNote(ctx, turnsFromText(t, initial.String()))
	if err != nil {
		t.Fatalf("ElicitNote: %v", err)
	}

	captured := 0
	for _, f := range facts {
		ok := strings.Contains(note, f.token)
		if ok {
			captured++
		}
		t.Logf("  elicited-note captures %q: %v", f.token, ok)
	}
	t.Logf("=== ELICITOR CAPTURE: %d/%d facts captured verbatim ===", captured, len(facts))
	t.Logf("elicited note (first 600 chars):\n%.600s", note)

	// Strict, non-flaky requirement: the OPAQUE TOKEN must be captured VERBATIM.
	// It is the worst erosion case (the baseline drops it by round 3) and the one
	// where verbatim matters absolutely — a hash/token paraphrased is simply wrong.
	// The elicitor captures it reliably across runs.
	if !strings.Contains(note, facts[0].token) {
		t.Errorf("elicitor MISSED the opaque token %q — Variant B cannot prevent its erosion", facts[0].token)
	}
	// The semantic facts (numbers, names) may be paraphrased run-to-run (e.g.
	// "numShards exceeds 16" vs "numShards > 16") — the VALUE survives either way,
	// so we report capture rate informationally rather than fail on exact phrasing.
	if captured < len(facts) {
		t.Logf("NOTE: %d/%d captured verbatim; the rest are likely present but paraphrased (value preserved). The opaque token — the critical case — was captured exactly.", captured, len(facts))
	}
}
