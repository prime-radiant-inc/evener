package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
)

// TestWatchSendDeliveredLockedFlipsOnSettle proves the delivered() oracle: a
// delivery ID is absent until its watch-send settles delivered, then present.
// This is the predicate the self-influence depth metric consults so a
// coalesced-away (never delivered) predecessor cannot inflate depth.
func TestWatchSendDeliveredLockedFlipsOnSettle(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	jm.mu.Lock()
	pre := jm.watchSendDeliveredLocked("wd_1")
	jm.mu.Unlock()
	if pre {
		t.Fatal("watchSendDeliveredLocked(\"wd_1\") = true before settle, want false")
	}

	cfg := &watchConfig{}
	state := jobstore.WatchSendState{
		Key:        jobstore.WatchSendKey{VisibleSessionID: jm.sessionID},
		DeliveryID: "wd_1",
	}
	if err := jm.settleWatchSendDelivered(cfg, state); err != nil {
		t.Fatal(err)
	}

	jm.mu.Lock()
	post := jm.watchSendDeliveredLocked("wd_1")
	unknown := jm.watchSendDeliveredLocked("wd_missing")
	jm.mu.Unlock()
	if !post {
		t.Fatal("watchSendDeliveredLocked(\"wd_1\") = false after settle, want true")
	}
	if unknown {
		t.Fatal("watchSendDeliveredLocked(\"wd_missing\") = true, want false for unknown id")
	}
}

func TestClassifySelfInfluenceLocked(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	cases := []struct {
		name      string
		delivered map[string]struct{}
		cfg       *watchConfig
		p         *provenance.Causal
		want      selfInfluence
	}{
		{
			name:      "not self-influenced",
			delivered: map[string]struct{}{},
			cfg:       &watchConfig{watchID: "w1", generation: "g1"},
			p: &provenance.Causal{
				WatchKeys: []provenance.WatchKey{{WatchID: "w2", WatchGeneration: "g9"}},
				Chain:     []provenance.Entry{{Kind: "watch", WatchID: "w2", WatchGeneration: "g9", DeliveryID: "wd_other"}},
			},
			want: selfInfluence{self: false, gradientDepth: 0, fuseDepth: 0, truncated: false},
		},
		{
			name:      "one delivered prior same generation",
			delivered: map[string]struct{}{"wd_1": {}},
			cfg:       &watchConfig{watchID: "w1", generation: "g1"},
			p: &provenance.Causal{
				WatchKeys: []provenance.WatchKey{{WatchID: "w1", WatchGeneration: "g1"}},
				Chain:     []provenance.Entry{{Kind: "watch", WatchID: "w1", WatchGeneration: "g1", DeliveryID: "wd_1"}},
			},
			want: selfInfluence{self: true, gradientDepth: 1, fuseDepth: 1, truncated: false},
		},
		{
			name:      "cross-generation scopes differ",
			delivered: map[string]struct{}{"wd_1": {}, "wd_2": {}},
			cfg:       &watchConfig{watchID: "w1", generation: "g2"},
			p: &provenance.Causal{
				WatchKeys: []provenance.WatchKey{
					{WatchID: "w1", WatchGeneration: "g1"},
					{WatchID: "w1", WatchGeneration: "g2"},
				},
				Chain: []provenance.Entry{
					{Kind: "watch", WatchID: "w1", WatchGeneration: "g1", DeliveryID: "wd_1"},
					{Kind: "watch", WatchID: "w1", WatchGeneration: "g2", DeliveryID: "wd_2"},
				},
			},
			want: selfInfluence{self: true, gradientDepth: 1, fuseDepth: 2, truncated: false},
		},
		{
			name:      "coalesced-away prior excluded",
			delivered: map[string]struct{}{"wd_survivor": {}},
			cfg:       &watchConfig{watchID: "w1", generation: "g1"},
			p: &provenance.Causal{
				WatchKeys: []provenance.WatchKey{{WatchID: "w1", WatchGeneration: "g1"}},
				Chain: []provenance.Entry{
					{Kind: "watch", WatchID: "w1", WatchGeneration: "g1", DeliveryID: "wd_survivor"},
					{Kind: "watch", WatchID: "w1", WatchGeneration: "g1", DeliveryID: "wd_coalesced"},
				},
			},
			want: selfInfluence{self: true, gradientDepth: 1, fuseDepth: 1, truncated: false},
		},
		{
			name:      "truncated backstop",
			delivered: map[string]struct{}{},
			cfg:       &watchConfig{watchID: "w1", generation: "g1"},
			p: &provenance.Causal{
				WatchKeys:      []provenance.WatchKey{{WatchID: "w1", WatchGeneration: "g1"}},
				Chain:          []provenance.Entry{},
				ChainTruncated: true,
			},
			want: selfInfluence{self: true, gradientDepth: 0, fuseDepth: 0, truncated: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jm.mu.Lock()
			jm.deliveredWatchSendIDs = tc.delivered
			got := jm.classifySelfInfluenceLocked(tc.cfg, tc.p)
			jm.mu.Unlock()
			if got != tc.want {
				t.Fatalf("classifySelfInfluenceLocked = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestRecordWatchSendRunawayFuse covers the circuit-breaker fuse core: a send
// whose carried fuseDepth is below the runaway threshold persists as pending
// with its depth stamped on the durable state, while a send at/over the
// threshold is dropped as a runaway (persist-then-drop, mirroring the
// unresolvable-target drop) carrying DiagnosticReason "runaway".
func TestRecordWatchSendRunawayFuse(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")
	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"assistant.message"}, Send: &watchSendArgs{To: "dlg_obs"}})
	cfg := onlyWatchConfigForTest(t, jm)

	build := func(fuseDepth int) watchSendDelivery {
		jm.mu.Lock()
		d := jm.watchSendSnapshot(cfg, "caller", "test", events.SessionEvent{SessionID: jm.sessionID})
		jm.mu.Unlock()
		d.fuseDepth = fuseDepth
		return d
	}

	// Below threshold: persists pending, depth stamped through.
	state, _, ok, err := jm.recordWatchSend(build(runawaySelfInfluenceDepth - 1))
	if err != nil || !ok {
		t.Fatalf("shallow send should persist: ok=%v err=%v", ok, err)
	}
	if state.SelfInfluenceDepth != runawaySelfInfluenceDepth-1 {
		t.Fatalf("SelfInfluenceDepth=%d, want %d", state.SelfInfluenceDepth, runawaySelfInfluenceDepth-1)
	}

	// At threshold: dropped as runaway (ok=false, no error). A fresh delivery
	// (new updateSeq) so it is the current pending.
	_, _, ok, err = jm.recordWatchSend(build(runawaySelfInfluenceDepth))
	if err != nil {
		t.Fatalf("runaway drop should not error: %v", err)
	}
	if ok {
		t.Fatal("send at runaway depth must be dropped (ok=false)")
	}

	var droppedReason string
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchSendDropped && event.WatchSend != nil {
			droppedReason = event.WatchSend.DiagnosticReason
		}
	}
	if droppedReason != "runaway" {
		t.Fatalf("dropped reason = %q, want %q", droppedReason, "runaway")
	}
}

// TestSelfInfluenceNotice covers the breaker's worker-facing wording: empty when
// the delivery is not self-influenced, a terse "responded to your last message"
// line when shallow, a pointed "~N exchanges deep ... disengaging" line as the
// loop tightens, and the depth-less "many exchanges deep" line when the chain
// truncated (so the exact depth is unknown). Every non-empty line is wrapped in a
// <system-reminder> so the worker reads it as machinery, not as the watcher.
func TestSelfInfluenceNotice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		self          bool
		gradientDepth int
		truncated     bool
		wantEmpty     bool
		wantContains  []string
		wantAbsent    []string
	}{
		{
			name:      "not self-influenced is empty",
			self:      false,
			wantEmpty: true,
		},
		{
			name:         "shallow depth 0 is terse",
			self:         true,
			wantContains: []string{"<system-reminder>", "</system-reminder>", "responded to your last message"},
		},
		{
			name:          "shallow depth 1 is terse",
			self:          true,
			gradientDepth: 1,
			wantContains:  []string{"<system-reminder>", "responded to your last message"},
		},
		{
			name:          "deeper depth is pointed with number",
			self:          true,
			gradientDepth: 3,
			wantContains:  []string{"<system-reminder>", "~3", "disengaging"},
		},
		{
			name:         "truncated is pointed without number",
			self:         true,
			truncated:    true,
			wantContains: []string{"<system-reminder>", "many exchanges deep", "disengaging"},
			wantAbsent:   []string{"~"},
		},
		{
			name:          "truncated overrides a known depth (no number)",
			self:          true,
			gradientDepth: 5,
			truncated:     true,
			wantContains:  []string{"many exchanges deep"},
			wantAbsent:    []string{"~5", "~"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := selfInfluenceNotice(tc.self, tc.gradientDepth, tc.truncated)
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("want empty, got %q", got)
				}
				return
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("notice missing %q:\n%s", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Fatalf("notice should not contain %q:\n%s", absent, got)
				}
			}
		})
	}
}

// TestSnapshotWatchSendFrameAddsSelfInfluenceNotice guards that a self-influenced
// send delivery gets the breaker's <system-reminder> line prepended to its frame
// (while the normal watch-frame body survives), and that a delivery that is not
// self-influenced carries no such line.
func TestSnapshotWatchSendFrameAddsSelfInfluenceNotice(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")
	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"assistant.message"}, Send: &watchSendArgs{To: "dlg_obs"}})
	cfg := onlyWatchConfigForTest(t, jm)

	build := func() watchSendDelivery {
		jm.mu.Lock()
		d := jm.watchSendSnapshot(cfg, "caller", "test", events.SessionEvent{SessionID: jm.sessionID})
		jm.mu.Unlock()
		return d
	}

	self := build()
	self.selfInfluence = true
	self.gradientDepth = 1
	self = jm.snapshotWatchSendFrame(self)
	if !strings.HasPrefix(self.frame, "<system-reminder>") {
		t.Fatalf("self-influenced frame should begin with the breaker line:\n%s", self.frame)
	}
	if !strings.Contains(self.frame, "responded to your last message") {
		t.Fatalf("self-influenced frame missing the breaker notice:\n%s", self.frame)
	}
	if !strings.Contains(self.frame, "Watch frame") {
		t.Fatalf("self-influenced frame dropped the normal body:\n%s", self.frame)
	}

	plain := build()
	plain.selfInfluence = false
	plain = jm.snapshotWatchSendFrame(plain)
	if strings.Contains(plain.frame, "<system-reminder>") {
		t.Fatalf("non-self-influenced frame should carry no breaker line:\n%s", plain.frame)
	}
	if !strings.Contains(plain.frame, "Watch frame") {
		t.Fatalf("non-self-influenced frame missing the normal body:\n%s", plain.frame)
	}
}
