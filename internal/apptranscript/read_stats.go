package apptranscript

import (
	"sync"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// ReadStats describes the work performed by one full-transcript scan. It is
// reported only through the read observer below, for package tests.
type ReadStats struct {
	usageScans   int64
	failureScans int64
	derivedScans int64
}

var (
	observeTurnIndexReadMu sync.RWMutex
	observeTurnIndexRead   func(ReadStats)
)

// InstallReadObserverForTesting installs instrumentation for full-transcript
// scans and returns a function that restores the previous observer. The
// callback is invoked without holding the observer lock.
func InstallReadObserverForTesting(observer func(ReadStats)) func() {
	observeTurnIndexReadMu.Lock()
	previous := observeTurnIndexRead
	observeTurnIndexRead = observer
	observeTurnIndexReadMu.Unlock()
	return func() {
		observeTurnIndexReadMu.Lock()
		observeTurnIndexRead = previous
		observeTurnIndexReadMu.Unlock()
	}
}

func observeIndexRead(stats ReadStats) {
	observeTurnIndexReadMu.RLock()
	observer := observeTurnIndexRead
	observeTurnIndexReadMu.RUnlock()
	if observer != nil {
		observer(stats)
	}
}

// fullProjector adapts a BoundedEntryProjector (which threads a shared
// tool-name resolver across a whole transcript) into an EntryProjector
// (one turn at a time), for the full, unbounded transcript projection.
func fullProjector(project BoundedEntryProjector) EntryProjector {
	toolNames := map[string]string{}
	return func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		if project == nil {
			return nil
		}
		return project(turn, turnID, turnIndex, toolNames)
	}
}
