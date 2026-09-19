package interactiveartifacts

import (
	"context"
	"reflect"
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

type HubRebootstrapConfig struct {
	Transport        appwire.Transport
	Authority        *HostAuthority
	HubEpoch         string
	ExpectedIdentity appwire.BrokerDaemonIdentity
}

// HubBrokerConnection owns one directly reinstalled daemon epoch. Task 4B.3
// extends its authenticated request loop with grant methods; this unit admits
// no method after Authenticate.
type HubBrokerConnection struct {
	transport appwire.Transport
	done      chan struct{}
	once      sync.Once
	errMu     sync.Mutex
	err       error
}

func EstablishHubRebootstrap(ctx context.Context, cfg HubRebootstrapConfig) (*HubBrokerConnection, error) {
	if cfg.Transport == nil || cfg.Authority == nil || cfg.HubEpoch == "" || validateDaemonIdentity(cfg.ExpectedIdentity) != nil {
		return nil, ErrBrokerAuthentication
	}
	root, err := cfg.Authority.RootAssociation(ctx, cfg.ExpectedIdentity.SessionID)
	if err != nil {
		return nil, ErrBrokerAuthentication
	}
	association := brokerAssociation(root)
	daemonEpoch, capability := randomID(), randomID()
	installID := appwire.NewIntID(1)
	install := appwire.BrokerInstallParams{
		HubEpoch: cfg.HubEpoch, DaemonEpoch: daemonEpoch, Capability: capability,
		Association: association, ExpectedDaemonIdentity: cfg.ExpectedIdentity,
	}
	if err := cfg.Transport.Send(ctx, appwire.RequestMessage(installID, appwire.MethodBrokerInstall, install)); err != nil {
		_ = cfg.Transport.Close()
		return nil, err
	}
	message, err := cfg.Transport.Recv(ctx)
	if err != nil {
		_ = cfg.Transport.Close()
		return nil, err
	}
	var installed appwire.BrokerInstallResponse
	if err := decodeResponse(message, installID, &installed); err != nil ||
		!reflect.DeepEqual(installed.ActualDaemonIdentity, cfg.ExpectedIdentity) ||
		installed.RootSessionID != cfg.ExpectedIdentity.SessionID || installed.AssociationDigest != associationDigest(association) ||
		installed.DaemonEpoch != daemonEpoch {
		_ = cfg.Transport.Close()
		return nil, ErrBrokerAuthentication
	}
	authMessage, err := cfg.Transport.Recv(ctx)
	if err != nil {
		_ = cfg.Transport.Close()
		return nil, err
	}
	if authMessage.Request == nil || authMessage.Request.Method != appwire.MethodBrokerAuthenticate {
		_ = cfg.Transport.Close()
		return nil, ErrBrokerAuthentication
	}
	var auth appwire.BrokerAuthenticateParams
	if decodePrivate(authMessage.Request.Params, &auth) != nil || auth.DaemonEpoch != daemonEpoch || auth.Capability != capability {
		_ = cfg.Transport.Close()
		return nil, ErrBrokerAuthentication
	}
	if err := cfg.Transport.Send(ctx, appwire.ResponseMessage(authMessage.Request.ID, appwire.BrokerAuthenticateResponse{DaemonEpoch: daemonEpoch})); err != nil {
		_ = cfg.Transport.Close()
		return nil, err
	}
	connection := &HubBrokerConnection{transport: cfg.Transport, done: make(chan struct{})}
	go connection.rejectUnsupported(context.WithoutCancel(ctx))
	return connection, nil
}

func (c *HubBrokerConnection) rejectUnsupported(ctx context.Context) {
	defer close(c.done)
	for {
		message, err := c.transport.Recv(ctx)
		if err != nil {
			c.errMu.Lock()
			c.err = err
			c.errMu.Unlock()
			return
		}
		if message.Request == nil {
			c.errMu.Lock()
			c.err = ErrBrokerAuthentication
			c.errMu.Unlock()
			_ = c.transport.Close()
			return
		}
		_ = c.transport.Send(ctx, appwire.ErrorMessage(message.Request.ID, appwire.InternalError("private broker method unavailable")))
	}
}

func (c *HubBrokerConnection) Close() error {
	c.once.Do(func() { _ = c.transport.Close() })
	return nil
}

func (c *HubBrokerConnection) Done() <-chan struct{} { return c.done }

func (c *HubBrokerConnection) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

type DaemonRebootstrapCallbacks struct {
	// Validate runs under the daemon identity-transition lock and returns a
	// pending generation plus the actual current identity.
	Validate func(appwire.BrokerDaemonIdentity) (appwire.BrokerDaemonIdentity, uint64, error)
	// Complete re-takes that lock and rejects a candidate whose generation or
	// identity changed while Hub I/O was in flight.
	Complete func(uint64) error
}

func (b *DaemonBroker) AcceptRebootstrap(ctx context.Context, transport appwire.Transport, callbacks DaemonRebootstrapCallbacks) error {
	if transport == nil || callbacks.Validate == nil || callbacks.Complete == nil {
		return ErrBrokerAuthentication
	}
	b.mu.Lock()
	trustedAssociation := b.association
	hasTrustedAssociation := b.client != nil && trustedAssociation.RootSessionID != ""
	b.mu.Unlock()
	if !hasTrustedAssociation {
		_ = transport.Close()
		return ErrBrokerAuthentication
	}
	message, err := transport.Recv(ctx)
	if err != nil {
		_ = transport.Close()
		return err
	}
	if message.Request == nil || message.Request.Method != appwire.MethodBrokerInstall {
		_ = transport.Close()
		return ErrBrokerAuthentication
	}
	var install appwire.BrokerInstallParams
	if decodePrivate(message.Request.Params, &install) != nil || install.HubEpoch == "" || install.DaemonEpoch == "" || install.Capability == "" ||
		!reflect.DeepEqual(install.Association, trustedAssociation) {
		_ = transport.Close()
		return ErrBrokerAuthentication
	}
	actual, generation, err := callbacks.Validate(install.ExpectedDaemonIdentity)
	if err != nil || validateDaemonIdentity(actual) != nil || !reflect.DeepEqual(actual, install.ExpectedDaemonIdentity) || actual.SessionID != trustedAssociation.RootSessionID {
		_ = transport.Close()
		return ErrBrokerAuthentication
	}
	installed := appwire.BrokerInstallResponse{
		ActualDaemonIdentity: actual, RootSessionID: actual.SessionID,
		AssociationDigest: associationDigest(trustedAssociation), DaemonEpoch: install.DaemonEpoch,
	}
	if err := transport.Send(ctx, appwire.ResponseMessage(message.Request.ID, installed)); err != nil {
		_ = transport.Close()
		return err
	}
	client := appwire.NewClient(transport)
	client.Start(ctx)
	var auth appwire.BrokerAuthenticateResponse
	if err := client.Request(ctx, appwire.MethodBrokerAuthenticate, appwire.BrokerAuthenticateParams{
		DaemonEpoch: install.DaemonEpoch, Capability: install.Capability,
	}, &auth); err != nil || auth.DaemonEpoch != install.DaemonEpoch {
		_ = client.Close()
		return ErrBrokerAuthentication
	}
	if err := callbacks.Complete(generation); err != nil {
		_ = client.Close()
		return err
	}
	b.mu.Lock()
	old := b.client
	b.client = client
	b.daemonEpoch = install.DaemonEpoch
	b.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func validateDaemonIdentity(identity appwire.BrokerDaemonIdentity) error {
	if identity.PID <= 0 || identity.Address == "" || identity.Endpoint == "" || identity.Protocol == "" ||
		identity.SourceID == "" || identity.ThreadID == "" || identity.SessionID == "" || identity.InstanceID == "" ||
		identity.WorkspaceRef == "" || identity.WorkingDir == "" || identity.StateDir == "" || identity.StartedAt == "" {
		return ErrBrokerAuthentication
	}
	if identity.ThreadID != identity.SessionID || identity.InstanceID != identity.SessionID {
		return ErrBrokerAuthentication
	}
	if err := identifier.ValidateSessionID(identity.SessionID); err != nil {
		return ErrBrokerAuthentication
	}
	return nil
}
