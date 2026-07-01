package doctor

import (
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/fuzz/oracle"
)

// doctor_reader is a tiny deterministic byte consumer that decodes a fuzz blob
// into the diagnostic-report structs the doctor Render* functions format. It
// never fails: when the blob runs dry it yields zero values, so every field
// combination is reachable from some input while a short blob still produces a
// valid (small) report. Bounds are kept low so the fuzzer explores field
// combinations, not giant reports.
type doctor_reader struct {
	b   []byte
	pos int
}

func doctor_newReader(b []byte) *doctor_reader { return &doctor_reader{b: b} }

func (r *doctor_reader) doctor_u8() byte {
	if r.pos >= len(r.b) {
		return 0
	}
	v := r.b[r.pos]
	r.pos++
	return v
}

func (r *doctor_reader) doctor_bool() bool { return r.doctor_u8()&1 == 1 }

// doctor_int returns a small non-negative int in [0, mod).
func (r *doctor_reader) doctor_int(mod int) int {
	if mod <= 0 {
		return 0
	}
	return int(r.doctor_u8()) % mod
}

// doctor_str reads a length-prefixed string. It can produce empty strings (to
// exercise the dash/omit branches) and, occasionally, strings carrying format
// verbs, newlines, or the WatchID of another watch — none of which the render
// functions may choke on.
func (r *doctor_reader) doctor_str() string {
	n := r.doctor_int(6) // 0..5 bytes keeps identifiers short but non-trivial
	if n == 0 {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteByte(r.doctor_u8())
	}
	return sb.String()
}

func doctor_buildProvenance(r *doctor_reader) *provenance.Causal {
	if !r.doctor_bool() {
		return nil
	}
	p := &provenance.Causal{ChainTruncated: r.doctor_bool()}
	for i, n := 0, r.doctor_int(3); i < n; i++ {
		p.WatchKeys = append(p.WatchKeys, provenance.WatchKey{
			WatchID:         r.doctor_str(),
			WatchGeneration: r.doctor_str(),
		})
	}
	for i, n := 0, r.doctor_int(3); i < n; i++ {
		p.Chain = append(p.Chain, provenance.Entry{
			Kind:       r.doctor_str(),
			WatchID:    r.doctor_str(),
			DeliveryID: r.doctor_str(),
		})
	}
	return p
}

func doctor_buildWatchReport(r *doctor_reader) WatchReport {
	rep := WatchReport{
		SessionID: r.doctor_str(),
		JobsPath:  r.doctor_str(),
	}
	// Filtered picks one of the render-relevant filter labels so the
	// empty-watches branch selects each emptyWatchesMessage arm.
	switch r.doctor_int(4) {
	case 1:
		rep.Filtered = "self-loops"
	case 2:
		rep.Filtered = "watch:" + r.doctor_str()
	case 3:
		rep.Filtered = "self-loops,watch:" + r.doctor_str()
	}
	for i, n := 0, r.doctor_int(6); i < n; i++ {
		rep.Watches = append(rep.Watches, doctor_buildWatchView(r))
	}
	return rep
}

func doctor_buildWatchView(r *doctor_reader) WatchView {
	w := WatchView{
		WatchID:            r.doctor_str(),
		Generation:         r.doctor_str(),
		OwnerSessionID:     r.doctor_str(),
		VisibleSessionID:   r.doctor_str(),
		Target:             r.doctor_str(),
		SendTo:             r.doctor_str(),
		Condition:          r.doctor_str(),
		Active:             r.doctor_bool(),
		EndReason:          r.doctor_str(),
		PendingLines:       r.doctor_int(20),
		DistinctDeliveries: r.doctor_int(20),
		Delivered:          r.doctor_int(20),
		Dropped:            r.doctor_int(20),
		Evicted:            r.doctor_int(20),
		StillPending:       r.doctor_int(20),
		Coalesced:          r.doctor_bool(),
	}
	if r.doctor_bool() {
		w.SelfLoop.Detected = true
		w.SelfLoop.ChainTruncated = r.doctor_bool()
		for i, n := 0, r.doctor_int(4); i < n; i++ {
			w.SelfLoop.DeliveryIDs = append(w.SelfLoop.DeliveryIDs, r.doctor_str())
		}
	}
	for i, n := 0, r.doctor_int(5); i < n; i++ {
		w.Deliveries = append(w.Deliveries, DeliveryView{
			DeliveryID:       r.doctor_str(),
			Terminal:         []string{"delivered", "dropped", "evicted"}[r.doctor_int(3)],
			TriggerIdentity:  r.doctor_str(),
			TriggerReason:    r.doctor_str(),
			CoalescedCount:   r.doctor_int(10),
			DiagnosticReason: r.doctor_str(),
			SelfLoop:         r.doctor_bool(),
			Provenance:       doctor_buildProvenance(r),
		})
	}
	return w
}

// doctor_writer mirrors doctor_reader so seeds can be authored as intent
// ("a self-looping ended watch with two deliveries") rather than opaque bytes,
// making the branch-covering seed corpus deterministic on replay. Strings must
// stay under 6 bytes so the length prefix round-trips through doctor_reader.
type doctor_writer struct{ b []byte }

func (w *doctor_writer) doctor_putBool(v bool) {
	w.b = append(w.b, map[bool]byte{false: 0, true: 1}[v])
}
func (w *doctor_writer) doctor_putInt(v int) { w.b = append(w.b, byte(v)) }
func (w *doctor_writer) doctor_putStr(s string) {
	if len(s) >= 6 {
		panic("seed string too long for the 0..5 length prefix")
	}
	w.b = append(w.b, byte(len(s)))
	w.b = append(w.b, s...)
}

// FuzzDoctorRenderWatches drives RenderWatches over arbitrary WatchReport
// diagnostic state. Oracles beyond never-panic:
//   - determinism: the same report renders byte-identical every time (the render
//     must not depend on map iteration or other nondeterminism);
//   - structural reflection: the session id and jobs path always appear; when
//     there are no watches the correct filter-specific empty message appears;
//     and every rendered watch's id, every non-empty delivery id, and every
//     self-loop delivery id the report carries appears in the text (the summary
//     truly reflects its input rather than silently dropping rows).
func FuzzDoctorRenderWatches(f *testing.F) {
	// empty-report seeds — one per emptyWatchesMessage arm.
	emptyReport := func(filteredCase int) []byte {
		w := &doctor_writer{}
		w.doctor_putStr("s1")  // SessionID
		w.doctor_putStr("job") // JobsPath
		w.doctor_putInt(filteredCase)
		if filteredCase == 2 || filteredCase == 3 {
			w.doctor_putStr("wid") // the watch:<id> the filter names
		}
		w.doctor_putInt(0) // zero watches
		return w.b
	}
	for c := 0; c < 4; c++ {
		f.Add(emptyReport(c))
	}

	// richWatch — an ended, coalesced, still-pending, self-looping watch with two
	// deliveries (one fully-populated with provenance, one bare) hits every render
	// branch: the ended/end-reason status, the target/owner lines, the coalescing
	// label, the still-pending line, the SELF-LOOP block with chain-truncated note,
	// and the per-delivery trigger/coalesced/reason/self-loop fields.
	richWatch := func(active bool) []byte {
		w := &doctor_writer{}
		w.doctor_putStr("s1")  // SessionID
		w.doctor_putStr("job") // JobsPath
		w.doctor_putInt(0)     // Filtered: none
		w.doctor_putInt(1)     // one watch
		w.doctor_putStr("w")   // WatchID
		w.doctor_putStr("g")   // Generation
		w.doctor_putStr("o")   // OwnerSessionID
		w.doctor_putStr("v")   // VisibleSessionID
		w.doctor_putStr("t")   // Target
		w.doctor_putStr("d")   // SendTo
		w.doctor_putStr("c")   // Condition
		w.doctor_putBool(active)
		w.doctor_putStr("e")   // EndReason
		w.doctor_putInt(10)    // PendingLines (> distinct -> coalesced label)
		w.doctor_putInt(2)     // DistinctDeliveries
		w.doctor_putInt(1)     // Delivered
		w.doctor_putInt(1)     // Dropped
		w.doctor_putInt(0)     // Evicted
		w.doctor_putInt(3)     // StillPending (> 0 -> still-pending line)
		w.doctor_putBool(true) // Coalesced
		w.doctor_putBool(true) // SelfLoop.Detected
		w.doctor_putBool(true) // SelfLoop.ChainTruncated
		w.doctor_putInt(1)     // one self-loop delivery id
		w.doctor_putStr("L")   // that id
		w.doctor_putInt(2)     // two deliveries
		// delivery 1 — fully populated, with provenance.
		w.doctor_putStr("D1")  // DeliveryID
		w.doctor_putInt(0)     // Terminal: delivered
		w.doctor_putStr("ti")  // TriggerIdentity
		w.doctor_putStr("tr")  // TriggerReason
		w.doctor_putInt(3)     // CoalescedCount
		w.doctor_putStr("dr")  // DiagnosticReason
		w.doctor_putBool(true) // SelfLoop
		w.doctor_putBool(true) // has provenance
		w.doctor_putBool(true) // provenance.ChainTruncated
		w.doctor_putInt(1)     // one watch key
		w.doctor_putStr("")    // key WatchID
		w.doctor_putStr("")    // key WatchGeneration
		w.doctor_putInt(1)     // one chain entry
		w.doctor_putStr("")    // chain Kind
		w.doctor_putStr("")    // chain WatchID
		w.doctor_putStr("")    // chain DeliveryID
		// delivery 2 — bare (empty id -> dash, evicted, no provenance).
		w.doctor_putStr("")     // DeliveryID
		w.doctor_putInt(2)      // Terminal: evicted
		w.doctor_putStr("")     // TriggerIdentity
		w.doctor_putStr("")     // TriggerReason
		w.doctor_putInt(0)      // CoalescedCount
		w.doctor_putStr("")     // DiagnosticReason
		w.doctor_putBool(false) // SelfLoop
		w.doctor_putBool(false) // no provenance
		return w.b
	}
	f.Add(richWatch(false)) // ended: exercises end-reason status
	f.Add(richWatch(true))  // active: exercises the "active" status + self-loop-none is skipped
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, raw []byte) {
		rep := doctor_buildWatchReport(doctor_newReader(raw))

		oracle.Deterministic(t, RenderWatches, rep, func(a, b string) bool { return a == b })

		out := RenderWatches(rep)
		if !strings.Contains(out, "session "+rep.SessionID) {
			t.Fatalf("output omits session id %q\n%s", rep.SessionID, out)
		}
		if !strings.Contains(out, rep.JobsPath) {
			t.Fatalf("output omits jobs path %q\n%s", rep.JobsPath, out)
		}
		if len(rep.Watches) == 0 {
			if !strings.Contains(out, emptyWatchesMessage(rep.Filtered)) {
				t.Fatalf("empty report omits the filter message for %q\n%s", rep.Filtered, out)
			}
			return
		}
		for _, w := range rep.Watches {
			if !strings.Contains(out, "watch "+w.WatchID) {
				t.Fatalf("output omits watch id %q\n%s", w.WatchID, out)
			}
			if w.SelfLoop.Detected {
				for _, id := range w.SelfLoop.DeliveryIDs {
					if id != "" && !strings.Contains(out, id) {
						t.Fatalf("output omits self-loop delivery id %q\n%s", id, out)
					}
				}
			}
			for _, d := range w.Deliveries {
				if d.DeliveryID != "" && !strings.Contains(out, d.DeliveryID) {
					t.Fatalf("output omits delivery id %q\n%s", d.DeliveryID, out)
				}
			}
		}
	})
}

func doctor_buildAPILogResult(r *doctor_reader) (APILogResult, APILogOpts) {
	res := APILogResult{SessionID: r.doctor_str()}
	res.Totals = APILogTotals{
		Calls:           r.doctor_int(50),
		Empties:         r.doctor_int(50),
		Errors:          r.doctor_int(50),
		InputTokens:     r.doctor_int(200),
		OutputTokens:    r.doctor_int(200),
		CacheReadTokens: r.doctor_int(200),
		TotalTokens:     r.doctor_int(200),
		AvgLatencyMs:    int64(r.doctor_int(200)),
	}
	for i, n := 0, r.doctor_int(6); i < n; i++ {
		res.Calls = append(res.Calls, APICallRow{
			Round:         r.doctor_int(50),
			Model:         r.doctor_str(),
			Provider:      r.doctor_str(),
			LatencyMs:     int64(r.doctor_int(200)),
			InputTokens:   r.doctor_int(200),
			OutputTokens:  r.doctor_int(200),
			CacheRead:     r.doctor_int(200),
			UncachedInput: r.doctor_int(200),
			FinishReason:  r.doctor_str(),
			TextLength:    r.doctor_int(200),
			ToolCalls:     r.doctor_int(10),
			Empty:         r.doctor_bool(),
			Error:         r.doctor_str(),
		})
	}
	opts := APILogOpts{
		EmptyOnly:      r.doctor_bool(),
		ErrorsOnly:     r.doctor_bool(),
		CacheSpikes:    r.doctor_bool(),
		SpikeThreshold: r.doctor_int(100000),
		SummaryOnly:    r.doctor_bool(),
	}
	return res, opts
}

// FuzzDoctorRenderAPILog drives the sibling RenderAPILog summary/table renderer.
// Oracles beyond never-panic: determinism, and structural reflection — the
// session id and the whole-session totals line (calls= count) always appear;
// SummaryOnly suppresses the per-call table; and a non-summary render with no
// rows emits the "(no calls match)" sentinel rather than a bare header.
func FuzzDoctorRenderAPILog(f *testing.F) {
	// putCall writes one APICallRow; error!="" hits the ERROR finish branch,
	// empty=true (with no error) hits the "(empty)" branch, and neither hits the
	// plain finish-reason path.
	putCall := func(w *doctor_writer, errMsg string, empty bool) {
		w.doctor_putInt(1)      // Round
		w.doctor_putStr("gpt5") // Model
		w.doctor_putStr("oai")  // Provider
		w.doctor_putInt(20)     // LatencyMs
		w.doctor_putInt(100)    // InputTokens
		w.doctor_putInt(50)     // OutputTokens
		w.doctor_putInt(10)     // CacheRead
		w.doctor_putInt(90)     // UncachedInput
		w.doctor_putStr("stop") // FinishReason
		w.doctor_putInt(12)     // TextLength
		w.doctor_putInt(1)      // ToolCalls
		w.doctor_putBool(empty) // Empty
		w.doctor_putStr(errMsg) // Error
	}
	// header writes SessionID + the eight totals fields.
	header := func(w *doctor_writer) {
		w.doctor_putStr("s1")
		for i := 0; i < 8; i++ {
			w.doctor_putInt(3)
		}
	}
	build := func(calls int, errMsg string, empty, summaryOnly bool) []byte {
		w := &doctor_writer{}
		header(w)
		w.doctor_putInt(calls)
		for i := 0; i < calls; i++ {
			putCall(w, errMsg, empty)
		}
		w.doctor_putBool(false) // EmptyOnly
		w.doctor_putBool(false) // ErrorsOnly
		w.doctor_putBool(false) // CacheSpikes
		w.doctor_putInt(0)      // SpikeThreshold
		w.doctor_putBool(summaryOnly)
		return w.b
	}
	f.Add(build(0, "", false, true))      // summary-only
	f.Add(build(0, "", false, false))     // table, no rows -> "(no calls match)"
	f.Add(build(2, "boom", false, false)) // rows with ERROR finish
	f.Add(build(2, "", true, false))      // rows with (empty) finish
	f.Add(build(2, "", false, false))     // rows with plain finish reason
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, raw []byte) {
		res, opts := doctor_buildAPILogResult(doctor_newReader(raw))

		render := func(in APILogResult) string { return RenderAPILog(in, opts) }
		oracle.Deterministic(t, render, res, func(a, b string) bool { return a == b })

		out := RenderAPILog(res, opts)
		if !strings.Contains(out, "session "+res.SessionID) {
			t.Fatalf("output omits session id %q\n%s", res.SessionID, out)
		}
		if !strings.Contains(out, fmt.Sprintf("calls=%d", res.Totals.Calls)) {
			t.Fatalf("totals line omits calls=%d\n%s", res.Totals.Calls, out)
		}
		if opts.SummaryOnly {
			if strings.Contains(out, "(no calls match)") || strings.Contains(out, "round ") {
				t.Fatalf("SummaryOnly still rendered the per-call table\n%s", out)
			}
			return
		}
		if len(res.Calls) == 0 && !strings.Contains(out, "(no calls match)") {
			t.Fatalf("non-summary render with no rows omits the sentinel\n%s", out)
		}
	})
}
