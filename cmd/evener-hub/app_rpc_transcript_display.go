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
				// store already published the new revision. Returning the
				// error without broadcasting would leave every other client
				// on the pre-patch revision, so fan out the applied layout
				// before surfacing the failure - and carry that same value in
				// the error itself so the REQUESTING client can reconcile
				// from it instead of treating its write as rejected (mirrors
				// KeybindingsPostRenameError's rule in app_rpc_keybindings.go).
				if postApply, ok := errors.AsType[*hubcore.TranscriptDisplayPostApplyError](err); ok {
					server.BroadcastAll(appwire.NotifyEvenerSettingsTranscriptDisplayChanged, appwire.TranscriptDisplayChangedParams{
						Layout:   postApply.Layout,
						Revision: postApply.Applied.Revision,
						Config:   postApply.Applied.Config,
					})
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
			if result.Revision != params.ExpectedRevision {
				server.BroadcastAll(appwire.NotifyEvenerSettingsTranscriptDisplayChanged,
					appwire.TranscriptDisplayChangedParams(result))
			}
			return result, nil
		})
}
