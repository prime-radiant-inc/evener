package main

import (
	"context"
	"fmt"

	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
)

func sourceForThread(sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	if ref != "" {
		return sources.SourceForRef(ref)
	}
	source, ok := sources.Source("local")
	if !ok {
		return nil, fmt.Errorf("source not found: local")
	}
	if threadID == "" {
		return source, nil
	}
	return source, nil
}

func sourceForThreadWithManagedLaunch(ctx context.Context, cfg WebConfig, sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	if sourceID, ok := managedLaunchSourceIDForRef(cfg, ref); ok {
		return cfg.CodexLauncher.EnsureSource(ctx, sourceID, sources)
	}
	return sourceForThread(sources, ref, threadID)
}

func managedLaunchSourceIDForRef(cfg WebConfig, ref string) (string, bool) {
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
func hubKnowsRef(cfg WebConfig, ref string) bool {
	if _, ok := managedLaunchSourceIDForRef(cfg, ref); ok {
		return true
	}
	_, ok := pastThreadForRead(cfg, appwire.ThreadReadParams{Ref: ref})
	return ok
}
