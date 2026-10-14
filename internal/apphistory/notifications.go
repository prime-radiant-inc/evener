package apphistory

import (
	"encoding/json"
	"sync"

	"primeradiant.com/evener/appwire"
)

// Filter belongs to one downstream subscription generation. A replacement
// creates a new filter; rolling the subscription back restores its old filter.
type Filter struct {
	mu        sync.Mutex
	selection appwire.ThreadContentSelection
	overlays  map[string]overlaySlot
}

type overlaySlot struct {
	streamID string
	roundID  string
	tool     bool
}

func NewFilter(selection appwire.ThreadContentSelection) *Filter {
	if selection == 0 {
		return nil
	}
	return &Filter{selection: selection, overlays: map[string]overlaySlot{}}
}

// Seed registers the selected snapshot's overlays before buffered continuation
// is released. Only identities are retained, never growing streamed payloads.
func (f *Filter) Seed(items []appwire.OverlayItem) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, overlay := range items {
		f.remember(overlay)
	}
}

func (f *Filter) remember(overlay appwire.OverlayItem) {
	// Notices have no deltas or end events, and the producer evicts them
	// silently. Only live streams need identity tracking here.
	if overlay.Kind != appwire.OverlayNotice {
		f.overlays[overlay.Key] = slot(overlay)
	}
}

func slot(overlay appwire.OverlayItem) overlaySlot {
	return overlaySlot{streamID: overlay.StreamID, roundID: overlay.RoundID, tool: overlay.Item.Type == "commandExecution"}
}

// Notification filters a value without mutating the shared recorded frame.
// Identity-only history updates and lifecycle messages still reach the reader.
func (f *Filter) Notification(n appwire.Notification) (appwire.Notification, bool, error) {
	if f == nil {
		return n, true, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch n.Method {
	case appwire.NotifyHistoryUpdated:
		var params appwire.HistoryUpdatedParams
		if err := json.Unmarshal(n.Params, &params); err != nil {
			return n, false, err
		}
		params.Items = f.selection.Items(params.Items)
		params.Snapshot = *Snapshot(&params.Snapshot, f.selection)
		return replaceFields(n, map[string]any{"items": params.Items, "snapshot": params.Snapshot})
	case appwire.NotifyOverlayUpserted:
		var params appwire.OverlayUpsertedParams
		if err := json.Unmarshal(n.Params, &params); err != nil {
			return n, false, err
		}
		item, keep := f.selection.Item(params.Item.Item)
		if !keep {
			delete(f.overlays, params.Item.Key)
			return n, false, nil
		}
		params.Item.Item = item
		f.remember(params.Item)
		return replaceFields(n, map[string]any{"item": params.Item})
	case appwire.NotifyOverlayDelta:
		var params appwire.OverlayDeltaParams
		if err := json.Unmarshal(n.Params, &params); err != nil {
			return n, false, err
		}
		overlay, keep := f.overlays[params.Key]
		if overlay.tool {
			keep = keep && f.selection.ToolCalls()
		}
		return n, keep, nil
	case appwire.NotifyOverlayReset:
		var params appwire.OverlayResetParams
		if err := json.Unmarshal(n.Params, &params); err != nil {
			return n, false, err
		}
		for key, overlay := range f.overlays {
			if overlay.streamID == params.StreamID {
				delete(f.overlays, key)
			}
		}
	case appwire.NotifyOverlayEnd:
		var params appwire.OverlayEndParams
		if err := json.Unmarshal(n.Params, &params); err != nil {
			return n, false, err
		}
		for key, overlay := range f.overlays {
			if overlay.roundID == params.RoundID {
				delete(f.overlays, key)
			}
		}
	}
	return n, true, nil
}

func replaceFields(n appwire.Notification, fields map[string]any) (appwire.Notification, bool, error) {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(n.Params, &params); err != nil {
		return n, false, err
	}
	for key, value := range fields {
		raw, err := json.Marshal(value)
		if err != nil {
			return n, false, err
		}
		params[key] = raw
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return n, false, err
	}
	n.Params = raw
	return n, true, nil
}
