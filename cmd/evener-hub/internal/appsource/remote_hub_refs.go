package appsource

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
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
// representable remotely ("local"); every other entry is dropped.
//
// An empty controller filter means "every source the controller has", NOT
// "whatever sources the remote hub has": this source must still ask the remote
// for its own "local" source alone. Left unfiltered, the remote would also
// return threads from ITS OWN nested remote sources, whose refs live in another
// hub's namespace and cannot be represented here. fromRemoteThread refuses such
// a ref and translateOut drops that unaddressable row, so an unfiltered forward
// would quietly omit threads the controller can never route to; asking only for
// "local" keeps the response to the threads this source can represent.
// sourceAllowedForList gates whether this source is called at all; within the
// call the answer is always exactly "local".
func remapRemoteSourceIDs(sourceID string, ids []string) []string {
	if len(ids) == 0 {
		return []string{remoteHubNamespace}
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
// forward to the remote hub today. It is the controller-side half of the
// capability answer: maskRemoteThreadCapabilities intersects it with the remote
// hub's own claim, so an action reaches a remote session only when this set names
// it AND the remote advertises it.
//
// Shutdown is the one entry. The controller forwards thread/shutdown on the owning
// host's client, and the remote hub serves it against its own local daemon, so a
// controller can stop a session it did not host. The intersection keeps the remote
// daemon's own claim in charge: a remote that does not advertise Shutdown stays
// refused. The remaining lifecycle verbs and the turn mutations are implemented in
// remote_hub_mutations.go but stay masked until the host capability probe can say
// the host supports them.
//
// A capability not named here stays masked, so a field added to
// appwire.ThreadCapabilities later starts masked until a component names the method
// that answers for it. TestRemoteHubCapabilitiesMatchForwardedMethods holds the
// correspondence between this set and those methods; change the two together.
func remoteForwardedThreadCapabilities() appwire.ThreadCapabilities {
	return appwire.ThreadCapabilities{Shutdown: true}
}

// maskRemoteThreadCapabilities intersects the capability set a remote hub reported
// for its own thread with the actions this source can carry today.
//
// The remote hub reports the capabilities of its OWN session daemon: they describe
// what the remote daemon can do, not what this controller can forward. Forwarding
// the remote's set verbatim would advertise an action this controller cannot carry:
// it would fail with an internal error the moment a client used one (the hub gates
// each of them on exactly these fields — cmd/evener-hub's threadActionAvailable).
// Component 05c implements the mutations but forwards only Shutdown here; the
// other flags turn on with 05d's host capability probe.
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
	s.fromRemoteDiagnostics(thread.Evener.Diagnostics)
	// The remote hub stamps its own origin-relative image URLs into the thread;
	// the shared visitor moves every one of them onto the host-qualified
	// controller route the browser fetches them from.
	rewriteThreadImageURLs(s.id, &thread)
	return thread, nil
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

// fromRemoteDiagnostics rewrites the session-valued refs nested in a thread's
// diagnostics tree. A delegate's TranscriptRef always names its child session,
// and a delegate job's TranscriptRef names the same child session, so both live
// in the remote hub's "local:" namespace and must move to "<host>:" or a
// controller client would route a child-thread read to the local source. A
// shell job's TranscriptRef is the opaque "job:<id>" ref, preserved
// byte-for-byte, and every bare-id field (sessionId, ownerSessionId,
// childSessionId, rootSessionId) is an id, not a ref, so none is touched.
func (s *RemoteHubSource) fromRemoteDiagnostics(diagnostics *appwire.EvenerDiagnostics) {
	if diagnostics == nil {
		return
	}
	for index := range diagnostics.Delegates {
		diagnostics.Delegates[index].TranscriptRef = s.fromRemoteRefOrOpaque(diagnostics.Delegates[index].TranscriptRef)
	}
	for index := range diagnostics.Jobs {
		diagnostics.Jobs[index].TranscriptRef = s.fromRemoteRefOrOpaque(diagnostics.Jobs[index].TranscriptRef)
	}
}

// fromRemoteRefOrOpaque maps a remote "local:<thread>" ref into the controller
// namespace, but leaves a value this source cannot address — an opaque
// "job:<id>" ref or a project-scoped ref — byte-for-byte rather than failing the
// enclosing thread. It is the single-value counterpart of translateActivityRefs's
// per-key policy.
func (s *RemoteHubSource) fromRemoteRefOrOpaque(raw string) string {
	if raw == "" {
		return ""
	}
	translated, err := s.fromRemoteRefString(raw)
	if err != nil {
		return raw
	}
	return translated
}

// translateOut applies outbound translation to whichever response type embeds
// a Thread, Turn, or ThreadItem: refs are rewritten into the controller
// namespace and every remote-stamped image URL is moved onto the
// host-qualified controller route (see rewriteThreadImageURLs). Responses
// without either (e.g. model/list) pass through unchanged.
func (s *RemoteHubSource) translateOut(out any) error {
	switch response := out.(type) {
	case *appwire.ThreadListResponse:
		// Translate rows independently: a row from a nested remote hub carries a
		// non-local ref this controller cannot address, and that one row must not
		// discard the valid local rows returned alongside it. The unrepresentable
		// row is skipped; every representable row is translated and retained.
		if len(response.Data) == 0 {
			break
		}
		translated := make([]appwire.Thread, 0, len(response.Data))
		for index := range response.Data {
			thread, err := s.fromRemoteThread(response.Data[index])
			if err != nil {
				continue
			}
			translated = append(translated, thread)
		}
		response.Data = translated
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
		rewriteTurnImageURLs(s.id, &response.Turn)
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
	case *appwire.ThreadTurnsListResponse:
		for index := range response.Data {
			rewriteTurnImageURLs(s.id, &response.Data[index])
		}
	case *appwire.ThreadTurnItemsListResponse:
		for index := range response.Data {
			rewriteItemImageURLs(s.id, &response.Data[index])
		}
	case *appwire.TurnStartResponse:
		rewriteTurnImageURLs(s.id, &response.Turn)
	case *appwire.EvenerSubagentPreviewResponse:
		for index := range response.Items {
			rewriteItemImageURLs(s.id, &response.Items[index])
		}
	case *appwire.JobsListResponse:
		response.Data = s.translateActivityRefs(response.Data)
	}
	return nil
}

// translateActivityRefs rewrites the session refs embedded in a remote hub's
// jobs/list response.
//
// The current shape is a decoded activity tree: JobsListResponse.Data is `any`,
// so the wire tree arrives as nested map[string]any/[]any rather than typed
// appwire.JobActivity* nodes. The walk follows ONLY the structural containers
// JobActivityTree declares (root, entries, job, delegate, delegate.child,
// delegate.turns) and rewrites ONLY the ref fields those nodes declare (a
// session's ref; a job's ownerRef and transcriptRef; a delegate's childRef). No
// other key is ever treated as a ref, so a key literally named "ref" or
// "transcriptRef" inside an opaque payload — a delegate's message or
// structuredResult, which are json.RawMessage on the typed struct, or an
// undeclared key such as a delegate-level "transcriptRef" (JobActivityDelegate
// has no such field) — survives byte-for-byte.
//
// The walk runs ONLY on a payload recognized as an activity tree
// (activityTreeRecognized) AND decodable as a complete appwire.JobActivityTree
// (activityTreeDecodable). A container name outside a tree is just data, and
// descending into an unrecognized object would re-point a ref inside a payload
// this source does not understand: a payload that fails recognition — an empty
// object, an object carrying `root` without its required fields, a payload whose
// required fields carry another type, or any unrelated object — is returned as
// the `any` value it arrived as, never rewritten. So is a payload that satisfies
// the discriminator test but is still malformed as a tree — `entries` as an
// object rather than an array, a declared container of the wrong type, a
// `revision` too large for a uint64 — because the wire contract this walk
// follows is the typed tree, not the two discriminator fields.
//
// An older daemon may still answer with the retired flat array of EvenerJobInfo
// (docs/appwire-protocol.md, evener/jobs/list); that shape is translated by the
// same declared-field policy as the tree's job nodes.
//
// A value that does not parse as a session ref (an opaque "job:" ref, a bare
// id from an older daemon, or a nested non-local ref) is left untouched rather
// than failing the whole list.
func (s *RemoteHubSource) translateActivityRefs(value any) any {
	switch node := value.(type) {
	case map[string]any:
		if !activityTreeRecognized(node) || !activityTreeDecodable(node) {
			return value
		}
		s.translateActivitySession(node["root"])
	case []any:
		s.translateLegacyJobRefs(node)
	}
	return value
}

// activityTreeRecognized reports whether a decoded jobs/list object is the
// activity-tree shape, by the required fields and their types. The wire types
// state them: JobActivityTree.Revision is a uint64, and neither it nor
// JobActivityTree.Root, JobActivitySession.SessionID, or JobActivitySession.Ref
// carries omitempty, so a tree the Go encoder wrote always carries `revision` as
// a JSON number and `root` as an object with `sessionId` and `ref` as strings.
//
//   - `revision` must be a JSON number holding a non-negative integer. A string,
//     an object, an array, a negative number, or a fractional number is not a
//     revision, so the payload is not a tree.
//   - `root` must be a JSON object carrying `sessionId` and `ref` as strings.
//     Both may be empty: the encoder emits them unconditionally.
//
// Nothing else is constrained, so a newer remote hub may add fields this
// controller does not know and the tree stays recognizable; the walk itself only
// touches the ref fields the declared nodes own. "It decoded without error" is
// deliberately not the test: `{}` and any unrelated object decode with no error,
// and they are exactly the payloads that must reach the controller untouched.
// Recognition is a cheap first gate only; activityTreeDecodable supplies the
// other half of the boundary, so a payload this test accepts but the typed tree
// rejects is still passed through untouched.
//
// The value is the generic decode of the wire response (appwire.Client decodes
// JobsListResponse.Data with json.Unmarshal), so a JSON number arrives as a
// float64.
func activityTreeRecognized(node map[string]any) bool {
	revision, ok := node["revision"].(float64)
	if !ok || revision < 0 || revision != math.Trunc(revision) {
		return false
	}
	root, ok := node["root"].(map[string]any)
	if !ok {
		return false
	}
	if _, ok := root["sessionId"].(string); !ok {
		return false
	}
	_, ok = root["ref"].(string)
	return ok
}

// activityTreeDecodable reports whether a payload that already passed
// activityTreeRecognized also decodes as a complete appwire.JobActivityTree.
//
// The discriminator test pins only `revision` and `root`, but the walk follows
// every container JobActivityTree declares (root/entries/job/delegate/child/
// turns) and rewrites the ref fields those nodes own. A payload can satisfy the
// discriminators and still be malformed as a tree — `entries` an object where
// the wire type is a []JobActivityEntry, a declared node carrying another type,
// a `revision` that overflows the uint64 field. Walking such a payload re-points
// a ref inside a value this source does not understand, which is precisely what
// the recognition boundary exists to prevent; only the typed contract decides
// whether the walk may descend at all.
//
// The decode is a GATE, never a translation: the value the walk operates on
// stays the decoded map so every key the typed struct does not declare survives
// byte-for-byte. Re-encoding the struct would drop those keys and break the
// pass-through guarantee for recognized trees. JobsListResponse.Data is `any`,
// so the payload arrives as a generic value; the round trip through json.Marshal
// is what reaches the typed decoder.
func activityTreeDecodable(node map[string]any) bool {
	encoded, err := json.Marshal(node)
	if err != nil {
		return false
	}
	var tree appwire.JobActivityTree
	return json.Unmarshal(encoded, &tree) == nil
}

// translateLegacyJobRefs rewrites the declared ref field of each element of the
// retired flat jobs array. EvenerJobInfo's only ref-valued field is
// transcriptRef: a session ref for a delegate turn and the opaque "job:<id>"
// for a shell job. Every other key is an id, not an address, so only
// transcriptRef is a candidate and translateActivityRefField leaves a value it
// cannot address (a bare id, an opaque "job:" ref) untouched.
func (s *RemoteHubSource) translateLegacyJobRefs(jobs []any) {
	for _, job := range jobs {
		entry, ok := job.(map[string]any)
		if !ok {
			continue
		}
		s.translateActivityRefField(entry, "transcriptRef")
	}
}

// translateActivitySession rewrites one JobActivitySession node: its own ref,
// then each entry's job or delegate.
func (s *RemoteHubSource) translateActivitySession(value any) {
	session, ok := value.(map[string]any)
	if !ok {
		return
	}
	s.translateActivityRefField(session, "ref")
	for _, entry := range activityChildren(session["entries"]) {
		s.translateActivityEntry(entry)
	}
}

// translateActivityEntry rewrites one JobActivityEntry node by dispatching to
// its job or delegate child.
func (s *RemoteHubSource) translateActivityEntry(value any) {
	entry, ok := value.(map[string]any)
	if !ok {
		return
	}
	s.translateActivityJob(entry["job"])
	s.translateActivityDelegate(entry["delegate"])
}

// translateActivityJob rewrites the ref fields one JobActivityJob node
// declares: its owner session ref and its transcript ref (a session ref for a
// delegate turn, the opaque "job:<id>" for a shell job).
func (s *RemoteHubSource) translateActivityJob(value any) {
	job, ok := value.(map[string]any)
	if !ok {
		return
	}
	s.translateActivityRefField(job, "ownerRef")
	s.translateActivityRefField(job, "transcriptRef")
}

// translateActivityDelegate rewrites a JobActivityDelegate node's childRef,
// then recurses into its child session and its delegate turns. The delegate
// node declares childRef as its only ref field (appwire.JobActivityDelegate),
// so a delegate-level "transcriptRef" is not a declared address and is
// deliberately left byte-for-byte; the turn nodes that DO declare transcriptRef
// are rewritten by translateActivityJob.
func (s *RemoteHubSource) translateActivityDelegate(value any) {
	delegate, ok := value.(map[string]any)
	if !ok {
		return
	}
	s.translateActivityRefField(delegate, "childRef")
	s.translateActivitySession(delegate["child"])
	for _, turn := range activityChildren(delegate["turns"]) {
		s.translateActivityJob(turn)
	}
}

// translateActivityRefField rewrites one declared ref field in place when its
// value parses as a remote session ref. A non-string, an empty value, or a
// value this source cannot address (an opaque "job:" ref, a nested non-local
// ref, a bare id) is left exactly as it arrived.
func (s *RemoteHubSource) translateActivityRefField(node map[string]any, key string) {
	raw, ok := node[key].(string)
	if !ok || raw == "" {
		return
	}
	if translated, err := s.fromRemoteRefString(raw); err == nil {
		node[key] = translated
	}
}

// activityChildren returns a decoded JSON array, or nil for any other shape.
func activityChildren(value any) []any {
	children, _ := value.([]any)
	return children
}
