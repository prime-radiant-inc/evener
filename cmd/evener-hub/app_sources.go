package hub

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func sourceForThread(sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	ref = strings.TrimSpace(ref)
	threadID = strings.TrimSpace(threadID)
	if sources == nil {
		return nil, errors.New("source registry unavailable")
	}
	if ref != "" {
		source, err := sources.SourceForRef(ref)
		if err != nil {
			if _, parseErr := appwire.ParseRef(ref); parseErr != nil {
				return nil, appwire.InvalidParams(parseErr.Error())
			}
			return nil, err
		}
		return source, nil
	}
	source, ok := sources.Source("local")
	if !ok {
		return nil, errors.New("source not found: local")
	}
	if threadID == "" {
		return source, nil
	}
	return source, nil
}

// sourceForThreadWithDeletionFence resolves a source while holding the
// session's ownership alias, so it must acquire that alias with the request's
// context: a canceled or disconnected RPC returns promptly instead of parking
// behind a long-running explicit Resume.
func sourceForThreadWithDeletionFence(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	return withDeletionTargetOwnership(ctx, cfg, ref, threadID, "", func() (appsource.Source, error) {
		return sourceForThread(sources, ref, threadID)
	})
}

func withDeletionTargetOwnership[R any](
	ctx context.Context,
	cfg hubcore.WebConfig,
	ref, threadID, clientMutationID string,
	action func() (R, error),
) (R, error) {
	epoch := sessionRequestRecoveryEpoch(ctx, cfg, ref, threadID)
	unlock, err := lockDeletionTarget(ctx, cfg, ref, threadID)
	if err != nil {
		var zero R
		return zero, err
	}
	defer unlock()
	if err := deletionFenceError(cfg, ref, threadID, clientMutationID); err != nil {
		var zero R
		return zero, err
	}
	if clientMutationID != "" {
		if err := sessionActionRecoveryError(ctx, cfg, ref, threadID, epoch); err != nil {
			var zero R
			return zero, blockedAdmissionMutationError(err, clientMutationID)
		}
		if err := daemonRestartRequiredError(ctx, cfg, ref, threadID, clientMutationID); err != nil {
			var zero R
			return zero, err
		}
	}
	result, err := action()
	if clientMutationID != "" && daemonOwnershipMayHaveChanged(err) {
		if restartErr := refreshDaemonRestartRequiredError(ctx, cfg, ref, threadID, clientMutationID); restartErr != nil {
			var zero R
			return zero, restartErr
		}
	}
	return result, err
}

// withSessionActionOwnership guards actions that have no durable mutation ID.
// Reads share deletion locking but must remain available for incompatible owners.
func withSessionActionOwnership[R any](ctx context.Context, cfg hubcore.WebConfig, ref, threadID string, action func() (R, error)) (R, error) {
	epoch := sessionRequestRecoveryEpoch(ctx, cfg, ref, threadID)
	return withDeletionTargetOwnership(ctx, cfg, ref, threadID, "", func() (R, error) {
		if err := sessionActionRecoveryError(ctx, cfg, ref, threadID, epoch); err != nil {
			var zero R
			return zero, err
		}
		if err := daemonRestartRequiredError(ctx, cfg, ref, threadID, ""); err != nil {
			var zero R
			return zero, err
		}
		result, err := action()
		if daemonOwnershipMayHaveChanged(err) {
			if restartErr := refreshDaemonRestartRequiredError(ctx, cfg, ref, threadID, ""); restartErr != nil {
				var zero R
				return zero, restartErr
			}
		}
		return result, err
	})
}

func daemonOwnershipMayHaveChanged(err error) bool {
	var initialization appsource.DaemonInitializeError
	var mismatch appwire.ProtocolVersionMismatchError
	return isSessionUnavailableError(err) || errors.As(err, &mismatch) || errors.As(err, &initialization)
}

func lockDeletionTarget(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string) (func(), error) {
	if cfg.ResumeLocks == nil {
		return func() {}, nil
	}
	threadID = deletionThreadID(ref, threadID)
	if threadID == "" {
		return func() {}, nil
	}
	lock := cfg.ResumeLocks.For(threadID)
	if err := lock.LockContext(ctx); err != nil {
		return nil, err
	}
	return lock.Unlock, nil
}

// tryLockDeletionTarget takes the deletion target's alias without blocking
// when it is immediately free, letting the relay's per-frame guard skip the
// bounded wait's context and timer allocation. It reports false whenever
// lockDeletionTarget must run instead — nil locks, an unresolvable target, or
// a held alias — so acquisition semantics are unchanged: the alias is a single
// token channel with no waiter queue, and the fast take is the acquisition the
// bounded wait would have made immediately.
func tryLockDeletionTarget(cfg hubcore.WebConfig, ref, threadID string) (func(), bool) {
	if cfg.ResumeLocks == nil {
		return nil, false
	}
	threadID = deletionThreadID(ref, threadID)
	if threadID == "" {
		return nil, false
	}
	lock := cfg.ResumeLocks.For(threadID)
	if !lock.TryLock() {
		return nil, false
	}
	return lock.Unlock, true
}

func deletionFenceError(cfg hubcore.WebConfig, ref, threadID, clientMutationID string) error {
	return deletionFenceErrorNaming(cfg, ref, threadID, ref, clientMutationID)
}

// deletionFenceErrorForGroup applies the deletion fence to every alias in an
// ownership group: a deletion record may name any alias in the group, not only
// the one the request addressed, so checking one alias would let a
// sibling-alias deletion slip past.
func deletionFenceErrorForGroup(cfg hubcore.WebConfig, aliases []string) error {
	for _, alias := range aliases {
		if err := deletionFenceError(cfg, "", alias, ""); err != nil {
			return err
		}
	}
	return nil
}

// deletionTargetLookup reads the retained deletion state for one stable target.
type deletionTargetLookup func(store *hubcore.DeletionStore, ref, threadID string) (hubcore.DeletionState, bool)

// deletionTargetState is the deletion-state lookup requests use. It is a
// package-level seam so cancellation-ordering tests can publish a deletion
// between two checks made by a single request; production reads the durable
// store directly. Work that runs in the background, past the request that
// started it, captures the lookup once when it is built instead of reading
// this variable live (see newHubRelayFunctions): a test restoring the seam must
// not race with, or be answered by, a relay some earlier test left running.
var deletionTargetState deletionTargetLookup = func(store *hubcore.DeletionStore, ref, threadID string) (hubcore.DeletionState, bool) {
	return store.TargetState(ref, threadID)
}

// deletionFenceErrorNaming looks the fence up under (ref, threadID) and names
// reportRef in the refusal. Fork resolves a stable workspace ref to the session
// that ref currently names, so the fence belongs to the resolved session while
// the message belongs to the ref the client asked about — naming the resolved
// id would report an identity the request never mentioned.
func deletionFenceErrorNaming(cfg hubcore.WebConfig, ref, threadID, reportRef, clientMutationID string) error {
	return deletionTargetState.fenceError(cfg, ref, threadID, reportRef, clientMutationID)
}

// fenceError is deletionFenceErrorNaming answered by this lookup.
func (lookup deletionTargetLookup) fenceError(cfg hubcore.WebConfig, ref, threadID, reportRef, clientMutationID string) error {
	if cfg.DeletionStore == nil {
		return nil
	}
	if _, deleted := lookup(cfg.DeletionStore, ref, threadID); !deleted {
		return nil
	}
	if reportRef == "" {
		reportRef = localAppRef(threadID)
	}
	return appwire.WireError{
		Code:    appwire.CodeUnavailable,
		Message: "target has been deleted: " + reportRef,
		Data: appwire.ErrorData{
			EvenerErrorInfo:  appwire.ErrorActionUnavailable,
			ClientMutationID: clientMutationID,
			MutationOutcome:  appwire.MutationOutcomeTargetDeleted,
			RetryDisposition: appwire.RetryDispositionNone,
		},
	}
}

// isTargetDeletedError reports whether err is the hub's typed deletion refusal:
// a WireError whose data marks the target as deleted. The wire client decodes
// Data as the typed appwire.ErrorData on some paths and as map[string]any on
// others, so a shape that is not already typed is re-marshaled rather than
// asserted — the convention app_retirement_resume.go's isLifecycleRetiringError
// follows. Reading only the typed shape would let a real deletion be wrapped as
// an unknown mutation outcome instead of reported as the deletion it is.
func isTargetDeletedError(err error) bool {
	wireErr, ok := wireErrorFromError(err)
	if !ok || wireErr.Data == nil {
		return false
	}
	data, ok := wireErr.Data.(appwire.ErrorData)
	if !ok {
		raw, merr := json.Marshal(wireErr.Data)
		if merr != nil {
			return false
		}
		if json.Unmarshal(raw, &data) != nil {
			return false
		}
	}
	return data.MutationOutcome == appwire.MutationOutcomeTargetDeleted
}

func deletionThreadID(ref, threadID string) string {
	if parsed, err := appwire.ParseRef(ref); err == nil && parsed.SourceID == "local" {
		return parsed.ThreadID
	}
	return threadID
}

// hubKnowsRef reports whether ref names a thread tracked in the local past
// index. It gates the retry that resumes a local past session after a live
// action reports that its daemon is unavailable.
//
// This fans out to 6 unrelated RPC handlers across the hub package (compact,
// model, vision-model, session-resume, plus its own callers here), none of
// which currently thread a request context this deep — passing
// context.Background() here (rather than widening every one of those call
// chains for one existence check) means the bounded delegate-journal scan
// still applies, just without real cancellation on this specific path.
func hubKnowsRef(cfg hubcore.WebConfig, ref string) bool {
	_, ok, _ := pastThreadForRead(context.Background(), cfg, appwire.ThreadReadParams{Ref: ref})
	return ok
}

func sessionRecoveryState(cfg hubcore.WebConfig, ref, threadID string) hubcore.SessionRecoveryState {
	if ref != "" {
		parsed, err := appwire.ParseRef(ref)
		if err != nil || parsed.SourceID != "local" {
			return hubcore.SessionRecoveryState{}
		}
	}
	if cfg.ResumeLocks == nil {
		return hubcore.SessionRecoveryState{}
	}
	id := deletionThreadID(ref, threadID)
	if id == "" {
		return hubcore.SessionRecoveryState{}
	}
	return cfg.ResumeLocks.RecoveryState(id)
}

// sessionRecoveryAdmissionError is terminal for an action already rejected by
// recovery, even if another request explicitly resumes before retry routing.
// Unwrap preserves the existing wire-level action-unavailable response.
type sessionRecoveryAdmissionError struct{ appwire.WireError }

func (err sessionRecoveryAdmissionError) Unwrap() error { return err.WireError }

func isSessionRecoveryAdmissionError(err error) bool {
	_, ok := errors.AsType[sessionRecoveryAdmissionError](err)
	return ok
}

// sessionResumeRequiredError is the refusal for an action whose session is
// under a recovery fence, whether the hub holds it in the resume locks or the
// session's own daemon announces it in the status it reports.
func sessionResumeRequiredError() error {
	return sessionRecoveryAdmissionError{appwire.Unavailable("session recovery requires an explicit thread/resume before submitting another action")}
}

// sessionStateRecoveryError is the recovery fence a session's own state puts on
// an action: a Stop drain, a resume-only obligation the request was not
// admitted through (sessionAdmitsResumeRequired carries that admission), and a
// stale admission epoch. It is the shared body of the two re-checks that differ
// only in whether the connection-sequence fence also applies -
// sessionActionRecoveryError for a fresh action, retirementAdmissionRecoveryError
// for an already-admitted one still in flight - so the carve-out and its
// refusal exist in exactly one place.
func sessionStateRecoveryError(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string, epoch uint64) error {
	state := sessionRecoveryState(cfg, ref, threadID)
	if state.Stopping > 0 || (state.ResumeRequired && (!state.ExitConfirmed || !sessionAdmitsResumeRequired(ctx, cfg, ref, threadID))) {
		return sessionResumeRequiredError()
	}
	if state.Epoch != epoch {
		return sessionRecoveryAdmissionError{appwire.Unavailable("session recovery canceled this pending action; submit it again")}
	}
	return nil
}

func sessionActionRecoveryError(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string, epoch uint64) error {
	if err := sessionConnectionRecoveryError(ctx, cfg, ref, threadID); err != nil {
		return err
	}
	return sessionStateRecoveryError(ctx, cfg, ref, threadID, epoch)
}

// sessionAdmitsResumeRequired reports whether the request that carries ctx is a
// turn/start for the same local session. Sending a prompt folds the resume into
// the send: the admitted turn/start runs that resume explicitly — app_rpc.go's
// resumeTurnStartThreadResume, the resume the turn/start handler runs before
// its retry, which turnStartResumeExplicit selects and which clears the
// ResumeRequired fence — so a session that only needs a resume is admitted
// rather than refused with the explicit-resume fence. prepareRelay's automatic
// resume is NOT this path; it refuses while the obligation stands. This is the
// ONLY carve-out: a Stop in flight (Stopping > 0), an unconfirmed force-stop
// exit (ExitConfirmed false — the same shape resumeOnlyFoldable, the wire bit,
// refuses), a stale admission epoch, the connection fence, and an incompatible
// daemon (daemonRestartRequiredError) are all still refused, and every action
// other than turn/start keeps the fence unchanged.
func sessionAdmitsResumeRequired(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string) bool {
	admission, ok := sessionRecoveryAdmissionFor(ctx, cfg, ref, threadID)
	return ok && admission.admitResumeRequired
}

// sessionRecoveryAdmissionFor returns the recovery admission that
// admitSessionRecovery stamped on ctx for (ref, threadID), when it stamped one
// for that session or for any alias of its recovery group. The admission carries
// the request's captured epoch, exactly what sessionActionRecoveryError compares
// against the live state's.
//
// The alias case exists for the retirement re-check: resumeAfterConfirmedRetirement
// re-reads the SAME request admission for every id in the resolved ownership
// group, and a force stop persists one obligation across the whole group, so an
// alias of the admitted session's group is the same request's session. The
// send-side fences read the request's own identity, where the exact match
// already holds, so membership only adds the alias case the grouped re-check
// needs. A request with no admission still matches nothing.
func sessionRecoveryAdmissionFor(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string) (sessionRecoveryAdmission, bool) {
	admission, ok := ctx.Value(sessionRecoveryAdmissionKey{}).(sessionRecoveryAdmission)
	if !ok {
		return sessionRecoveryAdmission{}, false
	}
	id := deletionThreadID(ref, threadID)
	if admission.sessionID == id {
		return admission, true
	}
	if cfg.ResumeLocks != nil && slices.Contains(cfg.ResumeLocks.RecoveryAliases(admission.sessionID), id) {
		return admission, true
	}
	return sessionRecoveryAdmission{}, false
}

// turnStartResumeExplicit reports whether a turn/start request's folded resume
// must run as an explicit (non-automatic) resume. That is exactly the request
// the resume-only carve-out admitted (sessionAdmitsResumeRequired) for a
// session the hub still fences with ResumeRequired: the automatic resume
// refuses while the obligation stands (app_threadlifecycle.go's
// `automatic && state.ResumeRequired`), so sending a prompt only resumes the
// session when the send's own resume owns the fence and clears it. Every other
// turn/start keeps the automatic resume, and a request that is not a turn/start
// (or names a different session) never qualifies. A session whose force-stop
// exit is still unconfirmed is among the still-refused causes: the carve-out
// requires the confirmed exit (sessionActionRecoveryError's ExitConfirmed
// guard), so such a request never reaches this path and Resume stays its way
// out.
//
// It revalidates the WHOLE resume-only admission here, at the retry, not only
// at the request's first admission: a Stop or another recovery can begin
// between turn/start's admission and this retry, so the marker and
// ResumeRequired captured then are stale evidence by now. Every clause the
// carve-out reads is re-read against the live state - ResumeRequired, no Stop
// draining (Stopping == 0), the confirmed exit (ExitConfirmed), the admission's
// captured epoch (the same comparison sessionActionRecoveryError makes against
// the request's epoch: an epoch advanced since admission cancels this action),
// the connection-recovery fence, and an incompatible-protocol daemon whose
// error case fails closed - so the folded resume never launches during a Stop
// drain or under any other fence the fresh admission would refuse.
func turnStartResumeExplicit(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string) bool {
	admission, ok := sessionRecoveryAdmissionFor(ctx, cfg, ref, threadID)
	if !ok || !admission.admitResumeRequired {
		return false
	}
	state := sessionRecoveryState(cfg, ref, threadID)
	if !state.ResumeRequired || state.Stopping > 0 || !state.ExitConfirmed {
		return false
	}
	if state.Epoch != admission.epoch {
		return false
	}
	if sessionConnectionRecoveryError(ctx, cfg, ref, threadID) != nil {
		return false
	}
	if _, required, err := restartRequiredDaemon(ctx, cfg, ref, threadID); err != nil || required {
		return false
	}
	return true
}

type sessionRecoveryAdmissionKey struct{}

type sessionRecoveryAdmission struct {
	sessionID string
	epoch     uint64
	// admitResumeRequired marks a turn/start request: it is the one method whose
	// action (a send) folds a pending resume into itself, so the resume-only
	// fence does not refuse it. sessionAdmitsResumeRequired reads it.
	admitResumeRequired bool
}

// admitSessionRecovery captures only the requested local identity; it performs
// no ownership discovery and leaves malformed or foreign targets to handlers.
func admitSessionRecovery(ctx context.Context, cfg hubcore.WebConfig, message appwire.Message) context.Context {
	if message.Request == nil || cfg.ResumeLocks == nil {
		return ctx
	}
	var rawRef, id string
	switch message.Request.Method {
	case appwire.MethodThreadResume:
		var params appwire.ThreadResumeParams
		if json.Unmarshal(message.Request.Params, &params) != nil {
			return ctx
		}
		rawRef, id = params.Ref, strings.TrimSpace(params.Session)
	case appwire.MethodTurnStart, appwire.MethodTurnSteer, appwire.MethodTurnInterrupt:
		var params appwire.TurnInterruptParams
		if json.Unmarshal(message.Request.Params, &params) != nil {
			return ctx
		}
		rawRef, id = params.Ref, strings.TrimSpace(params.ThreadID)
	case appwire.MethodEvenerSandboxEscalationResolve, appwire.MethodEvenerDelegateStop:
		var params appwire.SandboxEscalationResolveParams
		if json.Unmarshal(message.Request.Params, &params) != nil {
			return ctx
		}
		rawRef, id = params.Ref, strings.TrimSpace(params.ThreadID)
	case appwire.MethodThreadFork, appwire.MethodEvenerThreadNameSet, appwire.MethodThreadModelSet, appwire.MethodThreadVisionModelSet,
		appwire.MethodThreadReasoningEffortSet, appwire.MethodThreadCompactStart,
		appwire.MethodThreadClear, appwire.MethodThreadShutdown, appwire.MethodGoalSet,
		appwire.MethodTurnQueue, appwire.MethodTurnDrainAsSteer,
		appwire.MethodTurnPromoteQueuedAsSteer, appwire.MethodTurnCancelQueued,
		appwire.MethodNotesHumanSet, appwire.MethodUrlsRemove:
		var params struct {
			Ref string `json:"ref"`
		}
		if json.Unmarshal(message.Request.Params, &params) != nil {
			return ctx
		}
		rawRef = params.Ref
	default:
		return ctx
	}
	if rawRef != "" {
		ref, err := appwire.ParseRef(rawRef)
		if err != nil || ref.SourceID != "local" {
			return ctx
		}
		// Resume's explicit sessionId takes precedence; other handlers resolve ref
		// before their optional threadId. Ignored extra fields cannot change this.
		if message.Request.Method != appwire.MethodThreadResume || id == "" {
			id = ref.ThreadID
		}
	}
	if id == "" {
		return ctx
	}
	admission := sessionRecoveryAdmission{sessionID: id, epoch: cfg.ResumeLocks.RecoveryState(id).Epoch}
	if message.Request.Method == appwire.MethodTurnStart {
		admission.admitResumeRequired = true
	}
	return context.WithValue(ctx, sessionRecoveryAdmissionKey{}, admission)
}

func sessionRequestRecoveryEpoch(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string) uint64 {
	if admission, ok := ctx.Value(sessionRecoveryAdmissionKey{}).(sessionRecoveryAdmission); ok && admission.sessionID == deletionThreadID(ref, threadID) {
		return admission.epoch
	}
	return sessionRecoveryState(cfg, ref, threadID).Epoch
}

type sessionConnectionRecoveryKey struct{}

func admitSessionConnection(ctx context.Context, cfg hubcore.WebConfig) context.Context {
	if cfg.ResumeLocks == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionConnectionRecoveryKey{}, cfg.ResumeLocks.RecoverySequence())
}

func sessionConnectionRecoveryError(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string) error {
	sequence, ok := ctx.Value(sessionConnectionRecoveryKey{}).(uint64)
	if cfg.ResumeLocks == nil {
		return nil
	}
	stale := func() error {
		return sessionRecoveryAdmissionError{appwire.Unavailable("session recovery requires Resume on a fresh connection before submitting another action")}
	}
	if ok && sessionRecoveryState(cfg, ref, threadID).LastRecoverySequence > sequence {
		return stale()
	}
	if (!ok || cfg.ResumeLocks.RecoverySequence() <= sequence) && !cfg.ResumeLocks.HasUnconfirmedRecovery() {
		return nil
	}
	if ref != "" {
		parsed, err := appwire.ParseRef(ref)
		if err != nil {
			return err
		}
		if parsed.SourceID != "local" {
			return nil
		}
		threadID = parsed.ThreadID
	}
	// A failed exit wait can leave the owner creating delegates after its last
	// scan. Verify their ancestry at use time against the retained recovery
	// sequence; a journal descriptor, not fork provenance, establishes ownership.
	seen := make(map[string]bool)
	for threadID != "" && !seen[threadID] {
		seen[threadID] = true
		child, found, err := ownershipEntry(ctx, cfg, threadID)
		if err != nil {
			return err
		}
		if !found || (!child.Meta.IsSubagent && (child.Meta.JobTreeRootSessionID == "" || child.Meta.JobTreeRootSessionID == threadID)) {
			return nil
		}
		parent := child.Meta.ParentSessionID
		if parent == "" {
			return nil
		}
		owned, err := agent.SessionOwnsDelegate(ctx, child.StateDir, parent, threadID)
		if err != nil {
			return err
		}
		if !owned {
			return nil
		}
		state := cfg.ResumeLocks.RecoveryState(parent)
		if state.Stopping > 0 || (state.ResumeRequired && !state.ExitConfirmed) {
			return sessionRecoveryAdmissionError{appwire.Unavailable("delegate owner exit is unconfirmed; recover the owner before submitting another action")}
		}
		if ok && state.LastRecoverySequence > sequence {
			return stale()
		}
		threadID = parent
	}
	return nil
}
