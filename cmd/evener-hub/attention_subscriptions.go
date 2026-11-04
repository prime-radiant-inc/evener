package hub

import (
	"maps"
	"sort"
	"sync"

	"primeradiant.com/evener/appwire"
)

const rosterBatchLimit = 256

func (o *sessionOperations) rosterBroker() *attentionBroker {
	if o.navigation != nil {
		return &o.navigation.roster
	}
	return &o.roster
}

type rosterIdentity struct{ Ref, InstanceID string }

type attentionBroker struct {
	mu          sync.Mutex
	latest      map[string]appwire.AttentionEntry
	identities  map[string]rosterIdentity
	revision    uint64
	subscribers map[*rosterSubscription]struct{}
}

type rosterSubscription struct {
	Updates     <-chan appwire.SessionRosterUpdate
	Cancel      func()
	updates     chan appwire.SessionRosterUpdate
	callerRef   string
	baseline    map[string]appwire.AttentionEntry
	missingRefs map[string]bool
}

// Subscribe registers before publication can advance the initial snapshot.
func (b *attentionBroker) Subscribe(caller appwire.ManagerView) (*rosterSubscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	updates := make(chan appwire.SessionRosterUpdate, 1)
	sub := &rosterSubscription{Updates: updates, updates: updates, callerRef: caller.Ref}
	sub.baseline = b.filtered(sub)
	sub.missingRefs = make(map[string]bool)
	updates <- initialRosterUpdate(b.revision, sub.baseline)
	if b.subscribers == nil {
		b.subscribers = make(map[*rosterSubscription]struct{})
	}
	b.subscribers[sub] = struct{}{}
	sub.Cancel = func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subscribers[sub]; ok {
			delete(b.subscribers, sub)
			close(updates)
		}
	}
	return sub, nil
}

func sortedRosterIDs(entries map[string]appwire.AttentionEntry) []string {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func initialRosterUpdate(revision uint64, entries map[string]appwire.AttentionEntry) appwire.SessionRosterUpdate {
	update := appwire.SessionRosterUpdate{Revision: revision, Initial: true}
	for _, id := range sortedRosterIDs(entries) {
		entry := entries[id]
		kind := entry.Level
		if kind == "working" {
			kind = "idle"
		}
		update.Events = append(update.Events, appwire.RosterEvent{AttentionEntry: entry, Kind: kind})
	}
	return update
}

func (b *attentionBroker) filtered(sub *rosterSubscription) map[string]appwire.AttentionEntry {
	current := make(map[string]appwire.AttentionEntry)
	for id, entry := range b.latest {
		identity, ok := b.identities[id]
		if ok && identity.Ref != "" && identity.Ref != sub.callerRef {
			current[id] = entry
		}
	}
	return current
}

// Publish advances each subscriber's baseline even when no actionable delta is sent.
// Its channel operations are nonblocking, including replacement after a racing read.
func (b *attentionBroker) Publish(entries map[string]appwire.AttentionEntry, identity func(string) (rosterIdentity, bool)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.latest = make(map[string]appwire.AttentionEntry, len(entries))
	b.identities = make(map[string]rosterIdentity, len(entries))
	for id, entry := range entries {
		ident, ok := identity(id)
		if !ok || ident.Ref == "" {
			continue
		}
		entry.Ref = ident.Ref
		entry.RosterInstanceID = ident.InstanceID
		b.latest[id], b.identities[id] = entry, ident
	}
	b.revision++
	for sub := range b.subscribers {
		current := b.filtered(sub)
		update := appwire.SessionRosterUpdate{Revision: b.revision}
		currentRefs := make(map[string]bool, len(current))
		for _, id := range sortedRosterIDs(current) {
			entry := current[id]
			currentRefs[entry.Ref] = true
			delete(sub.missingRefs, entry.Ref)
			previous, existed := sub.baseline[id]
			kind := ""
			switch {
			case !existed || previous.RosterInstanceID != entry.RosterInstanceID:
				kind = "new"
			case entry.Level == "needs_you" && (previous.Level != entry.Level || previous.AskPending != entry.AskPending || previous.ApprovalPending != entry.ApprovalPending || previous.NeedsResponse != entry.NeedsResponse):
				kind = "needs_you"
			case entry.Level == "error" && previous.Level != entry.Level:
				kind = "error"
			}
			if kind != "" {
				update.Events = append(update.Events, appwire.RosterEvent{AttentionEntry: entry, Kind: kind})
			}
		}
		for _, id := range sortedRosterIDs(sub.baseline) {
			// Clear replaces the session ID without removing its workspace ref.
			if _, ok := current[id]; !ok && !currentRefs[sub.baseline[id].Ref] {
				entry := sub.baseline[id]
				if !sub.missingRefs[entry.Ref] {
					// Clear can leave one attention tick without a live entry.
					// Keep it in the effective baseline until absence is confirmed.
					sub.missingRefs[entry.Ref] = true
					current[id] = entry
					continue
				}
				delete(sub.missingRefs, entry.Ref)
				update.Events = append(update.Events, appwire.RosterEvent{AttentionEntry: sub.baseline[id], Kind: "disappeared"})
			}
		}
		changed := !maps.Equal(sub.baseline, current)
		sub.baseline = current
		if len(update.Events) == 0 {
			if changed {
				// A pending update describes an old population. Refresh its
				// initial snapshot, or request a resync if it was a delta.
				select {
				case pending := <-sub.updates:
					if pending.Initial {
						update = initialRosterUpdate(b.revision, current)
					} else {
						update = appwire.SessionRosterUpdate{Revision: b.revision, Resync: true}
					}
					select {
					case sub.updates <- update:
					default:
					}
				default:
				}
			}
			continue
		}
		if len(update.Events) > rosterBatchLimit {
			update = appwire.SessionRosterUpdate{Revision: b.revision, Resync: true}
		}
		select {
		case sub.updates <- update:
			// No older update was pending.
		default:
			// The consumer may drain the channel after the full-buffer test.
			// An absent pending update must never turn this into a blocking receive.
			var pending appwire.SessionRosterUpdate
			var hadPending bool
			select {
			case pending = <-sub.updates:
				hadPending = true
			default:
			}
			if pending.Initial {
				update = initialRosterUpdate(b.revision, current)
			} else if hadPending {
				update = appwire.SessionRosterUpdate{Revision: b.revision, Resync: true}
			}
			select {
			case sub.updates <- update:
			default:
			}
		}
	}
}
