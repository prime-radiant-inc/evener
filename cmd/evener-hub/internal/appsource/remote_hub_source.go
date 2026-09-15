package appsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/internal/appitempaging"
)

// RemoteHubClientFunc returns an attached, initialized AppWire client for the
// named remote host, attaching on first use. Component 04 supplies it.
type RemoteHubClientFunc func(ctx context.Context, host string) (*appwire.Client, error)

// RemoteHubSource exposes a remote evener hub as one more appsource.Source on
// the controller hub. Its ID is the host name from the controller's [[hosts]]
// config; every call is forwarded to the remote hub's AppWire edge over a
// single long-lived client, and every ref is translated between the
// controller's "<host>:<thread>" namespace and the remote hub's
// "local:<thread>" namespace.
//
// This is component 05a: the read path (ID, ListThreads, ReadThread, ListTurns,
// ListModels, and the item-mode paging seam) plus registration. Subscription
// fan-out (05b), turn/thread lifecycle mutations and mutation-unknown mapping
// (05c), and the capability probe (05d) are staged; their interface methods
// exist and fail loudly until then.
//
// The client is never cached here: every request re-invokes the connector, so a
// component-04 reconnect that swaps the underlying client is picked up
// automatically.
type RemoteHubSource struct {
	id string
	// roots is the host's configured [[hosts]].roots scoping. It is retained
	// for the staged 05b/05c/05d lifecycle, mutation and capability work, which
	// is the "advisory inputs to components 04/05" contract in
	// hubcore.HostConfig; the 05a read path does not narrow by root.
	roots  []string
	client RemoteHubClientFunc

	// itemPaging retains the opaque remote item cursor behind the
	// controller-owned cursor minted for it, mirroring the bounded local-daemon
	// snapshot: an evicted continuation degrades to a typed stale-cursor error
	// instead of leaking the remote hub's cursor identity into the controller.
	itemPaging remoteItemPagingCache

	// itemPagingLocks serializes the peek → remote I/O → put read-modify-write
	// for one remote thread, mirroring LocalDaemonSource.itemPagingLocks. Without
	// it, concurrent requests for the same thread interleave: one request's
	// RebaseCursor can be applied to another request's retained remote cursor and
	// replay a stale boundary under a newer incarnation. The key is the
	// controller-side ref, the same key the paging cache uses.
	itemPagingLocks keyedMutexRegistry
}

var (
	_ Source                  = (*RemoteHubSource)(nil)
	_ ItemCandidateSource     = (*RemoteHubSource)(nil)
	_ ItemReadCandidateSource = (*RemoteHubSource)(nil)
)

func NewRemoteHubSource(id string, roots []string, client RemoteHubClientFunc) *RemoteHubSource {
	return &RemoteHubSource{id: id, roots: roots, client: client}
}

func (s *RemoteHubSource) ID() string { return s.id }

// RelayOnThreadRead reports that a plain thread/read must not start a relay.
// SubscribeThread is staged until 05b, and the hub's default relay policy is
// true, so without this override every successful remote read would be
// discarded by startRelay's notImplemented SubscribeThread call. It is deleted
// when 05b wires a real subscription.
func (s *RemoteHubSource) RelayOnThreadRead() bool { return false }

// SupportsThreadRelay reports that this source has no relay fan-out at all, so
// the web client's subscribe:true first-hydration read is not relayed either;
// startRelay would call the notImplemented SubscribeThread and fail the read.
// It is deleted when 05b wires a real subscription.
func (s *RemoteHubSource) SupportsThreadRelay() bool { return false }

// EnrichThreadFileBackedImages reports that this source's threads name files on
// the remote host, not this one. The hub's file-backed output-image pass reads
// the thread's CWD on the local filesystem, so running it on a remote thread
// would probe controller-local paths named by remote data; the remote hub has
// already enriched its own replies.
func (s *RemoteHubSource) EnrichThreadFileBackedImages() bool { return false }

// call forwards one request over the current remote client and translates any
// refs in the response back into the controller namespace.
func (s *RemoteHubSource) call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return s.mapCallError(err)
	}
	client, err := s.client(ctx, s.id)
	if err != nil {
		return s.mapConnectError(err)
	}
	if err := client.Request(ctx, method, params, out); err != nil {
		return s.mapCallError(err)
	}
	return s.translateOut(out)
}

// mapCallError mirrors LocalDaemonSource's error shapes for a remote hub: a
// transport-level failure (dial, EOF, reset, closed, timeout) becomes
// SessionUnavailable so the hub's auto-resume gate can fire, while an
// application-level WireError carrying a semantic code is preserved exactly.
// A caller cancellation or deadline is the caller's own context expiring, not
// host unavailability, so it stays raw exactly as localDaemonCallError leaves
// it.
func (s *RemoteHubSource) mapCallError(err error) error {
	if err == nil {
		return nil
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return s.transportUnavailable(err)
	}
	if wire.Code != appwire.CodeInternalError {
		return err
	}
	if remoteHubTransportText(strings.ToLower(wire.Message)) {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + wire.Message)
	}
	return err
}

// mapConnectError mirrors localDaemonDialError for the attach step: a timeout
// or reset while opening the SSH channel is host unavailability, not a slow
// request, so it is classified before the request-level mapping applies.
func (s *RemoteHubSource) mapConnectError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[appwire.WireError](err); ok {
		return s.mapCallError(err)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return s.transportUnavailable(err)
}

// transportUnavailable maps a non-wire transport failure. Caller cancellation
// stays raw; every transport-shaped failure names the host so the fleet view
// and the auto-resume gate can attribute it.
func (s *RemoteHubSource) transportUnavailable(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	// Component 04's attach step returns sshconn's typed start failure for a host
	// it could not reach or bring up (spawn, initialize, or preflight). That is
	// host unavailability, so it maps like any other transient transport failure.
	// sshconn.ErrSSHAuth, ErrProtocolIncompatible, ErrUnsupportedHost, and the
	// other terminal classes are deliberately not matched: they name a host that
	// will never attach and must stay raw so recovery is not retried forever.
	if errors.Is(err, sshconn.ErrSSHStart) {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + err.Error())
	}
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded) {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + err.Error())
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + err.Error())
	}
	if remoteHubTransportText(strings.ToLower(err.Error())) {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + err.Error())
	}
	return err
}

// remoteHubTransportText recognizes transport-shaped error text. "eof" is
// matched as a standalone token only so an application message that merely
// contains those letters is not reclassified as host unavailability.
func remoteHubTransportText(lower string) bool {
	switch {
	case containsWord(lower, "eof"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "broken pipe"),
		strings.Contains(lower, "use of closed network connection"),
		strings.Contains(lower, "i/o timeout"):
		return true
	default:
		return false
	}
}

// containsWord reports whether text contains word delimited by non-word bytes.
func containsWord(text, word string) bool {
	for offset := 0; ; {
		index := strings.Index(text[offset:], word)
		if index < 0 {
			return false
		}
		index += offset
		end := index + len(word)
		if (index == 0 || !isWordByte(text[index-1])) && (end == len(text) || !isWordByte(text[end])) {
			return true
		}
		offset = index + 1
	}
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

func (s *RemoteHubSource) ListThreads(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	// The controller selected only other sources. remapRemoteSourceIDs would
	// drop every entry and forward a request with no filter at all, so an
	// explicit exclusion must never widen into an unfiltered list.
	if len(params.SourceIDs) > 0 && !slices.Contains(params.SourceIDs, s.id) {
		return appwire.ThreadListResponse{}, nil
	}
	remote := params
	// Only this hub's own namespace is representable in a controller ref, so an
	// unfiltered controller list is scoped to it: forwarding no filter lets a
	// nested remote hub return its own remote refs, which translateOut refuses
	// and which would otherwise abort the whole response.
	remote.SourceIDs = remapRemoteSourceIDs(s.id, params.SourceIDs)
	if len(remote.SourceIDs) == 0 {
		remote.SourceIDs = []string{remoteHubNamespace}
	}
	var out appwire.ThreadListResponse
	if err := s.call(ctx, appwire.MethodThreadList, remote, &out); err != nil {
		return appwire.ThreadListResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) ReadThread(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	remote = stripRemoteSubscription(remote)
	var out appwire.ThreadReadResponse
	if err := s.call(ctx, appwire.MethodThreadRead, remote, &out); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	return out, nil
}

// stripRemoteSubscription removes the controller's subscription intent from a
// remote read. Subscription fan-out is staged until 05b (SubscribeThread is not
// implemented), so forwarding Subscribe would make the remote hub register a
// persistent subscription the controller can never manage or retire — an
// orphan that outlives the read. The controller-side relay gate is
// RelayOnThreadRead; this only keeps the intent off the wire.
func stripRemoteSubscription(params appwire.ThreadReadParams) appwire.ThreadReadParams {
	params.Subscribe = false
	params.ReplaceSubscription = false
	return params
}

func (s *RemoteHubSource) ListTurns(ctx context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	var out appwire.ThreadTurnsListResponse
	if err := s.call(ctx, appwire.MethodThreadTurnsList, remote, &out); err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) ListModels(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	var out appwire.ModelListResponse
	if err := s.call(ctx, appwire.MethodModelList, params, &out); err != nil {
		return appwire.ModelListResponse{}, err
	}
	return out, nil
}

// remoteHubItemCursorProjectionVersion identifies cursor identities minted by
// RemoteHubSource. It is independent of the remote hub's own projection fence.
const remoteHubItemCursorProjectionVersion uint16 = 1

var remoteHubItemIncarnationSequence atomic.Uint64

// remoteItemPagingState retains the remote hub's opaque item cursor behind a
// controller-owned cursor, keyed by the controller ref that cursor names.
type remoteItemPagingState struct {
	identity appitempaging.CursorIdentity
	native   string
	// candidates is the chronological window observed under identity: every page
	// fetched for this continuation, merged. It is more than the page just
	// fetched because the remote hub returns only a tail page per request, so a
	// complete page (native == "") is the oldest page, not the whole transcript.
	// Retaining the merged window lets the hub's size packer mint a continuation
	// from identity AND lets any boundary observed under identity be replayed
	// locally, instead of failing ValidateCursorBoundary against the tail alone.
	candidates []appitempaging.TranscriptItemCandidate
	// complete marks a page the remote hub returned in full (it named no older
	// cursor).
	complete bool
	// head is the newest position observed for this thread at the last fresh
	// page, so a later fresh page whose newest position precedes it is
	// recognized as a divergent transcript and the incarnation rotates.
	head    appwire.ThreadItemPosition
	hasHead bool
}

// remoteItemPagingCapacity bounds retained continuations. An evicted entry
// turns its outstanding cursor into a typed stale error, exactly like the
// bounded local-daemon item snapshot cache.
const remoteItemPagingCapacity = 64

// remoteItemPagingCandidateCapacity and remoteItemPagingByteCapacity bound one
// continuation's retained candidate window. Every page is merged into the
// retained window so a complete page's earlier boundaries stay answerable, so
// an unbounded window would grow to the whole transcript and to every tool
// output in it.
const (
	remoteItemPagingCandidateCapacity = 1024
	remoteItemPagingByteCapacity      = 8 << 20
)

type remoteItemPagingCache struct {
	mu      sync.Mutex
	entries map[string]remoteItemPagingState
	order   []string
}

func (c *remoteItemPagingCache) put(key string, state remoteItemPagingState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]remoteItemPagingState)
	}
	// Recency is stamped on commit, matching itemSnapshotStateCache's
	// MoveToFront-on-put: a re-put of an already retained key moves it to the
	// back so eviction reclaims the least recently committed continuation, not
	// the one an actively paging thread just advanced.
	if index := slices.Index(c.order, key); index >= 0 {
		c.order = append(c.order[:index], c.order[index+1:]...)
	}
	c.order = append(c.order, key)
	c.entries[key] = state
	for len(c.order) > remoteItemPagingCapacity {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func (c *remoteItemPagingCache) peek(key string) (remoteItemPagingState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.entries[key]
	return state, ok
}

// remoteItemPagingKey is the controller-side ref that names one remote thread
// in the controller namespace; it identifies both the retained continuation
// and the identity fence minted into the controller's cursor.
func remoteItemPagingKey(sourceID, threadID string) string {
	return appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
}

// ListItemCandidates serves the controller's item-mode turn page for a remote
// thread. The remote hub's opaque cursor never reaches the controller: each
// page mints (or continues) a controller-owned identity, and the remote cursor
// is retained behind it.
func (s *RemoteHubSource) ListItemCandidates(ctx context.Context, params appwire.ThreadTurnsListParams) (ItemCandidateResult, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	key := remoteItemPagingKey(s.id, ref.ThreadID)
	// Hold the per-thread paging lock from the retained-state peek through the
	// remote page and its put, exactly as LocalDaemonSource does: the retained
	// remote cursor and its controller identity are one read-modify-write unit.
	unlock := s.itemPagingLocks.lock(key)
	defer unlock()
	itemLimit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	remote.ItemLimit = itemLimit

	if params.Cursor == "" {
		remote.Cursor = ""
		candidates, native, err := s.remoteItemPage(ctx, remote)
		if err != nil {
			return ItemCandidateResult{}, err
		}
		identity, head, hasHead := s.remoteItemPageIdentity(key, candidates)
		return s.recordRemoteItemPage(key, identity, candidates, native, head, hasHead)
	}

	state, ok := s.itemPaging.peek(key)
	if !ok {
		return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
	}
	if state.complete {
		return continueCompleteRemoteItemPage(params.Cursor, state, itemLimit)
	}
	before, err := appitempaging.DecodeCursor(params.Cursor, state.identity)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	// The decoded boundary is a fence, not free-form input: it must still name an
	// item the retained window observed before it is rebased and forwarded,
	// otherwise a caller that keeps a valid identity but moves the boundary would
	// page the remote from a position this incarnation never proved.
	if err := appitempaging.ValidateCursorBoundary(state.candidates, before); err != nil {
		return ItemCandidateResult{}, err
	}
	// Packing can drop the oldest selected item after the cursor is minted, so
	// the caller's boundary may be newer than the retained page boundary; the
	// retained remote cursor is rebased onto the caller's boundary before it is
	// replayed against the remote hub.
	native, err := appitempaging.RebaseCursor(state.native, before)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	remote.Cursor = native
	candidates, next, err := s.remoteItemPage(ctx, remote)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	// The requested boundary was validated under state.identity, so a page that
	// contradicts the retained window means the transcript was rewritten after
	// the cursor was minted. Answering under a rotated identity would splice old
	// and new history together, so the continuation fails closed instead.
	if _, compatible := remoteMergeCandidates(state.candidates, candidates); !compatible {
		return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
	}
	return s.recordRemoteItemPage(key, state.identity, candidates, next, state.head, state.hasHead)
}

// ReadItemCandidates materializes a remote item-mode thread read into the
// private candidate contract. It issues its own read and mints the same
// identity ItemCandidatesFromRead does.
func (s *RemoteHubSource) ReadItemCandidates(ctx context.Context, params appwire.ThreadReadParams) (ItemCandidateResult, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	remote.IncludeTurns = true
	remote = stripRemoteSubscription(remote)
	var out appwire.ThreadReadResponse
	if err := s.call(ctx, appwire.MethodThreadRead, remote, &out); err != nil {
		return ItemCandidateResult{}, err
	}
	return s.ItemCandidatesFromRead(ctx, params, out)
}

// ItemCandidatesFromRead converts an already-materialized remote item read
// into the controller's candidate contract without issuing another read, so
// the cursor minted from thread/read stays continuable through
// thread/turns/list.
func (s *RemoteHubSource) ItemCandidatesFromRead(ctx context.Context, params appwire.ThreadReadParams, response appwire.ThreadReadResponse) (ItemCandidateResult, error) {
	if err := ctx.Err(); err != nil {
		return ItemCandidateResult{}, err
	}
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	key := remoteItemPagingKey(s.id, ref.ThreadID)
	// The read is already materialized, but minting its controller identity and
	// replacing the retained cursor is the same read-modify-write unit as a
	// paged continuation, so it takes the same per-thread lock.
	unlock := s.itemPagingLocks.lock(key)
	defer unlock()
	candidates, err := appitempaging.CandidatesFromTurns(response.Thread.Turns)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	identity, head, hasHead := s.remoteItemPageIdentity(key, candidates)
	return s.recordRemoteItemPage(key, identity, candidates, response.OlderCursor, head, hasHead)
}

// remoteItemPage issues one remote item-mode turn page and returns its
// positioned candidates and the remote cursor for the next older page.
func (s *RemoteHubSource) remoteItemPage(ctx context.Context, remote appwire.ThreadTurnsListParams) ([]appitempaging.TranscriptItemCandidate, string, error) {
	var out appwire.ThreadTurnsListResponse
	if err := s.call(ctx, appwire.MethodThreadTurnsList, remote, &out); err != nil {
		return nil, "", err
	}
	candidates, err := appitempaging.CandidatesFromTurns(out.Data)
	if err != nil {
		return nil, "", err
	}
	return candidates, out.NextCursor, nil
}

// continueCompleteRemoteItemPage answers a continuation of a page the remote hub
// returned in full. No older remote page exists, so the retained candidate
// snapshot is re-served locally before the caller's boundary, exactly as the
// local daemon source does for a complete snapshot. The remote hub is not
// called; the same identity and snapshot keep further continuations answerable.
func continueCompleteRemoteItemPage(cursor string, state remoteItemPagingState, itemLimit int) (ItemCandidateResult, error) {
	before, err := appitempaging.DecodeCursor(cursor, state.identity)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	selected, hasOlder, err := appitempaging.SelectCandidates(state.candidates, &before, itemLimit)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	if len(selected) == 0 {
		return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
	}
	window := appitempaging.TranscriptItemWindow{Candidates: selected}
	if hasOlder {
		window.OlderCursor, err = appitempaging.EncodeCursor(state.identity, selected[0].Position)
		if err != nil {
			return ItemCandidateResult{}, err
		}
	}
	return ItemCandidateResult{Candidates: window, Identity: state.identity, Exhausted: !hasOlder}, nil
}

// remoteItemPageHead returns the newest position of an item page, which is the
// last candidate of the strictly chronological window.
func remoteItemPageHead(candidates []appitempaging.TranscriptItemCandidate) (appwire.ThreadItemPosition, bool) {
	if len(candidates) == 0 {
		return appwire.ThreadItemPosition{}, false
	}
	return candidates[len(candidates)-1].Position, true
}

// remotePositionCompare orders two positions the way the paging package does:
// negative when a is older, zero when equal, positive when newer.
func remotePositionCompare(a, b appwire.ThreadItemPosition) int {
	if a.Entry < b.Entry || (a.Entry == b.Entry && a.Item < b.Item) {
		return -1
	}
	if a == b {
		return 0
	}
	return 1
}

// remoteItemPageIdentity chooses the controller-owned identity for a fresh
// remote page. The incarnation is reused while the page's newest position is
// not older than the retained one AND the page does not contradict the retained
// window: an untouched or appended transcript keeps outstanding cursors valid,
// so a re-read between a page and a scroll no longer invalidates them. A page
// that starts before the retained head, or that re-reports an observed position
// with a different item, is a rewrite, so the incarnation rotates and
// outstanding cursors fail closed. The remote hub stays the authority for its
// own opaque cursor; this fence only stops the controller's cursor namespace
// from churning on every compatible read.
func (s *RemoteHubSource) remoteItemPageIdentity(key string, candidates []appitempaging.TranscriptItemCandidate) (appitempaging.CursorIdentity, appwire.ThreadItemPosition, bool) {
	head, hasHead := remoteItemPageHead(candidates)
	if !hasHead {
		return s.mintRemoteItemIdentity(key), appwire.ThreadItemPosition{}, false
	}
	if state, ok := s.itemPaging.peek(key); ok && state.hasHead && remotePositionCompare(head, state.head) >= 0 {
		if _, compatible := remoteMergeCandidates(state.candidates, candidates); compatible {
			return state.identity, head, true
		}
	}
	return s.mintRemoteItemIdentity(key), head, true
}

// remoteMergeCandidates folds a newly observed page into the retained window,
// keeping it chronological. It reports ok=false when a position observed before
// now carries a different fingerprint, i.e. the item at that position was
// replaced and the transcript rewritten under this incarnation. Items are keyed
// by position: an item-mode projection positions every item uniquely, and a
// replacement at an observed position must be recognized rather than silently
// appended at the same boundary.
//
// A forward page (one that shares no position with the retained window and lies
// entirely newer than it) is only unioned when it abuts the retained newest
// item. A forward page that leaves unobserved positions between the two windows
// would retain a holed window, and a later page that reaches the beginning of
// the transcript would mark that hole complete. A backward page is not gated
// here: it is fetched through the retained remote cursor, which is the remote
// hub's own authority for the next older page.
func remoteMergeCandidates(retained, observed []appitempaging.TranscriptItemCandidate) ([]appitempaging.TranscriptItemCandidate, bool) {
	if len(observed) == 0 {
		return retained, true
	}
	if len(retained) == 0 {
		return append([]appitempaging.TranscriptItemCandidate(nil), observed...), true
	}
	byPosition := make(map[appwire.ThreadItemPosition]appitempaging.TranscriptItemCandidate, len(retained))
	for _, candidate := range retained {
		byPosition[candidate.Position] = candidate
	}
	merged := append([]appitempaging.TranscriptItemCandidate(nil), retained...)
	overlap := false
	for _, candidate := range observed {
		if previous, ok := byPosition[candidate.Position]; ok {
			if transcriptItemFingerprint(previous) != transcriptItemFingerprint(candidate) {
				return nil, false
			}
			overlap = true
			continue
		}
		byPosition[candidate.Position] = candidate
		merged = append(merged, candidate)
	}
	if !overlap {
		retainedNewest := retained[len(retained)-1].Position
		observedOldest := observed[0].Position
		if remotePositionCompare(retainedNewest, observedOldest) < 0 && !remotePositionsAdjacent(retainedNewest, observedOldest) {
			return nil, false
		}
	}
	slices.SortFunc(merged, func(a, b appitempaging.TranscriptItemCandidate) int {
		return remotePositionCompare(a.Position, b.Position)
	})
	return merged, true
}

// remotePositionsAdjacent reports whether newer is the position immediately
// after older. Item-mode positions are (entry, item) pairs, so the successor of
// an item is the next item of the same entry or the first item of the next
// entry.
func remotePositionsAdjacent(older, newer appwire.ThreadItemPosition) bool {
	if older.Entry == newer.Entry {
		return newer.Item == older.Item+1
	}
	return newer.Entry == older.Entry+1
}

// remoteRetainedCandidatesExceedBounds reports whether a merged retained window
// exceeds either retention bound, so the caller rotates the incarnation instead
// of accumulating an unbounded window.
func remoteRetainedCandidatesExceedBounds(candidates []appitempaging.TranscriptItemCandidate) bool {
	return len(candidates) > remoteItemPagingCandidateCapacity || remoteItemCandidatesBytes(candidates) > remoteItemPagingByteCapacity
}

// remoteItemCandidatesBytes approximates the retained window's transcript
// payload so its memory can be bounded by bytes as well as by item count.
func remoteItemCandidatesBytes(candidates []appitempaging.TranscriptItemCandidate) int {
	total := 0
	for _, candidate := range candidates {
		item := candidate.Item
		total += len(item.Text) + len(item.Delta) + len(item.ArgumentsJSON) + len(item.Output) + len(item.Error) + len(item.Description) + len(item.Raw)
	}
	return total
}

// recordRemoteItemPage builds the controller window for one remote page and
// retains the remote cursor behind the controller identity.
func (s *RemoteHubSource) recordRemoteItemPage(
	key string,
	identity appitempaging.CursorIdentity,
	candidates []appitempaging.TranscriptItemCandidate,
	native string,
	head appwire.ThreadItemPosition,
	hasHead bool,
) (ItemCandidateResult, error) {
	retained := []appitempaging.TranscriptItemCandidate(nil)
	if previous, ok := s.itemPaging.peek(key); ok && previous.identity == identity {
		retained = previous.candidates
	}
	merged, compatible := remoteMergeCandidates(retained, candidates)
	if !compatible || remoteRetainedCandidatesExceedBounds(merged) {
		// The fresh page contradicts the retained window, or the union would
		// exceed the retained bound, so the accumulated history cannot be served
		// soundly: rotate the incarnation and retain only this page. The cursor
		// minted below stays live; boundaries older than the bounded window fail
		// closed as stale rather than being answered from a truncated history.
		identity = s.mintRemoteItemIdentity(key)
		merged = append([]appitempaging.TranscriptItemCandidate(nil), candidates...)
		head, hasHead = remoteItemPageHead(merged)
	}
	window := appitempaging.TranscriptItemWindow{Candidates: candidates}
	state := remoteItemPagingState{identity: identity, native: native, candidates: merged, complete: native == "", head: head, hasHead: hasHead}
	if native == "" {
		// The identity is returned even when the page is complete: packing can
		// still drop the oldest item for size and needs an identity to mint a
		// continuation cursor from, exactly as the local-daemon source does. The
		// merged window is retained so that continuation, and every boundary
		// observed under this identity, can be served locally.
		s.itemPaging.put(key, state)
		return ItemCandidateResult{Candidates: window, Identity: identity, Exhausted: true}, nil
	}
	if len(candidates) == 0 {
		return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
	}
	cursor, err := appitempaging.EncodeCursor(identity, candidates[0].Position)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	window.OlderCursor = cursor
	s.itemPaging.put(key, state)
	return ItemCandidateResult{Candidates: window, Identity: identity, Exhausted: false}, nil
}

func (s *RemoteHubSource) mintRemoteItemIdentity(key string) appitempaging.CursorIdentity {
	return appitempaging.CursorIdentity{
		ThreadRef:         key,
		Incarnation:       fmt.Sprintf("remote-hub-incarnation-%d", remoteHubItemIncarnationSequence.Add(1)),
		ProjectionVersion: remoteHubItemCursorProjectionVersion,
	}
}

// notImplemented is the staged-method error for interface methods 05b/05c/05d
// will fill in. It is deliberately loud and names the Go method so a wiring
// mistake surfaces instead of silently degrading.
func (s *RemoteHubSource) notImplemented(method string) error {
	return appwire.InternalError(fmt.Sprintf("remote hub source: %s is not implemented yet", method))
}

func (s *RemoteHubSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{}, s.notImplemented("StartThread")
}

func (s *RemoteHubSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{}, s.notImplemented("ResumeThread")
}

func (s *RemoteHubSource) ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	return appwire.ThreadForkResponse{}, s.notImplemented("ForkThread")
}

func (s *RemoteHubSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	return appwire.TurnStartResponse{}, s.notImplemented("StartTurn")
}

func (s *RemoteHubSource) SteerTurn(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	return appwire.TurnSteerResponse{}, s.notImplemented("SteerTurn")
}

func (s *RemoteHubSource) ResolveSandboxEscalation(context.Context, appwire.SandboxEscalationResolveParams) error {
	return s.notImplemented("ResolveSandboxEscalation")
}

func (s *RemoteHubSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	return appwire.TurnInterruptResponse{}, s.notImplemented("InterruptTurn")
}

func (s *RemoteHubSource) QueueTurn(context.Context, appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	return appwire.TurnQueueResponse{}, s.notImplemented("QueueTurn")
}

func (s *RemoteHubSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	return appwire.TurnDrainAsSteerResponse{}, s.notImplemented("DrainAsSteer")
}

func (s *RemoteHubSource) PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	return appwire.TurnPromoteQueuedAsSteerResponse{}, s.notImplemented("PromoteQueuedAsSteer")
}

func (s *RemoteHubSource) CancelQueued(context.Context, appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	return appwire.TurnCancelQueuedResponse{}, s.notImplemented("CancelQueued")
}

func (s *RemoteHubSource) CompactThread(context.Context, appwire.ThreadCompactStartParams) error {
	return s.notImplemented("CompactThread")
}

func (s *RemoteHubSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	return s.notImplemented("ShutdownThread")
}

func (s *RemoteHubSource) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	return s.notImplemented("SetThreadModel")
}

func (s *RemoteHubSource) SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error {
	return s.notImplemented("SetThreadReasoningEffort")
}

func (s *RemoteHubSource) SetThreadVisionModel(context.Context, appwire.ThreadVisionModelSetParams) error {
	return s.notImplemented("SetThreadVisionModel")
}

func (s *RemoteHubSource) SetThreadName(context.Context, appwire.ThreadNameSetParams) error {
	return s.notImplemented("SetThreadName")
}

func (s *RemoteHubSource) GoalSet(context.Context, appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	return appwire.GoalSetResponse{}, s.notImplemented("GoalSet")
}

func (s *RemoteHubSource) NotesHumanSet(context.Context, appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
	return appwire.NotesHumanSetResponse{}, s.notImplemented("NotesHumanSet")
}

func (s *RemoteHubSource) UrlsRemove(context.Context, appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	return appwire.UrlsRemoveResponse{}, s.notImplemented("UrlsRemove")
}

func (s *RemoteHubSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, s.notImplemented("ClearThread")
}

func (s *RemoteHubSource) ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	return appwire.TaskListResponse{}, s.notImplemented("ListTasks")
}

func (s *RemoteHubSource) ListJobs(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error) {
	return appwire.JobsListResponse{}, s.notImplemented("ListJobs")
}

func (s *RemoteHubSource) JobOutput(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	return appwire.JobsOutputResponse{}, s.notImplemented("JobOutput")
}

func (s *RemoteHubSource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	return nil, s.notImplemented("SubscribeThread")
}
