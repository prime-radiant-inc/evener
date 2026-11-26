package hub

// The hub remembers which remote hosts were attached when it stopped and dials
// them again on the next start. The record carries the registration the host
// attached under, so an edited or re-added host is never reconnected as the
// identity the change replaced.

import (
	"sync"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// attachedHostMutation is one queued durability write. The worker applies
// mutations in enqueue order.
type attachedHostMutation struct {
	name     string
	record   hubcore.AttachedHostRecord
	degraded bool
	remove   bool
}

// attachedHostsPersister serializes attached-host writes off the SSH event
// path. onSSHEvent runs under sshconn's per-host lock, so Record/RecordDegraded/
// Remove only enqueue and the single worker performs the store I/O. Close waits
// for every queued mutation to land.
type attachedHostsPersister struct {
	store *hubcore.AttachedHostsStore
	logf  func(format string, args ...any)
	// apply writes one mutation to the store. It is read and replaced under mu;
	// a test replaces it to observe the order the worker applies mutations in.
	apply func(attachedHostMutation)

	mu     sync.Mutex
	queue  []attachedHostMutation
	closed bool
	wake   chan struct{}
	done   chan struct{}
}

// newAttachedHostsPersister starts the serialized writer. A nil store means the
// state file could not be opened; the nil persister makes every method a no-op.
func newAttachedHostsPersister(store *hubcore.AttachedHostsStore, logf func(string, ...any)) *attachedHostsPersister {
	if store == nil {
		return nil
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	p := &attachedHostsPersister{store: store, logf: logf, wake: make(chan struct{}, 1), done: make(chan struct{})}
	p.apply = p.applyToStore
	go p.run()
	return p
}

// Record remembers name with rec once the worker reaches it.
func (p *attachedHostsPersister) Record(name string, rec hubcore.AttachedHostRecord) {
	p.enqueue(attachedHostMutation{name: name, record: rec})
}

// RecordDegraded remembers name without an identity: the worker writes an empty
// record only when the name has none, so a degraded boot's minted incarnation
// never overwrites a durable pair.
func (p *attachedHostsPersister) RecordDegraded(name string) {
	p.enqueue(attachedHostMutation{name: name, degraded: true})
}

// Remove drops name's record: its registration is gone or being replaced.
func (p *attachedHostsPersister) Remove(name string) {
	p.enqueue(attachedHostMutation{name: name, remove: true})
}

func (p *attachedHostsPersister) enqueue(m attachedHostMutation) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.queue = append(p.queue, m)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Close flushes every queued mutation and stops the worker. It is idempotent.
func (p *attachedHostsPersister) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		<-p.done
		return
	}
	p.closed = true
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
	<-p.done
}

func (p *attachedHostsPersister) run() {
	defer close(p.done)
	for {
		<-p.wake
		p.mu.Lock()
		batch, closed, apply := p.queue, p.closed, p.apply
		p.queue = nil
		p.mu.Unlock()
		for _, m := range batch {
			apply(m)
		}
		if closed {
			return
		}
	}
}

func (p *attachedHostsPersister) applyToStore(m attachedHostMutation) {
	var err error
	switch {
	case m.remove:
		err = p.store.Remove(m.name)
	case m.degraded:
		if p.store.Has(m.name) {
			return
		}
		err = p.store.Record(m.name, hubcore.AttachedHostRecord{})
	default:
		err = p.store.Record(m.name, m.record)
	}
	if err != nil {
		p.logf("attached host %q: %v", m.name, err)
	}
}

// shouldAutoReconnect reports whether startup may dial a host without a user
// action. The seam lets a future per-host policy refuse one host without
// rewiring startup.
func shouldAutoReconnect(hostreg.Host) bool {
	return true
}

// selectStartupReconnectHosts splits saved records into the hosts to dial and
// the names to keep. A record whose registration no longer matches the live
// entry belongs to an edited or re-added host and is neither dialed nor kept. A
// remnant-fenced host keeps its record but is not dialed. A degraded boot cannot
// trust live identities, so it matches by name and keeps every record.
func selectStartupReconnectHosts(entries []hostreg.Host, saved map[string]hubcore.AttachedHostRecord, degraded bool, hasOpenRemnant func(string) bool) (toReconnect []hostreg.Host, keepInStore map[string]bool) {
	keepInStore = make(map[string]bool, len(saved))
	for _, entry := range entries {
		rec, ok := saved[entry.Name]
		if !ok {
			continue
		}
		if !degraded && rec.IncarnationID != "" && (entry.Generation != rec.Generation || entry.IncarnationID != rec.IncarnationID) {
			continue
		}
		keepInStore[entry.Name] = true
		if hasOpenRemnant != nil && hasOpenRemnant(entry.Name) {
			continue
		}
		if !shouldAutoReconnect(entry) {
			continue
		}
		toReconnect = append(toReconnect, entry)
	}
	return toReconnect, keepInStore
}

// hostIdentitiesDegraded reports whether this boot's live host identities were
// minted at load rather than restored from durable records.
func (r *hubRuntime) hostIdentitiesDegraded() bool {
	return r.opsUnavailable || r.recordsUnreadable
}

// startStartupHostReconnect prunes records the host set no longer accounts for
// and starts one reconnect loop per saved host. It runs on the startBackground
// path after the host-management surface has loaded the remnant records.
func startStartupHostReconnect(r *hubRuntime, web *WebServer) {
	if r.attachedHostsStore == nil || r.sshManager == nil || r.hostRegistry == nil {
		return
	}
	degraded := r.hostIdentitiesDegraded()
	var hasOpenRemnant func(string) bool
	if web != nil && web.hostManage != nil {
		hasOpenRemnant = func(name string) bool {
			_, open := web.hostManage.openRemnantID(name)
			return open
		}
	}
	toReconnect, keepInStore := selectStartupReconnectHosts(r.hostRegistry.All(), r.attachedHostsStore.Snapshot(), degraded, hasOpenRemnant)
	if !degraded {
		if err := r.attachedHostsStore.Prune(func(name string, _ hubcore.AttachedHostRecord) bool {
			return keepInStore[name]
		}); err != nil {
			r.logf("prune attached hosts: %v", err)
		}
	}
	for _, entry := range toReconnect {
		if err := r.sshManager.StartReconnect(entry.Name); err != nil {
			r.logf("startup reconnect %q: %v", entry.Name, err)
		}
	}
}
