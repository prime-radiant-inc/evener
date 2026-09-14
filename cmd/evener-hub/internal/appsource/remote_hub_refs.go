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

// remoteForwardedThreadCapabilities lists the thread actions this source can
// actually forward to the remote hub. It is the controller-side half of the
// capability answer: maskRemoteThreadCapabilities intersects it with the remote
// hub's own claim, so a later component re-enables an action by adding it here
// once its method stops returning notImplemented (remote_hub_source.go):
//
//   - Send (StartTurn, ResumeThread), Steer (SteerTurn), Interrupt
//     (InterruptTurn), Queue (QueueTurn, DrainAsSteer, PromoteQueuedAsSteer,
//     CancelQueued), Compact (CompactThread), Clear (ClearThread), ForkFromTurn
//     (ForkThread), Shutdown (ShutdownThread), ChangeModel (SetThreadModel,
//     SetThreadReasoningEffort), ChangeVisionModel (SetThreadVisionModel),
//     Goal (GoalSet), SharedNotes (NotesHumanSet, UrlsRemove), and Rename
//     (SetThreadName) are the turn mutations and lifecycle verbs 05c forwards.
//   - SkillInput rides on the input-bearing turn mutations
//     (appwire.ValidateSkillInputSupport), so 05c re-enables it with Send.
//
// 05d replaces this constant answer with a completed capability probe of the host,
// intersected with the remote hub's claim; until then the intersection is empty
// and every action is masked.
func remoteForwardedThreadCapabilities() appwire.ThreadCapabilities {
	return appwire.ThreadCapabilities{}
}

// maskRemoteThreadCapabilities intersects the capability set a remote hub reported
// for its own thread with the actions this source can carry today.
//
// The remote hub reports the capabilities of its OWN session daemon: they describe
// what the remote daemon can do, not what this controller can forward. On this
// branch (05a) every mutating, lifecycle, and subscription method of
// RemoteHubSource still returns notImplemented, so forwarding the remote's set
// verbatim would advertise send, fork, rename, model, and queue actions that fail
// with an internal error the moment a client used one (the hub gates each of them
// on exactly these fields — cmd/evener-hub's threadActionAvailable).
//
// Fields are listed one by one rather than copied wholesale so a capability added
// to appwire.ThreadCapabilities later starts masked until a component names the
// method that answers for it.
func maskRemoteThreadCapabilities(remote appwire.ThreadCapabilities) appwire.ThreadCapabilities {
	forwarded := remoteForwardedThreadCapabilities()
	return appwire.ThreadCapabilities{
		Send:              remote.Send && forwarded.Send,
		Steer:             remote.Steer && forwarded.Steer,
		Interrupt:         remote.Interrupt && forwarded.Interrupt,
		Compact:           remote.Compact && forwarded.Compact,
		Clear:             remote.Clear && forwarded.Clear,
		ForkFromTurn:      remote.ForkFromTurn && forwarded.ForkFromTurn,
		Shutdown:          remote.Shutdown && forwarded.Shutdown,
		ChangeModel:       remote.ChangeModel && forwarded.ChangeModel,
		ChangeVisionModel: remote.ChangeVisionModel && forwarded.ChangeVisionModel,
		Queue:             remote.Queue && forwarded.Queue,
		Goal:              remote.Goal && forwarded.Goal,
		SharedNotes:       remote.SharedNotes && forwarded.SharedNotes,
		Rename:            remote.Rename && forwarded.Rename,
		SkillInput:        remote.SkillInput && forwarded.SkillInput,
	}
}

// capabilitiesField is the JSON field name of every capability set a remote
// response or notification can carry. appwire has exactly two shapes, and
// translateThreadRaw plus the status-frame branch of translateNotification cover
// both:
//
//   - EvenerThread.Capabilities (appwire/types.go), nested as
//     "capabilities" inside the "evener" object of every Thread — the snapshot's
//     thread and the one a thread/started notification carries.
//   - ThreadStatusChangedParams.Capabilities, the top-level "capabilities" of
//     the one notification whose params carry a set of their own
//     (thread/status/changed).
//
// Enumerating them like this is the point: a capability added to another payload
// later has to be named here, and until then it is not masked.
const capabilitiesField = "capabilities"

// maskRemoteCapabilitiesRaw applies maskRemoteThreadCapabilities to a raw JSON
// capabilities object in a payload this source is translating. A value it cannot
// decode is returned byte-for-byte: the mask narrows the actions a client can
// reach, and must never drop or re-mint a field a newer remote hub sent.
func maskRemoteCapabilitiesRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var remote appwire.ThreadCapabilities
	if err := json.Unmarshal(raw, &remote); err != nil {
		return raw
	}
	masked, err := json.Marshal(maskRemoteThreadCapabilities(remote))
	if err != nil {
		return raw
	}
	return masked
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
// Evener.ParentRef (sub-thread aliases) and each
// Evener.PendingEscalations[].Ref move from "local:" to "<host>:".
// Evener.InstanceID is deliberately left byte-for-byte untouched: it is an
// opaque precondition token round-tripped into turn/start.expectedInstanceId.
// Evener.Capabilities is masked to the actions this source can forward: the
// remote hub describes its own daemon, not this controller's ability to reach it.
// Nested session handles inside Evener.Diagnostics (a job's or delegate's
// transcriptRef) and Evener.PendingEscalations (each entry's ref, which clients
// route an escalation card by) are translated too; see translateNestedRef.
func (s *RemoteHubSource) fromRemoteThread(thread appwire.Thread) (appwire.Thread, error) {
	thread.Source = s.id
	thread.Evener.Capabilities = maskRemoteThreadCapabilities(thread.Evener.Capabilities)
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
	// The remote daemon stamps each pending escalation card with the owning
	// session's ref in its own namespace (server/thread_envelope.go), and a client
	// answers a card by that ref (evener/sandbox/escalation/resolve routes through
	// sourceForThread). An untranslated "local:<id>" would target the controller's
	// own local source — wrong session on an id collision, source-not-found
	// otherwise — so the cards are rewritten exactly like the nested diagnostic
	// refs. ThreadID is already unnamespaced and stays untouched.
	for index := range thread.Evener.PendingEscalations {
		thread.Evener.PendingEscalations[index].Ref = s.fromRemoteNestedRef(thread.Evener.PendingEscalations[index].Ref)
	}
	s.fromRemoteDiagnosticRefs(thread.Evener.Diagnostics)
	return thread, nil
}

// fromRemoteDiagnosticRefs rewrites the nested transcript references the remote
// hub put on a thread's diagnostics.
//
// Delegate refs are session refs in the remote hub's own "local:<session>"
// namespace (agent/delegate_tree_start.go stamps encodeRef("", childSessionID)),
// so an untranslated one collides with the controller's local source exactly
// like Evener.Ref does: a client following it reads a controller-local session,
// or fails, instead of the remote child. They are moved into this source's
// namespace.
//
// Shell-job refs are "job:<id>" (agent.jobTranscriptRef names a job record in a
// daemon-internal namespace, not a source-qualified thread ref), and a
// project-scoped "proj:<project>:<session>" ref names a remote daemon object the
// controller cannot address by source. Neither can collide with a controller
// source the way "local:" does, so both pass through byte-for-byte: refusing
// them would fail every remote read that carries a background shell job. A
// client that tries to follow one still fails loudly at lookup ("source not
// found: job"/"proj") rather than reaching the wrong host.
func (s *RemoteHubSource) fromRemoteDiagnosticRefs(diagnostics *appwire.EvenerDiagnostics) {
	if diagnostics == nil {
		return
	}
	for index := range diagnostics.Delegates {
		diagnostics.Delegates[index].TranscriptRef = s.fromRemoteNestedRef(diagnostics.Delegates[index].TranscriptRef)
	}
	for index := range diagnostics.Jobs {
		diagnostics.Jobs[index].TranscriptRef = s.fromRemoteNestedRef(diagnostics.Jobs[index].TranscriptRef)
	}
}

// fromRemoteNestedRef translates a nested diagnostic ref that names the remote
// hub's own thread namespace and leaves every other ref byte-for-byte unchanged.
// Only "local:" is remappable ("local:<thread>" becomes "<host>:<thread>"); a
// ref in any other namespace is passed through because the controller has no
// mapping for it and must not refuse a whole response over it.
func (s *RemoteHubSource) fromRemoteNestedRef(raw string) string {
	if raw == "" {
		return ""
	}
	ref, err := appwire.ParseRef(raw)
	if err != nil || ref.SourceID != remoteHubNamespace {
		return raw
	}
	return appwire.Ref{SourceID: s.id, ThreadID: ref.ThreadID}.String()
}

// translateThreadRaw rewrites a thread object's ref-bearing fields at the JSON
// level, preserving every field this hub does not understand. Round-tripping
// through appwire.Thread would silently drop a field a newer remote hub sent —
// the same reason the notification translator works in RawMessage throughout.
// Source, Evener.Ref and Evener.ParentRef are rewritten; Evener.Capabilities is
// masked to the actions this source can forward, exactly as fromRemoteThread
// masks the snapshot's, so a subscribed client cannot re-enable a mutation from
// a thread-bearing notification that the read path answered as unavailable.
// InstanceID and everything else pass through byte-for-byte. Nested session
// handles under Evener (diagnostics job/delegate transcriptRefs, and each
// pendingEscalations entry's ref) are rewritten as well.
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
		if rawCapabilities, ok := evenerFields[capabilitiesField]; ok {
			evenerFields[capabilitiesField] = maskRemoteCapabilitiesRaw(rawCapabilities)
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
	case *appwire.ThreadStartResponse:
		thread, err := s.fromRemoteThread(response.Thread)
		if err != nil {
			return err
		}
		response.Thread = thread
	case *appwire.ThreadResumeResponse:
		thread, err := s.fromRemoteThread(response.Thread)
		if err != nil {
			return err
		}
		response.Thread = thread
	case *appwire.ThreadForkResponse:
		thread, err := s.fromRemoteThread(response.Thread)
		if err != nil {
			return err
		}
		response.Thread = thread
	case *appwire.ThreadClearResponse:
		thread, err := s.fromRemoteThread(response.Thread)
		if err != nil {
			return err
		}
		response.Thread = thread
		ref, err := s.fromRemoteRefString(response.Ref)
		if err != nil {
			return err
		}
		response.Ref = ref
	}
	return nil
}
