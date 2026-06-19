package doctor

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
)

// writeJobsEvents appends events to a session's jobs.jsonl via the real Store,
// so the on-disk bytes are exactly what serf writes.
func writeJobsEvents(t *testing.T, jobsPath string, events []jobstore.Event) {
	t.Helper()
	st, err := jobstore.Open(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if err := st.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func key(visible, watchID, target, sendTo, gen string) jobstore.WatchSendKey {
	return jobstore.WatchSendKey{
		VisibleSessionID:        visible,
		WatchID:                 watchID,
		WatchTarget:             target,
		ResolvedWatchedIdentity: visible,
		ResolvedSendTo:          sendTo,
		WatchGeneration:         gen,
	}
}

// ownStamp builds the provenance a healthy delivery carries: just its own
// (watch_id, generation) WatchKey + a single Chain hop with its own delivery id.
func ownStamp(watchID, gen, deliveryID string) *provenance.Causal {
	return &provenance.Causal{
		WatchKeys: []provenance.WatchKey{{WatchID: watchID, WatchGeneration: gen}},
		Chain:     []provenance.Entry{{Kind: "watch", WatchID: watchID, WatchGeneration: gen, DeliveryID: deliveryID}},
	}
}

// loopStamp builds the provenance a self-loop delivery carries: a PRIOR hop of
// the same watch (different delivery id) followed by this delivery's own stamp.
func loopStamp(watchID, gen, priorDeliveryID, deliveryID string) *provenance.Causal {
	return &provenance.Causal{
		WatchKeys: []provenance.WatchKey{{WatchID: watchID, WatchGeneration: gen}},
		Chain: []provenance.Entry{
			{Kind: "watch", WatchID: watchID, WatchGeneration: gen, DeliveryID: priorDeliveryID},
			{Kind: "watch", WatchID: watchID, WatchGeneration: gen, DeliveryID: deliveryID},
		},
	}
}

func watchesFixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	writeSession(t, bucket, sid)
	jobsPath := filepath.Join(bucket, "sessions", sid, "jobs.jsonl")

	kW1Deliv := key("worker", "w1", "job:j1", "obs", "g1")
	kW1Drop := key("worker", "w1", "job:j2", "obs", "g1")
	kW1Evict := key("worker", "w1", "job:j3", "obs", "g1")
	kW2Healthy := key("worker", "w2", "job:j9", "obs", "g2")
	kW2Loop := key("worker", "w2", "job:j9b", "obs", "g2")

	writeJobsEvents(t, jobsPath, []jobstore.Event{
		// w1 registration.
		{Kind: jobstore.EventWatchRegistered, WatchID: "w1", Watch: &jobstore.WatchEvent{
			Generation: "g1", OwnerSessionID: "obs", VisibleSessionID: "worker",
			Target: "job:j1", SendTo: "obs", Condition: "output_match"}},
		// w1: 3 pending updates coalescing into ONE delivered (delivery d1).
		{Kind: jobstore.EventWatchSendPending, WatchID: "w1", WatchSend: &jobstore.WatchSendState{Key: kW1Deliv, DeliveryID: "d1", UpdateSeq: 1}},
		{Kind: jobstore.EventWatchSendPending, WatchID: "w1", WatchSend: &jobstore.WatchSendState{Key: kW1Deliv, DeliveryID: "d1", UpdateSeq: 2}},
		{Kind: jobstore.EventWatchSendPending, WatchID: "w1", WatchSend: &jobstore.WatchSendState{Key: kW1Deliv, DeliveryID: "d1", UpdateSeq: 3}},
		{Kind: jobstore.EventWatchSendDelivered, WatchID: "w1", WatchSend: &jobstore.WatchSendState{
			Key: kW1Deliv, DeliveryID: "d1", UpdateSeq: 3, CoalescedCount: 3,
			TriggerIdentity: "worker", TriggerReason: "output_match", Provenance: ownStamp("w1", "g1", "d1")}},
		// w1: a dropped delivery d2 (with a diagnostic reason).
		{Kind: jobstore.EventWatchSendPending, WatchID: "w1", WatchSend: &jobstore.WatchSendState{Key: kW1Drop, DeliveryID: "d2", UpdateSeq: 1}},
		{Kind: jobstore.EventWatchSendDropped, WatchID: "w1", WatchSend: &jobstore.WatchSendState{
			Key: kW1Drop, DeliveryID: "d2", UpdateSeq: 1, DiagnosticReason: "send_to gone"}},
		// w1: an evicted delivery d3.
		{Kind: jobstore.EventWatchSendPending, WatchID: "w1", WatchSend: &jobstore.WatchSendState{Key: kW1Evict, DeliveryID: "d3", UpdateSeq: 1}},
		{Kind: jobstore.EventWatchSendEvicted, WatchID: "w1", WatchSend: &jobstore.WatchSendState{Key: kW1Evict, DeliveryID: "d3", UpdateSeq: 1}},

		// w2 registration.
		{Kind: jobstore.EventWatchRegistered, WatchID: "w2", Watch: &jobstore.WatchEvent{
			Generation: "g2", OwnerSessionID: "obs", VisibleSessionID: "worker", Target: "job:j9", SendTo: "obs"}},
		// w2: a HEALTHY delivery (own stamp only).
		{Kind: jobstore.EventWatchSendDelivered, WatchID: "w2", WatchSend: &jobstore.WatchSendState{
			Key: kW2Healthy, DeliveryID: "dh", UpdateSeq: 1, Provenance: ownStamp("w2", "g2", "dh")}},
		// w2: a SELF-LOOP delivery (prior same-watch hop).
		{Kind: jobstore.EventWatchSendDelivered, WatchID: "w2", WatchSend: &jobstore.WatchSendState{
			Key: kW2Loop, DeliveryID: "dl", UpdateSeq: 1, Provenance: loopStamp("w2", "g2", "dprior", "dl")}},
	})
	return base, sid
}

func findWatch(r WatchReport, watchID string) *WatchView {
	for i := range r.Watches {
		if r.Watches[i].WatchID == watchID {
			return &r.Watches[i]
		}
	}
	return nil
}

func TestWatches_CoalescingCollapse(t *testing.T) {
	base, sid := watchesFixture(t)
	r, err := Watches(base, sid, WatchOpts{})
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}
	w1 := findWatch(r, "w1")
	if w1 == nil {
		t.Fatal("w1 missing from report")
	}
	if w1.PendingLines != 5 {
		t.Errorf("PendingLines = %d, want 5 (raw pending events)", w1.PendingLines)
	}
	if w1.DistinctDeliveries != 3 {
		t.Errorf("DistinctDeliveries = %d, want 3 (d1 delivered, d2 dropped, d3 evicted)", w1.DistinctDeliveries)
	}
	if w1.Delivered != 1 || w1.Dropped != 1 || w1.Evicted != 1 {
		t.Errorf("terminal breakdown = %d/%d/%d, want 1/1/1", w1.Delivered, w1.Dropped, w1.Evicted)
	}
	if !w1.Coalesced {
		t.Error("Coalesced should be true (5 pending lines collapsed to 3 deliveries)")
	}
}

func TestWatches_Registration(t *testing.T) {
	base, sid := watchesFixture(t)
	r, _ := Watches(base, sid, WatchOpts{})
	w1 := findWatch(r, "w1")
	if w1.Generation != "g1" || w1.OwnerSessionID != "obs" || w1.VisibleSessionID != "worker" {
		t.Errorf("registration not surfaced: %+v", w1)
	}
	if w1.Target != "job:j1" || w1.SendTo != "obs" || w1.Condition != "output_match" {
		t.Errorf("registration config not surfaced: %+v", w1)
	}
	if !w1.Active {
		t.Error("w1 should be active (registered, not cleared)")
	}
}

func TestWatches_DroppedAndEvictedHaveTerminalAndReason(t *testing.T) {
	base, sid := watchesFixture(t)
	r, _ := Watches(base, sid, WatchOpts{})
	w1 := findWatch(r, "w1")
	var dropped, evicted *DeliveryView
	for i := range w1.Deliveries {
		switch w1.Deliveries[i].Terminal {
		case "dropped":
			dropped = &w1.Deliveries[i]
		case "evicted":
			evicted = &w1.Deliveries[i]
		}
	}
	if dropped == nil || dropped.DiagnosticReason != "send_to gone" {
		t.Errorf("dropped delivery missing/without reason: %+v", dropped)
	}
	if evicted == nil {
		t.Error("evicted delivery missing")
	}
}

func TestWatches_SelfLoopVerdictFromChain(t *testing.T) {
	base, sid := watchesFixture(t)
	r, _ := Watches(base, sid, WatchOpts{})
	w2 := findWatch(r, "w2")
	if w2 == nil {
		t.Fatal("w2 missing")
	}
	if !w2.SelfLoop.Detected {
		t.Fatal("w2 self-loop should be detected (a prior same-watch_id hop)")
	}
	if len(w2.SelfLoop.DeliveryIDs) != 1 || w2.SelfLoop.DeliveryIDs[0] != "dl" {
		t.Errorf("self-loop delivery ids = %v, want [dl]", w2.SelfLoop.DeliveryIDs)
	}
	// The healthy delivery (own stamp only) must NOT be flagged, proving the
	// always-present WatchKeys stamp is not the discriminator.
	for _, d := range w2.Deliveries {
		if d.DeliveryID == "dh" && d.SelfLoop {
			t.Error("healthy delivery dh was wrongly flagged as a self-loop")
		}
	}
}

func TestWatches_HealthyWatchHasNoSelfLoop(t *testing.T) {
	base, sid := watchesFixture(t)
	r, _ := Watches(base, sid, WatchOpts{})
	w1 := findWatch(r, "w1")
	if w1.SelfLoop.Detected {
		t.Errorf("w1 should have no self-loop; got %v", w1.SelfLoop.DeliveryIDs)
	}
}

func TestWatches_SelfLoopsOnlyFilter(t *testing.T) {
	base, sid := watchesFixture(t)
	r, _ := Watches(base, sid, WatchOpts{SelfLoopsOnly: true})
	if len(r.Watches) != 1 || r.Watches[0].WatchID != "w2" {
		t.Errorf("SelfLoopsOnly should yield only w2, got %d watches", len(r.Watches))
	}
}

func TestWatches_WatchIDFilter(t *testing.T) {
	base, sid := watchesFixture(t)
	r, _ := Watches(base, sid, WatchOpts{WatchID: "w1"})
	if len(r.Watches) != 1 || r.Watches[0].WatchID != "w1" {
		t.Errorf("WatchID filter should yield only w1, got %d watches", len(r.Watches))
	}
}

func TestWatches_RenderMentionsCoalescingAndVerdict(t *testing.T) {
	base, sid := watchesFixture(t)
	r, _ := Watches(base, sid, WatchOpts{})
	out := RenderWatches(r)
	if !strings.Contains(out, "coalescing collapsed") {
		t.Error("render should note coalescing on w1")
	}
	if !strings.Contains(out, "SELF-LOOP") {
		t.Error("render should flag w2's self-loop")
	}
	if !strings.Contains(out, "3 distinct") {
		t.Errorf("render should show distinct delivery count; got:\n%s", out)
	}
}

func TestEmptyWatchesMessage(t *testing.T) {
	cases := map[string]string{
		"":                    "no watches recorded",
		"self-loops":          "no watches with a self-loop verdict (the session's watches are healthy)",
		"watch:w9":            "watch w9 not found in this session",
		"self-loops,watch:w9": "no watches with a self-loop verdict (the session's watches are healthy)",
	}
	for filtered, want := range cases {
		if got := emptyWatchesMessage(filtered); got != want {
			t.Errorf("emptyWatchesMessage(%q) = %q, want %q", filtered, got, want)
		}
	}
}

func TestFilterLabel(t *testing.T) {
	if got := filterLabel(WatchOpts{SelfLoopsOnly: true}); got != "self-loops" {
		t.Errorf("filterLabel(self-loops) = %q", got)
	}
	if got := filterLabel(WatchOpts{WatchID: "w1"}); got != "watch:w1" {
		t.Errorf("filterLabel(watch) = %q", got)
	}
	if got := filterLabel(WatchOpts{}); got != "" {
		t.Errorf("filterLabel(none) = %q", got)
	}
}

// A healthy watch under --self-loops must not read as "no watches recorded":
// w1 in the fixture has no self-loop, so the filtered result is empty but the
// session clearly has watches.
func TestWatches_SelfLoopsEmptyMessageIsUnambiguous(t *testing.T) {
	base, sid := watchesFixture(t)
	r, err := Watches(base, sid, WatchOpts{WatchID: "w1", SelfLoopsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderWatches(r)
	if strings.Contains(out, "no watches recorded") {
		t.Errorf("healthy --self-loops should not say 'no watches recorded':\n%s", out)
	}
	if !strings.Contains(out, "self-loop verdict") {
		t.Errorf("expected the self-loop-verdict empty message:\n%s", out)
	}
}
