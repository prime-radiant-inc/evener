# Retained thread subscription admission

A successful subscribed saved-history read retains the connection’s intent to
receive future transcript events and activity invalidations. Reading history
does not launch or resume a daemon. A separate connection may resume the source.

The hub resolves saved local delivery identity and lifecycle admission ownership
under its existing deletion fence. One captured retained entry supplies both
subscription identity and saved projection; an index rebuild cannot retarget the
hydration. Unknown targets keep their existing unavailable result without
speculative membership. It registers the existing hydration buffer
before materializing the bounded saved response. Disk projection runs outside
server projection gates. The existing response finalizer commits membership and
releases buffered events after the response; failed reads, server cancellation,
unsubscribe and closed connections withdraw the pending hydration.

Verify with independent WebSocket connections and real local daemon routing:
saved read before resume, resumed transcript and activity notifications, resume
during hydration, additive membership, explicit release, failed read and closure.
Do not add client polling, another recovery owner, or daemon launches for reads.
