package appsource

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/appwire"
)

// remoteHubNamespace is the source ID a remote hub always uses for its own
// local sessions (cmd/evener-hub builds ServerConfig{SourceID: "local"} and
// registers NewLocalDaemonSourceWithEntries("local", ...)). A remote hub's refs
// are therefore always "local:<thread>". Any other source ID in a remote
// response cannot be represented in the controller's source namespace without
// collision (appwire/refs.go splits on the first colon only), so it is refused
// rather than passed through.
const remoteHubNamespace = "local"

// toRemoteRef translates a controller-side address into the remote hub's
// namespace. An empty rawRef keeps the bare threadID (which may itself be
// empty) and defaults the source to "local". A non-empty rawRef must name this
// source; any other source is refused with "source not found: <id>" rather than
// silently retargeted, mirroring LocalDaemonSource.localEntryForRefMode.
func (s *RemoteHubSource) toRemoteRef(rawRef, threadID string) (appwire.Ref, error) {
	rawRef = strings.TrimSpace(rawRef)
	if rawRef == "" {
		return appwire.Ref{SourceID: remoteHubNamespace, ThreadID: threadID}, nil
	}
	ref, err := appwire.ParseRef(rawRef)
	if err != nil {
		return appwire.Ref{}, err
	}
	if ref.SourceID != s.id {
		return appwire.Ref{}, fmt.Errorf("source not found: %s", ref.SourceID)
	}
	return appwire.Ref{SourceID: remoteHubNamespace, ThreadID: ref.ThreadID}, nil
}

// remapRemoteSourceIDs rewrites a thread/list SourceIDs filter from controller
// host names into the remote hub's namespace. Only this source's own ID is
// representable remotely ("local"); every other entry is dropped. An empty
// result leaves the remote list unfiltered, which is the controller's intent
// when only this host was selected: sourceAllowedForList gates whether the
// source is called at all, and the remote side does the actual filtering.
func remapRemoteSourceIDs(sourceID string, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == sourceID {
			out = append(out, remoteHubNamespace)
		}
	}
	return out
}

// fromRemoteRefString maps a remote "local:<thread>" ref back into the
// controller's "<host>:<thread>" namespace. An empty ref stays empty. A ref
// whose source is not "local" is a nested remote hub the controller cannot
// address; it is refused with a typed InternalError.
func (s *RemoteHubSource) fromRemoteRefString(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	ref, err := appwire.ParseRef(raw)
	if err != nil {
		return "", err
	}
	if ref.SourceID != remoteHubNamespace {
		return "", appwire.InternalError(fmt.Sprintf(
			"remote hub source %s: remote ref %q uses unsupported source %q; only %q is representable",
			s.id, raw, ref.SourceID, remoteHubNamespace))
	}
	return appwire.Ref{SourceID: s.id, ThreadID: ref.ThreadID}.String(), nil
}

// nestedSessionRefFields are the JSON field names whose values carry a session
// handle nested inside a larger payload: thread diagnostics
// (jobs[].transcriptRef, delegates[].transcriptRef), the job and delegate
// projections those arrays hold, and job-activity rows (ownerRef/childRef). A
// value that is the remote hub's own "local:<thread>" handle is translated into
// the controller namespace; every other value is left exactly as it arrived, so
// the opaque "job:<id>" and "proj:<project>:<thread>" handles keep working and a
// nested hub's ref is not silently rewritten. The top-level routing "ref" and
// "parentRef" fields are deliberately NOT in this set: routing depends on them,
// so an unrepresentable one must be refused rather than passed through (see
// translateNotification and translateThreadRaw).
var nestedSessionRefFields = map[string]struct{}{
	"transcriptRef": {},
	"childRef":      {},
	"ownerRef":      {},
	"sessionRef":    {},
}

// pendingEscalationsField is the EvenerThread field holding the redacted
// approval cards for the sandbox escalations currently blocked on a session.
// Each entry's "ref" is a session handle a client routes the card by (see
// appwire.SandboxEscalationRequested), so it moves into the controller
// namespace with the rest of the thread. It is translated structurally in
// translateThreadRaw rather than by adding "ref" to nestedSessionRefFields:
// "ref" is the routing key at every structural location it appears in, and that
// shared set is only for field names that always hold a session handle.
const pendingEscalationsField = "pendingEscalations"

// translatePendingEscalationsRaw rewrites the "ref" of every entry in a
// pendingEscalations JSON array into the controller namespace, preserving every
// other field. It never fails: a value it cannot decode is returned byte-for-
// byte, mirroring translateNestedRefs, and an unrepresentable ref (a nested
// hub's, an opaque handle) is left exactly as it arrived.
func (s *RemoteHubSource) translatePendingEscalationsRaw(raw json.RawMessage) json.RawMessage {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return raw
	}
	for index := range entries {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entries[index], &fields); err != nil || fields == nil {
			continue
		}
		handle, ok := fields["ref"]
		if !ok {
			continue
		}
		var rawRef string
		if err := json.Unmarshal(handle, &rawRef); err != nil {
			continue
		}
		encoded, err := json.Marshal(s.translateNestedRef(rawRef))
		if err != nil {
			continue
		}
		fields["ref"] = encoded
		entry, err := json.Marshal(fields)
		if err != nil {
			continue
		}
		entries[index] = entry
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return raw
	}
	return encoded
}

// opaquePayloadFields are JSON fields whose values are arbitrary model or tool
// JSON, not the hub's own structures: a turn's items ("turns", and the "raw"
// item payload), a delegate's result packet ("message"/"structuredResult").
// Their contents share this doc's field names by coincidence — a tool argument
// named transcriptRef is a tool argument — so the nested-ref walk must not
// descend into them. No session handle this source must translate ever lives
// there.
var opaquePayloadFields = map[string]struct{}{
	"turns":            {},
	"raw":              {},
	"message":          {},
	"structuredResult": {},
}

// translateNestedRef maps a nested session handle into the controller
// namespace. A value that is not the remote hub's own "local:<thread>" handle is
// returned unchanged: "job:<id>" and "proj:<project>:<thread>" are opaque to
// this translation, and a nested hub's ref is not addressable here.
func (s *RemoteHubSource) translateNestedRef(handle string) string {
	if handle == "" {
		return handle
	}
	translated, err := s.fromRemoteRefString(handle)
	if err != nil {
		return handle
	}
	return translated
}

// translateNestedRefs walks a raw JSON value and translates every nested session
// handle it finds, preserving every leaf it does not recognize. It never fails:
// a value it cannot decode is returned byte-for-byte, so a notification can
// never be dropped by this pass. OpaquePayloadFields subtrees are skipped
// entirely; see their comment.
func (s *RemoteHubSource) translateNestedRefs(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw
	}
	switch trimmed[0] {
	case '{':
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
			return raw
		}
		for key, value := range fields {
			if _, opaque := opaquePayloadFields[key]; opaque {
				continue
			}
			if _, ok := nestedSessionRefFields[key]; ok {
				var handle string
				if err := json.Unmarshal(value, &handle); err != nil {
					continue
				}
				encoded, err := json.Marshal(s.translateNestedRef(handle))
				if err != nil {
					continue
				}
				fields[key] = encoded
				continue
			}
			fields[key] = s.translateNestedRefs(value)
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return raw
		}
		return encoded
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return raw
		}
		for index := range items {
			items[index] = s.translateNestedRefs(items[index])
		}
		encoded, err := json.Marshal(items)
		if err != nil {
			return raw
		}
		return encoded
	default:
		return raw
	}
}

// fromRemoteThread rewrites every ref-bearing field of a thread the remote hub
// returned. Thread.Source becomes this source's ID; Evener.Ref and
// Evener.ParentRef (sub-thread aliases) move from "local:" to "<host>:".
// Evener.InstanceID is deliberately left byte-for-byte untouched: it is an
// opaque precondition token round-tripped into turn/start.expectedInstanceId.
// Nested session handles inside Evener.Diagnostics (a job's or delegate's
// transcriptRef) and Evener.PendingEscalations (each entry's ref, which clients
// route an escalation card by) are translated too; see translateNestedRef.
func (s *RemoteHubSource) fromRemoteThread(thread appwire.Thread) (appwire.Thread, error) {
	thread.Source = s.id
	ref, err := s.fromRemoteRefString(thread.Evener.Ref)
	if err != nil {
		return appwire.Thread{}, err
	}
	thread.Evener.Ref = ref
	if thread.Evener.ParentRef != "" {
		parent, err := s.fromRemoteRefString(thread.Evener.ParentRef)
		if err != nil {
			return appwire.Thread{}, err
		}
		thread.Evener.ParentRef = parent
	}
	if diagnostics := thread.Evener.Diagnostics; diagnostics != nil {
		for index := range diagnostics.Jobs {
			diagnostics.Jobs[index].TranscriptRef = s.translateNestedRef(diagnostics.Jobs[index].TranscriptRef)
		}
		for index := range diagnostics.Delegates {
			diagnostics.Delegates[index].TranscriptRef = s.translateNestedRef(diagnostics.Delegates[index].TranscriptRef)
		}
	}
	for index := range thread.Evener.PendingEscalations {
		thread.Evener.PendingEscalations[index].Ref = s.translateNestedRef(thread.Evener.PendingEscalations[index].Ref)
	}
	return thread, nil
}

// translateThreadRaw rewrites a thread object's ref-bearing fields at the JSON
// level, preserving every field this hub does not understand. Round-tripping
// through appwire.Thread would silently drop a field a newer remote hub sent —
// the same reason the notification translator works in RawMessage throughout.
// Only Source, Evener.Ref and Evener.ParentRef are rewritten; InstanceID and
// everything else pass through byte-for-byte. Nested session handles under
// Evener (diagnostics job/delegate transcriptRefs, and each pendingEscalations
// entry's ref) are rewritten as well.
func (s *RemoteHubSource) translateThreadRaw(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		// JSON null is not a thread object: pass it through byte-for-byte rather
		// than materializing an empty object and stamping a source on it.
		return raw, nil
	}
	source, err := json.Marshal(s.id)
	if err != nil {
		return nil, err
	}
	fields["source"] = source
	if evener, ok := fields["evener"]; ok && len(evener) > 0 {
		var evenerFields map[string]json.RawMessage
		if err := json.Unmarshal(evener, &evenerFields); err != nil {
			return nil, err
		}
		for _, key := range []string{"ref", "parentRef"} {
			field, ok := evenerFields[key]
			if !ok {
				continue
			}
			var rawRef string
			if err := json.Unmarshal(field, &rawRef); err != nil {
				return nil, err
			}
			if rawRef == "" {
				continue
			}
			translated, err := s.fromRemoteRefString(rawRef)
			if err != nil {
				return nil, err
			}
			encoded, err := json.Marshal(translated)
			if err != nil {
				return nil, err
			}
			evenerFields[key] = encoded
		}
		if rawEscalations, ok := evenerFields[pendingEscalationsField]; ok && len(rawEscalations) > 0 {
			evenerFields[pendingEscalationsField] = s.translatePendingEscalationsRaw(rawEscalations)
		}
		encoded, err := json.Marshal(evenerFields)
		if err != nil {
			return nil, err
		}
		fields["evener"] = s.translateNestedRefs(encoded)
	}
	return json.Marshal(fields)
}

// translateOut applies outbound ref translation to whichever response type
// embeds a Thread. Responses without refs (e.g. thread/turns/list, model/list)
// pass through unchanged.
func (s *RemoteHubSource) translateOut(out any) error {
	switch response := out.(type) {
	case *appwire.ThreadListResponse:
		for index := range response.Data {
			thread, err := s.fromRemoteThread(response.Data[index])
			if err != nil {
				return err
			}
			response.Data[index] = thread
		}
	case *appwire.ThreadReadResponse:
		thread, err := s.fromRemoteThread(response.Thread)
		if err != nil {
			return err
		}
		response.Thread = thread
	}
	return nil
}
