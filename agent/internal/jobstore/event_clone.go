package jobstore

import "primeradiant.com/serf/agent/provenance"

// cloneEvent returns a copy of e that shares no mutable state with it: every
// pointer, slice and map reachable from the copy is its own.
//
// The Store hands every loaded event out through this. Its tail cursor keeps
// decoded events across loads, and a load's result must stay what it has always
// been — a private snapshot. Callers do write into what a load returned (agent
// tests plant bogus durable state by reaching into folded records, and such a
// write must not reach
// back into the cursor and change what a later load reports.
//
// Every reference-carrying field of Event must be handled here.
// TestCloneEventCoversEveryReferenceField walks Event's type graph and fails
// when a new one appears.
func cloneEvent(e Event) Event {
	out := e
	out.StartedAt = clonePtr(e.StartedAt)
	out.EndedAt = clonePtr(e.EndedAt)
	out.ExitCode = clonePtr(e.ExitCode)
	out.StructuredResultValid = clonePtr(e.StructuredResultValid)
	out.StructuredResult = cloneJSONValue(e.StructuredResult)
	out.Provenance = cloneCausal(e.Provenance)
	out.WatchSend = cloneWatchSend(e.WatchSend)
	out.Watch = cloneWatchEvent(e.Watch)
	return out
}

// clonePtr copies the pointee. Only for types whose fields are all values.
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// cloneSlice copies a slice, preserving the nil/empty distinction so a clone
// stays byte-identical to the same event decoded fresh from the log.
func cloneSlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	return out
}

// cloneJSONValue deep-copies a value decoded by encoding/json into an any: the
// containers (object, array) are rebuilt and the scalars (string, float64,
// bool, nil) are immutable values that copy by assignment. Cursor-held events
// come only from json.Unmarshal, so no other shape reaches here.
func cloneJSONValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = cloneJSONValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = cloneJSONValue(val)
		}
		return out
	default:
		return v
	}
}

// cloneCausal deep-copies causal provenance verbatim. provenance.Clone is not
// usable here: it also truncates the chain and nils an empty value, so it would
// not reproduce what the log holds.
func cloneCausal(p *provenance.Causal) *provenance.Causal {
	if p == nil {
		return nil
	}
	out := *p
	out.WatchKeys = cloneSlice(p.WatchKeys)
	out.Chain = cloneSlice(p.Chain)
	return &out
}

func cloneWatchSend(ws *WatchSendState) *WatchSendState {
	if ws == nil {
		return nil
	}
	out := *ws
	out.Provenance = cloneCausal(ws.Provenance)
	return &out
}

func cloneWatchEvent(w *WatchEvent) *WatchEvent {
	if w == nil {
		return nil
	}
	out := *w
	out.Config = cloneWatchConfigSnapshot(w.Config)
	return &out
}

func cloneWatchConfigSnapshot(c *WatchConfigSnapshot) *WatchConfigSnapshot {
	if c == nil {
		return nil
	}
	out := *c
	out.Events = cloneSlice(c.Events)
	// WatchEventFilterSnapshot is strings only.
	out.EventFilter = clonePtr(c.EventFilter)
	return &out
}
