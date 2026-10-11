package transcriptindex

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// TestPagedReadFlushesTrailingZeroItemGroupCommunicate is ported from
// internal/apptranscript's turn_index_zero_item_group_test.go (deleted from
// that package on this branch; see git show origin/main:internal/apptranscript/
// for the original), which guarded the bounded turn-paged reader's own
// group-skipping: it skipped a zero-item legacy group without projecting it,
// so a deferred communicate call the group's only entry issued never seeded
// CommRawArgs, and the tail flush that should render it saw nothing. That
// bounded reader is gone (this package's Index replaces it), but the shape it
// guards against — a trailing legacy group with no items of its own, holding
// only an unpaired communicate call — is still real, so this test exercises
// it against the index's own paged reads (Latest/Before) instead.
func trailingZeroItemGroupCommunicateFixture() []fixtureLine {
	return []fixtureLine{
		// Leading standalone turn: SUMMARY with text produces a
		// systemMessage item, so its group has items and gets a slot,
		// closing before the next entry opens its own group.
		entryLine(standalone(schema.TurnSummary, "compaction summary")),
		// Trailing text-less assistant turn with only a communicate call.
		// The SUMMARY closed its group, so this ASSISTANT starts its own
		// group with no items of its own (the communicate is deferred, no
		// text). No result turn follows: the communicate is unpaired, so
		// the transcript ends on it.
		entryLine(assistant(call("call_trailing_zero", "communicate", `{"message":"delivered at the tail"}`))),
	}
}

func TestPagedReadFlushesTrailingZeroItemGroupCommunicate(t *testing.T) {
	fx := fixture{header: transcript.Header{SessionID: "th_trailing_zero_item_group"}, lines: trailingZeroItemGroupCommunicateFixture()}
	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())

	want := referenceCandidates(t, path)
	flushed := false
	for _, c := range want {
		if c.Item.Type == "agentMessage" && c.Item.CallID == "call_trailing_zero" {
			flushed = true
			if c.Item.Text != "delivered at the tail" {
				t.Fatalf("reference flushed Text = %q, want %q", c.Item.Text, "delivered at the tail")
			}
		}
	}
	if !flushed {
		t.Fatalf("reference produced no flushed communicate item; candidates: %s", dump(want))
	}

	window, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, "latest flushes the trailing zero-item group's communicate", window, want, len(want), 40)

	// The same must hold on a bounded page that only reaches the tail via
	// Before: a paged (not full) read must see the identical flush.
	if len(want) < 2 {
		t.Fatalf("fixture too small to page: %d candidates", len(want))
	}
	before, err := x.Before(want[len(want)-1].Position, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, "before the last candidate", before, want, len(want)-1, 1)
}
