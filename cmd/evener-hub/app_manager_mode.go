package hub

import (
	"context"
	"errors"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func managerSavedPreferenceRef(sourceID string, meta schema.SessionMeta) string {
	workspaceID := meta.WorkspaceID
	if workspaceID == "" {
		workspaceID = meta.ID
	}
	return appwire.Ref{SourceID: sourceID, ThreadID: workspaceID}.String()
}

// managerPreferenceRef is private storage identity, not a replacement navigation
// ref. Historical instances remain independently addressable after Clear. Their
// explicit WorkspaceID shares the preference with the live workspace, while a
// legacy saved session without that field owns only its own ID.
func managerPreferenceRef(cfg hubcore.WebConfig, thread appwire.Thread) (string, error) {
	ref, err := appwire.ParseRef(thread.Evener.Ref)
	if err != nil || ref.SourceID != "local" || thread.Evener.InstanceID == "" {
		return "", appwire.Unavailable("session workspace identity is unresolved")
	}
	if cfg.Past != nil {
		if entry, ok := cfg.Past.Find(thread.Evener.InstanceID); ok {
			return managerSavedPreferenceRef(ref.SourceID, entry.Meta), nil
		}
	}
	// A bound live read already supplies the runtime workspace identity. A cold
	// read needs its saved metadata, not a guessed key from the requested alias.
	if thread.Status.Type == appwire.ThreadStatusNotLoaded {
		return "", appwire.Unavailable("saved session workspace identity is unavailable")
	}
	return ref.String(), nil
}

// projectManagerMode overlays only this hosting hub's local preference. Remote
// settings need a connection-bound authority operation, which is not shipped.
func projectManagerMode(cfg hubcore.WebConfig, thread appwire.Thread) appwire.Thread {
	thread.Evener.ManagerEditable = false
	ref, err := appwire.ParseRef(thread.Evener.Ref)
	if err != nil || ref.SourceID != "local" {
		return thread
	}
	thread.Evener.Manager = false
	thread.Evener.ManagerSetting = nil
	if cfg.SessionPreferences == nil {
		return thread
	}
	key, err := managerPreferenceRef(cfg, thread)
	if err != nil {
		return thread
	}
	setting, err := cfg.SessionPreferences.Manager(key)
	if err != nil {
		return thread
	}
	thread.Evener.Manager = setting.Enabled
	thread.Evener.ManagerSetting = &setting
	hubID, err := cfg.SessionPreferences.HubID()
	thread.Evener.ManagerEditable = err == nil && (!setting.Enabled || setting.HubID == hubID)
	return thread
}

func managerSetError(err error, view *appwire.ManagerView) error {
	wire := appwire.Unavailable(err.Error())
	info := appwire.ErrorSessionUnavailable
	if errors.Is(err, hubcore.ErrManagerAuthority) {
		wire = appwire.Conflict(err.Error())
		info = appwire.ErrorConflict
	}
	wire.Data = appwire.ThreadManagerSetErrorData{ErrorData: appwire.ErrorData{EvenerErrorInfo: info}, View: view}
	return wire
}

func registerThreadManagerSetHandler(server *appserver.Server, cfg hubcore.WebConfig, readThread func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error), resyncThread func(appwire.Thread, string)) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerThreadManagerSet, func(ctx context.Context, params appwire.ThreadManagerSetParams) (appwire.ThreadManagerSetResponse, error) {
		var response appwire.ThreadManagerSetResponse
		requested, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return response, appwire.InvalidParams(err.Error())
		}
		if requested.SourceID != "local" {
			return response, appwire.Unavailable("Manager settings on remote hosts are not supported yet")
		}
		if cfg.SessionPreferences == nil {
			return response, appwire.Unavailable("session preferences storage is not configured")
		}
		read, err := readThread(ctx, appwire.ThreadReadParams{Ref: requested.String()})
		if err != nil {
			return response, err
		}
		key, err := managerPreferenceRef(cfg, read.Thread)
		if err != nil {
			return response, err
		}
		setting, err := cfg.SessionPreferences.Manager(key)
		if err != nil {
			return response, managerSetError(err, nil)
		}
		view := appwire.ManagerView{Ref: read.Thread.Evener.Ref, InstanceID: read.Thread.Evener.InstanceID, Setting: setting}
		hubID, err := cfg.SessionPreferences.HubID()
		if err != nil {
			return response, managerSetError(err, &view)
		}
		current, err := cfg.SessionPreferences.SetManager(key, hubID, params.Enabled)
		if err != nil {
			// Read again after a failed operation: both authority conflicts and storage
			// failures must survive the router's error-only response. If the read fails
			// too, omit the view rather than fabricate a current disabled setting.
			observed, readErr := cfg.SessionPreferences.Manager(key)
			if readErr != nil {
				return response, managerSetError(err, nil)
			}
			view.Setting = observed
			return response, managerSetError(err, &view)
		}
		view.Setting = current
		response.View = view
		if current.Revision != setting.Revision {
			resyncThread(read.Thread, key)
		}
		return response, nil
	})
}
