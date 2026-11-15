package hub

import (
	"context"
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func registerKeybindingsHandlers(server *appserver.Server, store *hubcore.KeybindingsStore) {
	// The store owns the post-commit notification: a patch that advanced the
	// published revision broadcasts the canonical snapshot even when the
	// handler sees an error. Wiring it here, once, removes the manual pairing
	// a new handler could forget (#1634).
	store.OnCommitted(func(overrides appwire.KeybindingsOverrides) {
		server.BroadcastAll(appwire.NotifyEvenerSettingsKeybindingsChanged, overrides)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsKeybindingsGet,
		func(context.Context, appwire.EmptyParams) (appwire.KeybindingsOverrides, error) {
			overrides := store.Snapshot()
			// A malformed persisted state loads as the shipped-default fallback
			// and PATCH rejects - surface the diagnostic so clients keep the
			// editor read-only with an explanation instead of failing every
			// save (roborev PR #884 round 6). Same wording as Patch's rejection.
			if loadErr := store.LoadErr(); loadErr != nil {
				overrides.LoadError = fmt.Sprintf("keybindings state is unavailable: %v", loadErr)
			}
			return overrides, nil
		})
	server.Router().Handle(appwire.MethodEvenerSettingsKeybindingsPatch,
		func(_ context.Context, raw json.RawMessage) (any, error) {
			params, err := appwire.DecodeKeybindingsPatchParams(raw)
			if err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
			result, err := store.Patch(params)
			if err != nil {
				// A post-rename durable error means the patch APPLIED -
				// writeDidApply reads that back from the store's own
				// hubcore.ErrWriteApplied. Every other client needs the
				// canonical snapshot or it stays on the pre-patch revision,
				// and the error carries the same snapshot so the REQUESTING
				// client reconciles instead of treating its write as rejected
				// (roborev PR #884 round 2).
				if writeDidApply(err) {
					snapshot := store.Snapshot()
					return nil, appwire.WireError{
						Code:    appwire.CodeInternalError,
						Message: err.Error(),
						Data: appwire.KeybindingsPostRenameData{
							EvenerErrorInfo: appwire.ErrorKeybindingsPostRename,
							Applied:         snapshot,
						},
					}
				}
				return nil, err
			}
			return result, nil
		})
}
