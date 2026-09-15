package appsource

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestRemoteHubRefRoundTrip(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	refs := []string{"host:X", "host:sub-child", "host:owner", "host:turn_1"}
	for _, raw := range refs {
		remote, err := source.toRemoteRef(raw, "")
		if err != nil {
			t.Fatalf("toRemoteRef(%q): %v", raw, err)
		}
		if remote.SourceID != remoteHubNamespace {
			t.Fatalf("toRemoteRef(%q).SourceID = %q, want %q", raw, remote.SourceID, remoteHubNamespace)
		}
		back, err := source.fromRemoteRefString(remote.String())
		if err != nil {
			t.Fatalf("fromRemoteRefString(%q): %v", remote.String(), err)
		}
		if back != raw {
			t.Fatalf("round trip: got %q, want %q", back, raw)
		}
	}
}

func TestRemoteHubRefBareThreadID(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	remote, err := source.toRemoteRef("", "X")
	if err != nil {
		t.Fatalf("toRemoteRef bare thread id: %v", err)
	}
	if remote.String() != "local:X" {
		t.Fatalf("toRemoteRef(\"\", \"X\") = %q, want %q", remote.String(), "local:X")
	}
	empty, err := source.toRemoteRef("", "")
	if err != nil {
		t.Fatalf("toRemoteRef empty: %v", err)
	}
	if empty.String() != "" {
		t.Fatalf("toRemoteRef(\"\", \"\") = %q, want empty", empty.String())
	}
}

func TestRemoteHubRefForeignSourceRefused(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	_, err := source.toRemoteRef("other:X", "")
	if err == nil {
		t.Fatal("toRemoteRef accepted a foreign source")
	}
	if !strings.Contains(err.Error(), "source not found: other") {
		t.Fatalf("error = %v, want source not found: other", err)
	}
}

func TestRemoteHubNestedNonLocalRefRefused(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	_, err := source.fromRemoteRefString("other:X")
	if err == nil {
		t.Fatal("fromRemoteRefString accepted a nested non-local ref")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInternalError {
		t.Fatalf("wire code = %d, want %d", wire.Code, appwire.CodeInternalError)
	}
	if !strings.Contains(err.Error(), "only \"local\" is representable") {
		t.Fatalf("error = %v, want nested-ref refusal", err)
	}
}

func TestRemoteHubFromRemoteThreadTranslates(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	thread, err := source.fromRemoteThread(appwire.Thread{
		ID:     "t1",
		Source: "local",
		Evener: appwire.EvenerThread{
			Ref:        "local:t1",
			ParentRef:  "local:owner",
			InstanceID: "inst-1",
		},
	})
	if err != nil {
		t.Fatalf("fromRemoteThread: %v", err)
	}
	if thread.Source != "host" {
		t.Fatalf("Source = %q, want host", thread.Source)
	}
	if thread.Evener.Ref != "host:t1" {
		t.Fatalf("Evener.Ref = %q, want host:t1", thread.Evener.Ref)
	}
	if thread.Evener.ParentRef != "host:owner" {
		t.Fatalf("Evener.ParentRef = %q, want host:owner", thread.Evener.ParentRef)
	}
	if thread.Evener.InstanceID != "inst-1" {
		t.Fatalf("Evener.InstanceID = %q, want inst-1", thread.Evener.InstanceID)
	}
}

func TestRemoteHubFromRemoteThreadNestedRefRefused(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	_, err := source.fromRemoteThread(appwire.Thread{Evener: appwire.EvenerThread{Ref: "other:X"}})
	if err == nil {
		t.Fatal("fromRemoteThread accepted a nested remote ref")
	}
}

func TestRemapRemoteSourceIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty restricts to local", nil, []string{"local"}},
		{"host only", []string{"host"}, []string{"local"}},
		{"host and other drops other", []string{"host", "other"}, []string{"local"}},
		{"other only becomes empty", []string{"other"}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := remapRemoteSourceIDs("host", tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("remapRemoteSourceIDs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestRemoteHubListThreadsDefaultRestrictsToLocalSource pins the default
// (unfiltered) thread/list forward: this source must ask the remote hub for its
// own "local" source only. A remote hub's reflected response can advertise
// threads from ITS OWN nested remote sources, whose refs live in another host's
// namespace and are not representable in the controller namespace;
// fromRemoteThread rejects such a response outright, so an unfiltered forward
// would lose every thread from this host, not merely the nested ones. The
// scripted remote models that: it only answers when the forwarded filter names
// "local", and otherwise returns the nested thread that the controller refuses.
func TestRemoteHubListThreadsDefaultRestrictsToLocalSource(t *testing.T) {
	localThread := appwire.Thread{ID: "S", Source: "local", Evener: appwire.EvenerThread{Ref: "local:S"}}
	nestedThread := appwire.Thread{ID: "N", Source: "nested", Evener: appwire.EvenerThread{Ref: "nested:N"}}

	source, calls := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadList {
			t.Errorf("unexpected method %q", method)
			return scriptedReply{result: map[string]any{}}
		}
		var forwarded struct {
			SourceIDs []string `json:"sourceIds"`
		}
		if err := json.Unmarshal(params, &forwarded); err != nil {
			t.Errorf("decode forwarded thread/list params %s: %v", params, err)
		}
		if !reflect.DeepEqual(forwarded.SourceIDs, []string{"local"}) {
			// An unfiltered request reaches the remote's nested remote sources,
			// and the nested ref then fails the controller's whole translation.
			return scriptedReply{result: appwire.ThreadListResponse{Data: []appwire.Thread{localThread, nestedThread}}}
		}
		return scriptedReply{result: appwire.ThreadListResponse{Data: []appwire.Thread{localThread}}}
	})

	resp, err := source.ListThreads(t.Context(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Evener.Ref != "host:S" || resp.Data[0].Source != "host" {
		t.Fatalf("threads = %+v, want the single translated local thread", resp.Data)
	}
	params := lastMethodCall(t, calls(), appwire.MethodThreadList)
	var forwarded struct {
		SourceIDs []string `json:"sourceIds"`
	}
	if err := json.Unmarshal(params, &forwarded); err != nil {
		t.Fatalf("decode forwarded params %s: %v", params, err)
	}
	if !reflect.DeepEqual(forwarded.SourceIDs, []string{"local"}) {
		t.Fatalf("forwarded sourceIds = %v, want [local]", forwarded.SourceIDs)
	}
}
