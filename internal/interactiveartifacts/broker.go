package interactiveartifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

var (
	ErrBrokerAuthentication = errors.New("artifact broker authentication failed")
	ErrBrokerSealed         = errors.New("artifact broker sealed")
)

var brokerIDPattern = regexp.MustCompile(`\A[0-9a-f]{32}\z`)

func NewBrokerID() string { return randomID() }

type splitBrokerStream struct {
	reader io.ReadCloser
	writer io.WriteCloser
	once   sync.Once
	err    error
}

func (s *splitBrokerStream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *splitBrokerStream) Write(p []byte) (int, error) { return s.writer.Write(p) }
func (s *splitBrokerStream) Close() error {
	s.once.Do(func() { s.err = errors.Join(s.reader.Close(), s.writer.Close()) })
	return s.err
}

// NewPrivateBrokerTransport joins the two unidirectional private descriptors
// and applies the protocol's finite frame limit in both processes.
func NewPrivateBrokerTransport(reader io.ReadCloser, writer io.WriteCloser) appwire.Transport {
	return appwire.NewStreamTransportWithLimit(&splitBrokerStream{reader: reader, writer: writer}, appwire.PrivateBrokerFrameLimit)
}

// WriteLaunchCorrelation sends the Hub-owned launch correlation over the
// inherited private channel before AppWire framing begins.
func WriteLaunchCorrelation(writer io.Writer, launchID string) error {
	if !brokerIDPattern.MatchString(launchID) {
		return ErrBrokerAuthentication
	}
	data := append([]byte(launchID), '\n')
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// ReadLaunchCorrelation reads exactly one fixed-size correlation record. It
// does not buffer into the following AppWire frame, preserving one reader per
// handshake phase.
func ReadLaunchCorrelation(reader io.Reader) (string, error) {
	var data [33]byte
	if _, err := io.ReadFull(reader, data[:]); err != nil {
		return "", err
	}
	if data[len(data)-1] != '\n' {
		return "", ErrBrokerAuthentication
	}
	launchID := string(data[:len(data)-1])
	if !brokerIDPattern.MatchString(launchID) {
		return "", ErrBrokerAuthentication
	}
	return launchID, nil
}

type LaunchBrokerConfig struct {
	Transport appwire.Transport
	Authority *HostAuthority
	LaunchID  string
	HubEpoch  string
	ProjectID string
	ResumeID  string
}

// LaunchBroker owns one private inherited channel belonging to one exec.Cmd.
// It persists the actual root before acknowledging install, then waits for the
// ordinary rendezvous path to supply the complete identity for finalization.
type LaunchBroker struct {
	cfg LaunchBrokerConfig

	mu                  sync.Mutex
	capability          string
	daemonEpoch         string
	association         appwire.BrokerAssociation
	expectedOwnership   appwire.BrokerDaemonIdentity
	expectedOwnershipOK bool
	sealed              error

	expectedReady chan struct{}
	authenticated chan struct{}
	finalized     chan struct{}
	finalizeOnce  sync.Once
	serveDone     chan struct{}
	serveErr      error
}

func NewLaunchBroker(cfg LaunchBrokerConfig) *LaunchBroker {
	return &LaunchBroker{
		cfg: cfg, expectedReady: make(chan struct{}), authenticated: make(chan struct{}),
		finalized: make(chan struct{}), serveDone: make(chan struct{}),
	}
}

func (b *LaunchBroker) Serve(ctx context.Context) (resultErr error) {
	defer func() {
		b.mu.Lock()
		b.serveErr = resultErr
		b.mu.Unlock()
		close(b.serveDone)
	}()
	if err := b.validateConfig(); err != nil {
		_ = b.cfg.Transport.Close()
		return err
	}
	defer b.cfg.Transport.Close() //nolint:errcheck

	helloMessage, err := b.cfg.Transport.Recv(ctx)
	if err != nil {
		return err
	}
	if helloMessage.Request == nil || helloMessage.Request.Method != appwire.MethodBrokerLaunchHello {
		return b.rejectRequest(ctx, helloMessage, ErrBrokerAuthentication)
	}
	var hello appwire.BrokerLaunchHelloParams
	if err := decodePrivate(helloMessage.Request.Params, &hello); err != nil || hello.LaunchID != b.cfg.LaunchID || hello.RuntimeGeneration == 0 {
		return b.rejectRequest(ctx, helloMessage, ErrBrokerAuthentication)
	}
	if err := identifier.ValidateSessionID(hello.ActualRootSessionID); err != nil {
		return b.rejectRequest(ctx, helloMessage, ErrBrokerAuthentication)
	}
	if b.cfg.ResumeID != "" && hello.ActualRootSessionID != b.cfg.ResumeID {
		return b.rejectRequest(ctx, helloMessage, ErrBrokerAuthentication)
	}

	installation := b.cfg.Authority.Installation()
	if err := b.cfg.Authority.ReconcileDurability(ctx); err != nil {
		return b.rejectRequest(ctx, helloMessage, ErrBrokerAuthentication)
	}
	root, err := b.cfg.Authority.PrepareRoot(ctx, RootRequest{
		SessionID: hello.ActualRootSessionID, ProjectID: b.cfg.ProjectID,
		RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID,
	})
	if err != nil {
		return b.rejectRequest(ctx, helloMessage, ErrBrokerAuthentication)
	}
	association := brokerAssociation(root)
	capability := randomID()
	daemonEpoch := randomID()
	b.mu.Lock()
	b.capability = capability
	b.daemonEpoch = daemonEpoch
	b.association = association
	b.mu.Unlock()

	if err := b.cfg.Transport.Send(ctx, appwire.ResponseMessage(helloMessage.Request.ID, appwire.BrokerLaunchHelloResponse{})); err != nil {
		return err
	}
	installID := appwire.NewIntID(2)
	install := appwire.BrokerLaunchInstallParams{
		LaunchID: b.cfg.LaunchID, HubEpoch: b.cfg.HubEpoch, DaemonEpoch: daemonEpoch,
		Capability: capability, Association: association,
	}
	if err := b.cfg.Transport.Send(ctx, appwire.RequestMessage(installID, appwire.MethodBrokerLaunchInstall, install)); err != nil {
		return err
	}
	installedMessage, err := b.cfg.Transport.Recv(ctx)
	if err != nil {
		return err
	}
	var installed appwire.BrokerLaunchInstallResponse
	if err := decodeResponse(installedMessage, installID, &installed); err != nil ||
		installed.LaunchID != b.cfg.LaunchID || installed.RootSessionID != root.SessionID ||
		installed.AssociationDigest != associationDigest(association) || installed.DaemonEpoch != daemonEpoch {
		return ErrBrokerAuthentication
	}

	bootstrapRouter := appserver.NewRouter()
	bootstrapRouter.Handle(appwire.MethodBrokerFinalizeOwnership, func(handlerCtx context.Context, raw json.RawMessage) (any, error) {
		var params appwire.BrokerFinalizeOwnershipParams
		if err := decodePrivate(raw, &params); err != nil {
			return nil, ErrBrokerAuthentication
		}
		return b.finalizeOwnership(handlerCtx, params)
	})
	brokerRouter := appserver.NewRouter()
	authenticated := false
	brokerRouter.Handle(appwire.MethodBrokerAuthenticate, func(_ context.Context, raw json.RawMessage) (any, error) {
		var params appwire.BrokerAuthenticateParams
		if err := decodePrivate(raw, &params); err != nil {
			return nil, ErrBrokerAuthentication
		}
		if authenticated || params.DaemonEpoch != daemonEpoch || params.Capability != capability {
			return nil, ErrBrokerAuthentication
		}
		authenticated = true
		close(b.authenticated)
		return appwire.BrokerAuthenticateResponse{DaemonEpoch: daemonEpoch}, nil
	})

	for {
		message, recvErr := b.cfg.Transport.Recv(ctx)
		if recvErr != nil {
			return recvErr
		}
		if message.Request == nil {
			return ErrBrokerAuthentication
		}
		var router *appserver.Router
		switch message.Request.Method {
		case appwire.MethodBrokerAuthenticate:
			router = brokerRouter
		case appwire.MethodBrokerFinalizeOwnership:
			if !authenticated {
				return b.rejectRequest(ctx, message, ErrBrokerAuthentication)
			}
			router = bootstrapRouter
		default:
			return b.rejectRequest(ctx, message, ErrBrokerAuthentication)
		}
		result, dispatchErr := router.Dispatch(ctx, *message.Request)
		if dispatchErr != nil {
			_ = b.cfg.Transport.Send(ctx, appwire.ErrorMessage(message.Request.ID, appwire.InternalError("private broker authentication failed")))
			return ErrBrokerAuthentication
		}
		if err := b.cfg.Transport.Send(ctx, appwire.ResponseMessage(message.Request.ID, result)); err != nil {
			return err
		}
		if message.Request.Method == appwire.MethodBrokerFinalizeOwnership {
			b.finalizeOnce.Do(func() { close(b.finalized) })
		}
	}
}

func (b *LaunchBroker) validateConfig() error {
	if b.cfg.Transport == nil || b.cfg.Authority == nil || b.cfg.LaunchID == "" || b.cfg.HubEpoch == "" {
		return ErrBrokerAuthentication
	}
	if err := identifier.ValidateProjectID(b.cfg.ProjectID); err != nil {
		return ErrBrokerAuthentication
	}
	if b.cfg.ResumeID != "" {
		if err := identifier.ValidateSessionID(b.cfg.ResumeID); err != nil {
			return ErrBrokerAuthentication
		}
	}
	return nil
}

func (b *LaunchBroker) rejectRequest(ctx context.Context, message appwire.Message, err error) error {
	if message.Request != nil {
		_ = b.cfg.Transport.Send(ctx, appwire.ErrorMessage(message.Request.ID, appwire.InternalError("private broker authentication failed")))
	}
	return err
}

func (b *LaunchBroker) finalizeOwnership(ctx context.Context, params appwire.BrokerFinalizeOwnershipParams) (appwire.BrokerFinalizeOwnershipResponse, error) {
	if params.LaunchID != b.cfg.LaunchID {
		return appwire.BrokerFinalizeOwnershipResponse{}, ErrBrokerAuthentication
	}
	select {
	case <-ctx.Done():
		return appwire.BrokerFinalizeOwnershipResponse{}, ctx.Err()
	case <-b.expectedReady:
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sealed != nil || !b.expectedOwnershipOK || !reflect.DeepEqual(params.ActualDaemonIdentity, b.expectedOwnership) {
		return appwire.BrokerFinalizeOwnershipResponse{}, ErrBrokerAuthentication
	}
	return appwire.BrokerFinalizeOwnershipResponse{DaemonEpoch: b.daemonEpoch}, nil
}

func (b *LaunchBroker) ExpectOwnership(identity appwire.BrokerDaemonIdentity) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sealed != nil || b.expectedOwnershipOK {
		return ErrBrokerSealed
	}
	b.expectedOwnership = identity
	b.expectedOwnershipOK = true
	close(b.expectedReady)
	return nil
}

func (b *LaunchBroker) WaitFinalized(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.finalized:
		return nil
	case <-b.serveDone:
		b.mu.Lock()
		err := b.serveErr
		b.mu.Unlock()
		if err == nil {
			return ErrBrokerSealed
		}
		return errors.Join(ErrBrokerSealed, err)
	}
}

// FinalizeLaunch publishes the rendezvous identity and, when the daemon had
// already authenticated before rendezvous, waits for its exact same-channel
// finalization acknowledgment. A broker first used later can finalize against
// the already-published identity without holding up an ordinary launch.
func (b *LaunchBroker) FinalizeLaunch(ctx context.Context, identity appwire.BrokerDaemonIdentity) error {
	if err := b.ExpectOwnership(identity); err != nil {
		return err
	}
	select {
	case <-b.authenticated:
		return b.WaitFinalized(ctx)
	default:
		return nil
	}
}

func (b *LaunchBroker) Seal(cause error) {
	b.mu.Lock()
	if b.sealed == nil {
		b.sealed = errors.Join(ErrBrokerSealed, cause)
		if !b.expectedOwnershipOK {
			close(b.expectedReady)
		}
	}
	b.mu.Unlock()
	_ = b.cfg.Transport.Close()
}

type DaemonBrokerConfig struct {
	Transport appwire.Transport
	LaunchID  string
}

// DaemonBroker is the daemon-owned end of one private connection. EstablishRoot
// is the lazy seam a managed binding can call during construction or restore;
// no session constructor must return before it can authenticate.
type DaemonBroker struct {
	cfg DaemonBrokerConfig

	mu           sync.Mutex
	establishing chan struct{}
	establishErr error
	association  appwire.BrokerAssociation
	daemonEpoch  string
	client       *appwire.Client
	ownership    appwire.BrokerDaemonIdentity
	ownershipSet bool
}

func NewDaemonBroker(cfg DaemonBrokerConfig) *DaemonBroker { return &DaemonBroker{cfg: cfg} }

func (b *DaemonBroker) EstablishRoot(ctx context.Context, rootSessionID string, runtimeGeneration uint64) (appwire.BrokerAssociation, error) {
	b.mu.Lock()
	if b.client != nil {
		association := b.association
		b.mu.Unlock()
		if association.RootSessionID != rootSessionID {
			return appwire.BrokerAssociation{}, ErrBrokerAuthentication
		}
		return association, nil
	}
	if b.establishing != nil {
		done := b.establishing
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return appwire.BrokerAssociation{}, ctx.Err()
		case <-done:
		}
		b.mu.Lock()
		association, err := b.association, b.establishErr
		b.mu.Unlock()
		return association, err
	}
	b.establishing = make(chan struct{})
	done := b.establishing
	b.mu.Unlock()

	association, daemonEpoch, client, err := b.establish(ctx, rootSessionID, runtimeGeneration)
	b.mu.Lock()
	if err == nil {
		b.association = association
		b.daemonEpoch = daemonEpoch
		b.client = client
	}
	ownership, ownershipSet := b.ownership, b.ownershipSet
	b.establishErr = err
	close(done)
	b.mu.Unlock()
	if err == nil && ownershipSet {
		if finalizeErr := b.FinalizeOwnership(ctx, ownership); finalizeErr != nil {
			return appwire.BrokerAssociation{}, finalizeErr
		}
	}
	return association, err
}

func (b *DaemonBroker) establish(ctx context.Context, rootSessionID string, runtimeGeneration uint64) (appwire.BrokerAssociation, string, *appwire.Client, error) {
	if b.cfg.Transport == nil || b.cfg.LaunchID == "" || runtimeGeneration == 0 {
		return appwire.BrokerAssociation{}, "", nil, ErrBrokerAuthentication
	}
	helloID := appwire.NewIntID(1)
	hello := appwire.BrokerLaunchHelloParams{LaunchID: b.cfg.LaunchID, ActualRootSessionID: rootSessionID, RuntimeGeneration: runtimeGeneration}
	if err := b.cfg.Transport.Send(ctx, appwire.RequestMessage(helloID, appwire.MethodBrokerLaunchHello, hello)); err != nil {
		return appwire.BrokerAssociation{}, "", nil, err
	}
	helloResponse, err := b.cfg.Transport.Recv(ctx)
	if err != nil {
		return appwire.BrokerAssociation{}, "", nil, err
	}
	var acknowledged appwire.BrokerLaunchHelloResponse
	if err := decodeResponse(helloResponse, helloID, &acknowledged); err != nil {
		return appwire.BrokerAssociation{}, "", nil, ErrBrokerAuthentication
	}
	installMessage, err := b.cfg.Transport.Recv(ctx)
	if err != nil {
		return appwire.BrokerAssociation{}, "", nil, err
	}
	if installMessage.Request == nil || installMessage.Request.Method != appwire.MethodBrokerLaunchInstall {
		return appwire.BrokerAssociation{}, "", nil, ErrBrokerAuthentication
	}
	var install appwire.BrokerLaunchInstallParams
	if err := decodePrivate(installMessage.Request.Params, &install); err != nil ||
		install.LaunchID != b.cfg.LaunchID || install.HubEpoch == "" || install.DaemonEpoch == "" || install.Capability == "" ||
		install.Association.RootSessionID != rootSessionID {
		return appwire.BrokerAssociation{}, "", nil, ErrBrokerAuthentication
	}
	response := appwire.BrokerLaunchInstallResponse{
		LaunchID: b.cfg.LaunchID, RootSessionID: rootSessionID,
		AssociationDigest: associationDigest(install.Association), DaemonEpoch: install.DaemonEpoch,
	}
	if err := b.cfg.Transport.Send(ctx, appwire.ResponseMessage(installMessage.Request.ID, response)); err != nil {
		return appwire.BrokerAssociation{}, "", nil, err
	}

	client := appwire.NewClient(b.cfg.Transport)
	client.Start(ctx)
	var auth appwire.BrokerAuthenticateResponse
	if err := client.Request(ctx, appwire.MethodBrokerAuthenticate, appwire.BrokerAuthenticateParams{DaemonEpoch: install.DaemonEpoch, Capability: install.Capability}, &auth); err != nil || auth.DaemonEpoch != install.DaemonEpoch {
		_ = client.Close()
		return appwire.BrokerAssociation{}, "", nil, ErrBrokerAuthentication
	}
	return install.Association, install.DaemonEpoch, client, nil
}

func (b *DaemonBroker) FinalizeOwnership(ctx context.Context, identity appwire.BrokerDaemonIdentity) error {
	b.mu.Lock()
	client, epoch := b.client, b.daemonEpoch
	b.mu.Unlock()
	if client == nil {
		return ErrBrokerAuthentication
	}
	var response appwire.BrokerFinalizeOwnershipResponse
	if err := client.Request(ctx, appwire.MethodBrokerFinalizeOwnership, appwire.BrokerFinalizeOwnershipParams{LaunchID: b.cfg.LaunchID, ActualDaemonIdentity: identity}, &response); err != nil {
		return err
	}
	if response.DaemonEpoch != epoch {
		return ErrBrokerAuthentication
	}
	return nil
}

// InstallOwnership records the complete rendezvous identity and finalizes it
// immediately when construction already established the private connection.
// If registration wins the race, EstablishRoot finalizes before returning.
func (b *DaemonBroker) InstallOwnership(ctx context.Context, identity appwire.BrokerDaemonIdentity) error {
	b.mu.Lock()
	b.ownership = identity
	b.ownershipSet = true
	established := b.client != nil
	b.mu.Unlock()
	if !established {
		return nil
	}
	return b.FinalizeOwnership(ctx, identity)
}

func (b *DaemonBroker) Close() error {
	b.mu.Lock()
	client := b.client
	b.mu.Unlock()
	if client != nil {
		return client.Close()
	}
	if b.cfg.Transport != nil {
		return b.cfg.Transport.Close()
	}
	return nil
}

func brokerAssociation(root RootAssociation) appwire.BrokerAssociation {
	return appwire.BrokerAssociation{
		RealmID: root.RealmID, HumanOwnerID: root.HumanOwnerID, RootSessionID: root.RootSessionID,
		PrincipalID: root.PrincipalID, NamespaceID: root.NamespaceID, ProjectID: root.ProjectID,
	}
}

func DaemonIdentity(entry rendezvous.Entry) appwire.BrokerDaemonIdentity {
	return appwire.BrokerDaemonIdentity{
		PID: entry.PID, Address: entry.Address, Endpoint: entry.Endpoint, Protocol: entry.Protocol,
		SourceID: entry.SourceID, ThreadID: entry.ThreadID, SessionID: entry.SessionID, InstanceID: entry.InstanceID,
		WorkspaceRef: entry.WorkspaceRef, WorkingDir: entry.WorkingDir, StateDir: entry.StateDir,
		StartedAt: entry.StartedAt.UTC().Format(time.RFC3339Nano),
	}
}

func associationDigest(association appwire.BrokerAssociation) string {
	data, _ := json.Marshal(association)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func decodePrivate(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("multiple private payload values")
	}
	return nil
}

func decodeResponse(message appwire.Message, id appwire.ID, out any) error {
	if message.Error != nil {
		return ErrBrokerAuthentication
	}
	if message.Response == nil || message.Response.ID.String() != id.String() {
		return ErrBrokerAuthentication
	}
	data, err := json.Marshal(message.Response.Result)
	if err != nil {
		return err
	}
	if err := decodePrivate(data, out); err != nil {
		return fmt.Errorf("decode private response: %w", err)
	}
	return nil
}
