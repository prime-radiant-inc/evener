package hub

import (
	"context"
	"errors"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func isSessionUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	return wire.Code == appwire.CodeUnavailable && evenerErrorInfoFromData(wire.Data) == string(appwire.ErrorSessionUnavailable)
}

func compactThreadWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadCompactStartParams) error {
	_, err := relayWithResume(ctx, cfg, sources, params.Ref, "", "compact",
		func(source appsource.Source) (struct{}, error) {
			return struct{}{}, source.CompactThread(ctx, params)
		})
	return err
}

func clearThreadWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return relayWithResume(ctx, cfg, sources, params.Ref, params.ClientMutationID, "clear",
		func(source appsource.Source) (appwire.ThreadClearResponse, error) {
			return source.ClearThread(ctx, params)
		})
}

func ensureThreadActionAvailable(ctx context.Context, source appsource.Source, ref, threadID, action string) error {
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, ThreadID: threadID, IncludeTurns: false})
	if err != nil {
		return err
	}
	if threadActionAvailable(resp.Thread.Evener.Capabilities, action) {
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
	case "vision-model":
		return caps.ChangeVisionModel
	case "rename":
		return caps.Rename
	case "queue":
		return caps.Queue
	case "goal":
		return caps.Goal
	case "shared-notes":
		return caps.SharedNotes
	default:
		return false
	}
}
