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

// fromRemoteThread rewrites every ref-bearing field of a thread the remote hub
// returned. Thread.Source becomes this source's ID; Evener.Ref and
// Evener.ParentRef (sub-thread aliases) move from "local:" to "<host>:".
// Evener.InstanceID is deliberately left byte-for-byte untouched: it is an
// opaque precondition token round-tripped into turn/start.expectedInstanceId.
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
	return thread, nil
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
