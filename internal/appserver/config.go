package appserver

import (
	"context"
	"encoding/json"
	"time"

	"primeradiant.com/evener/appwire"
)

type ServerConfig struct {
	ServerName   string
	Version      string
	BuildVersion string
	SourceID     string
	Features     appwire.FeatureSet
	// OS, Arch and Roots describe the serving machine, reported in
	// ServerInfo so a client can show the hub's own machine's system and
	// project roots beside the hosts it reaches over SSH. Empty OS/Arch and a
	// nil Roots leave them absent; a server that does not claim a machine
	// leaves all three unset.
	OS    string
	Arch  string
	Roots []string
	// WebSocketTrace records raw data frames and lifecycle events for each
	// accepted connection. Nil keeps tracing disabled.
	WebSocketTrace *WebSocketTrace
	// Navigation is absent until a server supports navigation HTTP resources.
	Navigation *appwire.NavigationCapability
	// NavigationCapability is evaluated for every initialize request. It lets a
	// hub advertise a current generation/sequence rather than freezing those
	// values when its RPC server was constructed.
	NavigationCapability func() *appwire.NavigationCapability
	// AdapterNativeInitialize keeps the shared JSON-RPC server usable in tests
	// for adapters whose upstream protocol owns a different initialize shape.
	AdapterNativeInitialize bool
	// Logf reports server-initiated diagnostics a peer cannot see from its side
	// of the socket, including handler failures and connection lifecycle events.
	// Nil means silent.
	Logf func(format string, args ...any)
	// SubscriptionAdmissionResolver classifies a thread/read or
	// thread/unsubscribe request for ordered dispatch: the canonical
	// pending-admission key of a subscribing read, an unresolvable one
	// (unavailable, unless its params are invalid) or a request that is not a
	// subscribe at all. It must be pure and synchronous; ordered dispatch uses
	// it before starting the read or invoking the unsubscribe handler. It does
	// not determine delivery routing.
	SubscriptionAdmissionResolver func(appwire.Message) SubscriptionAdmissionResolution
	// RequestAdmissionContext captures immutable request metadata before queueing.
	// It must be pure, synchronous, and derive its result from the supplied
	// connection context so cancellation remains inherited.
	RequestAdmissionContext func(context.Context, appwire.Message) context.Context
	// ConnectionAdmissionContext captures immutable metadata before accepting
	// any WebSocket frames. It must preserve the supplied context cancellation.
	ConnectionAdmissionContext func(context.Context) context.Context
	// ConcurrentRequest names requests beyond the built-in slow reads
	// (concurrentDispatchMethod) that may leave the connection's serial worker,
	// judged from the method and its params: the hub uses it for a forwarded
	// read, whose cost is the remote host's. It must be pure and synchronous,
	// and answer ok only for a request safe to run out of order against
	// every other request on the connection, and that a client can simply
	// retry: one beyond its full pool (concurrentRequestCap) is answered
	// Unavailable without running. pool names what the request waits on (the
	// hub names the remote host), and each pool has its own slots, so one
	// slow pool cannot refuse another's requests. It runs on the serial
	// worker, which has no panic barrier, so it must not panic. Nil adds none.
	ConcurrentRequest func(method string, params json.RawMessage) (pool string, ok bool)
}

type SubscriptionAdmissionIntent uint8

const (
	SubscriptionAdmissionNotSubscribe SubscriptionAdmissionIntent = iota
	SubscriptionAdmissionResolved
	SubscriptionAdmissionUnresolved
	SubscriptionAdmissionInvalid
)

type SubscriptionAdmissionResolution struct {
	Key          string
	SecondaryKey string
	Intent       SubscriptionAdmissionIntent
}

// requestQueueCap bounds each connection's inbound request queue. It must
// hold any legitimate pipelined burst from one client (observed bursts are a
// handful of requests) while keeping the queue's worst-case memory boring; a
// full queue applies blocking backpressure to the receive loop, never a wire
// error. Deliberately not appwire.NotificationBufferCap: that constant sizes
// the outbound notification firehose, where overflow means eviction; this one
// sizes inbound pipelining, where overflow means flow control. Coupling them
// would let the wrong contract resize this one.
const requestQueueCap = 64

// slowReadDispatchCap bounds how many concurrent slow reads one connection
// holds in flight. 16 is the operating point the slice-0 audit's client sweep
// chose (Jesse, 2026-08-30): routine flows exceed the earlier draft's 4 — the
// web delegate rail mounts every visible delegate card with a full
// thread/read watch, reconnect resync reads every tracked and watched ref at
// once, and the TUI subscribes N+1 children on session entry — and 16 covers
// those bursts while still closing the formerly unbounded goroutine/params
// retention. A full cap blocks the worker's next slow-read dispatch — no wire
// error, no Unavailable — so later requests head-of-line wait for a read to
// finish; that wait is the design's second deliberate scheduling change.
const slowReadDispatchCap = 16

// concurrentRequestCap bounds how many requests one connection holds in
// flight in each pool a server's ConcurrentRequest names, apart from the slow
// reads' slots so neither kind can take the other's: the hub's forwarded
// reads wait on remote hosts, and a slow host must not cost thread reads
// their burst room. A stalled pool fills only its own slots. A full pool
// refuses the next one as Unavailable rather than parking the worker: these
// requests wait on something the hub does not bound (a remote host), so
// parking would wedge every request on the connection behind them, the very
// stall the pool exists to prevent. The
// refusal is one a read can simply retry. It is sized well past a pane's
// burst (a spawn form opened on a remote host sends about 7). The goroutines
// it bounds are cheap.
const concurrentRequestCap = 32

// concurrentRequestTotalCap bounds a connection's admitted requests across
// all its pools. Pool names come from the requests themselves (the hub's are
// the host names a client sends), so without it a client could hold any number
// of goroutines by naming a new pool each time. It leaves room for four
// stalled hosts' full pools.
const concurrentRequestTotalCap = 4 * concurrentRequestCap

// slowReadCapStallAdvisory is how long a single blocked slow-read acquire
// parks before the worker reports the wedged lane — the same scale as
// webSocketWriteTimeout. The ping bypass keeps a connection looking healthy
// while its serial lane is dead behind a saturated cap, so the wedge needs an
// operational signature even though the client sees a live heartbeat.
const slowReadCapStallAdvisory = 30 * time.Second
