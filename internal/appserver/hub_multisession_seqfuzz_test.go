package appserver

import (
	"fmt"
	"sort"
	"testing"

	"pgregory.net/rapid"
)

// TestHubMultiSessionSeqFuzz is the Phase-8 stateful lane "8.3 hub
// multi-session". Where TestRouterSeqFuzz models a SINGLE connection's protocol
// lifecycle, this models the hub's MULTI-SESSION fan-out fabric: the
// appserver.Server's connection registry plus its Subscriptions index, which is
// how the live hub multiplexes many sessions (threads) over many clients
// (connections). The hub's relay machinery (cmd/serf-hub/app_rpc.go) drives this
// exact fabric: startRelay calls appserver.Subscribe / server.Broadcast /
// server.SubscriberCount, and the websocket lifecycle calls registerConnection /
// unregisterConnection. So driving those seams directly exercises the real
// multi-session routing state — no LLM, no daemon, no network, fully offline.
//
// Every non-crash bug found in this project has been a STATE bug, and the
// dangerous class here is a CROSS-session bug: a notification for session A
// leaking to a client watching only session B, or a disconnected/cleared
// session lingering in the routing tables so future broadcasts mis-deliver. A
// single-connection model cannot see those; this one can.
//
// OP TABLE (each op draws its connection / thread from small fixed pools so
// sequences collide and interleave across sessions):
//
//	opConnect       register a new client connection (no-op if the id is already
//	                connected; production connection ids are unique)
//	opDisconnect    unregister a connection (server.unregisterConnection): drops
//	                it from the registry AND from every thread's subscriber set
//	opSubscribe     conn.Subscribe(thread): the client "selects" a session to
//	                receive its live notifications
//	opReplace       conn.ReplaceSubscriptions(thread): swap the client to watch
//	                exactly this one session (thread=="" clears all selections)
//	opBroadcast     server.Broadcast(thread, ...): deliver a session-scoped
//	                notification (the "steering lands only there" surface) — then
//	                drain every connection and check exactly the subscribers got it
//	opBroadcastAll  server.BroadcastAll(...): a hub-wide notification to every
//	                connected client regardless of subscription
//
// INVARIANTS, checked after every op against a thin model (model[connID] = set
// of subscribed threadIDs, kept only for currently-connected conns). Each is
// domain-true by reading the code:
//
//	INV1 (registry consistency): the server's live connection-id set equals the
//	     model's connected set — no session lost, none duplicated. (registerConnection
//	     adds, unregisterConnection deletes; the harness connects an id at most once.)
//
//	INV2 (index symmetry / clean teardown): for every connected conn,
//	     subs.Threads(conn) equals its model subscriptions; and for every thread
//	     ever touched, subs.Connections(thread) equals exactly the connected conns
//	     the model says subscribe it, with SubscriberCount matching. Subscribe
//	     writes byConn and byThread symmetrically; ReplaceConnectionSubscriptions
//	     and RemoveConnection rewrite both sides together — so a disconnected conn
//	     must vanish from EVERY thread's subscriber list (no orphan, no leak).
//
//	INV3 (routing isolation — the headline cross-session invariant): a
//	     Broadcast(thread) is delivered to exactly the connected subscribers of
//	     that thread and to no one else; BroadcastAll reaches exactly the connected
//	     set. Broadcast enqueues only to subs.Connections(thread); since the index
//	     holds only connected conns, that set is precisely the model's predicted
//	     subscribers. A non-subscriber is never in that set, so it must never
//	     receive the message.
//
// SCOPE (honest): this models the hub's in-process multi-session ROUTING fabric,
// which is the deterministic, offline-reachable multi-session state. The
// session/turn business lifecycle (a real start/steer/clear mutating a daemon's
// transcript) is delegated to live sources/daemons that need a subprocess and a
// socket; under the offline sandbox the source set is empty and every such verb
// resolves "thread not found" before touching state, so there is no
// deterministic multi-session business state to model there (it is covered live,
// not here). A multi-SOURCE model was scoped out for the same reason: distinct
// sources are distinct daemon/codex backends with no shared in-process state to
// cross-check — the only shared fabric they feed is this subscription index,
// which is keyed by an opaque "<sourceID>:<threadID>" relay key and so is already
// exercised by the thread pool here (the keys are opaque strings to the fabric).
func TestHubMultiSessionSeqFuzz(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		ops := rapid.SliceOfN(drawMSOp, 1, 64).Draw(rt, "ops")
		h := newMSHarness()
		for i, op := range ops {
			h.apply(rt, i, op)
			h.checkInvariants(rt, i, op)
		}
	})
}

// msConnPool and msThreadPool are the small fixed identifier pools. Keeping them
// small forces sequences to revisit the same connection and thread, so
// subscribe/replace/disconnect/broadcast collide and the cross-session paths are
// actually hit. msThreadPool includes "" so opReplace can clear all selections.
var (
	msConnPool   = []string{"c0", "c1", "c2", "c3"}
	msThreadPool = []string{"t0", "t1", "t2", ""}
)

type msOpCode int

const (
	opMSConnect msOpCode = iota
	opMSDisconnect
	opMSSubscribe
	opMSReplace
	opMSBroadcast
	opMSBroadcastAll
	msOpCount
)

type msOp struct {
	code   msOpCode
	conn   string
	thread string
}

func (o msOp) name() string {
	switch o.code {
	case opMSConnect:
		return "connect"
	case opMSDisconnect:
		return "disconnect"
	case opMSSubscribe:
		return "subscribe"
	case opMSReplace:
		return "replace"
	case opMSBroadcast:
		return "broadcast"
	case opMSBroadcastAll:
		return "broadcastAll"
	default:
		return fmt.Sprintf("op(%d)", int(o.code))
	}
}

// drawMSOp draws one operation: a code plus a connection and thread from the
// pools. Unused fields for a given code are simply ignored when applied.
var drawMSOp = rapid.Custom(func(rt *rapid.T) msOp {
	return msOp{
		code:   msOpCode(rapid.IntRange(0, int(msOpCount)-1).Draw(rt, "code")),
		conn:   rapid.SampledFrom(msConnPool).Draw(rt, "conn"),
		thread: rapid.SampledFrom(msThreadPool).Draw(rt, "thread"),
	}
})

// msHarness pairs the real Server with the thin model.
type msHarness struct {
	server *Server
	// live holds the *Connection for every currently-connected id, so the harness
	// can drain each connection's send buffer to observe delivery.
	live map[string]*Connection
	// model[connID] is the set of threads that connection subscribes; present only
	// for connected conns, mirroring the server's registry.
	model map[string]map[string]bool
	// threadsSeen is every thread id ever subscribed or broadcast to, so INV2 can
	// confirm a thread that should now have no subscribers really has none.
	threadsSeen map[string]bool
	seq         int
}

func newMSHarness() *msHarness {
	return &msHarness{
		server:      NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"}),
		live:        map[string]*Connection{},
		model:       map[string]map[string]bool{},
		threadsSeen: map[string]bool{},
	}
}

func (h *msHarness) apply(rt *rapid.T, step int, op msOp) {
	switch op.code {
	case opMSConnect:
		// Production connection ids are unique; connecting an already-connected id
		// is a no-op so the model stays unambiguous.
		if _, ok := h.live[op.conn]; ok {
			return
		}
		conn := h.server.NewConnection(op.conn)
		h.server.registerConnection(conn)
		h.live[op.conn] = conn
		h.model[op.conn] = map[string]bool{}

	case opMSDisconnect:
		if _, ok := h.live[op.conn]; !ok {
			return
		}
		h.server.unregisterConnection(op.conn)
		delete(h.live, op.conn)
		delete(h.model, op.conn)

	case opMSSubscribe:
		// Only a connected conn can select a session, and "" is not a real
		// selection (the live Subscribe seam guards it); skip both.
		conn, ok := h.live[op.conn]
		if !ok || op.thread == "" {
			return
		}
		conn.Subscribe(op.thread)
		h.model[op.conn][op.thread] = true
		h.threadsSeen[op.thread] = true

	case opMSReplace:
		conn, ok := h.live[op.conn]
		if !ok {
			return
		}
		conn.ReplaceSubscriptions(op.thread)
		h.model[op.conn] = map[string]bool{}
		if op.thread != "" {
			h.model[op.conn][op.thread] = true
			h.threadsSeen[op.thread] = true
		}

	case opMSBroadcast:
		if op.thread != "" {
			h.threadsSeen[op.thread] = true
		}
		h.checkBroadcast(rt, step, op.thread)

	case opMSBroadcastAll:
		h.checkBroadcastAll(rt, step)
	}
}

// checkBroadcast performs a real Broadcast(thread) with a per-call unique method
// string, then drains every connected conn and asserts the set of conns that
// received it equals exactly the model's connected subscribers of that thread
// (INV3 routing isolation, both directions).
func (h *msHarness) checkBroadcast(rt *rapid.T, step int, thread string) {
	h.seq++
	method := fmt.Sprintf("bcast/%d", h.seq)
	predicted := map[string]bool{}
	for cid, threads := range h.model {
		if threads[thread] {
			predicted[cid] = true
		}
	}
	h.server.Broadcast(thread, method, nil)
	delivered := h.drainAll(rt, step, method)
	h.assertSet(rt, step, fmt.Sprintf("broadcast thread=%q", thread), predicted, delivered)
}

// checkBroadcastAll performs a real BroadcastAll and asserts it reaches exactly
// the connected set.
func (h *msHarness) checkBroadcastAll(rt *rapid.T, step int) {
	h.seq++
	method := fmt.Sprintf("ball/%d", h.seq)
	predicted := map[string]bool{}
	for cid := range h.live {
		predicted[cid] = true
	}
	h.server.BroadcastAll(method, nil)
	delivered := h.drainAll(rt, step, method)
	h.assertSet(rt, step, "broadcastAll", predicted, delivered)
}

// drainAll empties every connected conn's send buffer and returns the set of
// conns whose buffer held a message carrying the expected method. The harness
// drains after every broadcast op, so each buffer holds at most this one
// message; any leftover with a different method, or a duplicate, is itself a
// routing defect and fails the test.
func (h *msHarness) drainAll(rt *rapid.T, step int, method string) map[string]bool {
	got := map[string]bool{}
	for cid, conn := range h.live {
		for {
			select {
			case msg, ok := <-conn.send:
				if !ok {
					rt.Fatalf("step %d: send channel for %q closed while connected", step, cid)
				}
				if msg.Notification == nil {
					rt.Fatalf("step %d: conn %q received non-notification %v", step, cid, msg)
				}
				if msg.Notification.Method != method {
					rt.Fatalf("step %d: conn %q received stray notification method=%q want=%q",
						step, cid, msg.Notification.Method, method)
				}
				if got[cid] {
					rt.Fatalf("step %d: conn %q received duplicate of %q", step, cid, method)
				}
				got[cid] = true
			default:
				goto next
			}
		}
	next:
	}
	return got
}

// checkInvariants verifies INV1 and INV2 after every op.
func (h *msHarness) checkInvariants(rt *rapid.T, step int, op msOp) {
	// INV1: the server registry's connection-id set equals the model's.
	h.server.mu.RLock()
	realConns := map[string]bool{}
	for id := range h.server.conns {
		realConns[id] = true
	}
	h.server.mu.RUnlock()
	modelConns := map[string]bool{}
	for id := range h.live {
		modelConns[id] = true
	}
	h.assertSet(rt, step, "registry "+op.name(), modelConns, realConns)

	// INV2a: each connected conn's subscribed-thread set matches the model.
	for cid := range h.live {
		actual := sliceToSet(h.server.subs.Threads(cid))
		h.assertSet(rt, step, fmt.Sprintf("threads(%s) after %s", cid, op.name()), h.model[cid], actual)
	}

	// INV2b: each thread ever touched has exactly the connected subscribers the
	// model predicts — a disconnected conn must not linger anywhere.
	for thread := range h.threadsSeen {
		predicted := map[string]bool{}
		for cid, threads := range h.model {
			if threads[thread] {
				predicted[cid] = true
			}
		}
		actual := sliceToSet(h.server.subs.Connections(thread))
		h.assertSet(rt, step, fmt.Sprintf("connections(%s) after %s", thread, op.name()), predicted, actual)
		if got := h.server.SubscriberCount(thread); got != len(predicted) {
			rt.Fatalf("step %d: SubscriberCount(%q) = %d, want %d (after %s)",
				step, thread, got, len(predicted), op.name())
		}
	}
}

func (h *msHarness) assertSet(rt *rapid.T, step int, what string, want, got map[string]bool) {
	if !setsEqual(want, got) {
		rt.Fatalf("step %d: %s set mismatch: want %v got %v", step, what, sortedKeys(want), sortedKeys(got))
	}
}

func setsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func sliceToSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
