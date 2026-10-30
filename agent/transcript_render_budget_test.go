package agent

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestMarkdownBudgetProbeStopsAfterOverflow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		fits bool
	}{
		{"overflow", strings.Repeat("x", convBudgetChars+1), false},
		{"fitting", "b8d3-first", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const sentinel = "b8d3-later-42f7"
			entries := []transcript.Entry{
				makeEntry(schema.NewTurn(schema.TurnUserInput, llm.User(tc.body))),
				makeEntry(schema.NewTurn(schema.TurnUserInput, llm.User(sentinel))),
			}
			header := transcript.Header{}
			full := renderMarkdown(header, entries, 0, renderOpts{})
			if !strings.Contains(full, sentinel) {
				t.Fatal("unlimited control did not render later fixture data")
			}
			var b strings.Builder
			writeDocumentHeader(&b, header, renderOpts{})
			if fits := writeEntriesBodyLimited(&b, entries, 0, renderOpts{}, convBudgetChars); fits != tc.fits {
				t.Fatalf("fits=%v, want %v", fits, tc.fits)
			}
			want := full
			if !tc.fits {
				want = renderMarkdown(header, entries[:1], 0, renderOpts{})
				if len(want) <= convBudgetChars {
					t.Fatal("first-entry control did not exceed byte budget")
				}
				if strings.Contains(b.String(), sentinel) {
					t.Error("budget probe emitted later entry after established overflow")
				}
			} else if !strings.Contains(b.String(), sentinel) {
				t.Error("fitting probe skipped later fixture data")
			}
			if b.String() != want {
				t.Errorf("probe emitted %d bytes, want exact %d-byte prefix", b.Len(), len(want))
			}
		})
	}
}

func TestMarkdownBudgetProbeMatchesFullRender(t *testing.T) {
	t.Parallel()
	user := func(body string) transcript.Entry {
		return makeEntry(schema.NewTurn(schema.TurnUserInput, llm.User(body)))
	}
	paired := []transcript.Entry{
		toolCallEntry(call("fit-call", "exec_command", `{"command":"printf x"}`)),
		user("d3c8-between"),
		toolResultEntry(result("fit-call", "exec_command", makeNumberedLines(45), false)),
	}
	multiple := []transcript.Entry{
		toolCallEntry(call("fit-a", "exec_command", `{}`), call("fit-b", "read_file", `{}`)),
		toolResultEntry(result("fit-b", "read_file", "d3c8-result-b", false), result("fit-a", "exec_command", "d3c8-result-a", false)),
	}
	const delivered, ack = "d3c8-delivered", "d3c8-mechanical-ack"
	ackEntries := []transcript.Entry{
		toolCallEntry(call("fit-ack", "publish", `{"message":"`+delivered+`"}`)),
		toolResultEntry(result("fit-ack", "publish", ack, false)),
	}
	meta := schema.SessionMeta{}
	meta.Config.ResultToolName = "publish"
	assistantPin, resultPin, previewSeq := 0, 2, 0
	oversized := strings.Repeat("z", 4*oversizedTurnEvidenceChars)
	for _, tc := range []struct {
		name    string
		entries []transcript.Entry
		start   int
		opt     renderOpts
		preview bool
		ack     bool
	}{
		{name: "header-only"},
		{name: "utf8-nonzero-seq", entries: []transcript.Entry{user(strings.Repeat("界", 9000))}, start: 23},
		{name: "nonadjacent-pair", entries: paired},
		{name: "multiple-pairs", entries: multiple},
		{name: "pending-slice", entries: paired[:1]},
		{name: "unpaired-slice", entries: paired[2:], start: 2},
		{name: "configured-ack", entries: ackEntries, opt: renderOpts{meta: meta}, ack: true},
		{name: "explicit-ack", entries: ackEntries, opt: renderOpts{resultToolName: "publish"}, ack: true},
		{name: "assistant-pin", entries: paired, opt: renderOpts{fullResultFor: &assistantPin}},
		{name: "result-pin", entries: paired, opt: renderOpts{fullResultFor: &resultPin}},
		{name: "entry-head-tail", entries: []transcript.Entry{user(oversized)}, opt: renderOpts{headTailEvidenceFor: &previewSeq}, preview: true},
		{name: "orphan-head-tail", entries: []transcript.Entry{toolResultEntry(result("fit-orphan", "exec_command", oversized, false))}, opt: renderOpts{fullResultFor: &previewSeq, headTailEvidenceFor: &previewSeq}, preview: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := transcript.Header{Task: "d3c8-task"}
			full := renderMarkdown(header, tc.entries, tc.start, tc.opt)
			n := len(full)
			if n <= 1 {
				t.Fatalf("invalid reference length %d", n)
			}
			if tc.ack && (!strings.Contains(full, delivered) || strings.Contains(full, ack)) {
				t.Fatal("configured-result fixture did not consume acknowledgement and retain delivered data")
			}
			if tc.preview {
				rawOpt := tc.opt
				rawOpt.headTailEvidenceFor = nil
				raw := renderMarkdown(header, tc.entries, tc.start, rawOpt)
				if utf8.RuneCountInString(raw) <= oversizedTurnEvidenceChars || len(raw) <= n {
					t.Fatalf("preview fixture did not reduce oversized intermediate: raw=%d final=%d", len(raw), n)
				}
			}
			for _, limit := range []int{n - 1, n, n + 1, convBudgetChars} {
				t.Run(fmt.Sprintf("limit-%d", limit), func(t *testing.T) {
					sink := transcriptEvidence{spans: []transcriptEvidenceSpan{{seq: -9, start: 11, end: 22, complete: true}}}
					before := slices.Clone(sink.spans)
					opt := tc.opt
					opt.evidence = &sink
					if got, want := renderMarkdownFits(header, tc.entries, tc.start, opt, limit), n <= limit; got != want {
						t.Errorf("fits=%v, want %v for actual %d bytes at limit %d", got, want, n, limit)
					}
					if !slices.Equal(sink.spans, before) {
						t.Errorf("probe mutated caller evidence: got %+v, want %+v", sink.spans, before)
					}
				})
			}
		})
	}

	t.Run("fixed-conversation-thresholds", func(t *testing.T) {
		header := transcript.Header{}
		// Nonempty text emits a newline that an empty content part does not.
		overhead := len(renderMarkdown(header, []transcript.Entry{user("p")}, 0, renderOpts{})) - 1
		for _, target := range []int{convBudgetChars - 1, convBudgetChars, convBudgetChars + 1} {
			t.Run(fmt.Sprintf("bytes-%d", target), func(t *testing.T) {
				entries := []transcript.Entry{user(strings.Repeat("p", target-overhead))}
				full := renderMarkdown(header, entries, 0, renderOpts{})
				if len(full) != target {
					t.Fatalf("actual rendered length=%d, want threshold %d", len(full), target)
				}
				if got, want := renderMarkdownFits(header, entries, 0, renderOpts{}, convBudgetChars), target <= convBudgetChars; got != want {
					t.Errorf("fits=%v, want %v at actual %d bytes", got, want, len(full))
				}
			})
		}
	})

	t.Run("header-overflow-skips-entry", func(t *testing.T) {
		var b strings.Builder
		writeDocumentHeader(&b, transcript.Header{}, renderOpts{})
		want := b.String()
		entries := []transcript.Entry{user("d3c8-after-header")}
		if writeEntriesBodyLimited(&b, entries, 0, renderOpts{}, len(want)-1) {
			t.Error("overflowing header reported a fit")
		}
		if b.String() != want {
			t.Errorf("overflowing header emitted entry: got %d bytes, want %d", b.Len(), len(want))
		}
	})
}

func TestBudgetedEndMatchesFullRender(t *testing.T) {
	t.Parallel()
	user := func(body string) transcript.Entry {
		return makeEntry(schema.NewTurn(schema.TurnUserInput, llm.User(body)))
	}
	header := transcript.Header{}
	fitting := []transcript.Entry{user("a72e-first"), user("a72e-last")}
	exact := []transcript.Entry{user("p"), user("a72e-last")}
	overhead := len(renderMarkdown(header, exact, 0, renderOpts{})) - 1
	exact[0] = user(strings.Repeat("p", convBudgetChars-overhead))
	if n := len(renderMarkdown(header, exact, 0, renderOpts{})); n != convBudgetChars {
		t.Fatalf("exact candidate has %d bytes, want %d", n, convBudgetChars)
	}
	overflow := []transcript.Entry{user(strings.Repeat("x", convBudgetChars+1)), user("a72e-middle"), user("a72e-last")}
	nonzero := []transcript.Entry{user("a72e-outside"), user(strings.Repeat("a", 14000)), user(strings.Repeat("b", 14000)), user("a72e-last")}
	pinned := []transcript.Entry{
		user(strings.Repeat("x", convBudgetChars+1)),
		toolCallEntry(call("end-pin", "exec_command", `{}`)),
		toolResultEntry(result("end-pin", "exec_command", "a72e-result", false)),
		user("a72e-last"),
	}
	pastEnd := []transcript.Entry{
		user(strings.Repeat("a", 15000)), user(strings.Repeat("b", 15000)),
		toolCallEntry(call("end-past", "exec_command", `{}`)),
		toolResultEntry(result("end-past", "exec_command", "a72e-past-result", false)),
	}
	assistantPin, resultPin, pastPin := 1, 2, 2
	for _, tc := range []struct {
		name       string
		entries    []transcript.Entry
		start, end int
		pin        *int
		want       int
	}{
		{"fitting", fitting, 0, 1, nil, 1},
		{"exact-threshold", exact, 0, 1, nil, 1},
		{"overflow-to-one", overflow, 0, 2, nil, 0},
		{"single-entry-floor", overflow[:1], 0, 0, nil, 0},
		{"nonzero-start", nonzero, 1, 3, nil, 1},
		{"whole-assistant-pin", pinned, 0, 3, &assistantPin, 2},
		{"whole-result-pin", pinned, 0, 3, &resultPin, 2},
		{"result-past-end", pastEnd, 0, 2, &pastPin, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt := renderOpts{fullResultFor: tc.pin}
			want := budgetedEndFullRenderReference(header, tc.entries, tc.start, tc.end, opt)
			if want != tc.want {
				t.Fatalf("reference end=%d, want fixture boundary %d", want, tc.want)
			}
			if got := budgetedEnd(header, tc.entries, tc.start, tc.end, opt); got != want {
				t.Errorf("end=%d, want full-render end %d", got, want)
			}
		})
	}
}

// Preserve the pre-probe candidate algorithm as a test-only reference.
func budgetedEndFullRenderReference(header transcript.Header, entries []transcript.Entry, start, end int, opt renderOpts) int {
	pinFloor := -1
	if opt.fullResultFor != nil {
		if as, lastSeq, ok := resolvePinnedSpan(entries, *opt.fullResultFor); ok && as >= start && lastSeq <= end {
			pinFloor = lastSeq
		}
	}
	last := end
	for last > start {
		body := renderMarkdown(header, entries[start:last+1], start, opt)
		if len(body) <= convBudgetChars {
			break
		}
		if last <= pinFloor {
			break
		}
		last--
	}
	return last
}
