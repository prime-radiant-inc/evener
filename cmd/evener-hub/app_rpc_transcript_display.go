package hub

import (
	"context"
	"encoding/json"

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
			// A patch whose rename landed and whose follow-up failed has
			// applied, and carries the state it published: the canonical state
			// goes out or every other client stays on the pre-patch revision
			// (the rule the keybindings post-rename path already follows).
			if writeDidApply(err) && (err != nil || result.Revision != params.ExpectedRevision) {
				server.BroadcastAll(appwire.NotifyEvenerSettingsTranscriptDisplayChanged,
					appwire.TranscriptDisplayChangedParams(result))
			}
			if err != nil {
				if writeDidApply(err) {
					// The broadcast above already reconciles every OTHER
					// client; the REQUESTING client only sees this response,
					// so it carries the same applied state - otherwise the
					// client that asked is the one client left treating an
					// applied write as rejected.
					return nil, appwire.WireError{
						Code:    appwire.CodeInternalError,
						Message: err.Error(),
						Data: appwire.TranscriptDisplayPostApplyData{
							EvenerErrorInfo: appwire.ErrorTranscriptDisplayPostApply,
							Layout:          result.Layout,
							Applied:         appwire.TranscriptDisplayDefault{Revision: result.Revision, Config: result.Config},
						},
					}
				}
				return nil, err
			}
			return result, nil
		})
}
