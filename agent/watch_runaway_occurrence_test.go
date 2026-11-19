package agent

import (
	"slices"
	"testing"

	"primeradiant.com/evener/agent/provenance"
)

// runawayChainProvenance builds the lineage of a fire that descends from depth
// delivered fires of watchID, i.e. a chain the guard has to stop.
func runawayChainProvenance(watchID string, fires ...string) *provenance.Causal {
	p := &provenance.Causal{}
	for _, fire := range fires {
		p.FireKeys = append(p.FireKeys, provenance.FireKey{WatchID: watchID, DeliveryID: fire})
	}
	return p
}

// runawayChainFires names depth distinct delivered fires, one chain's worth.
func runawayChainFires(prefix string, depth int) []string {
	fires := make([]string, 0, depth)
	for i := range depth {
		fires = append(fires, prefix+":"+string(rune('a'+i)))
	}
	return fires
}

// A stopped runaway chain reports itself once. The report is identified by its
// source PLUS an occurrence, because two independent chains on one watch share
// their watch, job and reason: without an occurrence the second chain's report
// is byte-identical to the first, so the durable append reads it as a replay and
// the second stop is never shown to the model.
func TestStoppedRunawayChainReportsADistinctOccurrence(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	cfg := &watchConfig{watchID: "watch:runaway-occurrence", generation: "9@worker"}
	depth := runawaySelfInfluenceDepth

	stop := func(fires []string) jobNotification {
		jm.mu.Lock()
		defer jm.mu.Unlock()
		notifications, fired := jm.conditionFireNotificationLocked(cfg, "job:runaway-occurrence", "event: communicate", runawayChainProvenance(cfg.watchID, fires...))
		if fired {
			t.Fatal("a fire at the runaway depth reported a delivery, want the guard to stop it")
		}
		if len(notifications) != 1 {
			t.Fatalf("stopped chain produced %d notification(s), want its one diagnostic", len(notifications))
		}
		return notifications[0]
	}

	firstChain := runawayChainFires("wd_first", depth)
	secondChain := runawayChainFires("wd_second", depth)
	first := stop(firstChain)
	second := stop(secondChain)

	if got := stableWatchNoticeOccurrence(first); got == "" {
		t.Fatal("the stopped chain's diagnostic carries no occurrence, so every chain on this watch shares one identity")
	}
	if stableWatchNoticeAttentionID(first) == stableWatchNoticeAttentionID(second) {
		t.Fatalf("two stopped chains share attention identity %q: the second chain's report reads as a replay of the first and is dropped",
			stableWatchNoticeAttentionID(first))
	}
	// A retried append replays the retained notice, but the occurrence must also
	// survive a rebuild of the same chain's report: the fires are a set, and
	// their order in the lineage is not part of which chain this is.
	reordered := slices.Clone(firstChain)
	slices.Reverse(reordered)
	if got, want := stoppedRunawayOccurrence(reordered), stoppedRunawayOccurrence(firstChain); got != want {
		t.Fatalf("occurrence of a reordered chain = %q, want %q: a retry must replay, not duplicate", got, want)
	}
}

// Once a chain is stopped, every later fire that descends from one of its
// fires is suppressed: the stop's single report has already been given, and a
// repeat would be the runaway itself.
func TestStoppedRunawayChainSuppressesLaterFiresFromTheSameChain(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	cfg := &watchConfig{watchID: "watch:runaway-suppress", generation: "9@worker"}
	fires := runawayChainFires("wd_suppress", runawaySelfInfluenceDepth)

	jm.mu.Lock()
	_, fired := jm.conditionFireNotificationLocked(cfg, "job:runaway-suppress", "event: communicate", runawayChainProvenance(cfg.watchID, fires...))
	jm.mu.Unlock()
	if fired {
		t.Fatal("the chain's first stop reported a delivery, want the guard to stop it")
	}

	// The same chain, one fire longer, arrives again.
	deeper := runawayChainProvenance(cfg.watchID, fires...)
	deeper.FireKeys = append(deeper.FireKeys, provenance.FireKey{WatchID: cfg.watchID, DeliveryID: "wd_suppress:later"})
	jm.mu.Lock()
	notifications, fired := jm.conditionFireNotificationLocked(cfg, "job:runaway-suppress", "event: communicate", deeper)
	jm.mu.Unlock()
	if fired {
		t.Fatal("a fire descending from the stopped chain reported a delivery")
	}
	if len(notifications) != 0 {
		t.Fatalf("a fire descending from the stopped chain produced %d report(s), want none", len(notifications))
	}
}
