package hub

import (
	"context"
	"errors"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func sourceForThread(sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	if ref != "" {
		return sources.SourceForRef(ref)
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
	unlock := lockDeletionTarget(cfg, ref, threadID)
	defer unlock()
	if err := deletionFenceError(cfg, ref, threadID, clientMutationID); err != nil {
		var zero R
		return zero, err
	}
	if clientMutationID != "" {
		if err := daemonRestartRequiredError(ctx, cfg, ref, threadID, clientMutationID); err != nil {
			var zero R
			return zero, err
		}
	}
	result, err := action()
	if clientMutationID != "" && isSessionUnavailableError(err) {
		if restartErr := refreshDaemonRestartRequiredError(ctx, cfg, ref, threadID, clientMutationID); restartErr != nil {
			var zero R
			return zero, restartErr
		}
	}
	return result, err
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
	if cfg.DeletionStore == nil {
		return nil
	}
	if _, deleted := cfg.DeletionStore.TargetState(ref, threadID); !deleted {
		return nil
	}
	if ref == "" {
		ref = localAppRef(threadID)
	}
	return appwire.WireError{
		Code:    appwire.CodeUnavailable,
		Message: "target has been deleted: " + ref,
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
