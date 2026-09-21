package appsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

type LocalDaemonSource struct {
	sourceID string
	entries  func() []LocalDaemonEntry
	client   *http.Client
	dial     appwireDialFunc

	itemPagingLocks keyedMutexRegistry
	itemSnapshots   *itemSnapshotStateCache

	callsMu sync.Mutex
	calls   map[daemonIdentity]*daemonCalls

	relayMu       sync.Mutex
	relaySessions map[string]*relaySession
	legacyRelays  map[string]legacyRelayRead
}

var _ RelaySessionSource = (*LocalDaemonSource)(nil)
var _ RelaySessionRoutePublicationLease = (*relaySessionLease)(nil)

type legacyRelayRead struct {
	lease   RelaySessionLease
	handoff RelayHandoff
	stop    func() bool
}

type LocalDaemonEntry struct {
	Entry     rendezvous.Entry
	SessionID string
	Status    string
	// ReadOnlyAlias addresses an in-process descendant through its owner's
	// daemon. It is valid for reads/subscriptions only; mutation methods must not
	// accidentally apply a child-targeted request to the owning root session.
	ReadOnlyAlias bool
	// OwnerSessionID is the owning root session's ID, set only when
	// ReadOnlyAlias is true. threadFromEntry uses it to populate
	// Evener.ParentRef so hub views can trace a subagent back to its root
	// (ledger #112).
	OwnerSessionID string
	// PendingAsk mirrors hubcore.LiveEntry.PendingAsk — true while the daemon
	// reports an unanswered ask_user question. threadFromEntry carries it into
	// appwire.EvenerThread.AskPending so the TUI's per-row ask marker (Task 29)
	// sees it when attaching through the hub.
	PendingAsk bool
	// PendingEscalation mirrors hubcore.LiveEntry.PendingEscalation — true
	// while the daemon reports a blocked sandbox-exemption escalation. The
	// unprobed fallback folds it out of Clear, matching the daemon's own
	// clear gate (clearBlockedReasonLocked's unresolved-approval-work branch).
	PendingEscalation bool
	// RunningJobs carries the roster's non-terminal, non-agent work into the
	// typed thread diagnostics consumed by hub and TUI status views.
	RunningJobs []appwire.EvenerJobInfo
	// CompletedJobs carries the recent terminal non-agent jobs into the same
	// typed diagnostics snapshot for local compatibility consumers.
	CompletedJobs []appwire.EvenerJobInfo
	// Watches carries the roster's live watches into the same typed thread
	// diagnostics the hub navigation path already projects, so local AppWire
	// clients see watch state too. A root entry holds the root session's own
	// watches; a read-only descendant alias holds the child's own watches,
	// copied from LiveEntry.ChildWatches by the hub entry builder. threadFromEntry
	// emits a watch-only diagnostics block for such an alias even though its
	// other diagnostics (jobs) stay suppressed.
	Watches []appwire.EvenerWatchInfo
	// Capabilities carries the daemon's own Evener capability set when the
	// roster's probe (or a spawned daemon's confirming read) captured one.
	// threadFromEntry then mirrors the daemon's answer instead of the fallback
	// approximation, so a list row agrees with the ThreadRead of the same
	// session (#1840). CapabilitiesKnown false means no answer was captured
	// and the fallback takes over.
	Capabilities      appwire.ThreadCapabilities
	CapabilitiesKnown bool
}

func NewLocalDaemonSource(sourceID string, entries func() []rendezvous.Entry, client *http.Client) *LocalDaemonSource {
	return NewLocalDaemonSourceWithEntries(sourceID, func() []LocalDaemonEntry {
		if entries == nil {
			return nil
		}
		raw := entries()
		out := make([]LocalDaemonEntry, 0, len(raw))
		for _, entry := range raw {
			out = append(out, LocalDaemonEntry{Entry: entry})
		}
		return out
	}, client)
}

// AnnounceDaemonGone tells the subscribers of the daemon that wrote entry that
// it has left for good: the roster saw its process or its rendezvous file go.
// The daemon's own close frame is not guaranteed to reach them (it may be
// revoked with the connection), and nothing else would. A daemon nobody is
// relaying has nobody to tell. sessionID is the session the roster resolved
// for the entry - a legacy entry names none of its own, and its relay session
// is keyed by the resolved one.
func (s *LocalDaemonSource) AnnounceDaemonGone(entry rendezvous.Entry, sessionID string) {
	if sessionID != "" {
		entry.SessionID = sessionID
	}
	ref, err := s.relaySessionRef(entry)
	if err != nil {
		return
	}
	s.relayMu.Lock()
	session := s.relaySessions[ref.String()]
	s.relayMu.Unlock()
	if session != nil {
		session.publishDaemonGoneResync()
	}
}

func NewLocalDaemonSourceWithEntries(sourceID string, entries func() []LocalDaemonEntry, client *http.Client) *LocalDaemonSource {
	if sourceID == "" {
		sourceID = "local"
	}
	return &LocalDaemonSource{
		sourceID:      sourceID,
		entries:       entries,
		client:        client,
		dial:          defaultAppwireDial,
		itemSnapshots: newItemSnapshotStateCache(defaultItemSnapshotStateEntries),
		relaySessions: map[string]*relaySession{},
		legacyRelays:  map[string]legacyRelayRead{},
	}
}

func (s *LocalDaemonSource) ID() string {
	return s.sourceID
}

func (s *LocalDaemonSource) ResolveRelaySession(params appwire.ThreadReadParams) (appwire.Ref, error) {
	canonical, _, err := s.ResolveRelaySessionWithAdmission(params)
	return canonical, err
}

// ResolveRelaySessionWithAdmission derives both identities from one inventory
// entry. A descendant owns its admission even when it shares the root relay.
func (s *LocalDaemonSource) ResolveRelaySessionWithAdmission(params appwire.ThreadReadParams) (appwire.Ref, string, error) {
	item, err := s.localEntryForRefMode(params.Ref, params.ThreadID, true)
	if err != nil {
		return appwire.Ref{}, "", err
	}
	canonical, err := s.relaySessionRef(localDaemonRendezvousEntry(item))
	if err != nil {
		return appwire.Ref{}, "", err
	}
	owner := canonical.String()
	if item.ReadOnlyAlias {
		owner = (appwire.Ref{SourceID: s.sourceID, ThreadID: localDaemonThreadID(item)}).String()
	}
	return canonical, owner, nil
}

// ResolveSubscriptionAdmission preserves stable/current root aliases while
// keeping read-only descendants distinct from their shared relay session.
func (s *LocalDaemonSource) ResolveSubscriptionAdmission(params appwire.ThreadReadParams) (appwire.Ref, error) {
	_, owner, err := s.ResolveRelaySessionWithAdmission(params)
	if err != nil {
		return appwire.Ref{}, err
	}
	return appwire.ParseRef(owner)
}

// relayEntry may refresh an endpoint, but a canonical key cannot fall back to
// a newly reassociated current-ID alias with a different semantic owner.
func (s *LocalDaemonSource) relayEntry(ref appwire.Ref) (rendezvous.Entry, error) {
	entry, err := s.entryForReadRef(ref.String(), "")
	if err != nil {
		return rendezvous.Entry{}, err
	}
	canonical, err := s.relaySessionRef(entry)
	if err != nil {
		return rendezvous.Entry{}, err
	}
	if canonical != ref {
		return rendezvous.Entry{}, appwire.SessionUnavailable("relay session identity changed")
	}
	return entry, nil
}

func (s *LocalDaemonSource) relaySessionRef(entry rendezvous.Entry) (appwire.Ref, error) {
	threadID := entry.ThreadID
	if entry.SessionID != "" {
		threadID = entry.SessionID
	}
	ref, err := appwire.ParseRef(localDaemonWorkspaceRef(s.sourceID, entry, threadID))
	if err != nil {
		return appwire.Ref{}, appwire.SessionUnavailable("local daemon returned an empty workspace ref")
	}
	return ref, nil
}

func (s *LocalDaemonSource) AcquireRelaySession(ref appwire.Ref) (RelaySessionRoutePublicationLease, error) {
	if ref.SourceID != s.sourceID || ref.String() == "" {
		return nil, appwire.SessionUnavailable("invalid relay session ref")
	}
	_, err := s.relayEntry(ref)
	if err != nil {
		return nil, err
	}
	key := ref.String()

	s.relayMu.Lock()
	session := s.relaySessions[key]
	if session != nil {
		if lease := session.acquire(); lease != nil {
			s.relayMu.Unlock()
			return lease, nil
		}
		delete(s.relaySessions, key)
		session = nil
	}
	if session == nil {
		created := newRelaySession(
			func(ctx context.Context, epoch uint64, observe func(uint64, appwire.Message, error)) (*appwire.Client, appwire.Transport, error) {
				currentEntry, resolveErr := s.relayEntry(ref)
				if resolveErr != nil {
					return nil, nil, resolveErr
				}
				transport, dialErr := s.dial(ctx, currentEntry.Endpoint, s.client, daemonAuthHeader(currentEntry.HubToken))
				if dialErr != nil {
					if callerErr := ctx.Err(); callerErr != nil {
						return nil, nil, callerErr
					}
					return nil, nil, localDaemonDialError(dialErr)
				}
				client := appwire.NewClient(transport)
				client.SetLogf(hubConnectionLogf)
				client.SetOrderedFrameHandler(func(message appwire.Message, err error) {
					observe(epoch, message, err)
				})
				client.Start(ctx)
				if _, initializeErr := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "evener-hub"}}); initializeErr != nil {
					_ = transport.Close()
					if callerErr := ctx.Err(); callerErr != nil {
						return nil, nil, callerErr
					}
					return nil, nil, localDaemonInitializeError(initializeErr)
				}
				return client, transport, nil
			},
			func(idle *relaySession) {
				s.relayMu.Lock()
				if s.relaySessions[key] == idle {
					delete(s.relaySessions, key)
				}
				s.relayMu.Unlock()
			},
		)
		session = created
		s.relaySessions[key] = session
	}
	lease := session.acquire()
	s.relayMu.Unlock()
	if lease == nil {
		return nil, appwire.SessionUnavailable("relay session retired during acquisition")
	}
	return lease, nil
}

// acquireRelaySession preserves the old parameter-based source call sites while
// keeping canonical resolution separate from relay session acquisition.
func (s *LocalDaemonSource) acquireRelaySession(params appwire.ThreadReadParams) (RelaySessionLease, error) {
	ref, err := s.ResolveRelaySession(params)
	if err != nil {
		return nil, err
	}
	return s.AcquireRelaySession(ref)
}

func (s *LocalDaemonSource) ListThreads(ctx context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	if err := ctx.Err(); err != nil {
		return appwire.ThreadListResponse{}, err
	}
	out := appwire.ThreadListResponse{}
	for _, entry := range s.listedEntries() {
		if err := ctx.Err(); err != nil {
			return appwire.ThreadListResponse{}, err
		}
		out.Data = append(out.Data, s.threadFromEntry(entry))
	}
	if err := ctx.Err(); err != nil {
		return appwire.ThreadListResponse{}, err
	}
	sort.SliceStable(out.Data, func(i, j int) bool {
		return localThreadLess(out.Data[i], out.Data[j])
	})
	if err := ctx.Err(); err != nil {
		return appwire.ThreadListResponse{}, err
	}
	return out, nil
}

func (s *LocalDaemonSource) ReadThread(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if params.Subscribe {
		lease, err := s.acquireRelaySession(params)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		result, err := lease.Read(ctx, params)
		if err != nil {
			lease.Close()
			return appwire.ThreadReadResponse{}, err
		}
		key := result.Response.Thread.Evener.Ref
		if key == "" {
			key = relaySessionKey(s.sourceID, params.Ref, params.ThreadID)
		}
		s.relayMu.Lock()
		previous := s.legacyRelays[key]
		pending := legacyRelayRead{lease: lease, handoff: result.Handoff}
		pending.stop = context.AfterFunc(ctx, func() {
			s.relayMu.Lock()
			current := s.legacyRelays[key]
			if current.lease == lease {
				delete(s.legacyRelays, key)
			}
			s.relayMu.Unlock()
			if current.lease == lease {
				current.handoff.Abort()
				current.lease.Close()
			}
		})
		s.legacyRelays[key] = pending
		s.relayMu.Unlock()
		if previous.handoff != nil {
			if previous.stop != nil {
				previous.stop()
			}
			previous.handoff.Abort()
			previous.lease.Close()
		}
		return result.Response, nil
	}
	entry, err := s.entryForReadRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	return s.ReadThreadAtEntry(ctx, entry, params)
}

// ReadThreadAtEntry reads an exact daemon endpoint, including before roster
// admission, within the source's recovery cancellation scope.
func (s *LocalDaemonSource) ReadThreadAtEntry(ctx context.Context, entry rendezvous.Entry, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	var out appwire.ThreadReadResponse
	err := s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.ThreadRead(ctx, params)
		return callErr
	})
	return out, err
}

// RetireDaemonAtEntry forwards a safe-retire request to an exact daemon
// endpoint within the source's recovery cancellation scope. The hub has
// already validated and locked this exact entry; the params — including the
// rendered ownership identity — pass through verbatim, and the daemon's
// answer (fresh blocker refusal or accepted claim) passes back untransformed.
func (s *LocalDaemonSource) RetireDaemonAtEntry(ctx context.Context, entry rendezvous.Entry, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
	var out appwire.DaemonRetireResponse
	err := s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.DaemonRetire(ctx, params)
		return callErr
	})
	return out, err
}

// SetDaemonIdleTimeoutAtEntry forwards an idle-deadline change to an exact
// daemon endpoint within the source's recovery cancellation scope. The hub has
// already resolved this entry from its roster; the params — including the
// rendered ownership identity — pass through verbatim, as do the daemon's
// answer and errors.
func (s *LocalDaemonSource) SetDaemonIdleTimeoutAtEntry(ctx context.Context, entry rendezvous.Entry, params appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error) {
	var out appwire.DaemonIdleTimeoutSetResponse
	err := s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.DaemonIdleTimeoutSet(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) ListTurns(ctx context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	entry, err := s.entryForReadRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	var out appwire.ThreadTurnsListResponse
	err = s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.ThreadTurnsList(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{}, appwire.Unavailable("local daemon source cannot start threads directly")
}

func (s *LocalDaemonSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{}, appwire.Unavailable("local daemon source cannot resume threads directly")
}

func (s *LocalDaemonSource) ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	return appwire.ThreadForkResponse{}, appwire.Unavailable("local daemon source cannot fork threads directly")
}

func (s *LocalDaemonSource) StartTurn(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	entry, err := s.entryForRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	return s.StartTurnAtEntry(ctx, entry, params)
}

// StartTurnAtEntry sends input to an exact daemon within shared recovery cancellation.
func (s *LocalDaemonSource) StartTurnAtEntry(ctx context.Context, entry rendezvous.Entry, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	var out appwire.TurnStartResponse
	err := s.withMutationClient(ctx, entry, params.ClientMutationID, func(ctx context.Context, client *appwire.Client) error {
		if err := gateSkillInputOnClient(ctx, client, params.Ref, params.ThreadID, params.Input); err != nil {
			return err
		}
		var callErr error
		out, callErr = client.TurnStart(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) SteerTurn(ctx context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	entry, err := s.entryForRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.TurnSteerResponse{}, localDaemonMutationEntryError(params.ClientMutationID, err)
	}
	var out appwire.TurnSteerResponse
	err = s.withMutationClient(ctx, entry, params.ClientMutationID, func(ctx context.Context, client *appwire.Client) error {
		if err := gateSkillInputOnClient(ctx, client, params.Ref, params.ThreadID, params.Input); err != nil {
			return err
		}
		return client.Request(ctx, appwire.MethodTurnSteer, params, &out)
	})
	return out, err
}

func (s *LocalDaemonSource) ResolveSandboxEscalation(ctx context.Context, params appwire.SandboxEscalationResolveParams) error {
	entry, err := s.entryForRef(params.Ref, params.ThreadID)
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodEvenerSandboxEscalationResolve, params, nil)
	})
}

func (s *LocalDaemonSource) InterruptTurn(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	entry, err := s.entryForRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.TurnInterruptResponse{}, localDaemonMutationEntryError(params.ClientMutationID, err)
	}
	var out appwire.TurnInterruptResponse
	err = s.withMutationClient(ctx, entry, params.ClientMutationID, func(ctx context.Context, client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodTurnInterrupt, params, &out)
	})
	return out, err
}

func (s *LocalDaemonSource) QueueTurn(ctx context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.TurnQueueResponse{}, localDaemonMutationEntryError(params.ClientMutationID, err)
	}
	var out appwire.TurnQueueResponse
	err = s.withMutationClient(ctx, entry, params.ClientMutationID, func(ctx context.Context, client *appwire.Client) error {
		if err := gateSkillInputOnClient(ctx, client, params.Ref, "", params.Input); err != nil {
			return err
		}
		return client.Request(ctx, appwire.MethodTurnQueue, params, &out)
	})
	return out, err
}

func (s *LocalDaemonSource) DrainAsSteer(ctx context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, localDaemonMutationEntryError(params.ClientMutationID, err)
	}
	var out appwire.TurnDrainAsSteerResponse
	err = s.withMutationClient(ctx, entry, params.ClientMutationID, func(ctx context.Context, client *appwire.Client) error {
		if err := gateSkillInputOnClient(ctx, client, params.Ref, "", params.Input); err != nil {
			return err
		}
		return client.Request(ctx, appwire.MethodTurnDrainAsSteer, params, &out)
	})
	return out, err
}

func (s *LocalDaemonSource) PromoteQueuedAsSteer(ctx context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, localDaemonMutationEntryError(params.ClientMutationID, err)
	}
	var out appwire.TurnPromoteQueuedAsSteerResponse
	err = s.withMutationClient(ctx, entry, params.ClientMutationID, func(ctx context.Context, client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodTurnPromoteQueuedAsSteer, params, &out)
	})
	return out, err
}

func (s *LocalDaemonSource) CancelQueued(ctx context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.TurnCancelQueuedResponse{}, localDaemonMutationEntryError(params.ClientMutationID, err)
	}
	var resp appwire.TurnCancelQueuedResponse
	err = s.withMutationClient(ctx, entry, params.ClientMutationID, func(ctx context.Context, client *appwire.Client) error {
		var cerr error
		resp, cerr = client.TurnCancelQueued(ctx, params)
		return cerr
	})
	if err != nil {
		return appwire.TurnCancelQueuedResponse{}, err
	}
	return resp, nil
}

func (s *LocalDaemonSource) CompactThread(ctx context.Context, params appwire.ThreadCompactStartParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		return client.ThreadCompactStart(ctx, params)
	})
}

func (s *LocalDaemonSource) ShutdownThread(ctx context.Context, params appwire.ThreadShutdownParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		return client.ThreadShutdown(ctx, params)
	})
}

func (s *LocalDaemonSource) SetThreadModel(ctx context.Context, params appwire.ThreadModelSetParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		return client.ThreadModelSet(ctx, params)
	})
}

func (s *LocalDaemonSource) SetThreadVisionModel(ctx context.Context, params appwire.ThreadVisionModelSetParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		return client.ThreadVisionModelSet(ctx, params)
	})
}

func (s *LocalDaemonSource) SetThreadReasoningEffort(ctx context.Context, params appwire.ThreadReasoningEffortSetParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		return client.ThreadReasoningEffortSet(ctx, params)
	})
}

func (s *LocalDaemonSource) SetThreadName(ctx context.Context, params appwire.ThreadNameSetParams) error {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return err
	}
	return s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		return client.ThreadNameSet(ctx, params)
	})
}

func (s *LocalDaemonSource) GoalSet(ctx context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.GoalSetResponse{}, err
	}
	var out appwire.GoalSetResponse
	err = s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.GoalSet(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) NotesHumanSet(ctx context.Context, params appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.NotesHumanSetResponse{}, localDaemonMutationEntryError(params.ClientMutationID, err)
	}
	var out appwire.NotesHumanSetResponse
	err = s.withMutationClient(ctx, entry, params.ClientMutationID, func(ctx context.Context, client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodNotesHumanSet, params, &out)
	})
	return out, err
}

func (s *LocalDaemonSource) UrlsRemove(ctx context.Context, params appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.UrlsRemoveResponse{}, localDaemonMutationEntryError(params.ClientMutationID, err)
	}
	var out appwire.UrlsRemoveResponse
	err = s.withMutationClient(ctx, entry, params.ClientMutationID, func(ctx context.Context, client *appwire.Client) error {
		return client.Request(ctx, appwire.MethodUrlsRemove, params, &out)
	})
	return out, err
}

func (s *LocalDaemonSource) ClearThread(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.ThreadClearResponse{}, err
	}
	var out appwire.ThreadClearResponse
	err = s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.ThreadClear(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) ListModels(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	entries := s.liveEntries()
	if len(entries) == 0 {
		return appwire.ModelListResponse{}, nil
	}
	var out appwire.ModelListResponse
	err := s.withClient(ctx, localDaemonRendezvousEntry(entries[0]), func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.ModelList(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) ListTasks(ctx context.Context, params appwire.TaskListParams) (appwire.TaskListResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.TaskListResponse{}, err
	}
	var out appwire.TaskListResponse
	err = s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.TasksList(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) ListJobs(ctx context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.JobsListResponse{}, err
	}
	var out appwire.JobsListResponse
	err = s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.JobsList(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) JobOutput(ctx context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.JobsOutputResponse{}, err
	}
	var out appwire.JobsOutputResponse
	err = s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.JobOutput(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) SubscribeThread(ctx context.Context, params appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	key := relaySessionKey(s.sourceID, params.Ref, params.ThreadID)
	s.relayMu.Lock()
	pending := s.legacyRelays[key]
	delete(s.legacyRelays, key)
	s.relayMu.Unlock()
	if pending.stop != nil {
		pending.stop()
	}

	lease := pending.lease
	handoff := pending.handoff
	var err error
	if lease == nil {
		lease, err = s.acquireRelaySession(params)
		if err != nil {
			return nil, err
		}
		result, readErr := lease.Read(ctx, params)
		if readErr != nil {
			lease.Close()
			return nil, readErr
		}
		handoff = result.Handoff
	}
	deliveries, err := lease.Listen(ctx)
	if err != nil {
		handoff.Abort()
		lease.Close()
		return nil, err
	}
	handoff.Commit()
	out := make(chan appwire.Notification, 128)
	go func() {
		defer close(out)
		defer lease.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case delivery, ok := <-deliveries:
				if !ok {
					return
				}
				forwardLocalDaemonNotification(ctx, out, delivery.Notification)
				delivery.Acknowledge()
			}
		}
	}()
	return out, nil
}

func relaySessionKey(sourceID, rawRef, threadID string) string {
	if ref, err := appwire.ParseRef(rawRef); err == nil {
		return ref.String()
	}
	return appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
}

func forwardLocalDaemonNotification(ctx context.Context, out chan<- appwire.Notification, notification appwire.Notification) {
	select {
	case <-ctx.Done():
	case out <- notification:
	}
}

func (s *LocalDaemonSource) withClient(ctx context.Context, entry rendezvous.Entry, fn func(context.Context, *appwire.Client) error) error {
	return s.withClientCallMapper(ctx, entry, fn, localDaemonCallError)
}

func (s *LocalDaemonSource) withMutationClient(
	ctx context.Context,
	entry rendezvous.Entry,
	clientMutationID string,
	fn func(context.Context, *appwire.Client) error,
) error {
	return s.withClientCallMapper(ctx, entry, fn, func(err error) error {
		return localDaemonMutationCallError(clientMutationID, err)
	})
}

// hasSkillInputItem reports whether an input carries a canonical skill
// selection, the condition under which a forwarded mutation must first prove
// the target advertises skill input support.
func hasSkillInputItem(items []appwire.InputItem) bool {
	for _, item := range items {
		if item.Type == "skill" {
			return true
		}
	}
	return false
}

// gateSkillInputOnClient enforces the target daemon's SkillInput capability on
// the same connection as the mutation being forwarded: the capability read and
// the mutation cannot be split across endpoints, so a daemon restart between
// them cannot let a selection reach a target that never advertised it. The
// read carries no subscription and no turns — only the capability verdict.
// A read failure is a rejection, never permission: a target whose capability
// cannot be read is treated exactly like one that does not support skill
// input, and an older daemon's absent capability reads false the same way.
func gateSkillInputOnClient(ctx context.Context, client *appwire.Client, ref, threadID string, input []appwire.InputItem) error {
	if !hasSkillInputItem(input) {
		return nil
	}
	response, err := client.ThreadRead(ctx, appwire.ThreadReadParams{
		Ref:          ref,
		ThreadID:     threadID,
		Subscribe:    false,
		IncludeTurns: false,
	})
	if err != nil {
		return err
	}
	if err := appwire.ValidateSkillInputSupport(input, response.Thread.Evener.Capabilities.SkillInput); err != nil {
		return appwire.InvalidParams(err.Error())
	}
	return nil
}

func (s *LocalDaemonSource) withClientCallMapper(
	ctx context.Context,
	entry rendezvous.Entry,
	fn func(context.Context, *appwire.Client) error,
	mapCallError func(error) error,
) error {
	ctx, finish := s.beginDaemonCall(ctx, entry)
	defer finish()
	if err := ctx.Err(); err != nil {
		return err
	}
	transport, err := s.dial(ctx, entry.Endpoint, s.client, daemonAuthHeader(entry.HubToken))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return localDaemonDialError(err)
	}
	defer transport.Close() //nolint:errcheck // transport cleanup; error is not actionable
	client := appwire.NewClient(transport)
	client.SetLogf(hubConnectionLogf)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "evener-hub"}}); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return localDaemonInitializeError(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := fn(ctx, client); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return mapCallError(err)
	}
	return nil
}

// localDaemonDialError maps transport-level failures observed during the
// websocket dial or Initialize handshake to a SessionUnavailable wire error,
// so the hub's auto-resume gate can fire. It does NOT map application-level
// JSON-RPC error responses (which retain semantic meaning) or caller context
// cancellation (which the call site checks separately).
func localDaemonDialError(err error) error {
	if err == nil {
		return nil
	}

	// syscall-level signals that the daemon process is gone or the kernel
	// tore down the socket mid-handshake.
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}

	// Daemon accepted the connection then immediately closed it (process died
	// mid-handshake, or the websocket library surfaced an EOF during the
	// Initialize round trip).
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}

	// Transport-level timeouts: hung daemon, slow loopback, or network filter
	// holding the SYN. These manifest as net.Error.Timeout()==true (e.g.
	// kernel TCP retransmit timeout, *net.OpError with i/o timeout) or as
	// context.DeadlineExceeded from a child context inside the dial library
	// (the caller's ctx was already checked at the call site).
	if errors.Is(err, context.DeadlineExceeded) {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}

	// Websocket close error during the handshake: the daemon answered the
	// HTTP upgrade but the connection died before Initialize completed.
	if _, ok := errors.AsType[websocket.CloseError](err); ok {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}

	// Fallback string match for transport-shaped errors that the underlying
	// library wraps without exposing a typed sentinel (e.g. "use of closed
	// network connection", "connection reset by peer", "broken pipe").
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "use of closed network connection") ||
		strings.Contains(lower, "i/o timeout") {
		return appwire.SessionUnavailable("local daemon unavailable: " + err.Error())
	}

	return err
}

func localDaemonCallError(err error) error {
	var wire appwire.WireError
	if errors.As(err, &wire) && wire.Code != appwire.CodeInternalError {
		return err
	}
	if !errors.As(err, &wire) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return localDaemonDialError(err)
	}
	msg := strings.ToLower(wire.Message)
	if strings.Contains(msg, "failed to get reader") ||
		strings.Contains(msg, "websocket") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "i/o timeout") {
		return appwire.SessionUnavailable("local daemon unavailable: " + wire.Message)
	}
	return err
}

func localDaemonMutationCallError(clientMutationID string, err error) error {
	mapped := localDaemonCallError(err)
	var wire appwire.WireError
	if !errors.As(mapped, &wire) {
		return mapped
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if wire.Code != appwire.CodeUnavailable || !ok || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		return mapped
	}
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "mutation outcome is unknown after local daemon response loss",
		Data: appwire.ErrorData{
			EvenerErrorInfo:  appwire.ErrorMutationOutcomeUnknown,
			ClientMutationID: clientMutationID,
			MutationOutcome:  appwire.MutationOutcomeUnknown,
			RetryDisposition: appwire.RetryDispositionAutomatic,
		},
	}
}

func localDaemonMutationEntryError(clientMutationID string, err error) error {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return err
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if wire.Code != appwire.CodeUnavailable || !ok || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		return err
	}
	data.ClientMutationID = clientMutationID
	data.MutationOutcome = appwire.MutationOutcomeNotAccepted
	data.RetryDisposition = appwire.RetryDispositionNone
	wire.Data = data
	return wire
}

// DaemonInitializeError identifies failures before a daemon accepts session RPCs.
// Its underlying wire error remains available for protocol error reporting.
type DaemonInitializeError struct {
	Err error
}

func (e DaemonInitializeError) Error() string { return e.Err.Error() }
func (e DaemonInitializeError) Unwrap() error { return e.Err }

func localDaemonInitializeError(err error) error {
	mapped := localDaemonCallError(err)
	var wire appwire.WireError
	if errors.As(mapped, &wire) && wire.Code != appwire.CodeInternalError {
		return DaemonInitializeError{Err: mapped}
	}
	return DaemonInitializeError{Err: localDaemonDialError(mapped)}
}

func localDaemonSubscribeReadError(err error) error {
	mapped := localDaemonCallError(err)
	var wire appwire.WireError
	if errors.As(mapped, &wire) && wire.Code != appwire.CodeInternalError {
		return mapped
	}
	return localDaemonDialError(mapped)
}

func daemonAuthHeader(token string) http.Header {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	return header
}

func (s *LocalDaemonSource) entryForRef(rawRef, threadID string) (rendezvous.Entry, error) {
	return s.entryForRefMode(rawRef, threadID, false)
}

func (s *LocalDaemonSource) entryForReadRef(rawRef, threadID string) (rendezvous.Entry, error) {
	return s.entryForRefMode(rawRef, threadID, true)
}

func (s *LocalDaemonSource) entryForRefMode(rawRef, threadID string, allowReadOnlyAlias bool) (rendezvous.Entry, error) {
	item, err := s.localEntryForRefMode(rawRef, threadID, allowReadOnlyAlias)
	if err != nil {
		return rendezvous.Entry{}, err
	}
	return localDaemonRendezvousEntry(item), nil
}

func (s *LocalDaemonSource) localEntryForRefMode(rawRef, threadID string, allowReadOnlyAlias bool) (LocalDaemonEntry, error) {
	requestedRef := strings.TrimSpace(rawRef)
	if requestedRef != "" {
		ref, err := appwire.ParseRef(requestedRef)
		if err != nil {
			return LocalDaemonEntry{}, err
		}
		if ref.SourceID != s.sourceID {
			return LocalDaemonEntry{}, fmt.Errorf("source not found: %s", ref.SourceID)
		}
		threadID = ref.ThreadID
	}
	for _, item := range s.liveEntries() {
		if item.ReadOnlyAlias && !allowReadOnlyAlias {
			continue
		}
		if s.localEntryNamesRef(item, requestedRef, threadID) {
			return item, nil
		}
	}
	return LocalDaemonEntry{}, appwire.SessionUnavailable("thread not found: " + threadID)
}

// localEntryNamesRef reports whether an entry is the one a ref (or, without a
// ref, a thread id) addresses: by its workspace ref, its thread id or its
// session id.
func (s *LocalDaemonSource) localEntryNamesRef(item LocalDaemonEntry, requestedRef, threadID string) bool {
	entry := localDaemonRendezvousEntry(item)
	if requestedRef != "" && localDaemonWorkspaceRef(s.sourceID, entry, localDaemonThreadID(item)) == requestedRef {
		return true
	}
	return localDaemonThreadID(item) == threadID || entry.SessionID == threadID
}

func (s *LocalDaemonSource) liveEntries() []LocalDaemonEntry {
	entries := s.listedEntries()
	out := make([]LocalDaemonEntry, 0, len(entries))
	for _, item := range entries {
		if item.Entry.Protocol == appwire.ProtocolVersion && item.Status != appwire.ThreadStatusRestartRequired {
			out = append(out, item)
		}
	}
	return out
}

// listedEntries preserves confirmed incompatible owners for display, without
// admitting them to the live RPC routes selected by liveEntries.
func (s *LocalDaemonSource) listedEntries() []LocalDaemonEntry {
	if s.entries == nil {
		return nil
	}
	entries := s.entries()
	out := make([]LocalDaemonEntry, 0, len(entries))
	for _, item := range entries {
		entry := item.Entry
		if entry.Endpoint == "" || localDaemonThreadID(item) == "" || (entry.Protocol != appwire.ProtocolVersion && item.Status != appwire.ThreadStatusRestartRequired) {
			continue
		}
		sourceID := entry.SourceID
		if sourceID == "" {
			sourceID = s.sourceID
		}
		if sourceID != s.sourceID {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (s *LocalDaemonSource) threadFromEntry(item LocalDaemonEntry) appwire.Thread {
	entry := localDaemonRendezvousEntry(item)
	threadID := localDaemonThreadID(item)
	ref := localDaemonWorkspaceRef(s.sourceID, entry, threadID)
	instanceID := firstLocalNonEmpty(entry.InstanceID, threadID)
	status := localDaemonThreadStatus(item.Status)
	startedAt := int64(0)
	if !entry.StartedAt.IsZero() {
		startedAt = entry.StartedAt.Unix()
	}
	thread := appwire.Thread{
		ID:            threadID,
		SessionID:     entry.SessionID,
		Preview:       entry.SessionID,
		ModelProvider: firstLocalNonEmpty(entry.Model, entry.Provider),
		CreatedAt:     startedAt,
		UpdatedAt:     startedAt,
		CWD:           entry.WorkingDir,
		Path:          filepath.Base(entry.WorkingDir),
		Source:        s.sourceID,
		Evener: appwire.EvenerThread{
			Ref:          ref,
			InstanceID:   instanceID,
			Capabilities: listRowCapabilities(item, status),
			AskPending:   item.PendingAsk,
		},
		Status: appwire.ThreadStatus{Type: status},
	}
	if status == appwire.ThreadStatusRestartRequired {
		// A restart-required session cannot act, but its saved notes are still
		// readable: advertise the read capability alone and let the write gate
		// (and the daemon's admission fence) refuse every mutation. The alias
		// guard is redundant with the alias gate below, which clears every
		// capability; it is here so this advertisement never depends on that
		// branch running after it.
		thread.Evener.Capabilities = appwire.ThreadCapabilities{SharedNotes: !item.ReadOnlyAlias}
	}
	if item.ReadOnlyAlias {
		// A read-only descendant alias still carries its own live watches: they
		// are read-only row state, not a mutation surface, so the alias guard
		// that suppresses the jobs block does not apply to them. A child with no
		// watches gets no diagnostics block at all.
		if len(item.Watches) > 0 {
			thread.Evener.Diagnostics = &appwire.EvenerDiagnostics{
				Watches: cloneLocalDaemonWatches(item.Watches),
			}
		}
	} else if len(item.RunningJobs) > 0 || len(item.CompletedJobs) > 0 || len(item.Watches) > 0 {
		jobs := make([]appwire.EvenerJobInfo, 0, len(item.RunningJobs)+len(item.CompletedJobs))
		jobs = append(jobs, item.RunningJobs...)
		jobs = append(jobs, item.CompletedJobs...)
		thread.Evener.Diagnostics = &appwire.EvenerDiagnostics{
			Jobs:    cloneLocalDaemonJobs(jobs),
			Watches: cloneLocalDaemonWatches(item.Watches),
		}
	}
	if item.ReadOnlyAlias {
		thread.Evener.Ref = appwire.Ref{SourceID: s.sourceID, ThreadID: threadID}.String()
		thread.Evener.Capabilities = appwire.ThreadCapabilities{}
		thread.Evener.Kind = "subagent"
		if item.OwnerSessionID != "" {
			thread.Evener.ParentRef = localDaemonWorkspaceRef(s.sourceID, item.Entry, item.OwnerSessionID)
		}
	}
	return thread
}

// listRowCapabilities projects a local list row's capability set. A probed
// row mirrors the daemon's own answer — the same set a ThreadRead of the
// session serves, cut from the same probe as the row's status — so the same
// session cannot read differently from ListThreads and from ThreadRead
// (#1840). Fork is no exception: the daemon hardwires its bit false, and
// the hub's applyHubForkCapability is the single owner that turns it on,
// on every path that serves a local row.
//
// The hand literal below is the fallback for rows no probe answered: it
// approximates the daemon's expected answer at the row's status, folding
// what the daemon folds (appCapabilitiesLocked) rather than overstating a
// session until a read hydrates it. The restart-required and read-only
// alias branches in threadFromEntry still replace the whole set after this
// projection.
func listRowCapabilities(item LocalDaemonEntry, status string) appwire.ThreadCapabilities {
	if item.CapabilitiesKnown {
		return item.Capabilities
	}
	// Spell the daemon's own status gates once, positive, the way
	// appCapabilitiesLocked does.
	active := status == appwire.ThreadStatusActive
	closed := status == appwire.ThreadStatusClosed
	return appwire.ThreadCapabilities{
		// The daemon refuses a plain send while a turn runs and once the
		// session is closed; a session awaiting a user answer keeps it.
		Send: !active && !closed,
		// Steer, Interrupt and Queue advertise harness support and are
		// withheld the way the daemon withholds them: closed removes all
		// three (appCapabilitiesLocked's `!closed`), while `active` moves
		// none of them (#1363, #1375). Gating only Queue left a closed
		// entry advertising two actions the daemon it mirrors refuses.
		Steer:     !closed,
		Interrupt: !closed,
		Compact:   !closed,
		// Clear is the one !closed bit that also folds activity — the daemon
		// gates it on !active for the same reason as Send — and folds
		// unresolved approval work (clearBlockedReasonLocked): the roster
		// carries the ask and escalation flags, so an unprobed row withholds
		// Clear while either is pending. Queued work stays beyond the
		// roster's view, the approximation's honest limit.
		Clear:       !item.ReadOnlyAlias && !active && !closed && !item.PendingAsk && !item.PendingEscalation,
		Shutdown:    true,
		ChangeModel: !closed,
		// The daemon advertises this whenever its vision-model seam is wired
		// (every current daemon wires it at startup) and withholds it when
		// closed, like its siblings here; the live-hub probe showed list rows
		// understating it while the same session's read advertised it, the
		// same drift class SkillInput had.
		ChangeVisionModel: !closed,
		// Folding `active` into Queue made ListThreads disagree with
		// ThreadRead and the status frames for the same session.
		Queue:       !item.ReadOnlyAlias && !closed,
		Goal:        !closed,
		SharedNotes: !item.ReadOnlyAlias && !closed,
		Rename:      !closed,
		// SkillInput follows the harness-support rule Steer, Interrupt
		// and Queue follow (#1375, #1840): every current daemon wires all
		// four input-bearing turn mutations, so a live local session's
		// list row must not understate it until a read hydrates. It is
		// deliberately not closed-gated, because the daemon's own
		// advertisement is not either (appCapabilitiesLocked's
		// skillInputSupportedLocked) — the actions that could carry a
		// selection are the ones `!closed` withholds. The hub's mutation
		// gates re-verify against the live daemon, so a daemon that
		// genuinely lacks the support still refuses each selection
		// honestly.
		SkillInput: true,
	}
}

func cloneLocalDaemonJobs(in []appwire.EvenerJobInfo) []appwire.EvenerJobInfo {
	return appwire.CloneEvenerJobs(in)
}

func cloneLocalDaemonWatches(in []appwire.EvenerWatchInfo) []appwire.EvenerWatchInfo {
	return appwire.CloneEvenerWatches(in)
}

func localDaemonRendezvousEntry(item LocalDaemonEntry) rendezvous.Entry {
	entry := item.Entry
	if item.SessionID != "" {
		entry.SessionID = item.SessionID
	}
	return entry
}

func localDaemonWorkspaceRef(sourceID string, entry rendezvous.Entry, threadID string) string {
	if ref, err := appwire.ParseRef(strings.TrimSpace(entry.WorkspaceRef)); err == nil && ref.SourceID == sourceID {
		return ref.String()
	}
	return appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
}

func localDaemonItemDaemonIdentity(entry rendezvous.Entry) string {
	instanceID := strings.TrimSpace(entry.InstanceID)
	if instanceID == "" {
		instanceID = strings.TrimSpace(entry.SessionID)
	}
	return fmt.Sprintf("instance=%s;pid=%d;started=%s;endpoint=%s", instanceID, entry.PID, entry.StartedAt.UTC().String(), strings.TrimSpace(entry.Endpoint))
}

func localDaemonThreadID(item LocalDaemonEntry) string {
	if item.SessionID != "" {
		return item.SessionID
	}
	return firstLocalNonEmpty(item.Entry.ThreadID, item.Entry.SessionID)
}

func localDaemonThreadStatus(status string) string {
	switch strings.TrimSpace(status) {
	case appwire.ThreadStatusActive:
		return appwire.ThreadStatusActive
	case appwire.ThreadStatusAwaiting:
		return appwire.ThreadStatusAwaiting
	case appwire.ThreadStatusWarning:
		return appwire.ThreadStatusWarning
	case appwire.ThreadStatusSystemError:
		return appwire.ThreadStatusSystemError
	case appwire.ThreadStatusClosed:
		return appwire.ThreadStatusClosed
	case appwire.ThreadStatusNotLoaded:
		return appwire.ThreadStatusNotLoaded
	case appwire.ThreadStatusRestartRequired:
		return appwire.ThreadStatusRestartRequired
	case appwire.ThreadStatusIdle:
		return appwire.ThreadStatusIdle
	default:
		return appwire.ThreadStatusIdle
	}
}

func localThreadLess(a, b appwire.Thread) bool {
	au, bu := localThreadUpdatedAt(a), localThreadUpdatedAt(b)
	if au != bu {
		return au > bu
	}
	ac, bc := localThreadCreatedAt(a), localThreadCreatedAt(b)
	if ac != bc {
		return ac > bc
	}
	if cmp := compareLocalOrderText(localThreadTitle(a), localThreadTitle(b)); cmp != 0 {
		return cmp < 0
	}
	return compareLocalOrderText(firstLocalNonEmpty(a.ID, a.SessionID), firstLocalNonEmpty(b.ID, b.SessionID)) < 0
}

func localThreadUpdatedAt(thread appwire.Thread) int64 {
	if thread.UpdatedAt > 0 {
		return thread.UpdatedAt
	}
	if thread.CreatedAt > 0 {
		return thread.CreatedAt
	}
	return 0
}

func localThreadCreatedAt(thread appwire.Thread) int64 {
	if thread.CreatedAt > 0 {
		return thread.CreatedAt
	}
	if thread.UpdatedAt > 0 {
		return thread.UpdatedAt
	}
	return 0
}

func localThreadTitle(thread appwire.Thread) string {
	return firstLocalNonEmpty(thread.Name, thread.Preview, thread.SessionID, thread.ID)
}

func compareLocalOrderText(a, b string) int {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	af := strings.ToLower(a)
	bf := strings.ToLower(b)
	if af < bf {
		return -1
	}
	if af > bf {
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func firstLocalNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
