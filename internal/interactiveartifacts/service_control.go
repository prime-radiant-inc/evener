package interactiveartifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

const (
	controlMetrics   = "artifact/metrics"
	controlBootstrap = "artifact/bootstrap"
	controlNamespace = "artifact/namespace"
	controlGrant     = "artifact/grant"
	controlRevoke    = "artifact/revoke"
	controlBackup    = "artifact/backup"
)

type NamespacePolicy struct {
	NamespaceID   string `json:"namespaceId"`
	RealmID       string `json:"realmId"`
	OwnerThreadID string `json:"ownerThreadId"`
	Tombstone     bool   `json:"tombstone"`
}

// Grant carries an opaque credential only on private trusted IPC. Never log it
// or include it in public status, tool results or model arguments.
type Grant struct {
	Token     string    `json:"token"`
	Readiness Readiness `json:"readiness"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type bootstrapParams struct {
	Root string `json:"root"`
}
type revokeParams struct {
	Hash [32]byte `json:"hash"`
}
type backupParams struct {
	Path string `json:"path"`
}
type controlResult struct {
	Value       json.RawMessage `json:"value,omitempty"`
	Error       *DomainError    `json:"error,omitempty"`
	Unavailable bool            `json:"unavailable,omitempty"`
}

type pipeStream struct {
	reader, writer *os.File
	once           sync.Once
	err            error
}

func (p *pipeStream) Read(b []byte) (int, error)  { return p.reader.Read(b) }
func (p *pipeStream) Write(b []byte) (int, error) { return p.writer.Write(b) }
func (p *pipeStream) Close() error {
	p.once.Do(func() { p.err = errors.Join(p.reader.Close(), p.writer.Close()) })
	return p.err
}

// RunInheritedService is the internal installed-binary entry. Descriptors 3 and
// 4 are private read/write pipes supplied by the owning Supervisor. No authority
// is accepted from argv, environment, HTTP, or a discovered process identifier.
func RunInheritedService() error { return runInheritedService(StoreOptions{}) }
func runInheritedService(options StoreOptions) error {
	reader := os.NewFile(3, "artifact-control-read")
	writer := os.NewFile(4, "artifact-control-write")
	if reader == nil || writer == nil {
		return errors.New("missing artifact control descriptors")
	}
	syscall.CloseOnExec(3)
	syscall.CloseOnExec(4)
	return serveControl(&pipeStream{reader: reader, writer: writer}, options)
}
func serveControl(stream io.ReadWriteCloser, options StoreOptions) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := appwire.NewStreamTransport(stream)
	defer func() { _ = transport.Close() }()
	// Bootstrap has a connection-wide deadline: a child whose parent never
	// configures it must not remain an orphaned process.
	bootCtx, bootCancel := context.WithTimeout(ctx, 15*time.Second)
	msg, err := transport.Recv(bootCtx)
	bootCancel()
	if err != nil {
		return err
	}
	if msg.Request == nil || msg.Request.Method != controlBootstrap {
		return errors.New("artifact bootstrap required")
	}
	var params bootstrapParams
	if err := json.Unmarshal(msg.Request.Params, &params); err != nil || params.Root == "" {
		return errors.New("invalid artifact bootstrap")
	}
	s, err := startService(params.Root, options)
	if err != nil {
		return err
	}
	defer func() { _ = s.close(context.Background()) }()
	if err := sendControl(ctx, transport, msg.Request.ID, s.ready, nil); err != nil {
		return err
	}
	router := s.controlRouter()
	requests := make(chan appwire.Request, 32)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer cancel()
		defer s.admission.close()
		for {
			msg, err := transport.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil {
				return
			}
			select {
			case requests <- *msg.Request:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); _ = transport.Close(); <-readDone }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case req := <-requests:
			value, err := router.Dispatch(ctx, req)
			if err := sendControl(ctx, transport, req.ID, value, err); err != nil {
				return err
			}
		}
	}
}
func sendControl(ctx context.Context, transport appwire.Transport, id appwire.ID, value any, err error) error {
	result := controlResult{}
	if err != nil {
		if domain, ok := errors.AsType[*DomainError](err); ok {
			result.Error = domain
		} else {
			result.Unavailable = true
		}
	} else {
		result.Value, err = json.Marshal(value)
		if err != nil {
			return err
		}
	}
	return transport.Send(ctx, appwire.Message{Response: &appwire.Response{ID: id, Result: result}})
}
func (s *service) controlRouter() *appserver.Router {
	r := appserver.NewRouter()
	appserver.HandleTyped(r, controlMetrics, func(context.Context, appwire.EmptyParams) (AdmissionStats, error) { return s.admission.stats(), nil })
	appserver.HandleTyped(r, controlNamespace, func(ctx context.Context, p NamespacePolicy) (appwire.EmptyResponse, error) {
		var err error
		if p.Tombstone {
			err = s.store.TombstoneNamespace(ctx, p.NamespaceID, p.RealmID, p.OwnerThreadID)
		} else {
			err = s.store.EnsureNamespace(ctx, p.NamespaceID, p.RealmID, p.OwnerThreadID)
		}
		return appwire.EmptyResponse{}, err
	})
	appserver.HandleTyped(r, controlGrant, func(ctx context.Context, scope Scope) (Grant, error) {
		now := s.store.clock()
		if scope.ExpiresAt.IsZero() {
			scope.ExpiresAt = now.Add(10 * time.Minute)
		}
		if scope.ExpiresAt.After(now.Add(10 * time.Minute)) {
			scope.ExpiresAt = now.Add(10 * time.Minute)
		}
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return Grant{}, err
		}
		token := base64.RawURLEncoding.EncodeToString(secret[:])
		if err := s.store.InstallGrant(ctx, sha256.Sum256([]byte(token)), scope); err != nil {
			return Grant{}, err
		}
		return Grant{Token: token, Readiness: s.ready, ExpiresAt: scope.ExpiresAt}, nil
	})
	appserver.HandleTyped(r, controlRevoke, func(ctx context.Context, p revokeParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, s.store.RevokeGrant(ctx, p.Hash)
	})
	appserver.HandleTyped(r, controlBackup, func(ctx context.Context, p backupParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, s.store.Backup(ctx, p.Path)
	})
	return r
}

// lifetimeTransport prevents one canceled AppWire request from poisoning shared
// framing. The client still cancels its waiter; a dispatched control mutation
// may have completed and must be treated as uncertain until reconciled.
type lifetimeTransport struct {
	appwire.Transport
	ctx context.Context
}

func (t *lifetimeTransport) Send(_ context.Context, m appwire.Message) error {
	return t.Transport.Send(t.ctx, m)
}
