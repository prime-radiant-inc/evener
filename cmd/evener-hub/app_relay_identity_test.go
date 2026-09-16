package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

// relayIdentityProbeSource records the relay identity every attach it is asked
// for carried, so a test can pin that the hub's relay supervisor labels its
// attaches with the relay key it is registered under.
type relayIdentityProbeSource struct {
	*scriptedAppSource
	identities chan string
}

func (s *relayIdentityProbeSource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	select {
	case s.identities <- appsource.RelayIdentityFromContext(ctx):
	default:
	}
	out := make(chan appwire.Notification)
	close(out)
	return out, nil
}

// TestHubRelayLabelsAttachesWithItsRelayKey pins the wiring behind the remote hub
// source's foreign-relay admission: every attach the relay supervisor issues must
// carry the relay key the relay is registered under, so a source that keys its
// subscriptions by remote thread identity can tell this relay re-attaching from a
// second relay that reached the same thread by another address. Without it the
// source sees two identical attaches and lets them displace one another forever.
func TestHubRelayLabelsAttachesWithItsRelayKey(t *testing.T) {
	thread := appwire.Thread{
		ID:     "current",
		Source: "host",
		Evener: appwire.EvenerThread{Ref: "host:current"},
	}
	source := &relayIdentityProbeSource{
		scriptedAppSource: &scriptedAppSource{id: "host", thread: thread},
		identities:        make(chan string, 8),
	}
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "relay-test", SourceID: "local"})
	relays := newHubRelayFunctions(server, hubcore.WebConfig{}, appsource.NewRegistry())
	defer relays.stopRelay("host:current")

	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{Ref: "host:current"}, thread); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-source.identities:
		if got != "host:current" {
			t.Fatalf("attach carried relay identity %q, want the relay key host:current", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the relay never issued a source attach")
	}
}
