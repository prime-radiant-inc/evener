package hostops

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// The per-host gate (deploy pipeline 08b §5). One gate exists per host name,
// shared by every holder of that host's work: `plan`'s validation-plus-mint
// window, `deploy`/`restart` operations, `Ensure`-triggered deploys, supervisor
// activity, and the update/remove reservations. Acquisition is try-acquire —
// nothing in the pipeline waits on a held gate — and a refusal is the typed busy
// error, discriminated by who holds the gate.
//
// A gate is not process-wide: two hubs in one process (a hub and the web
// server's own instance) each own theirs, and the production hub's is the
// sshconn Manager's existing per-host lock, which implements Gate. This file
// owns the vocabulary — the holder classes and the busy error — and the
// standalone implementation used where no manager owns the hosts (tests and
// embedders).

// HolderKind classifies who holds a per-host gate. The classification decides
// how a busy refusal renders: an operation holder names its operation record
// (open/wait-able), while the plan and manager classes render the typed
// transient form carrying no operation reference.
type HolderKind string

const (
	// HolderOperation is a `deploy`/`restart` operation — including an
	// `Ensure`-triggered deploy, which holds its own operation-store record —
	// holding the gate. OperationID names the controller-assigned record id the
	// busy payload's open/wait-able reference resolves through.
	HolderOperation HolderKind = "operation"
	// HolderPlan is `plan`'s validation-plus-mint window, which holds no
	// operation-store record: the busy error is the typed transient form with no
	// operation reference.
	HolderPlan HolderKind = "plan"
	// HolderManager is the manager's own internal work — an attach/Ensure, a
	// supervisor reconnect, an update/remove/teardown — which likewise holds no
	// operation-store record. Activity names it for the operator ("attach",
	// "update", "remove", "teardown", "reconnect"), never invented when empty.
	HolderManager HolderKind = "manager"
)

// Holder is what a gate acquisition presents: the class of holder, plus the
// operation id an operation holder names.
type Holder struct {
	Kind        HolderKind
	OperationID string
	// Activity names a manager holder's work; it is used only when Kind is
	// HolderManager.
	Activity string
}

// BusyError is §5's typed busy refusal: the host whose gate is held and the
// holder that holds it. It is discriminated by holder class, never by prose:
// a caller maps HolderOperation to `host-busy-operation` (with the operation
// id) and every other class to `host-busy-transient` (no operation reference).
type BusyError struct {
	Host   string
	Holder Holder
}

// Error renders §5's two forms: the operation form names the operation, and the
// transient form names the plan or the manager activity — never an operation id
// it does not have.
func (e *BusyError) Error() string {
	switch e.Holder.Kind {
	case HolderOperation:
		id := strings.TrimSpace(e.Holder.OperationID)
		if id == "" {
			return fmt.Sprintf("host %q busy: an operation is in progress", e.Host)
		}
		return fmt.Sprintf("host %q busy: operation %s is in progress", e.Host, id)
	case HolderPlan:
		return fmt.Sprintf("host %q busy (plan in progress)", e.Host)
	default:
		activity := strings.TrimSpace(e.Holder.Activity)
		if activity == "" {
			activity = "another host operation"
		}
		return fmt.Sprintf("host %q busy (%s in progress)", e.Host, activity)
	}
}

// Busy builds a busy refusal for host held by holder.
func Busy(host string, holder Holder) *BusyError {
	return &BusyError{Host: host, Holder: holder}
}

// Gate is the per-host gate as the pipeline consumes it: try-acquire, nothing
// waits, and a held gate answers the typed busy error. The production
// implementation is the sshconn Manager's existing per-host lock, so a plan
// contends with attaches, supervisors, updates, and removals for the same host;
// ProcessGate below is the standalone implementation for hubs with no manager.
type Gate interface {
	// TryAcquire try-acquires host's gate for holder. On success it returns a
	// release func that must be called exactly once; on a held gate it returns
	// *BusyError naming the current holder. Nothing waits.
	TryAcquire(host string, holder Holder) (release func(), err error)
}

// ProcessGate is the standalone in-process Gate: one mutex per host name,
// reference-counted so a name with no holder or waiter does not leak an entry,
// with the holder recorded for busy rendering. It is what a hub with no
// sshconn Manager (tests, embedders) gates its hosts on; in production the
// Manager's lock is the one gate every path takes.
type ProcessGate struct {
	mu    sync.Mutex
	hosts map[string]*gateHost
}

// gateHost is one host's gate cell: the gate mutex itself, the live-user
// reference count that keeps the cell alive while anyone holds or acquires it,
// and the current holder. holder and refs are guarded by the owning
// ProcessGate's mu; mu is the gate.
type gateHost struct {
	mu     sync.Mutex
	refs   int
	holder Holder
}

// NewGate builds an empty standalone gate.
func NewGate() *ProcessGate {
	return &ProcessGate{hosts: map[string]*gateHost{}}
}

// TryAcquire implements Gate. An empty host name is refused: a gate keyed by ""
// would silently serialize unrelated work.
func (g *ProcessGate) TryAcquire(host string, holder Holder) (func(), error) {
	name := strings.TrimSpace(host)
	if name == "" {
		return nil, errors.New("hostops: a gate acquisition needs a host name")
	}
	g.mu.Lock()
	entry := g.hosts[name]
	if entry == nil {
		entry = &gateHost{}
		g.hosts[name] = entry
	}
	entry.refs++
	g.mu.Unlock()

	if !entry.mu.TryLock() {
		g.mu.Lock()
		held := entry.holder
		g.mu.Unlock()
		g.dropRef(name, entry)
		return nil, Busy(name, held)
	}
	g.mu.Lock()
	entry.holder = holder
	g.mu.Unlock()

	// The release is idempotent: a double release must not double-unlock, and a
	// caller that defers it on a path that also calls it explicitly stays safe.
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			entry.holder = Holder{}
			g.mu.Unlock()
			entry.mu.Unlock()
			g.dropRef(name, entry)
		})
	}, nil
}

// dropRef releases one live-user reference, dropping the cell when the last one
// goes: at zero no goroutine holds the gate or is acquiring it, so a later
// acquisition builds a fresh cell rather than splitting one host's exclusion
// across two mutexes.
func (g *ProcessGate) dropRef(name string, entry *gateHost) {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(g.hosts, name)
	}
}
