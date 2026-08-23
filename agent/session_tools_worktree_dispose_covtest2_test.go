package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestWatchesTargeting_SendMatch covers the cfg.send.To == id branch (lines 422-424).
func TestWatchesTargeting_SendMatch(t *testing.T) {
	jm := &jobManager{
		watches: map[watchKey]*watchConfig{
			{Target: "job_1"}: {
				send: &watchSendArgs{To: "dlg_target"},
			},
		},
		terminalFlush: map[*watchConfig]bool{},
	}
	if !jm.watchesTargeting("dlg_target") {
		t.Fatal("expected true for send.To match")
	}
}

// TestWatchesTargeting_PendingMatch covers the cfg.pending match branch (lines 425-428).
func TestWatchesTargeting_PendingMatch(t *testing.T) {
	jm := &jobManager{
		watches: map[watchKey]*watchConfig{
			{Target: "job_1"}: {
				pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{
					{ResolvedSendTo: "dlg_target"}: {},
				},
			},
		},
		terminalFlush: map[*watchConfig]bool{},
	}
	if !jm.watchesTargeting("dlg_target") {
		t.Fatal("expected true for pending match")
	}
}

// TestWatchesTargeting_TerminalFlushMatch covers the terminalFlush match branch (lines 431-435).
func TestWatchesTargeting_TerminalFlushMatch(t *testing.T) {
	cfg := &watchConfig{
		pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{
			{ResolvedSendTo: "dlg_target"}: {},
		},
	}
	jm := &jobManager{
		watches:       map[watchKey]*watchConfig{},
		terminalFlush: map[*watchConfig]bool{cfg: true},
	}
	if !jm.watchesTargeting("dlg_target") {
		t.Fatal("expected true for terminalFlush match")
	}
}

// TestWatchesTargeting_NoMatch covers the false return (line 438).
func TestWatchesTargeting_NoMatch(t *testing.T) {
	jm := &jobManager{
		watches: map[watchKey]*watchConfig{
			{Target: "job_1"}: {
				send: &watchSendArgs{To: "dlg_other"},
				pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{
					{ResolvedSendTo: "dlg_other"}: {},
				},
			},
		},
		terminalFlush: map[*watchConfig]bool{},
	}
	if jm.watchesTargeting("dlg_target") {
		t.Fatal("expected false for no match")
	}
}

// TestWatchesTargeting_SendNil covers the nil-send skip within the watches loop.
func TestWatchesTargeting_SendNil(t *testing.T) {
	jm := &jobManager{
		watches: map[watchKey]*watchConfig{
			{Target: "job_1"}: {
				send:    nil,
				pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{},
			},
		},
		terminalFlush: map[*watchConfig]bool{},
	}
	if jm.watchesTargeting("dlg_target") {
		t.Fatal("expected false for nil send and empty pending")
	}
}

// TestSubtreeWatchesTargeting_NilJobManager covers the nil jobManager path
// with subagents.
func TestSubtreeWatchesTargeting_NilJobManagerWithSubagents(t *testing.T) {
	s := &Session{
		subagents: &subagentManager{
			subs: map[string]*subagent{},
		},
	}
	if s.subtreeWatchesTargeting("dlg_test") {
		t.Fatal("expected false with nil jobManager and empty subagents")
	}
}

// TestSubtreeWatchesTargeting_NilSubagents covers the nil subagents path.
func TestSubtreeWatchesTargeting_NilSubagents(t *testing.T) {
	s := &Session{
		subagents: &subagentManager{subs: map[string]*subagent{}},
	}
	if s.subtreeWatchesTargeting("dlg_test") {
		t.Fatal("expected false with empty subagents")
	}
}

// TestSubtreeWatchesTargeting_JobManagerMatch covers the jobManager match path (line 404-405).
func TestSubtreeWatchesTargeting_JobManagerMatch(t *testing.T) {
	jm := &jobManager{
		watches: map[watchKey]*watchConfig{
			{Target: "job_1"}: {
				send: &watchSendArgs{To: "dlg_target"},
			},
		},
		terminalFlush: map[*watchConfig]bool{},
	}
	s := &Session{
		jobManager: jm,
		subagents:  &subagentManager{subs: map[string]*subagent{}},
	}
	if !s.subtreeWatchesTargeting("dlg_target") {
		t.Fatal("expected true for jobManager match")
	}
}
