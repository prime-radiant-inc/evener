package interactiveartifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Readiness describes the fixed bundled service, never a caller-selected route.
type Readiness struct {
	ServiceID          string `json:"serviceId"`
	ServiceRunID       string `json:"serviceRunId"`
	Endpoint           string `json:"endpoint"`
	SchemaVersion      int    `json:"schemaVersion"`
	ContractVersion    int    `json:"contractVersion"`
	CatalogVersion     int    `json:"catalogVersion"`
	CoreVersion        string `json:"coreVersion"`
	ApplicationVersion string `json:"applicationVersion"`
}

type service struct {
	store     *Store
	ready     Readiness
	server    *http.Server
	lock      *os.File
	admission *admission
}
type callerKey struct{}

func startService(root string, options StoreOptions) (_ *service, resultErr error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := requirePrivatePath(root, true); err != nil {
		return nil, err
	}
	lock, err := acquireServiceLock(filepath.Join(root, "owner.lock"))
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = lock.Close()
		}
	}()
	store, err := OpenStore(filepath.Join(root, "artifacts.sqlite"), options)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = store.Close()
		}
	}()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = listener.Close()
		}
	}()
	s := &service{store: store, lock: lock, admission: newAdmission()}
	s.ready = Readiness{ServiceID: store.ServiceID(), ServiceRunID: randomID(), Endpoint: "http://" + listener.Addr().String() + "/mcp", SchemaVersion: 1, ContractVersion: 1, CatalogVersion: 1, CoreVersion: "2025-11-25", ApplicationVersion: "2026-01-26"}
	sdk := mcp.NewServer(&mcp.Implementation{Name: "evener-artifacts", Version: "1"}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}, Resources: &mcp.ResourceCapabilities{}}})
	catalog, err := Tools()
	if err != nil {
		return nil, err
	}
	for _, tool := range catalog {
		sdk.AddTool(tool, s.call)
	}
	sdk.AddResource(&ViewerResource, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: ViewerResource.URI, MIMEType: ViewerResource.MIMEType, Text: "<!doctype html><title>Artifact viewer unavailable</title><p>Interim viewer: interactive rendering is unavailable.</p>"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	s.server = &http.Server{Handler: s.guard(listener.Addr().String(), handler), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { _ = s.server.Serve(listener) }()
	return s, nil
}
func (s *service) close(ctx context.Context) error {
	s.admission.close()
	drain, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := s.server.Shutdown(drain)
	if err != nil {
		_ = s.server.Close()
	}
	return errors.Join(err, s.store.Close(), s.lock.Close())
}
func (s *service) authenticate(ctx context.Context, hash [32]byte) (Scope, error) {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	scope, ok := s.store.grants[hash]
	if !ok || !s.store.clock().Before(scope.ExpiresAt) {
		return Scope{}, &DomainError{Code: NotFoundOrForbidden}
	}
	if err := s.store.checkNamespace(ctx, scope); err != nil {
		return Scope{}, err
	}
	return scope, nil
}
func (s *service) guard(host string, next http.Handler) http.Handler {
	// This front gate also bounds requests waiting to inspect Store authority.
	ingress := make(chan struct{}, 160)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host || hasHeader(r.Header, "Origin") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		auth := r.Header.Values("Authorization")
		if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") || len(auth[0]) <= 7 || strings.ContainsAny(auth[0][7:], " \t\r\n,") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		hash := sha256.Sum256([]byte(auth[0][7:]))
		r.Header.Del("Authorization")
		select {
		case ingress <- struct{}{}:
			defer func() { <-ingress }()
		default:
			writeBusy(w)
			return
		}
		scope, err := s.authenticate(r.Context(), hash)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		releaseIngress, err := s.admission.reserveIngress(principalKey{scope.RealmID, scope.PrincipalID})
		if err != nil {
			writeBusy(w)
			return
		}
		defer releaseIngress()
		// Read the bounded body before queuing. net/http cannot observe a
		// peer's disconnect behind an unread request body; queued cancellation
		// therefore requires completing this read first. The ingress gate
		// includes these readers in its fixed 160-request bound.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		release, err := s.admission.acquire(r.Context(), principalKey{scope.RealmID, scope.PrincipalID})
		if err != nil {
			writeBusy(w)
			return
		}
		defer release()
		// Grants may expire or be revoked while waiting. Metadata/resource
		// requests need the same current-authority check as domain operations.
		if _, err := s.authenticate(r.Context(), hash); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && !selectedProtocolMessage(r, body, s.ready.CoreVersion) {
			http.Error(w, "artifact service requires a single message in its selected MCP profile", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		// Buffer the complete SDK response before headers become observable. Failure
		// after a mutation is transport uncertainty, never an assertion of rollback.
		bounded := &responseBuffer{header: make(http.Header)}
		next.ServeHTTP(bounded, r.WithContext(context.WithValue(r.Context(), callerKey{}, hash)))
		if bounded.overflow {
			http.Error(w, "artifact response unavailable; reconcile mutations by receipt", http.StatusServiceUnavailable)
			return
		}
		maps.Copy(w.Header(), bounded.header)
		status := bounded.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(bounded.body.Bytes())
	})
}

// selectedProtocolMessage prevents the SDK's legacy batch path from dispatching
// multiple domain calls behind one HTTP admission lease or buffering a combined
// response beyond the service budget. The SDK still owns single-message codecs.
func selectedProtocolMessage(r *http.Request, body []byte, version string) bool {
	headers := r.Header.Values("MCP-Protocol-Version")
	if len(headers) > 1 || (len(headers) == 1 && headers[0] != version) {
		return false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] == '[' {
		return false
	}
	message, err := jsonrpc.DecodeMessage(body)
	if err != nil {
		return false
	}
	if request, ok := message.(*jsonrpc.Request); ok && request.Method == "initialize" {
		var params mcp.InitializeParams
		if !request.IsCall() || json.Unmarshal(request.Params, &params) != nil || params.ProtocolVersion != version {
			return false
		}
		// Initial requests legitimately have no negotiated-version header. Keep
		// even that single initialize out of the SDK's legacy transport profile.
		r.Header.Set("MCP-Protocol-Version", version)
		return true
	}
	return len(headers) == 1
}

func hasHeader(h http.Header, name string) bool {
	for key := range h {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
func writeBusy(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(RejectedResult{Status: StatusRejected, Error: DomainError{Code: Busy, Retryable: true}})
}

type responseBuffer struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (b *responseBuffer) Header() http.Header { return b.header }
func (b *responseBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *responseBuffer) Write(data []byte) (int, error) {
	if b.overflow || b.body.Len()+len(data) > MaxResponseBytes {
		b.overflow = true
		return 0, errors.New("artifact response exceeds limit")
	}
	return b.body.Write(data)
}

func (s *service) call(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	hash, ok := ctx.Value(callerKey{}).([32]byte)
	if !ok {
		return nil, errors.New("missing authority")
	}
	var value any
	var err error
	raw := r.Params.Arguments
	switch r.Params.Name {
	case "artifact_publish":
		value, err = s.store.Publish(ctx, hash, raw, PublicationOrigin{})
	case "artifact_read":
		value, err = s.store.Read(ctx, hash, raw)
	case "artifact_list":
		value, err = s.store.List(ctx, hash, raw)
	case "artifact_open":
		value, err = s.store.Open(ctx, hash, raw)
	case "artifact_get_view":
		value, err = s.store.GetView(ctx, hash, raw)
	case "artifact_save_state":
		value, err = s.store.SaveState(ctx, hash, raw)
	case "artifact_report_diagnostic":
		value, err = s.store.ReportDiagnostic(ctx, hash, raw)
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Artifact operation completed."}}, StructuredContent: value}
	if err != nil {
		var domain *DomainError
		if !errors.As(err, &domain) {
			return nil, errors.New("artifact request failed")
		}
		result.IsError = true
		result.StructuredContent = RejectedResult{Status: StatusRejected, Error: *domain}
		result.Content = []mcp.Content{&mcp.TextContent{Text: "Artifact operation rejected: " + string(domain.Code)}}
	}
	return result, nil
}

type bearerTransport struct {
	token     string
	transport *http.Transport
}

func (t *bearerTransport) CloseIdleConnections() { t.transport.CloseIdleConnections() }

func (t *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.transport.RoundTrip(clone)
}

// NewHTTPClient keeps managed credentials on direct loopback requests. The
// endpoint itself comes only from authenticated private service readiness.
// Lease owners close their MCP session and then call CloseIdleConnections to
// release their own sockets without stopping the shared service.
func NewHTTPClient(token string) *http.Client {
	return &http.Client{Transport: &bearerTransport{token: token, transport: &http.Transport{Proxy: nil, MaxIdleConns: 32, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
