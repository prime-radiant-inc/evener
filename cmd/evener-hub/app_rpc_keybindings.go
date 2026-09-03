package hub

import (
	"context"
	"encoding/json"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func registerKeybindingsHandlers(server *appserver.Server, store *hubcore.KeybindingsStore) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsKeybindingsGet,
		func(context.Context, appwire.EmptyParams) (appwire.KeybindingsOverrides, error) {
			return store.Snapshot(), nil
		})
	server.Router().Handle(appwire.MethodEvenerSettingsKeybindingsPatch,
		func(_ context.Context, raw json.RawMessage) (any, error) {
			params, err := appwire.DecodeKeybindingsPatchParams(raw)
			if err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
			result, err := store.Patch(params)
			if err != nil {
				return nil, err
			}
			if result.Revision != params.ExpectedRevision {
				server.BroadcastAll(appwire.NotifyEvenerSettingsKeybindingsChanged, result)
			}
			return result, nil
		})
}
