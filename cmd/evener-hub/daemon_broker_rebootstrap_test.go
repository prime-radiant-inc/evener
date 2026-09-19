package hub

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/interactiveartifacts"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

func TestRebootstrapLiveDaemonUsesAuthenticatedPrivateWebSocket(t *testing.T) {
	// TRIPWIRE: every step is local bounded AppWire over net.Pipe or loopback;
	// ten seconds fires only if a handshake reader or HTTP owner is stranded.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	authority, err := interactiveartifacts.OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close() //nolint:errcheck
	rootID := identifier.MustNewSessionID()

	launchHubSide, launchDaemonSide := net.Pipe()
	launchHub := interactiveartifacts.NewLaunchBroker(interactiveartifacts.LaunchBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(launchHubSide, appwire.PrivateBrokerFrameLimit),
		Authority: authority, LaunchID: "launch", HubEpoch: "hub-one", ProjectID: "project-0123456789", ResumeID: rootID,
	})
	daemon := interactiveartifacts.NewDaemonBroker(interactiveartifacts.DaemonBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(launchDaemonSide, appwire.PrivateBrokerFrameLimit), LaunchID: "launch",
	})
	defer daemon.Close() //nolint:errcheck
	go func() { _ = launchHub.Serve(ctx) }()
	if _, err := daemon.EstablishRoot(ctx, rootID, 1); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host := listener.Addr().String()
	entry := rendezvous.Entry{
		PID: 42, Address: host, Endpoint: "ws://" + host + "/rpc", Protocol: appwire.ProtocolVersion,
		SourceID: "local", ThreadID: rootID, SessionID: rootID, InstanceID: rootID, WorkspaceRef: "local:" + rootID,
		WorkingDir: "/work", StateDir: "/state", StartedAt: time.Unix(123, 456).UTC(), HubToken: "current-daemon-token",
	}
	identity := interactiveartifacts.DaemonIdentity(entry)
	if err := launchHub.ExpectOwnership(identity); err != nil {
		t.Fatal(err)
	}
	if err := daemon.InstallOwnership(ctx, identity); err != nil {
		t.Fatal(err)
	}

	generation := uint64(4)
	completed := make(chan struct{})
	daemonServer := server.NewServer(server.ServerConfig{
		HubToken: entry.HubToken, AllowedHost: host,
		PrivateBrokerHandler: func(_ context.Context, transport appwire.Transport) {
			_ = daemon.AcceptRebootstrap(ctx, transport, interactiveartifacts.DaemonRebootstrapCallbacks{
				Validate: func(expected appwire.BrokerDaemonIdentity) (appwire.BrokerDaemonIdentity, uint64, error) {
					if !reflect.DeepEqual(expected, identity) {
						return appwire.BrokerDaemonIdentity{}, 0, interactiveartifacts.ErrBrokerAuthentication
					}
					return identity, generation, nil
				},
				Complete: func(candidate uint64) error {
					if candidate != generation {
						return interactiveartifacts.ErrBrokerAuthentication
					}
					generation++
					close(completed)
					return nil
				},
			})
		},
	})
	httpServer := &http.Server{Handler: daemonServer}
	go func() { _ = httpServer.Serve(listener) }()
	defer httpServer.Close() //nolint:errcheck

	connection, err := rebootstrapLiveDaemon(ctx, authority, "hub-two", entry)
	if err != nil {
		t.Fatalf("rebootstrap live daemon: %v", err)
	}
	defer connection.Close() //nolint:errcheck
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatalf("daemon did not complete rebootstrap: %v", ctx.Err())
	}
	if generation != 5 {
		t.Fatalf("generation = %d, want completed direct install", generation)
	}
	stale := entry
	stale.PID++
	if staleConnection, err := rebootstrapLiveDaemon(ctx, authority, "hub-three", stale); err == nil {
		_ = staleConnection.Close()
		t.Fatal("stale daemon PID authenticated against the current owned process")
	}
}
