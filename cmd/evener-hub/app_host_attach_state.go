package hub

import (
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// hostAttachRecord is one host's retained attach state: what the lifecycle
// events say about an in-progress or failed attach, plus the last-known facts
// of the last row that rendered attached. retiredThrough is the highest entry
// generation this name's record has been retired through.
type hostAttachRecord struct {
	midAttach bool
	lastErr   string
	known     appwire.HostRow // only the fact fields are read back
	// retiredThrough fences the retained state to the entry generations that are
	// still live: content is served only to a row whose entry generation is
	// above this mark, and a row at or below it writes nothing back. It is the
	// generation of the retired entry an edit's swap replaced (retire), kept
	// monotonically non-decreasing so a later retirement cannot lower it. Zero
	// means no identity has been retired for this name, which is why a fresh
	// name's rows are never fenced.
	retiredThrough uint64
}

// hostAttachState retains per-host attach records from the SSH manager's
// lifecycle events and from attached rows this surface renders, so offline
// and in-progress rows keep the metadata the wire contract promises (a host
// mid-attach renders midAttach; a host that failed renders lastAttachError;
// an offline row keeps its last-known facts).
//
// It is generation-scoped, not merely name-keyed: an update retires the
// identity a name's record describes, and the retirement is marked by that
// identity's entry generation (retire). Rows carry the generation of the entry
// they were built from, and only a row above the mark is served or may write
// back. That is what makes an edit's clear immune to a row that captured the
// pre-swap entry before the update but only reaches its state write after the
// swap's retirement: its generation is at or below the mark, so its stale facts
// are neither folded into the row it is building nor recorded for later rows.
// Records for removed hosts are dropped wholesale — remove, the clean-slate
// path — so a re-add starts clean, its mark included.
type hostAttachState struct {
	mu      sync.Mutex
	records map[string]*hostAttachRecord
}

func newHostAttachState() *hostAttachState {
	return &hostAttachState{records: map[string]*hostAttachRecord{}}
}

// observe records one SSH lifecycle event. It only writes the map: it runs
// from sshconn's OnEvent with the per-host lock held, so it must never call
// back into the manager.
func (s *hostAttachState) observe(ev sshconn.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[ev.Host]
	if rec == nil {
		rec = &hostAttachRecord{}
		s.records[ev.Host] = rec
	}
	switch ev.Kind {
	case sshconn.EventAttached:
		// A fresh attach supersedes the previous attempt's error and ends
		// the in-progress state.
		rec.midAttach = false
		rec.lastErr = ""
	case sshconn.EventFailed:
		rec.midAttach = false
		if ev.Err != nil {
			rec.lastErr = ev.Err.Error()
		}
	case sshconn.EventDetached:
		// A reconnecting detach does not end the in-progress attach: the
		// supervisor's link-drop path emits StateReconnecting immediately
		// before this event and keeps retrying through its backoff, so the
		// row keeps reporting the attach and the Connect control stays
		// disabled while the manager is already reconnecting, instead of
		// inviting a redundant manual attempt. Every other detach — a
		// teardown, a shutdown, a publish race — carries StateDisconnected
		// and ends the in-progress attach.
		if ev.State != sshconn.StateReconnecting {
			rec.midAttach = false
		}
	case sshconn.EventState:
		switch ev.State {
		case sshconn.StatePreflighting, sshconn.StateDeploying, sshconn.StateRestarting,
			sshconn.StateAttaching, sshconn.StateReconnecting:
			rec.midAttach = true
		default:
			rec.midAttach = false
		}
	}
}

// hostFactsValidity records which of an attached row's fact fields the live
// lookups actually refreshed: the handshake seam owns the server identity
// pair, the facts seam the hub/os/arch triple. The seams report one success
// flag per group, so validity is per lookup, not per field — a successful
// lookup's values are authoritative even when empty.
type hostFactsValidity struct {
	handshake bool
	facts     bool
}

// recordKnown keeps the facts of the last row that rendered attached, so the
// host's later offline rows still render them. Only the fields whose live
// lookup succeeded are written: a transient handshake or facts failure on an
// attached host leaves those fields empty in the row, and recording them would
// blank the previously retained facts — the offline rows that follow would
// lose the metadata the wire contract promises. gen is the entry generation the
// row was built from; a row at or below the name's retired mark describes an
// identity the record no longer belongs to, so it writes nothing — this is the
// write that a retirement fences out, and without it a late pre-swap row would
// recreate the retired identity's facts after the clear.
func (s *hostAttachState) recordKnown(row appwire.HostRow, validity hostFactsValidity, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[row.Name]
	if rec != nil && gen <= rec.retiredThrough {
		return
	}
	if rec == nil {
		rec = &hostAttachRecord{}
		s.records[row.Name] = rec
	}
	if validity.handshake {
		rec.known.ServerName = row.ServerName
		rec.known.ServerVersion = row.ServerVersion
	}
	if validity.facts {
		rec.known.HubVersion = row.HubVersion
		rec.known.OS = row.OS
		rec.known.Arch = row.Arch
	}
}

// apply folds the retained record into row: the attach state always, the
// last-known facts only when the row is not attached (an attached row's facts
// come from the live channel). gen is the entry generation the row was built
// from; a row at or below the name's retired mark folds nothing, so a stale
// pre-swap row renders clean instead of the retired identity's state.
func (s *hostAttachState) apply(row *appwire.HostRow, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[row.Name]
	if rec == nil || gen <= rec.retiredThrough {
		return
	}
	row.MidAttach = rec.midAttach
	row.LastAttachErr = rec.lastErr
	if !row.Attached {
		row.ServerName = rec.known.ServerName
		row.ServerVersion = rec.known.ServerVersion
		row.HubVersion = rec.known.HubVersion
		row.OS = rec.known.OS
		row.Arch = rec.known.Arch
	}
}

// retire drops the record's content for name and marks every row whose entry
// generation is <= gen as retired: such a row must neither be served the
// name's retained state nor write any back. gen is the generation of the entry
// an update retired, so a row that captured that entry — or any earlier one —
// is fenced, while a row built from the new identity's higher generation is
// served and records normally. The mark is monotonic (the max of the existing
// and the new generation) so a retirement can never lower it, and the record
// entry is kept (with its mark) so the mark survives even though the content is
// gone.
func (s *hostAttachState) retire(name string, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[name]
	if rec == nil {
		rec = &hostAttachRecord{}
		s.records[name] = rec
	}
	if gen > rec.retiredThrough {
		rec.retiredThrough = gen
	}
	rec.midAttach = false
	rec.lastErr = ""
	rec.known = appwire.HostRow{}
}

// remove drops name's record; a removed or re-added host starts clean.
func (s *hostAttachState) remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, name)
}

// detach sweeps name's record and hands it back, for a caller that must commit
// nothing until its commit phases land: the record leaves the live map now (a
// re-added name must not inherit it) and a refusal restores it, so a refused
// add leaves the retained state exactly as it was.
func (s *hostAttachState) detach(name string) *hostAttachRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[name]
	delete(s.records, name)
	return rec
}

// restore puts back the record detach handed out — or leaves the entry absent
// when there was none.
func (s *hostAttachState) restore(name string, rec *hostAttachRecord) {
	if rec == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[name] = rec
}
