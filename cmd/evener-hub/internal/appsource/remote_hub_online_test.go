package appsource

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestRemoteHubSourceOnlineDefaultsTrueAndSignal covers the availability
// signal: no signal installed reports online (the pre-06 default); a settable
// signal is honored.
func TestRemoteHubSourceOnlineDefaultsTrueAndSignal(t *testing.T) {
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return nil, nil
	})
	var _ OnlineSource = source
	if !source.Online() {
		t.Fatal("Online() with no signal = false, want true")
	}
	source.SetHostOnline(func() bool { return false })
	if source.Online() {
		t.Fatal("Online() with an offline signal = true, want false")
	}
	source.SetHostOnline(func() bool { return true })
	if !source.Online() {
		t.Fatal("Online() with an online signal = false, want true")
	}
}

// TestRemoteHubSourceStartThreadClearsRoutingHints covers the forward-clearing
// rule: Source names this host in the controller's registry, and a non-"evener"
// Harness is read as a source id by the remote hub's launchSourceID, so neither
// routing hint may reach the remote hub, which would resolve it against its own
// registry and could spawn on the wrong host. Source is cleared outright; the
// harness is neutralized to the remote hub's own default, which launchSourceID
// does not read as a source id.
func TestRemoteHubSourceStartThreadClearsRoutingHints(t *testing.T) {
	source, calls := newScriptedRemote(t, "remote-host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadStart {
			return scriptedReply{result: appwire.ThreadStartResponse{}}
		}
		return scriptedReply{result: appwire.EmptyResponse{}}
	})

	if _, err := source.StartThread(t.Context(), appwire.ThreadStartParams{Source: "remote-host", Harness: "remote-b", CWD: "/tmp"}); err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	var forwarded map[string]any
	if err := json.Unmarshal(lastMethodCall(t, calls(), appwire.MethodThreadStart), &forwarded); err != nil {
		t.Fatalf("unmarshal forwarded params: %v", err)
	}
	if v, ok := forwarded["source"]; ok {
		t.Fatalf("forwarded params carry source=%v, want none", v)
	}
	if v, ok := forwarded["harness"]; !ok || v != "evener" {
		t.Fatalf("forwarded harness = %v, want the remote hub's own default (\"evener\"), never a source id", v)
	}
	if forwarded["cwd"] != "/tmp" {
		t.Fatalf("forwarded cwd = %v, want /tmp (other fields preserved)", forwarded["cwd"])
	}
}
