package interactiveartifacts

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

func TestLaunchBrokerAuthenticatesAndFinalizesOwnedRoot(t *testing.T) {
	authority, err := OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close() //nolint:errcheck
	installation := authority.Installation()
	rootID := identifier.MustNewSessionID()

	hubSide, daemonSide := net.Pipe()
	hub := NewLaunchBroker(LaunchBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(hubSide, appwire.PrivateBrokerFrameLimit),
		Authority: authority,
		LaunchID:  "launch-correlation",
		HubEpoch:  "hub-epoch",
		ProjectID: "project-0123456789",
		ResumeID:  rootID,
	})
	daemon := NewDaemonBroker(DaemonBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(daemonSide, appwire.PrivateBrokerFrameLimit),
		LaunchID:  "launch-correlation",
	})
	defer daemon.Close() //nolint:errcheck

	hubDone := make(chan error, 1)
	go func() { hubDone <- hub.Serve(t.Context()) }()
	association, err := daemon.EstablishRoot(t.Context(), rootID, 1)
	if err != nil {
		t.Fatalf("EstablishRoot: %v", err)
	}
	if association.RootSessionID != rootID || association.RealmID != installation.RealmID || association.HumanOwnerID != installation.HumanOwnerID {
		t.Fatalf("association = %+v", association)
	}

	identity := appwire.BrokerDaemonIdentity{
		PID: 42, Address: "127.0.0.1:4131", Endpoint: "http://127.0.0.1:4131", Protocol: appwire.ProtocolVersion,
		SourceID: "local", ThreadID: rootID, SessionID: rootID, InstanceID: "instance-a", WorkspaceRef: "local:" + rootID,
		WorkingDir: "/work", StateDir: "/state", StartedAt: time.Unix(123, 456).UTC().Format(time.RFC3339Nano),
	}
	if err := hub.ExpectOwnership(identity); err != nil {
		t.Fatal(err)
	}
	if err := daemon.InstallOwnership(t.Context(), identity); err != nil {
		t.Fatalf("InstallOwnership: %v", err)
	}
	if err := hub.WaitFinalized(t.Context()); err != nil {
		t.Fatalf("WaitFinalized: %v", err)
	}

	if err := daemon.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-hubDone:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
			t.Fatalf("hub Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hub Serve did not exit after the owned channel closed")
	}
}

func TestLaunchBrokerRejectsWrongResumeBeforePersistingAssociation(t *testing.T) {
	authority, err := OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close() //nolint:errcheck

	expectedRoot := identifier.MustNewSessionID()
	actualRoot := identifier.MustNewSessionID()
	hubSide, daemonSide := net.Pipe()
	hub := NewLaunchBroker(LaunchBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(hubSide, appwire.PrivateBrokerFrameLimit),
		Authority: authority,
		LaunchID:  "launch-correlation",
		HubEpoch:  "hub-epoch",
		ProjectID: "project-0123456789",
		ResumeID:  expectedRoot,
	})
	daemon := NewDaemonBroker(DaemonBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(daemonSide, appwire.PrivateBrokerFrameLimit),
		LaunchID:  "launch-correlation",
	})
	defer daemon.Close() //nolint:errcheck
	go func() { _ = hub.Serve(t.Context()) }()

	if _, err := daemon.EstablishRoot(t.Context(), actualRoot, 1); err == nil {
		t.Fatal("wrong resumed root established a broker")
	}
	if _, err := authority.RootAssociation(t.Context(), actualRoot); !errors.Is(err, ErrAssociationMissing) {
		t.Fatalf("wrong resumed root association error = %v, want ErrAssociationMissing", err)
	}
}

func TestDaemonBrokerSealsConnectionAfterOwnershipFinalizationFails(t *testing.T) {
	authority, err := OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close() //nolint:errcheck
	rootID := identifier.MustNewSessionID()

	hubSide, daemonSide := net.Pipe()
	hub := NewLaunchBroker(LaunchBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(hubSide, appwire.PrivateBrokerFrameLimit),
		Authority: authority, LaunchID: "launch", HubEpoch: "hub", ProjectID: "project-0123456789", ResumeID: rootID,
	})
	daemon := NewDaemonBroker(DaemonBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(daemonSide, appwire.PrivateBrokerFrameLimit), LaunchID: "launch",
	})
	defer daemon.Close() //nolint:errcheck
	go func() { _ = hub.Serve(t.Context()) }()
	if _, err := daemon.EstablishRoot(t.Context(), rootID, 1); err != nil {
		t.Fatal(err)
	}

	expected := completeBrokerIdentity(rootID)
	actual := expected
	actual.PID++
	if err := hub.ExpectOwnership(expected); err != nil {
		t.Fatal(err)
	}
	if err := daemon.InstallOwnership(t.Context(), actual); err == nil {
		t.Fatal("mismatched ownership unexpectedly finalized")
	}
	if _, err := daemon.EstablishRoot(t.Context(), rootID, 1); err == nil {
		t.Fatal("failed ownership epoch remained usable")
	}
}

type dropResponseTransport struct {
	appwire.Transport
}

func (t dropResponseTransport) Send(ctx context.Context, message appwire.Message) error {
	if message.Response != nil {
		_ = t.Close()
		return io.ErrClosedPipe
	}
	return t.Transport.Send(ctx, message)
}

func TestLaunchBrokerReusesDurableAssociationAfterInstallAcknowledgmentIsLost(t *testing.T) {
	authority, err := OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close() //nolint:errcheck
	rootID := identifier.MustNewSessionID()

	firstHubSide, firstDaemonSide := net.Pipe()
	firstHub := NewLaunchBroker(LaunchBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(firstHubSide, appwire.PrivateBrokerFrameLimit),
		Authority: authority, LaunchID: "first-launch", HubEpoch: "first-hub", ProjectID: "project-0123456789", ResumeID: rootID,
	})
	firstDaemon := NewDaemonBroker(DaemonBrokerConfig{
		Transport: dropResponseTransport{Transport: appwire.NewStreamTransportWithLimit(firstDaemonSide, appwire.PrivateBrokerFrameLimit)},
		LaunchID:  "first-launch",
	})
	firstDone := make(chan error, 1)
	go func() { firstDone <- firstHub.Serve(t.Context()) }()
	if _, err := firstDaemon.EstablishRoot(t.Context(), rootID, 1); err == nil {
		t.Fatal("dropped install acknowledgment unexpectedly established the first connection")
	}
	_ = firstDaemon.Close()
	// TRIPWIRE: both in-memory pipe ends were closed above; this only guards a
	// regression that strands the single launch-broker reader.
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first launch broker remained blocked after acknowledgment loss")
	}
	persisted, err := authority.RootAssociation(t.Context(), rootID)
	if err != nil {
		t.Fatalf("durable association after acknowledgment loss: %v", err)
	}

	secondHubSide, secondDaemonSide := net.Pipe()
	secondHub := NewLaunchBroker(LaunchBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(secondHubSide, appwire.PrivateBrokerFrameLimit),
		Authority: authority, LaunchID: "second-launch", HubEpoch: "second-hub", ProjectID: "project-0123456789", ResumeID: rootID,
	})
	secondDaemon := NewDaemonBroker(DaemonBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(secondDaemonSide, appwire.PrivateBrokerFrameLimit), LaunchID: "second-launch",
	})
	defer secondDaemon.Close() //nolint:errcheck
	go func() { _ = secondHub.Serve(t.Context()) }()
	retried, err := secondDaemon.EstablishRoot(t.Context(), rootID, 1)
	if err != nil {
		t.Fatalf("retry EstablishRoot: %v", err)
	}
	if got := brokerAssociation(persisted); !reflect.DeepEqual(retried, got) {
		t.Fatalf("retried association = %+v, want persisted %+v", retried, got)
	}
}

func completeBrokerIdentity(rootID string) appwire.BrokerDaemonIdentity {
	return appwire.BrokerDaemonIdentity{
		PID: 42, Address: "127.0.0.1:4131", Endpoint: "ws://127.0.0.1:4131/rpc", Protocol: appwire.ProtocolVersion,
		SourceID: "local", ThreadID: rootID, SessionID: rootID, InstanceID: rootID, WorkspaceRef: "local:" + rootID,
		WorkingDir: "/work", StateDir: "/state", StartedAt: time.Unix(123, 456).UTC().Format(time.RFC3339Nano),
	}
}

func TestDirectRebootstrapRejectsStandaloneDaemonBeforeCandidateActivation(t *testing.T) {
	hubSide, daemonSide := net.Pipe()
	hubTransport := appwire.NewStreamTransportWithLimit(hubSide, appwire.PrivateBrokerFrameLimit)
	defer hubTransport.Close() //nolint:errcheck
	daemon := NewDaemonBroker(DaemonBrokerConfig{})
	validateCalled := false
	completeCalled := false
	err := daemon.AcceptRebootstrap(t.Context(), appwire.NewStreamTransportWithLimit(daemonSide, appwire.PrivateBrokerFrameLimit), DaemonRebootstrapCallbacks{
		Validate: func(appwire.BrokerDaemonIdentity) (appwire.BrokerDaemonIdentity, uint64, error) {
			validateCalled = true
			return appwire.BrokerDaemonIdentity{}, 1, nil
		},
		Complete: func(uint64) error {
			completeCalled = true
			return nil
		},
	})
	if !errors.Is(err, ErrBrokerAuthentication) {
		t.Fatalf("AcceptRebootstrap error = %v, want authentication rejection", err)
	}
	if validateCalled || completeCalled {
		t.Fatalf("standalone daemon activated candidate callbacks: validate=%v complete=%v", validateCalled, completeCalled)
	}
	if message, err := hubTransport.Recv(t.Context()); err == nil {
		t.Fatalf("standalone daemon sent an install response: %+v", message)
	}
	daemon.mu.Lock()
	client, epoch := daemon.client, daemon.daemonEpoch
	daemon.mu.Unlock()
	if client != nil || epoch != "" {
		t.Fatalf("standalone daemon accepted candidate client=%v epoch=%q", client != nil, epoch)
	}
}

func TestDirectRebootstrapRejectsAlteredOriginalAssociationBeforeCandidateActivation(t *testing.T) {
	authority, err := OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close() //nolint:errcheck
	rootID := identifier.MustNewSessionID()
	identity := completeBrokerIdentity(rootID)

	launchHubSide, launchDaemonSide := net.Pipe()
	launchHub := NewLaunchBroker(LaunchBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(launchHubSide, appwire.PrivateBrokerFrameLimit),
		Authority: authority, LaunchID: "launch", HubEpoch: "hub-one", ProjectID: "project-0123456789", ResumeID: rootID,
	})
	daemon := NewDaemonBroker(DaemonBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(launchDaemonSide, appwire.PrivateBrokerFrameLimit), LaunchID: "launch",
	})
	defer daemon.Close() //nolint:errcheck
	go func() { _ = launchHub.Serve(t.Context()) }()
	if _, err := daemon.EstablishRoot(t.Context(), rootID, 1); err != nil {
		t.Fatal(err)
	}
	if err := launchHub.ExpectOwnership(identity); err != nil {
		t.Fatal(err)
	}
	if err := daemon.InstallOwnership(t.Context(), identity); err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	priorClient := daemon.client
	priorEpoch := daemon.daemonEpoch
	altered := daemon.association
	daemon.mu.Unlock()
	altered.NamespaceID += "-altered"

	hubSide, daemonSide := net.Pipe()
	hubTransport := appwire.NewStreamTransportWithLimit(hubSide, appwire.PrivateBrokerFrameLimit)
	defer hubTransport.Close() //nolint:errcheck
	validateCalled := false
	completeCalled := false
	daemonDone := make(chan error, 1)
	go func() {
		daemonDone <- daemon.AcceptRebootstrap(t.Context(), appwire.NewStreamTransportWithLimit(daemonSide, appwire.PrivateBrokerFrameLimit), DaemonRebootstrapCallbacks{
			Validate: func(appwire.BrokerDaemonIdentity) (appwire.BrokerDaemonIdentity, uint64, error) {
				validateCalled = true
				return identity, 2, nil
			},
			Complete: func(uint64) error {
				completeCalled = true
				return nil
			},
		})
	}()
	install := appwire.BrokerInstallParams{
		HubEpoch: "hub-two", DaemonEpoch: "daemon-two", Capability: "connection-capability",
		Association: altered, ExpectedDaemonIdentity: identity,
	}
	if err := hubTransport.Send(t.Context(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodBrokerInstall, install)); err != nil {
		t.Fatalf("send altered install: %v", err)
	}
	if message, err := hubTransport.Recv(t.Context()); err == nil {
		t.Fatalf("altered association received an install response: %+v", message)
	}
	if err := <-daemonDone; !errors.Is(err, ErrBrokerAuthentication) {
		t.Fatalf("AcceptRebootstrap error = %v, want authentication rejection", err)
	}
	if validateCalled || completeCalled {
		t.Fatalf("altered association activated candidate callbacks: validate=%v complete=%v", validateCalled, completeCalled)
	}
	daemon.mu.Lock()
	retainedClient := daemon.client
	retainedEpoch := daemon.daemonEpoch
	daemon.mu.Unlock()
	if retainedClient != priorClient || retainedEpoch != priorEpoch {
		t.Fatal("altered association replaced the original authenticated connection")
	}
}

func TestDirectRebootstrapAuthenticatesCurrentOwnedDaemonAndReplacesConnection(t *testing.T) {
	authority, err := OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close() //nolint:errcheck
	rootID := identifier.MustNewSessionID()
	identity := completeBrokerIdentity(rootID)

	launchHubSide, launchDaemonSide := net.Pipe()
	launchHub := NewLaunchBroker(LaunchBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(launchHubSide, appwire.PrivateBrokerFrameLimit),
		Authority: authority, LaunchID: "launch", HubEpoch: "hub-one", ProjectID: "project-0123456789", ResumeID: rootID,
	})
	daemon := NewDaemonBroker(DaemonBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(launchDaemonSide, appwire.PrivateBrokerFrameLimit), LaunchID: "launch",
	})
	defer daemon.Close() //nolint:errcheck
	launchDone := make(chan error, 1)
	go func() { launchDone <- launchHub.Serve(t.Context()) }()
	if _, err := daemon.EstablishRoot(t.Context(), rootID, 1); err != nil {
		t.Fatal(err)
	}
	if err := launchHub.ExpectOwnership(identity); err != nil {
		t.Fatal(err)
	}
	if err := daemon.InstallOwnership(t.Context(), identity); err != nil {
		t.Fatal(err)
	}

	hubSide, daemonSide := net.Pipe()
	generation := uint64(9)
	daemonDone := make(chan error, 1)
	go func() {
		daemonDone <- daemon.AcceptRebootstrap(t.Context(), appwire.NewStreamTransportWithLimit(daemonSide, appwire.PrivateBrokerFrameLimit), DaemonRebootstrapCallbacks{
			Validate: func(expected appwire.BrokerDaemonIdentity) (appwire.BrokerDaemonIdentity, uint64, error) {
				if !reflect.DeepEqual(expected, identity) {
					return appwire.BrokerDaemonIdentity{}, 0, ErrBrokerAuthentication
				}
				return identity, generation, nil
			},
			Complete: func(candidate uint64) error {
				if candidate != generation {
					return ErrBrokerAuthentication
				}
				generation++
				return nil
			},
		})
	}()
	connection, err := EstablishHubRebootstrap(t.Context(), HubRebootstrapConfig{
		Transport: appwire.NewStreamTransportWithLimit(hubSide, appwire.PrivateBrokerFrameLimit), Authority: authority,
		HubEpoch: "hub-two", ExpectedIdentity: identity,
	})
	if err != nil {
		t.Fatalf("EstablishHubRebootstrap: %v", err)
	}
	defer connection.Close() //nolint:errcheck
	if err := <-daemonDone; err != nil {
		t.Fatalf("AcceptRebootstrap: %v", err)
	}
	if generation != 10 {
		t.Fatalf("generation = %d, want completed candidate", generation)
	}
	// TRIPWIRE: successful replacement closes the former launch client, so its
	// single in-memory reader must finish without wall-clock pacing.
	select {
	case <-launchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("superseded launch connection remained usable")
	}
}

func TestDirectRebootstrapRejectsReplacedGenerationWithoutReplacingConnection(t *testing.T) {
	authority, err := OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close() //nolint:errcheck
	rootID := identifier.MustNewSessionID()
	identity := completeBrokerIdentity(rootID)

	launchHubSide, launchDaemonSide := net.Pipe()
	launchHub := NewLaunchBroker(LaunchBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(launchHubSide, appwire.PrivateBrokerFrameLimit),
		Authority: authority, LaunchID: "launch", HubEpoch: "hub-one", ProjectID: "project-0123456789", ResumeID: rootID,
	})
	daemon := NewDaemonBroker(DaemonBrokerConfig{
		Transport: appwire.NewStreamTransportWithLimit(launchDaemonSide, appwire.PrivateBrokerFrameLimit), LaunchID: "launch",
	})
	defer daemon.Close() //nolint:errcheck
	go func() { _ = launchHub.Serve(t.Context()) }()
	if _, err := daemon.EstablishRoot(t.Context(), rootID, 1); err != nil {
		t.Fatal(err)
	}
	if err := launchHub.ExpectOwnership(identity); err != nil {
		t.Fatal(err)
	}
	if err := daemon.InstallOwnership(t.Context(), identity); err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	prior := daemon.client
	daemon.mu.Unlock()

	hubSide, daemonSide := net.Pipe()
	daemonDone := make(chan error, 1)
	go func() {
		daemonDone <- daemon.AcceptRebootstrap(t.Context(), appwire.NewStreamTransportWithLimit(daemonSide, appwire.PrivateBrokerFrameLimit), DaemonRebootstrapCallbacks{
			Validate: func(expected appwire.BrokerDaemonIdentity) (appwire.BrokerDaemonIdentity, uint64, error) {
				return identity, 7, nil
			},
			Complete: func(uint64) error { return ErrBrokerAuthentication },
		})
	}()
	type hubResult struct {
		connection *HubBrokerConnection
		err        error
	}
	hubDone := make(chan hubResult, 1)
	go func() {
		connection, establishErr := EstablishHubRebootstrap(t.Context(), HubRebootstrapConfig{
			Transport: appwire.NewStreamTransportWithLimit(hubSide, appwire.PrivateBrokerFrameLimit), Authority: authority,
			HubEpoch: "hub-two", ExpectedIdentity: identity,
		})
		hubDone <- hubResult{connection: connection, err: establishErr}
	}()
	if err := <-daemonDone; !errors.Is(err, ErrBrokerAuthentication) {
		t.Fatalf("AcceptRebootstrap error = %v, want authentication rejection", err)
	}
	result := <-hubDone
	if result.connection != nil {
		_ = result.connection.Close()
	}
	daemon.mu.Lock()
	retained := daemon.client
	daemon.mu.Unlock()
	if retained != prior {
		t.Fatal("rejected generation replaced the prior authenticated connection")
	}
}
