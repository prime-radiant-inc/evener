package hub

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubThreadListEmptyDataIsArray(t *testing.T) {
	response, err := hubThreadList(context.Background(), hubcore.WebConfig{}, appsource.NewRegistry(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if response.Data == nil || len(response.Data) != 0 {
		t.Fatalf("thread list data = %#v, want non-nil empty slice", response.Data)
	}
	wire, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal thread list: %v", err)
	}
	var decoded struct {
		Data []appwire.Thread `json:"data"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal thread list: %v", err)
	}
	if decoded.Data == nil || len(decoded.Data) != 0 {
		t.Fatalf("serialized thread list data = %#v, want empty JSON array", decoded.Data)
	}
}

// TestRemoteHostNamesPrefersLiveRegistry pins the round-2 medium: the
// attachment gates classify hosts from the live registry, so a runtime-added
// host is remote exactly like a configured one; the configured entries remain
// the fallback when no registry is threaded.
func TestRemoteHostNamesPrefersLiveRegistry(t *testing.T) {
	reg, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	if err := reg.Add(hostreg.Host{Name: "side", SSH: "s.example"}); err != nil {
		t.Fatalf("reg.Add: %v", err)
	}
	names := remoteHostNames(hubcore.WebConfig{RemoteHostRegistry: reg})
	if len(names) != 2 {
		t.Fatalf("remoteHostNames over the live registry = %v, want m4 and the runtime-added side", names)
	}
	if _, ok := names["m4"]; !ok {
		t.Fatalf("remoteHostNames = %v, want m4", names)
	}
	if _, ok := names["side"]; !ok {
		t.Fatalf("remoteHostNames = %v, want the runtime-added host classified remote", names)
	}
	// Without a registry the configured entries are the set.
	fallback := remoteHostNames(hubcore.WebConfig{RemoteHosts: []hostreg.Host{{Name: "m4"}}})
	if len(fallback) != 1 {
		t.Fatalf("remoteHostNames fallback = %v, want only the configured entry", fallback)
	}
	if _, ok := fallback["m4"]; !ok {
		t.Fatalf("remoteHostNames fallback = %v, want m4", fallback)
	}
	if got := remoteHostNames(hubcore.WebConfig{}); got != nil {
		t.Fatalf("remoteHostNames over an empty cfg = %v, want nil", got)
	}
}
