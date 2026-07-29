package main

import (
	"context"
	"errors"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

type deletionOwnershipContextKey struct{}

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

func sourceForThreadWithManagedLaunch(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	return withDeletionTargetOwnership(cfg, ref, threadID, "", func() (appsource.Source, error) {
		return sourceForThreadWithManagedLaunchUnlocked(ctx, cfg, sources, ref, threadID)
	})
}

func sourceForThreadWithManagedLaunchUnlocked(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	if sourceID, ok := managedLaunchSourceIDForRef(cfg, ref); ok {
		return cfg.CodexLauncher.EnsureSource(ctx, sourceID, sources)
	}
	return sourceForThread(sources, ref, threadID)
}

func withDeletionTargetOwnership[R any](
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
	return action()
}

func contextWithDeletionTargetOwnership(ctx context.Context, ref, threadID string) context.Context {
	return context.WithValue(ctx, deletionOwnershipContextKey{}, deletionThreadID(ref, threadID))
}

func contextOwnsDeletionTarget(ctx context.Context, ref, threadID string) bool {
	ownedThreadID, _ := ctx.Value(deletionOwnershipContextKey{}).(string)
	return ownedThreadID != "" && ownedThreadID == deletionThreadID(ref, threadID)
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
			SerfErrorInfo:    appwire.ErrorActionUnavailable,
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

func managedLaunchSourceIDForRef(cfg hubcore.WebConfig, ref string) (string, bool) {
	if ref == "" || cfg.CodexLauncher == nil {
		return "", false
	}
	parsed, err := appwire.ParseRef(ref)
	if err != nil || parsed.SourceID == "local" || !cfg.CodexLauncher.Manages(parsed.SourceID) {
		return "", false
	}
	return parsed.SourceID, true
}

// hubKnowsRef reports whether the hub recognizes ref: either as a
// managed-launch source (e.g. codex) or as a thread tracked in the local past
// index. Used to gate auto-resume retries after live-action failures so that
// non-local refs (which never appear in the local past index) still get the
// retry when their backing daemon dies.
func hubKnowsRef(cfg hubcore.WebConfig, ref string) bool {
	if _, ok := managedLaunchSourceIDForRef(cfg, ref); ok {
		return true
	}
	_, ok, _ := pastThreadForRead(cfg, appwire.ThreadReadParams{Ref: ref})
	return ok
}
