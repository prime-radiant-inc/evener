package hub

import (
	"context"
	"encoding/json"
	"errors"
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

func sourceForThreadWithDeletionFence(cfg hubcore.WebConfig, sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	return withDeletionTargetOwnership(context.Background(), cfg, ref, threadID, "", func() (appsource.Source, error) {
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
	unlock := lockDeletionTarget(cfg, ref, threadID)
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

func lockDeletionTarget(cfg hubcore.WebConfig, ref, threadID string) func() {
	if cfg.ResumeLocks == nil {
		return func() {}
	}
	threadID = deletionThreadID(ref, threadID)
	if threadID == "" {
		return func() {}
	}
	lock := cfg.ResumeLocks.For(threadID)
	lock.Lock()
	return lock.Unlock
}

func deletionFenceError(cfg hubcore.WebConfig, ref, threadID, clientMutationID string) error {
	return deletionFenceErrorNaming(cfg, ref, threadID, ref, clientMutationID)
}

// deletionFenceErrorNaming looks the fence up under (ref, threadID) and names
// reportRef in the refusal. Fork resolves a stable workspace ref to the session
// that ref currently names, so the fence belongs to the resolved session while
// the message belongs to the ref the client asked about — naming the resolved
// id would report an identity the request never mentioned.
func deletionFenceErrorNaming(cfg hubcore.WebConfig, ref, threadID, reportRef, clientMutationID string) error {
	if cfg.DeletionStore == nil {
		return nil
	}
	if _, deleted := cfg.DeletionStore.TargetState(ref, threadID); !deleted {
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

func isTargetDeletedError(err error) bool {
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		return false
	}
	data, ok := wireErr.Data.(appwire.ErrorData)
	return ok && data.MutationOutcome == appwire.MutationOutcomeTargetDeleted
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

func sessionActionRecoveryError(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string, epoch uint64) error {
	if err := sessionConnectionRecoveryError(ctx, cfg, ref, threadID); err != nil {
		return err
	}
	state := sessionRecoveryState(cfg, ref, threadID)
	if state.Stopping > 0 || state.ResumeRequired {
		return sessionResumeRequiredError()
	}
	if state.Epoch != epoch {
		return sessionRecoveryAdmissionError{appwire.Unavailable("session recovery canceled this pending action; submit it again")}
	}
	return nil
}

type sessionRecoveryAdmissionKey struct{}

type sessionRecoveryAdmission struct {
	sessionID string
	epoch     uint64
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
	case appwire.MethodEvenerSandboxEscalationResolve:
		var params appwire.SandboxEscalationResolveParams
		if json.Unmarshal(message.Request.Params, &params) != nil {
			return ctx
		}
		rawRef, id = params.Ref, strings.TrimSpace(params.ThreadID)
	case appwire.MethodThreadFork, appwire.MethodEvenerThreadNameSet, appwire.MethodThreadModelSet, appwire.MethodThreadVisionModelSet,
		appwire.MethodThreadReasoningEffortSet, appwire.MethodThreadCompactStart,
		appwire.MethodThreadClear, appwire.MethodThreadShutdown, appwire.MethodGoalSet,
		appwire.MethodTurnQueue, appwire.MethodTurnDrainAsSteer,
		appwire.MethodTurnPromoteQueuedAsSteer, appwire.MethodTurnCancelQueued:
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
	return context.WithValue(ctx, sessionRecoveryAdmissionKey{}, sessionRecoveryAdmission{sessionID: id, epoch: cfg.ResumeLocks.RecoveryState(id).Epoch})
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
