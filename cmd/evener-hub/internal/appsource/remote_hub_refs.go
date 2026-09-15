package appsource

import (
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
// representable remotely ("local"); every other entry is dropped.
//
// An empty controller filter means "every source the controller has", NOT
// "whatever sources the remote hub has": this source must still ask the remote
// for its own "local" source alone. Left unfiltered, the remote would also
// return threads from ITS OWN nested remote sources, whose refs live in another
// hub's namespace and cannot be represented here; fromRemoteThread refuses such
// a ref, so one nested thread would fail the whole response and lose every
// thread from this host. sourceAllowedForList gates whether this source is
// called at all; within the call the answer is always exactly "local".
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

// fromRemoteThread rewrites every ref-bearing field of a thread the remote hub
// returned. Thread.Source becomes this source's ID; Evener.Ref and
// Evener.ParentRef (sub-thread aliases) move from "local:" to "<host>:"; and the
// session-valued refs nested in Evener.Diagnostics (a delegate's and a delegate
// job's TranscriptRef) move the same way. Evener.InstanceID is deliberately left
// byte-for-byte untouched: it is an opaque precondition token round-tripped into
// turn/start.expectedInstanceId.
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
	s.fromRemoteDiagnostics(thread.Evener.Diagnostics)
	return thread, nil
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
		s.translateActivitySession(node["root"])
	case []any:
		s.translateLegacyJobRefs(node)
	}
	return value
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
