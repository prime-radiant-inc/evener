package appwire

import (
	"encoding/json"
	"testing"
)

// TestNotesWireCatalog pins the shared-notes wire surface: the two hub RPCs
// (notes/human/set, urls/remove) are cataloged with ScopeBoth and the
// retry-safe envelope shape; the two push notifications exist; ValidateMutationParams
// enforces clientMutationId + expectedInstanceId on the hub RPCs; and
// CloneThread deep-copies the session URL list.
//
// The agent-tool verbs (notes/agent/set, urls/add) are deliberately NOT in the
// method catalog: they are agent-invoked tools with no hub-to-daemon RPC route
// (cross-channel invocation fails as unknown method by construction), the same
// way update_goal has no catalog entry. They carry no mutation envelope, so
// they are absent from ValidateMutationParams as well.
func TestNotesWireCatalog(t *testing.T) {
	methods := map[string]MethodSpec{}
	for _, m := range Methods {
		methods[m.Name] = m
	}
	// Catalog presence + scope + param/response types for the hub RPCs.
	if m, ok := methods[MethodNotesHumanSet]; !ok {
		t.Fatalf("method %q missing from catalog", MethodNotesHumanSet)
	} else {
		if m.Scope != ScopeBoth {
			t.Errorf("method %q scope = %q, want %q", MethodNotesHumanSet, m.Scope, ScopeBoth)
		}
		if _, ok := m.Params.(NotesHumanSetParams); !ok {
			t.Errorf("method %q params type = %T, want NotesHumanSetParams", MethodNotesHumanSet, m.Params)
		}
		if _, ok := m.Result.(NotesHumanSetResponse); !ok {
			t.Errorf("method %q result type = %T, want NotesHumanSetResponse", MethodNotesHumanSet, m.Result)
		}
		if m.Summary == "" {
			t.Errorf("method %q has empty Summary", MethodNotesHumanSet)
		}
	}
	if m, ok := methods[MethodUrlsRemove]; !ok {
		t.Fatalf("method %q missing from catalog", MethodUrlsRemove)
	} else {
		if m.Scope != ScopeBoth {
			t.Errorf("method %q scope = %q, want %q", MethodUrlsRemove, m.Scope, ScopeBoth)
		}
		if _, ok := m.Params.(UrlsRemoveParams); !ok {
			t.Errorf("method %q params type = %T, want UrlsRemoveParams", MethodUrlsRemove, m.Params)
		}
		if _, ok := m.Result.(UrlsRemoveResponse); !ok {
			t.Errorf("method %q result type = %T, want UrlsRemoveResponse", MethodUrlsRemove, m.Result)
		}
		if m.Summary == "" {
			t.Errorf("method %q has empty Summary", MethodUrlsRemove)
		}
	}
	// Agent-tool verbs must NOT be cataloged as RPC methods.
	for _, m := range []string{MethodNotesAgentSet, MethodUrlsAdd} {
		if _, ok := methods[m]; ok {
			t.Errorf("agent-tool verb %q must not be in the RPC method catalog", m)
		}
	}
	// Push notifications exist with the right payload types.
	notifs := map[string]NotificationSpec{}
	for _, n := range Notifications {
		notifs[n.Name] = n
	}
	if n, ok := notifs[NotifyEvenerNotesUpdated]; !ok {
		t.Errorf("notification %q missing from catalog", NotifyEvenerNotesUpdated)
	} else if _, ok := n.Payload.(NotesUpdatedParams); !ok {
		t.Errorf("notification %q payload type = %T, want NotesUpdatedParams", NotifyEvenerNotesUpdated, n.Payload)
	}
	if n, ok := notifs[NotifyEvenerUrlsUpdated]; !ok {
		t.Errorf("notification %q missing from catalog", NotifyEvenerUrlsUpdated)
	} else if _, ok := n.Payload.(UrlsUpdatedParams); !ok {
		t.Errorf("notification %q payload type = %T, want UrlsUpdatedParams", NotifyEvenerUrlsUpdated, n.Payload)
	}
}

// TestNotesMutationParamsRequireIdentityAndFence pins the retry-safe envelope
// on the two hub RPCs: a valid shape passes, and anything missing
// clientMutationId or expectedInstanceId is rejected. Agent-tool verbs carry
// no envelope, so the validator ignores them.
func TestNotesMutationParamsRequireIdentityAndFence(t *testing.T) {
	for _, method := range []string{MethodNotesHumanSet, MethodUrlsRemove} {
		t.Run(method, func(t *testing.T) {
			valid := `{"clientMutationId":"m1","expectedInstanceId":"i1"}`
			if err := ValidateMutationParams(method, json.RawMessage(valid)); err != nil {
				t.Fatalf("valid shape rejected: %v", err)
			}
			for _, raw := range []string{
				`{}`,
				`{"clientMutationId":"m1"}`,
				`{"expectedInstanceId":"i1"}`,
				`{"clientMutationId":"","expectedInstanceId":"i1"}`,
			} {
				if err := ValidateMutationParams(method, json.RawMessage(raw)); err == nil {
					t.Fatalf("invalid shape %s accepted", raw)
				}
			}
		})
	}
	for _, method := range []string{MethodNotesAgentSet, MethodUrlsAdd} {
		t.Run(method+"/no-envelope", func(t *testing.T) {
			if err := ValidateMutationParams(method, json.RawMessage(`{}`)); err != nil {
				t.Fatalf("agent-tool verb %q rejected by mutation validator: %v", method, err)
			}
		})
	}
}

// TestNotesCloneDoesNotAliasURLSlice pins the clone safety for the session
// URL list: mutating the clone's entries must not touch the original.
func TestNotesCloneDoesNotAliasURLSlice(t *testing.T) {
	a := EvenerThread{SessionURLs: []SessionURL{{ID: "u1", URL: "https://x.test/"}}}
	b := cloneEvenerThread(a)
	b.SessionURLs[0].URL = "mutated"
	if a.SessionURLs[0].URL != "https://x.test/" {
		t.Fatalf("clone aliases URL slice: original URL = %q", a.SessionURLs[0].URL)
	}
	// Appending to the clone must not disturb the original either.
	b.SessionURLs = append(b.SessionURLs, SessionURL{ID: "u2", URL: "https://y.test/"})
	if len(a.SessionURLs) != 1 {
		t.Fatalf("clone append disturbed original: len = %d", len(a.SessionURLs))
	}
}
