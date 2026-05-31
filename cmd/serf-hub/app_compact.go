package main

import (
	"context"
	"errors"

	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
)

func shouldResumeAfterTurnStartError(err error) bool {
	return shouldResumeAfterSessionUnavailable(err)
}

func shouldResumeAfterSessionUnavailable(err error) bool {
	return isSessionUnavailableError(err)
}

func isSessionUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	return wire.Code == appwire.CodeUnavailable && serfErrorInfoFromData(wire.Data) == string(appwire.ErrorSessionUnavailable)
}

func compactThreadWithResume(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadCompactStartParams) error {
	err := compactThreadOnce(ctx, cfg, sources, params)
	if err == nil {
		return nil
	}
	if params.Ref != "" && !hubKnowsRef(cfg, params.Ref) {
		return err
	}
	if !shouldResumeAfterSessionUnavailable(err) {
		return err
	}
	if _, resumeErr := hubThreadResume(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref}); resumeErr != nil {
		return resumeErr
	}
	return compactThreadOnce(ctx, cfg, sources, params)
}

func compactThreadOnce(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadCompactStartParams) error {
	source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
	if err != nil {
		return err
	}
	if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "compact"); err != nil {
		return err
	}
	return source.CompactThread(ctx, params)
}

func ensureThreadActionAvailable(ctx context.Context, source appsource.Source, ref, threadID, action string) error {
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, ThreadID: threadID, IncludeTurns: false})
	if err != nil {
		return err
	}
	if threadActionAvailable(resp.Thread.Serf.Capabilities, action) {
		return nil
	}
	return appwire.Unavailable(action + " is not available for this session")
}

func threadActionAvailable(caps appwire.ThreadCapabilities, action string) bool {
	switch action {
	case "send":
		return caps.Send
	case "steer":
		return caps.Steer
	case "interrupt":
		return caps.Interrupt
	case "compact":
		return caps.Compact
	case "clear":
		return caps.Clear
	case "fork":
		return caps.ForkFromTurn
	case "shutdown":
		return caps.Shutdown
	case "model":
		return caps.ChangeModel
	case "queue":
		return caps.Queue
	default:
		return false
	}
}
