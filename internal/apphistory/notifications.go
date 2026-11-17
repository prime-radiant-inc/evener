package apphistory

import (
	"encoding/json"
	"maps"
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

// Selection reports the content selection this filter applies. It is fixed at
// construction and never reassigned, so a fanout can read it without the filter
// lock to tell which encoded frame a delivery needs.
func (f *Filter) Selection() appwire.ThreadContentSelection {
	if f == nil {
		return 0
	}
	return f.selection
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

// PreparedNotification is one recorded notification's payload decoded for
// filtering to any number of content selections. Decoding is a pure function of
// the payload, so one value serves every subscriber of the record: a fanout
// decodes a payload once per recorded notification instead of once per
// subscriber.
//
// Two decodes of the same bytes are needed, not one. Filtering a selected view
// has to read typed members — items, overlay identities, snapshot identity —
// and it has to re-emit the payload with those members replaced while leaving
// every other member and the member ordering alone (a client's bytes must not
// change). Re-encoding from the typed value would drop members this build does
// not know, so the replaced members are spliced into the payload's decoded
// members instead. Both decodes happen once here, per record, rather than once
// per subscriber.
//
// A value is used by one goroutine at a time and is built afresh for each
// record, so it carries no state that could go stale.
type PreparedNotification struct {
	notification appwire.Notification
	method       string
	// decodeErr is the payload failing to decode into its typed params. It is
	// reported only to a reader whose selection inspects the payload: a full
	// view receives the recorded bytes untouched, decode failure or not.
	decodeErr error
	// params is the payload's members, decoded once so each selected view
	// splices its replaced members in instead of decoding again.
	params   map[string]json.RawMessage
	history  *appwire.HistoryUpdatedParams
	upserted *appwire.OverlayUpsertedParams
	delta    *appwire.OverlayDeltaParams
	reset    *appwire.OverlayResetParams
	end      *appwire.OverlayEndParams
}

// PrepareNotification decodes a notification's payload for filtering. A method
// no filter inspects decodes nothing, so preparing one costs nothing.
func PrepareNotification(n appwire.Notification) PreparedNotification {
	prepared := PreparedNotification{notification: n, method: n.Method}
	switch n.Method {
	case appwire.NotifyHistoryUpdated:
		params := appwire.HistoryUpdatedParams{}
		prepared.decodeErr = json.Unmarshal(n.Params, &params)
		prepared.history = &params
	case appwire.NotifyOverlayUpserted:
		params := appwire.OverlayUpsertedParams{}
		prepared.decodeErr = json.Unmarshal(n.Params, &params)
		prepared.upserted = &params
	case appwire.NotifyOverlayDelta:
		params := appwire.OverlayDeltaParams{}
		prepared.decodeErr = json.Unmarshal(n.Params, &params)
		prepared.delta = &params
	case appwire.NotifyOverlayReset:
		params := appwire.OverlayResetParams{}
		prepared.decodeErr = json.Unmarshal(n.Params, &params)
		prepared.reset = &params
	case appwire.NotifyOverlayEnd:
		params := appwire.OverlayEndParams{}
		prepared.decodeErr = json.Unmarshal(n.Params, &params)
		prepared.end = &params
	default:
		return prepared
	}
	if prepared.Reencodes() {
		// A typed decode that succeeds proves the payload is a JSON object, so
		// this decode succeeds too; a typed decode that failed is reported
		// before the members are ever read.
		if prepared.decodeErr == nil {
			prepared.decodeErr = json.Unmarshal(n.Params, &prepared.params)
		}
	}
	return prepared
}

// Reencodes reports whether filtering rebuilds this notification's payload. The
// methods that only decide whether to drop a frame — overlay deltas and
// lifecycle messages — reach a reader exactly as they were recorded.
func (p PreparedNotification) Reencodes() bool {
	return p.method == appwire.NotifyHistoryUpdated || p.method == appwire.NotifyOverlayUpserted
}

// Frame builds the notification a reader with this selection receives, and
// reports whether the selection keeps it. Frame is pure — it reads no filter
// state — so the same selection always yields the same bytes, and a fanout can
// share one frame across every subscriber holding that selection.
func (p *PreparedNotification) Frame(selection appwire.ThreadContentSelection) (appwire.Notification, bool, error) {
	// A full view has no filter, so it receives the recorded frame as it was
	// recorded, including a payload this build cannot decode.
	if selection == 0 || !p.Reencodes() {
		return p.notification, true, nil
	}
	if p.decodeErr != nil {
		return appwire.Notification{}, false, p.decodeErr
	}
	switch p.method {
	case appwire.NotifyHistoryUpdated:
		params := *p.history
		params.Items = selection.Items(params.Items)
		params.Snapshot = *Snapshot(&params.Snapshot, selection)
		return replaceFields(p.notification, p.params, map[string]any{"items": params.Items, "snapshot": params.Snapshot})
	case appwire.NotifyOverlayUpserted:
		item, keep := selection.Item(p.upserted.Item.Item)
		if !keep {
			return appwire.Notification{}, false, nil
		}
		overlay := p.upserted.Item
		overlay.Item = item
		return replaceFields(p.notification, p.params, map[string]any{"item": overlay})
	}
	return p.notification, true, nil
}

// Decide applies this filter's live state to a prepared notification and
// reports whether the reader keeps it. It is the one part of filtering that is
// per-subscription rather than per-selection: an upsert teaches the identities
// a later delta needs, and a reset or end forgets them. Decide never encodes, so
// a fanout calls it for every delivery in order while the payload's frame is
// encoded once per distinct selection.
func (f *Filter) Decide(p *PreparedNotification) (bool, error) {
	if f == nil {
		return true, nil
	}
	if p.decodeErr != nil {
		return false, p.decodeErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch p.method {
	case appwire.NotifyOverlayUpserted:
		item, keep := f.selection.Item(p.upserted.Item.Item)
		if !keep {
			delete(f.overlays, p.upserted.Item.Key)
			return false, nil
		}
		overlay := p.upserted.Item
		overlay.Item = item
		f.remember(overlay)
		return true, nil
	case appwire.NotifyOverlayDelta:
		overlay, keep := f.overlays[p.delta.Key]
		if overlay.tool {
			keep = keep && f.selection.ToolCalls()
		}
		return keep, nil
	case appwire.NotifyOverlayReset:
		for key, overlay := range f.overlays {
			if overlay.streamID == p.reset.StreamID {
				delete(f.overlays, key)
			}
		}
	case appwire.NotifyOverlayEnd:
		for key, overlay := range f.overlays {
			if overlay.roundID == p.end.RoundID {
				delete(f.overlays, key)
			}
		}
	}
	return true, nil
}

// Notification filters a value without mutating the shared recorded frame.
// Identity-only history updates and lifecycle messages still reach the reader.
// This is the single-subscriber form of the decode-decide-encode steps a fanout
// shares across many.
func (f *Filter) Notification(n appwire.Notification) (appwire.Notification, bool, error) {
	prepared := PrepareNotification(n)
	keep, err := f.Decide(&prepared)
	if err != nil || !keep {
		return n, false, err
	}
	frame, frameKeep, err := prepared.Frame(f.Selection())
	if err != nil || !frameKeep {
		return n, false, err
	}
	return frame, true, nil
}

// replaceFields returns a copy of the notification whose payload has fields
// replaced, preserving every other member. params is the already-decoded
// payload: it is copied rather than mutated, so one decode serves every reader
// of the record.
func replaceFields(n appwire.Notification, params map[string]json.RawMessage, fields map[string]any) (appwire.Notification, bool, error) {
	spliced := make(map[string]json.RawMessage, len(params)+len(fields))
	maps.Copy(spliced, params)
	for key, value := range fields {
		raw, err := json.Marshal(value)
		if err != nil {
			return n, false, err
		}
		spliced[key] = raw
	}
	raw, err := json.Marshal(spliced)
	if err != nil {
		return n, false, err
	}
	n.Params = raw
	return n, true, nil
}
