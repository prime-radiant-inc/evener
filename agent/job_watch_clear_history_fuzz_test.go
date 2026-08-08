//go:build serffuzz

package agent

import (
	"testing"
)

// FuzzWatchClearHistoryResidue exercises lookup and history behavior at the
// boundary between live watches and terminal-flush residue. The manager uses a
// real test-owned job store and a pinned clock; no process or provider is used.
func FuzzWatchClearHistoryResidue(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{1})
	f.Add([]byte{1, 1, 7})

	f.Fuzz(func(t *testing.T, data []byte) {
		jm := newTestJM(t)
		t.Cleanup(func() { _ = jm.closeStoreOnly() })

		receiverSessionID := ""
		if watchClearHistoryByte(data, 0)&1 != 0 {
			receiverSessionID = testOwnerSessionID
		}
		cfg := &watchConfig{
			id:                "watch-history",
			watchID:           "watch-history",
			sourcePublic:      "self",
			target:            "job_history",
			receiverSessionID: receiverSessionID,
			createdAt:         frozenTestTime,
		}

		jm.mu.Lock()
		jm.terminalFlush = make(map[*watchConfig]bool)
		jm.terminalFlush[nil] = true
		jm.terminalFlush[cfg] = true
		jm.mu.Unlock()

		exists, err := jm.hasWatchID(cfg.watchID)
		if err != nil || !exists {
			t.Fatalf("terminal watch lookup = %t, %v; want visible residue", exists, err)
		}
		jm.mu.Lock()
		delete(jm.terminalFlush, nil)
		delete(jm.terminalFlush, cfg)
		jm.mu.Unlock()

		jm.mu.Lock()
		before := len(jm.watchHistory)
		jm.recordWatchEndedLocked(watchKey{Target: cfg.target}, nil, "ignored")
		if len(jm.watchHistory) != before {
			jm.mu.Unlock()
			t.Fatalf("nil history config changed history length from %d to %d", before, len(jm.watchHistory))
		}
		jm.mu.Unlock()

		if got := inspectResultFromWatchConfig(watchKey{}, nil); got.Watching || got.WatchID != "" {
			t.Fatalf("nil watch inspection = %+v, want inactive empty result", got)
		}
		if got := inspectResultFromWatchConfig(watchKey{Target: cfg.target}, cfg); !got.Watching || got.WatchID != cfg.watchID {
			t.Fatalf("terminal config inspection = %+v", got)
		}

		errorJM := newTestJM(t)
		if err := errorJM.closeStoreOnly(); err != nil {
			t.Fatalf("close durable-clear store: %v", err)
		}
		if _, err := errorJM.clearWatchByID("watch-missing-from-runtime"); err == nil {
			t.Fatal("durable clear against a closed store succeeded")
		}
	})
}

func watchClearHistoryByte(data []byte, index int) byte {
	if index >= len(data) {
		return 0
	}
	return data[index]
}
