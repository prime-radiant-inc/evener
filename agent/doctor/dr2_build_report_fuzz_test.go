package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/fuzz/oracle"
	"primeradiant.com/serf/llm"
)

// This lane fuzzes the doctor READ/BUILD path — the functions that reconstruct a
// diagnostic view from the raw jobstore event log and the raw transcript file —
// as opposed to render_report_fuzz_test.go, which fuzzes the Render* formatters
// over already-built structs. It exercises buildWatchReport/buildWatchView (over
// fuzzed jobstore.Event logs) and APILog (over fuzzed transcript JSONL files),
// closing their reader-side branches. It reuses the doctor_reader byte consumer
// and doctor_writer defined in render_report_fuzz_test.go; every new top-level
// identifier is prefixed dr2_ to stay collision-free with parallel same-package
// lanes.

// dr2WatchIDs and dr2DeliveryIDs are small correlated pools so that watch
// registrations, their deliveries, and provenance chain hops share ids — the
// only way coalescing, multi-delivery, and same-watch self-loop verdicts become
// reachable from fuzzed events instead of requiring an astronomically unlikely
// string collision.
var dr2WatchIDs = []string{"w0", "w1", "w2"}
var dr2DeliveryIDs = []string{"d0", "d1", "d2"}

func dr2_pickWatchID(r *doctor_reader) string { return dr2WatchIDs[r.doctor_int(len(dr2WatchIDs))] }
func dr2_pickDeliveryID(r *doctor_reader) string {
	return dr2DeliveryIDs[r.doctor_int(len(dr2DeliveryIDs))]
}

// dr2_maybeStr returns s or "" so both the populated and empty-field branches of
// the folds and views (register-accept vs register-reject on a missing field,
// dash vs value, omit vs present) stay reachable.
func dr2_maybeStr(r *doctor_reader, s string) string {
	if r.doctor_bool() {
		return s
	}
	return ""
}

func dr2_buildProvenance(r *doctor_reader) *provenance.Causal {
	if !r.doctor_bool() {
		return nil
	}
	p := &provenance.Causal{ChainTruncated: r.doctor_bool()}
	for i, n := 0, r.doctor_int(3); i < n; i++ {
		p.WatchKeys = append(p.WatchKeys, provenance.WatchKey{WatchID: dr2_pickWatchID(r)})
	}
	for i, n := 0, r.doctor_int(4); i < n; i++ {
		p.Chain = append(p.Chain, provenance.Entry{
			Kind:       "watch_delivery",
			WatchID:    dr2_pickWatchID(r),
			DeliveryID: dr2_pickDeliveryID(r),
		})
	}
	return p
}

// dr2_buildEvents decodes a fuzz blob into a jobstore event log. Seq is the event
// index — unique and monotonic, honoring the Store's append-time contract (a real
// jobs.jsonl never repeats a Seq), which keeps the terminal-resolution and
// delivery-ordering deterministic (a duplicate Seq would make the unstable sort
// in buildWatchView legitimately nondeterministic — not a production-reachable
// bug, so the harness must not manufacture it).
func dr2_buildEvents(r *doctor_reader) []jobstore.Event {
	n := r.doctor_int(13) // 0..12 events keeps logs small so the fuzzer explores shapes
	events := make([]jobstore.Event, 0, n)
	for i := 0; i < n; i++ {
		e := jobstore.Event{Seq: int64(i)}
		wID := dr2_pickWatchID(r)
		switch r.doctor_int(6) {
		case 0: // watch_registered
			e.Kind = jobstore.EventWatchRegistered
			e.WatchID = wID
			e.Watch = &jobstore.WatchEvent{
				Generation:       dr2_maybeStr(r, "g"),
				OwnerSessionID:   dr2_maybeStr(r, "own"),
				VisibleSessionID: dr2_maybeStr(r, "vis"),
				Target:           dr2_maybeStr(r, "tgt"),
				ConfigHash:       dr2_maybeStr(r, "cfg"),
				SendTo:           dr2_maybeStr(r, "to"),
				Condition:        dr2_maybeStr(r, "cond"),
				Deliveries:       r.doctor_int(5),
			}
		case 1: // watch_cleared
			e.Kind = jobstore.EventWatchCleared
			e.WatchID = wID
			e.Watch = &jobstore.WatchEvent{Generation: dr2_maybeStr(r, "g"), EndReason: dr2_maybeStr(r, "done")}
		default: // watch_send_pending / delivered / dropped / evicted
			e.Kind = []jobstore.EventKind{
				jobstore.EventWatchSendPending,
				jobstore.EventWatchSendDelivered,
				jobstore.EventWatchSendDropped,
				jobstore.EventWatchSendEvicted,
			}[r.doctor_int(4)]
			e.WatchID = wID
			e.WatchSend = &jobstore.WatchSendState{
				Key:              jobstore.WatchSendKey{WatchID: wID, VisibleSessionID: dr2_maybeStr(r, "vis")},
				DeliveryID:       dr2_pickDeliveryID(r),
				UpdateSeq:        uint64(r.doctor_int(6)),
				TriggerIdentity:  dr2_maybeStr(r, "ti"),
				TriggerReason:    dr2_maybeStr(r, "tr"),
				CoalescedCount:   r.doctor_int(5),
				DiagnosticReason: dr2_maybeStr(r, "dr"),
				Provenance:       dr2_buildProvenance(r),
			}
		}
		events = append(events, e)
	}
	return events
}

func dr2_buildWatchOpts(r *doctor_reader) WatchOpts {
	opts := WatchOpts{SelfLoopsOnly: r.doctor_bool()}
	switch r.doctor_int(3) {
	case 1:
		opts.WatchID = dr2_pickWatchID(r) // a real watch id — may or may not exist
	case 2:
		opts.WatchID = "absent" // a watch id no event carries
	}
	return opts
}

// dr2_distinctTerminals independently counts unique (watch id, delivery id)
// terminal pairs — mirroring how buildWatchReport keys its terminals map — so the
// harness can assert the report's total distinct-delivery count against a second,
// simpler implementation (a count-preservation oracle) when no filter narrows it.
func dr2_distinctTerminals(events []jobstore.Event) int {
	seen := map[[2]string]bool{}
	for _, e := range events {
		if e.WatchSend == nil {
			continue
		}
		switch e.Kind {
		case jobstore.EventWatchSendDelivered, jobstore.EventWatchSendDropped, jobstore.EventWatchSendEvicted:
			seen[[2]string{e.WatchSend.Key.WatchID, e.WatchSend.DeliveryID}] = true
		}
	}
	return len(seen)
}

func dr2_reportEqual(a, b WatchReport) bool { return reflect.DeepEqual(a, b) }

// FuzzDr2BuildWatchReport drives buildWatchReport (and, through it,
// buildWatchView, orderedWatchIDs, and terminalKind) over a
// fuzzed jobstore event log and watch-filter opts. Oracles beyond never-panic:
//   - determinism: the same event log + opts fold to a DeepEqual-identical report
//     (no map-iteration order or unstable-sort leaks into the output);
//   - structural reflection: the report carries the session id, jobs path, and
//     the opts-derived filter label unchanged;
//   - per-view internal consistency: the delivery-row count equals
//     DistinctDeliveries, the delivered/dropped/evicted tallies sum to it, the
//     Coalesced flag is exactly PendingLines>DistinctDeliveries, and the
//     breaker telemetry (max depth, runaway drops) matches the per-delivery
//     recorded stamps;
//   - filter honesty: a WatchID filter yields only that watch, and SelfLoopsOnly
//     yields only self-looping watches;
//   - count preservation: with no filter, the report's total distinct deliveries
//     equals the independent unique-terminal-pair count.
func FuzzDr2BuildWatchReport(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	// A spread of byte patterns so the seed corpus reaches registrations, clears,
	// each watch_send terminal kind, coalescing, and both filter arms before the
	// fuzzer takes over.
	for _, seed := range dr2_watchSeeds() {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		r := doctor_newReader(raw)
		paths := Paths{SessionID: r.doctor_str(), JobsPath: r.doctor_str()}
		events := dr2_buildEvents(r)
		opts := dr2_buildWatchOpts(r)

		build := func(evs []jobstore.Event) WatchReport { return buildWatchReport(paths, evs, opts) }
		oracle.Deterministic(t, build, events, dr2_reportEqual)

		rep := buildWatchReport(paths, events, opts)

		if rep.SessionID != paths.SessionID {
			t.Fatalf("report session id %q != paths %q", rep.SessionID, paths.SessionID)
		}
		if rep.JobsPath != paths.JobsPath {
			t.Fatalf("report jobs path %q != paths %q", rep.JobsPath, paths.JobsPath)
		}
		if got, want := rep.Filtered, filterLabel(opts); got != want {
			t.Fatalf("filtered label %q != %q", got, want)
		}

		for _, w := range rep.Watches {
			if len(w.Deliveries) != w.DistinctDeliveries {
				t.Fatalf("watch %s: %d delivery rows but DistinctDeliveries=%d", w.WatchID, len(w.Deliveries), w.DistinctDeliveries)
			}
			if sum := w.Delivered + w.Dropped + w.Evicted; sum != w.DistinctDeliveries {
				t.Fatalf("watch %s: delivered+dropped+evicted=%d != distinct=%d", w.WatchID, sum, w.DistinctDeliveries)
			}
			if want := w.PendingLines > w.DistinctDeliveries; w.Coalesced != want {
				t.Fatalf("watch %s: Coalesced=%v but PendingLines=%d DistinctDeliveries=%d", w.WatchID, w.Coalesced, w.PendingLines, w.DistinctDeliveries)
			}
			if opts.WatchID != "" && w.WatchID != opts.WatchID {
				t.Fatalf("WatchID filter %q leaked watch %q", opts.WatchID, w.WatchID)
			}
			if opts.SelfLoopsOnly && w.RunawayDrops == 0 {
				t.Fatalf("SelfLoopsOnly returned watch %q with no runaway drops", w.WatchID)
			}

			// Breaker telemetry is read from recorded state, not re-derived:
			// the view's max depth must equal the deepest per-delivery stamp,
			// and runaway drops must match dropped-with-reason-runaway rows.
			maxDepth, runaway := 0, 0
			for _, d := range w.Deliveries {
				if d.SelfInfluenceDepth > maxDepth {
					maxDepth = d.SelfInfluenceDepth
				}
				if d.Terminal == "dropped" && d.DiagnosticReason == "runaway" {
					runaway++
				}
			}
			if w.MaxSelfInfluenceDepth != maxDepth {
				t.Fatalf("watch %s: MaxSelfInfluenceDepth=%d but deepest delivery stamp=%d", w.WatchID, w.MaxSelfInfluenceDepth, maxDepth)
			}
			if w.RunawayDrops != runaway {
				t.Fatalf("watch %s: RunawayDrops=%d but %d dropped-runaway deliveries", w.WatchID, w.RunawayDrops, runaway)
			}
		}

		if opts.WatchID == "" && !opts.SelfLoopsOnly {
			distinct := 0
			for _, w := range rep.Watches {
				distinct += w.DistinctDeliveries
			}
			if want := dr2_distinctTerminals(events); distinct != want {
				t.Fatalf("report has %d distinct deliveries; %d independent terminal pairs", distinct, want)
			}
		}
	})
}

// dr2_watchSeeds authors a handful of branch-covering event logs via doctor_writer
// so the corpus is deterministic on replay. Each seed is a full byte stream the
// reader consumes in dr2_buildEvents order: SessionID, JobsPath, event count,
// then per-event fields, then the opts trailer.
func dr2_watchSeeds() [][]byte {
	// dr2_maybeStr consumes exactly one bool (true -> the literal, false -> ""), so
	// a "present" field in a seed is a single true bool with no string bytes.
	present := func(w *doctor_writer) { w.doctor_putBool(true) }
	putRegistered := func(w *doctor_writer, wid int) {
		w.doctor_putInt(wid) // watch id pool index
		w.doctor_putInt(0)   // kind: registered
		present(w)           // Generation
		present(w)           // OwnerSessionID
		present(w)           // VisibleSessionID
		present(w)           // Target
		present(w)           // ConfigHash
		present(w)           // SendTo
		present(w)           // Condition
		w.doctor_putInt(2)   // Deliveries
	}
	putCleared := func(w *doctor_writer, wid int) {
		w.doctor_putInt(wid)
		w.doctor_putInt(1) // kind: cleared
		present(w)         // Generation (matches the registration)
		present(w)         // EndReason
	}
	// putSend writes one watch_send_* event. term selects the terminal kind
	// (0 pending, 1 delivered, 2 dropped, 3 evicted); did is the delivery-id pool
	// index; withProv adds a self-referential provenance chain hop.
	putSend := func(w *doctor_writer, wid, term, did, updateSeq int, withProv, chainTruncated bool, provDelivered int) {
		w.doctor_putInt(wid)
		w.doctor_putInt(2)         // kind: default arm -> watch_send
		w.doctor_putInt(term)      // terminal kind selector
		present(w)                 // Key.VisibleSessionID
		w.doctor_putInt(did)       // DeliveryID pool index
		w.doctor_putInt(updateSeq) // UpdateSeq
		present(w)                 // TriggerIdentity
		present(w)                 // TriggerReason
		w.doctor_putInt(1)         // CoalescedCount
		present(w)                 // DiagnosticReason
		if !withProv {
			w.doctor_putBool(false) // no provenance
			return
		}
		w.doctor_putBool(true)           // has provenance
		w.doctor_putBool(chainTruncated) // ChainTruncated
		w.doctor_putInt(0)               // zero watch keys
		w.doctor_putInt(1)               // one chain hop
		w.doctor_putInt(wid)             // hop WatchID (same watch)
		w.doctor_putInt(provDelivered)   // hop DeliveryID (points at another delivered id)
	}

	trailer := func(w *doctor_writer, selfLoopsOnly bool, watchFilter int) {
		w.doctor_putBool(selfLoopsOnly)
		w.doctor_putInt(watchFilter) // 0 none, 1 pool id, 2 absent
		if watchFilter == 1 {
			w.doctor_putInt(0) // filter to w0
		}
	}

	head := func(w *doctor_writer, events int) {
		w.doctor_putStr("s1")  // SessionID
		w.doctor_putStr("job") // JobsPath
		w.doctor_putInt(events)
	}

	seeds := [][]byte{}

	// 1: a registered-then-cleared watch with two distinct deliveries (delivered +
	// dropped) — exercises registration, clear, the coalescing label, and the
	// delivered/dropped tallies.
	{
		w := &doctor_writer{}
		head(w, 4)
		putRegistered(w, 0)
		putSend(w, 0, 1, 0, 1, false, false, 0) // delivered d0
		putSend(w, 0, 2, 1, 1, false, false, 0) // dropped d1
		putCleared(w, 0)
		trailer(w, false, 0)
		seeds = append(seeds, w.b)
	}

	// 2: a self-loop with a TRUNCATED chain — two delivered deliveries of one
	// watch, the second's chain naming the first's (delivered) delivery id and
	// flagged truncated (the SelfLoop.ChainTruncated arm).
	{
		w := &doctor_writer{}
		head(w, 3)
		putRegistered(w, 1)
		putSend(w, 1, 1, 0, 1, false, false, 0) // delivered d0
		putSend(w, 1, 1, 1, 2, true, true, 0)   // delivered d1, chain hop -> d0 (delivered), truncated
		trailer(w, true, 0)                     // SelfLoopsOnly filter
		seeds = append(seeds, w.b)
	}

	// 3: an evicted delivery plus a still-pending frame, filtered to a watch id.
	{
		w := &doctor_writer{}
		head(w, 3)
		putRegistered(w, 0)
		putSend(w, 0, 3, 0, 1, false, false, 0) // evicted d0
		putSend(w, 0, 0, 1, 2, false, false, 0) // pending d1 (still-pending, never settled)
		trailer(w, false, 1)                    // filter to w0
		seeds = append(seeds, w.b)
	}

	// 4: no watches at all under a watch:<id> filter — the empty branch.
	{
		w := &doctor_writer{}
		head(w, 0)
		trailer(w, false, 2) // filter to an absent watch id
		seeds = append(seeds, w.b)
	}

	return seeds
}

// ---- APILog reader lane ----

func dr2_maybeHistoryMode(r *doctor_reader) llm.HistoryMode {
	switch r.doctor_int(6) {
	case 1:
		return llm.HistoryModeResponsesDelta
	case 2:
		return llm.HistoryModeFullHistory
	case 3:
		return llm.HistoryModeFullHistoryFallback
	case 4:
		return llm.HistoryMode("unrecognized") // hits the default (ignored) arm
	default:
		return ""
	}
}

func dr2_maybeEndpointFamily(r *doctor_reader) string {
	switch r.doctor_int(3) {
	case 1:
		return "openai_public"
	case 2:
		return "  " // trimmed to empty -> the early-return arm
	default:
		return ""
	}
}

func dr2_buildAPICall(r *doctor_reader) transcript.APICall {
	call := transcript.APICall{
		Kind:      "api_call",
		Round:     r.doctor_int(50),
		LatencyMs: int64(r.doctor_int(500)),
		Request: llm.APILogRequest{
			Model:          dr2_maybeStr(r, "gpt-5.5"),
			Provider:       dr2_maybeStr(r, "openai"),
			EndpointFamily: dr2_maybeEndpointFamily(r),
			HistoryMode:    dr2_maybeHistoryMode(r),
		},
	}
	if r.doctor_bool() {
		call.Error = "boom"
	}
	if r.doctor_bool() {
		resp := &llm.APILogResponse{
			FinishReason:  dr2_maybeStr(r, "stop"),
			TextLength:    r.doctor_int(500),
			ToolCallCount: r.doctor_int(5),
			Usage: llm.Usage{
				InputTokens:  r.doctor_int(100000), // spans the cache-spike threshold
				OutputTokens: r.doctor_int(1000),
			},
		}
		if r.doctor_bool() {
			cr := r.doctor_int(100000)
			resp.Usage.CacheReadTokens = &cr
		}
		call.Response = resp
	}
	return call
}

// dr2_buildTranscript decodes a fuzz blob into a transcript JSONL file body: a
// header, then a mix of valid api_call lines, corrupt-but-kind-tagged api_call
// lines (which loadTranscript keeps but APILog's json.Unmarshal rejects — the
// diagnostic-data continue), and entry lines (ignored by APILog but part of the
// real file shape loadTranscript walks).
func dr2_buildTranscript(r *doctor_reader) string {
	var b strings.Builder
	b.WriteString(`{"kind":"header","format_version":1,"session_id":"s1"}` + "\n")
	n := r.doctor_int(9)
	for i := 0; i < n; i++ {
		switch r.doctor_int(5) {
		case 0:
			// Valid JSON, kind peeks as api_call, but round is an object where an
			// int is wanted -> APICall unmarshal fails -> diagnostic continue.
			b.WriteString(`{"kind":"api_call","round":{"x":1}}` + "\n")
		case 1:
			b.WriteString(`{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[]}}}` + "\n")
		default:
			line, err := json.Marshal(dr2_buildAPICall(r))
			if err != nil {
				continue
			}
			b.Write(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func dr2_writeSession(t *testing.T, body string) string {
	t.Helper()
	base := t.TempDir()
	path := filepath.Join(base, "sessions", "s1.transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return base
}

func dr2_apiOptsEqual(a, b APILogResult) bool { return reflect.DeepEqual(a, b) }

// FuzzDr2APILog drives APILog end-to-end over a fuzzed on-disk transcript: Locate
// resolves an override/scratch state base, loadTranscript walks the JSONL, and
// APILog flattens each api_call into a row plus the whole-session aggregate.
// Oracles beyond never-panic:
//   - determinism: two reads of the same file + opts yield a DeepEqual result;
//   - structural reflection: the result carries the session id;
//   - totals arithmetic: TotalTokens == InputTokens+OutputTokens, and the average
//     latency never exceeds the session's own token/latency totals invariants;
//   - filter honesty: every displayed row satisfies the active filter, and the
//     displayed rows never outnumber the whole-session call count.
func FuzzDr2APILog(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	for _, seed := range dr2_apiSeeds() {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		r := doctor_newReader(raw)
		body := dr2_buildTranscript(r)
		opts := APILogOpts{
			EmptyOnly:      r.doctor_bool(),
			ErrorsOnly:     r.doctor_bool(),
			CacheSpikes:    r.doctor_bool(),
			SpikeThreshold: r.doctor_int(100000),
			SummaryOnly:    r.doctor_bool(),
		}
		// A trailing mode byte occasionally steers the read down APILog's two
		// error returns: an unresolvable selector (Locate fails) or a transcript
		// whose non-last garbage line makes loadTranscript fail. The no-panic and
		// error-determinism floors must still hold on those paths.
		selector := "local:s1"
		switch r.doctor_int(6) {
		case 4:
			selector = "local:absent"
		case 5:
			body = "not-json\n" + body // a non-last unparseable line -> loadTranscript error
		}
		base := dr2_writeSession(t, body)

		res, err := APILog(base, selector, opts)
		res2, err2 := APILog(base, selector, opts)
		if (err == nil) != (err2 == nil) {
			t.Fatalf("APILog error nondeterministic: %v vs %v", err, err2)
		}
		if err != nil {
			if err.Error() != err2.Error() {
				t.Fatalf("APILog error text nondeterministic: %q vs %q", err, err2)
			}
			return // error path: no-panic + determinism proven, no result to inspect
		}
		if !dr2_apiOptsEqual(res, res2) {
			t.Fatalf("APILog result nondeterministic for the same file+opts")
		}

		if res.SessionID != "s1" {
			t.Fatalf("result session id %q != s1", res.SessionID)
		}
		if want := res.Totals.InputTokens + res.Totals.OutputTokens; res.Totals.TotalTokens != want {
			t.Fatalf("TotalTokens=%d != in+out=%d", res.Totals.TotalTokens, want)
		}
		if res.Totals.Calls == 0 && res.Totals.AvgLatencyMs != 0 {
			t.Fatalf("no calls but avg latency=%d", res.Totals.AvgLatencyMs)
		}
		if len(res.Calls) > res.Totals.Calls {
			t.Fatalf("%d displayed rows exceed %d total calls", len(res.Calls), res.Totals.Calls)
		}

		threshold := opts.SpikeThreshold
		if threshold <= 0 {
			threshold = defaultSpikeThreshold
		}
		for _, row := range res.Calls {
			if !rowMatchesFilter(row, opts, threshold) {
				t.Fatalf("displayed row fails the active filter: %+v opts=%+v", row, opts)
			}
			if row.UncachedInput != row.InputTokens-row.CacheRead {
				t.Fatalf("row uncached=%d != in-cache=%d", row.UncachedInput, row.InputTokens-row.CacheRead)
			}
		}
	})
}

// dr2_apiSeeds authors transcript files that reach each api_call branch.
func dr2_apiSeeds() [][]byte {
	putCall := func(w *doctor_writer, family int, mode int, withErr, withResp, spike bool) {
		w.doctor_putInt(2)      // dr2_buildTranscript arm: default -> marshaled api_call
		w.doctor_putInt(5)      // Round
		w.doctor_putInt(20)     // LatencyMs
		w.doctor_putBool(true)  // Model present (dr2_maybeStr: one bool, no string)
		w.doctor_putBool(true)  // Provider present
		w.doctor_putInt(family) // EndpointFamily selector
		w.doctor_putInt(mode)   // HistoryMode selector
		w.doctor_putBool(withErr)
		w.doctor_putBool(withResp)
		if withResp {
			w.doctor_putBool(true) // FinishReason present
			w.doctor_putInt(12)    // TextLength
			w.doctor_putInt(1)     // ToolCallCount
			if spike {
				w.doctor_putInt(90000) // InputTokens above the default 50k floor
			} else {
				w.doctor_putInt(100)
			}
			w.doctor_putInt(50)     // OutputTokens
			w.doctor_putBool(false) // no CacheReadTokens
		}
	}
	opts := func(w *doctor_writer, empty, errs, spikes, summary bool, threshold int) {
		w.doctor_putBool(empty)
		w.doctor_putBool(errs)
		w.doctor_putBool(spikes)
		w.doctor_putInt(threshold)
		w.doctor_putBool(summary)
	}

	seeds := [][]byte{}

	// A healthy call with a continuation-family/history-mode pair, summary only.
	{
		w := &doctor_writer{}
		w.doctor_putInt(1) // one line
		putCall(w, 1, 1, false, true, false)
		opts(w, false, false, false, true, 0)
		seeds = append(seeds, w.b)
	}
	// An errored call plus a cache-spike call, filtered to errors.
	{
		w := &doctor_writer{}
		w.doctor_putInt(2)
		putCall(w, 0, 0, true, false, false)
		putCall(w, 1, 2, false, true, true)
		opts(w, false, true, false, false, 0)
		seeds = append(seeds, w.b)
	}
	// A cache-spike call filtered by CacheSpikes with a custom threshold.
	{
		w := &doctor_writer{}
		w.doctor_putInt(1)
		putCall(w, 1, 3, false, true, true)
		opts(w, false, false, true, false, 10000)
		seeds = append(seeds, w.b)
	}
	// A corrupt api_call line + an entry line + a healthy call — exercises the
	// diagnostic-continue and the entry skip alongside a real row.
	{
		w := &doctor_writer{}
		w.doctor_putInt(3)
		w.doctor_putInt(0) // corrupt api_call
		w.doctor_putInt(1) // entry line
		putCall(w, 0, 4, false, true, false)
		opts(w, false, false, false, false, 0)
		seeds = append(seeds, w.b)
	}
	// A call with a real endpoint family but an unrecognized history mode — the
	// recordContinuationHistoryMode default (ignored) arm.
	{
		w := &doctor_writer{}
		w.doctor_putInt(1)
		putCall(w, 1, 4, false, true, false) // family=openai_public, mode=unrecognized
		opts(w, false, false, false, false, 0)
		w.doctor_putInt(0) // mode: normal read
		seeds = append(seeds, w.b)
	}
	// A healthy call, then a trailing mode byte steering the unresolvable-selector
	// error return.
	{
		w := &doctor_writer{}
		w.doctor_putInt(1)
		putCall(w, 1, 1, false, true, false)
		opts(w, false, false, false, false, 0)
		w.doctor_putInt(4) // mode: bad selector -> Locate error
		seeds = append(seeds, w.b)
	}
	// A healthy call, then a trailing mode byte steering the corrupt-transcript
	// error return (a non-last unparseable line makes loadTranscript fail).
	{
		w := &doctor_writer{}
		w.doctor_putInt(1)
		putCall(w, 1, 1, false, true, false)
		opts(w, false, false, false, false, 0)
		w.doctor_putInt(5) // mode: corrupt transcript -> loadTranscript error
		seeds = append(seeds, w.b)
	}

	return seeds
}
