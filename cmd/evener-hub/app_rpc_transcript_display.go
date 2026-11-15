package hub

import (
	"context"
	"encoding/json"
	"errors"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func registerTranscriptDisplayHandlers(server *appserver.Server, store *hubcore.TranscriptDisplayStore) {
	// The store owns the post-commit notification (#1634): a patch that
	// advanced a layout's published revision broadcasts the canonical value,
	// including the post-apply-failure case. Wiring the port here, once,
	// removes the manual pairing a new handler could forget.
	store.OnCommitted(func(changed appwire.TranscriptDisplayChangedParams) {
		server.BroadcastAll(appwire.NotifyEvenerSettingsTranscriptDisplayChanged, changed)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsTranscriptDisplayGet,
		func(context.Context, appwire.EmptyParams) (appwire.TranscriptDisplayDefaults, error) {
			return store.Snapshot(), nil
		})
	server.Router().Handle(appwire.MethodEvenerSettingsTranscriptDisplayPatch,
		func(_ context.Context, raw json.RawMessage) (any, error) {
			params, err := appwire.DecodeTranscriptDisplayDefaultsPatchParams(raw)
			if err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
			result, err := store.Patch(params)
			if err != nil {
				// A post-apply durable error means the patch APPLIED: the
				// store already published the new revision and broadcast it
				// through OnCommitted. Carry that same value in the error so
				// the REQUESTING client can reconcile from it instead of
				// treating its write as rejected (mirrors
				// KeybindingsPostRenameError's rule in app_rpc_keybindings.go).
				if postApply, ok := errors.AsType[*hubcore.TranscriptDisplayPostApplyError](err); ok {
					return nil, appwire.WireError{
						Code:    appwire.CodeInternalError,
						Message: err.Error(),
						Data: appwire.TranscriptDisplayPostApplyData{
							EvenerErrorInfo: appwire.ErrorTranscriptDisplayPostApply,
							Layout:          postApply.Layout,
							Applied:         postApply.Applied,
						},
					}
				}
				return nil, err
			}
			return result, nil
		})
}
