package interactiveartifacts

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
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
