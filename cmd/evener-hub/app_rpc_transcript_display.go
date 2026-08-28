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
			if err != nil {
				return nil, err
			}
			if result.Revision != params.ExpectedRevision {
				server.BroadcastAll(appwire.NotifyEvenerSettingsTranscriptDisplayChanged,
					appwire.TranscriptDisplayChangedParams(result))
			}
			return result, nil
		})
}
