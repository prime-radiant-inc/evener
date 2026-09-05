package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func registerKeybindingsHandlers(server *appserver.Server, store *hubcore.KeybindingsStore) {
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
				// A post-rename durable error means the patch APPLIED: the
				// store already published the new revision. Returning the
				// error without broadcasting would leave every other client
				// on the pre-patch revision, so fan out the canonical
				// snapshot before surfacing the failure - and carry that same
				// snapshot in the error itself so the REQUESTING client can
				// reconcile from it instead of treating its write as rejected
				// (roborev PR #884 round 2).
				if _, postRename := errors.AsType[*hubcore.KeybindingsPostRenameError](err); postRename {
					snapshot := store.Snapshot()
					server.BroadcastAll(appwire.NotifyEvenerSettingsKeybindingsChanged, snapshot)
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
			if result.Revision != params.ExpectedRevision {
				server.BroadcastAll(appwire.NotifyEvenerSettingsKeybindingsChanged, result)
			}
			return result, nil
		})
}
