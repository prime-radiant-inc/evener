package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
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

// TestRecordWatchSendRunawayFuseSeesCoalescedDepth: coalescing unions the
// superseded pending's provenance into the survivor, so the runaway fuse must
// run against the UNIONED ancestry, not the latest trigger's own stamp — two
// below-threshold sends for the same key whose delivered same-watch priors are
// disjoint can cross the threshold only in union.
func TestRecordWatchSendRunawayFuseSeesCoalescedDepth(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"assistant.message"}, Send: &watchSendArgs{To: "dlg_obs"}})
	cfg := onlyWatchConfigForTest(t, jm)

	// Half the threshold of delivered priors on each branch, disjoint IDs.
	half := runawaySelfInfluenceDepth / 2
	branch := func(prefix string) *provenance.Causal {
		p := &provenance.Causal{WatchKeys: []provenance.WatchKey{{WatchID: cfg.watchID, WatchGeneration: cfg.generation}}}
		jm.mu.Lock()
		for i := range half {
			id := prefix + string(rune('0'+i))
			p.Chain = append(p.Chain, provenance.Entry{Kind: "watch", WatchID: cfg.watchID, WatchGeneration: cfg.generation, DeliveryID: id})
			jm.deliveredWatchSendIDs[id] = struct{}{}
		}
		jm.mu.Unlock()
		return p
	}

	build := func(p *provenance.Causal) watchSendDelivery {
		jm.mu.Lock()
		d := jm.watchSendSnapshot(cfg, "caller", "test", events.SessionEvent{SessionID: jm.sessionID, Provenance: p})
		c := jm.classifySelfInfluenceLocked(cfg, p)
		jm.mu.Unlock()
		return d.withSelfInfluence(c)
	}

	// First send: depth = half, persists.
	_, _, ok, err := jm.recordWatchSend(build(branch("wa_")))
	if err != nil || !ok {
		t.Fatalf("first send should persist: ok=%v err=%v", ok, err)
	}

	// Second send for the same key, disjoint priors: its own depth is also
	// half, but the coalesced union reaches the threshold — dropped as runaway.
	_, _, ok, err = jm.recordWatchSend(build(branch("wb_")))
	if err != nil {
		t.Fatalf("coalesced runaway drop should not error: %v", err)
	}
	if ok {
		t.Fatal("coalesced union at runaway depth must be dropped (ok=false)")
	}
	var dropped *jobstore.WatchSendState
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchSendDropped && event.WatchSend != nil {
			dropped = event.WatchSend
		}
	}
	if dropped == nil || dropped.DiagnosticReason != "runaway" {
		t.Fatalf("dropped state = %+v, want runaway drop", dropped)
	}
	// The recorded drop must carry the COALESCED evidence: both branches'
	// delivered priors and the union depth that tripped the fuse.
	if dropped.SelfInfluenceDepth != runawaySelfInfluenceDepth {
		t.Fatalf("dropped SelfInfluenceDepth = %d, want union depth %d", dropped.SelfInfluenceDepth, runawaySelfInfluenceDepth)
	}
	ids := map[string]bool{}
	if dropped.Provenance != nil {
		for _, e := range dropped.Provenance.Chain {
			ids[e.DeliveryID] = true
		}
	}
	if !ids["wa_0"] || !ids["wb_0"] {
		t.Fatalf("dropped provenance chain %v must carry both coalesced branches (wa_*, wb_*)", ids)
	}
}

// TestClassifySelfInfluenceRecreatedWatchStartsFresh: a watch re-created on
// the same key (replaced with a changed config, or cleared and configured
// again) mints a fresh watchID and starts fresh — fuseDepth counts only the
// delivered priors of the CURRENT identity, never the predecessor's.
func TestClassifySelfInfluenceRecreatedWatchStartsFresh(t *testing.T) {
	t.Parallel()
	args := watchArgs{
		Target: "caller",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "v1"},
	}
	recreates := map[string]func(t *testing.T, jm *jobManager){
		"replaced": func(t *testing.T, jm *jobManager) {
			replaced := args
			replaced.Send = &watchSendArgs{To: runtimeMessageAliasCaller, Message: "v2 changed"}
			if _, err := jm.configureWatch(replaced); err != nil {
				t.Fatalf("configure v2 (replace): %v", err)
			}
		},
		"cleared and recreated": func(t *testing.T, jm *jobManager) {
			clearArgs := args
			clearArgs.Clear = true
			if _, err := jm.configureWatch(clearArgs); err != nil {
				t.Fatalf("clear: %v", err)
			}
			if _, err := jm.configureWatch(args); err != nil {
				t.Fatalf("recreate: %v", err)
			}
		},
	}
	for name, recreate := range recreates {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			jm := newTestJM(t)
			if _, err := jm.configureWatch(args); err != nil {
				t.Fatalf("configure v1: %v", err)
			}
			oldCfg := onlyWatchConfigForTest(t, jm)
			oldID, oldGen := oldCfg.watchID, oldCfg.generation
			recreate(t, jm)
			cfg := onlyWatchConfigForTest(t, jm)
			if cfg.watchID == oldID {
				t.Fatal("test premise broken: re-create did not mint a fresh watchID")
			}

			// An event descending from two delivered priors of the OLD identity
			// and one delivered prior of the re-created watch.
			p := &provenance.Causal{
				WatchKeys: []provenance.WatchKey{
					{WatchID: oldID, WatchGeneration: oldGen},
					{WatchID: cfg.watchID, WatchGeneration: cfg.generation},
				},
				Chain: []provenance.Entry{
					{Kind: "watch", WatchID: oldID, WatchGeneration: oldGen, DeliveryID: "wd_old1"},
					{Kind: "watch", WatchID: oldID, WatchGeneration: oldGen, DeliveryID: "wd_old2"},
					{Kind: "watch", WatchID: cfg.watchID, WatchGeneration: cfg.generation, DeliveryID: "wd_new1"},
				},
			}
			jm.mu.Lock()
			for _, id := range []string{"wd_old1", "wd_old2", "wd_new1"} {
				jm.deliveredWatchSendIDs[id] = struct{}{}
			}
			got := jm.classifySelfInfluenceLocked(cfg, p)
			jm.mu.Unlock()

			if got.fuseDepth != 1 {
				t.Fatalf("fuseDepth = %d, want 1 (only the re-created watch's own delivered priors count)", got.fuseDepth)
			}
			if got.gradientDepth != 1 {
				t.Fatalf("gradientDepth = %d, want 1 (gradient stays scoped to the current identity)", got.gradientDepth)
			}
		})
	}
}
