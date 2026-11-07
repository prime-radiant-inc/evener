package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const voiceGrantLifetime = 20 * time.Minute

type voicePolicy struct {
	TrailingSilenceMS int `json:"trailingSilenceMs"`
	MaxUtteranceMS    int `json:"maxUtteranceMs"`
	FollowupMS        int `json:"followupMs"`
	IdleInputMS       int `json:"idleInputMs"`
}

func currentVoicePolicy() voicePolicy {
	return voicePolicy{TrailingSilenceMS: 600, MaxUtteranceMS: 30000, FollowupMS: 12000, IdleInputMS: 30000}
}

type voiceArmRequest struct {
	Ref                string `json:"ref"`
	ExpectedInstanceID string `json:"expectedInstanceId"`
	DeviceID           string `json:"deviceId"`
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	Voice              string `json:"voice"`
	Takeover           bool   `json:"takeover"`
}

type voiceArmResponse struct {
	BindingID  string      `json:"bindingId"`
	Epoch      string      `json:"epoch"`
	Grant      string      `json:"grant"`
	ExpiresAt  time.Time   `json:"expiresAt"`
	SocketPath string      `json:"socketPath"`
	SampleRate int         `json:"sampleRate"`
	Policy     voicePolicy `json:"policy"`
	Provider   string      `json:"provider"`
}

type voiceService struct {
	web     *WebServer
	mu      sync.Mutex
	journal *hubcore.VoiceJournal
	closed  bool
	sockets map[string]*voiceBridge
}

func (v *voiceService) store() (*hubcore.VoiceJournal, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, context.Canceled
	}
	if v.journal == nil {
		journal, err := hubcore.NewVoiceJournal(v.web.cfg.HubStateRoot)
		if err != nil {
			return nil, err
		}
		v.journal = journal
	}
	return v.journal, nil
}

// Authoritative session deletion also removes cold journals left by an earlier
// hub process. A host that has never used voice needs no journal to delete.
func (v *voiceService) deleteSession(ctx context.Context, ref string) error {
	root := v.web.cfg.HubStateRoot
	if root == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, "voice", "journal.db")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	journal, err := v.store()
	if err != nil {
		return err
	}
	if err := journal.DeleteSession(ctx, ref); err != nil {
		return err
	}
	v.refreshSocket(ctx, ref)
	return nil
}

func (v *voiceService) close() {
	v.mu.Lock()
	v.closed = true
	journal := v.journal
	var stopped []<-chan struct{}
	for _, bridge := range v.sockets {
		bridge.cancel()
		stopped = append(stopped, bridge.done)
	}
	v.mu.Unlock()
	for _, done := range stopped {
		<-done
	}
	if journal != nil {
		_ = journal.Close()
	}
}

func (v *voiceService) handler() http.Handler {
	foreground := hubedge.AuthGuard(v.web.cfg.AuthToken)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			voiceHTTPError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		switch r.URL.Path {
		case "/api/voice/arm":
			v.arm(w, r)
		case "/api/voice/disarm":
			v.disarm(w, r)
		default:
			voiceHTTPError(w, http.StatusNotFound, "not_found")
		}
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !voiceTransportAllowed(r) {
			voiceHTTPError(w, http.StatusForbidden, "secure_transport_required")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				voiceHTTPError(w, http.StatusForbidden, "origin_not_allowed")
				return
			}
		}
		if r.URL.Path == "/api/voice/socket" {
			v.socket(w, r)
			return
		}
		foreground.ServeHTTP(w, r)
	})
}

func (v *voiceService) arm(w http.ResponseWriter, r *http.Request) {
	var request voiceArmRequest
	if !decodeVoiceHTTP(w, r, &request) {
		return
	}
	ref, err := appwire.ParseRef(request.Ref)
	if err != nil || request.Ref != ref.String() || request.ExpectedInstanceID == "" || !validDeviceLabel(request.DeviceID) || len(request.Model) > 128 || len(request.Voice) > 64 || len(request.Provider) > 128 {
		voiceHTTPError(w, http.StatusBadRequest, "invalid_binding")
		return
	}
	client, err := v.web.voiceClient(r.Context())
	if err != nil {
		voiceHTTPError(w, http.StatusServiceUnavailable, "session_unavailable")
		return
	}
	defer func() { _ = client.Close() }()
	read, err := client.ThreadRead(r.Context(), appwire.ThreadReadParams{Ref: request.Ref})
	if err != nil {
		voiceHTTPError(w, http.StatusServiceUnavailable, "session_unavailable")
		return
	}
	if read.Thread.Evener.InstanceID != request.ExpectedInstanceID {
		voiceHTTPError(w, http.StatusConflict, "instance_changed")
		return
	}
	if !read.Thread.Evener.MutationStateAuthoritative || !read.Thread.Evener.Manager {
		voiceHTTPError(w, http.StatusConflict, "live_manager_required")
		return
	}
	if !read.Thread.Evener.Capabilities.VoicePresentationContextV1 {
		voiceHTTPError(w, http.StatusConflict, "voice_presentation_unsupported")
		return
	}
	if !validVoiceProvenance(read.Thread.Evener.ConversationProvenance, request.ExpectedInstanceID) {
		voiceHTTPError(w, http.StatusConflict, "voice_provenance_unavailable")
		return
	}
	if request.Provider == "" {
		name, err := v.web.defaultOpenAIVoiceProvider()
		if err != nil {
			voiceHTTPError(w, http.StatusServiceUnavailable, "voice_provider_unavailable")
			return
		}
		request.Provider = name
	}
	if request.Model == "" {
		request.Model = "gpt-realtime-2.1"
	}
	if request.Voice == "" {
		request.Voice = "marin"
	}
	if _, _, err := v.web.voiceProvider(r.Context(), request.Provider); err != nil {
		voiceHTTPError(w, http.StatusServiceUnavailable, "voice_provider_unavailable")
		return
	}
	journal, err := v.store()
	if err != nil {
		voiceHTTPError(w, http.StatusServiceUnavailable, "voice_storage_unavailable")
		return
	}
	grant, bindingID, epoch := voiceOpaqueID(), voiceOpaqueID(), voiceOpaqueID()
	digest := sha256.Sum256([]byte(grant))
	expires := time.Now().UTC().Add(voiceGrantLifetime)
	binding := hubcore.VoiceBinding{
		ID: bindingID, DeviceID: request.DeviceID, Ref: request.Ref,
		InstanceID: request.ExpectedInstanceID, Epoch: epoch,
		GrantHash: hex.EncodeToString(digest[:]), ExpiresAt: expires, Active: true,
		Provider: request.Provider, Model: request.Model, Voice: request.Voice,
	}
	if err := journal.CreateBinding(r.Context(), binding, request.Takeover); err != nil {
		if errors.Is(err, hubcore.ErrVoiceBindingBusy) {
			voiceHTTPError(w, http.StatusConflict, "binding_owned")
		} else {
			voiceHTTPError(w, http.StatusServiceUnavailable, "voice_storage_unavailable")
		}
		return
	}
	v.refreshSocket(r.Context(), binding.Ref)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(voiceArmResponse{BindingID: bindingID, Epoch: epoch, Grant: grant, ExpiresAt: expires, SocketPath: "/api/voice/socket", SampleRate: 24000, Policy: currentVoicePolicy(), Provider: request.Provider})
}

func (v *voiceService) refreshSocket(ctx context.Context, ref string) {
	v.mu.Lock()
	bridge := v.sockets[ref]
	v.mu.Unlock()
	if bridge == nil {
		return
	}
	binding, err := bridge.journal.Binding(ctx, bridge.binding.ID)
	if err != nil || !binding.Active || !binding.ExpiresAt.After(time.Now()) {
		bridge.cancel()
	}
}

func (v *voiceService) disarm(w http.ResponseWriter, r *http.Request) {
	var request struct {
		BindingID string `json:"bindingId"`
		Epoch     string `json:"epoch"`
	}
	if !decodeVoiceHTTP(w, r, &request) {
		return
	}
	journal, err := v.store()
	if err != nil {
		voiceHTTPError(w, http.StatusServiceUnavailable, "voice_storage_unavailable")
		return
	}
	binding, err := journal.Binding(r.Context(), request.BindingID)
	if err != nil || binding.Epoch != request.Epoch {
		voiceHTTPError(w, http.StatusConflict, "binding_changed")
		return
	}
	if err := journal.RetireBinding(r.Context(), request.BindingID, request.Epoch); err != nil {
		voiceHTTPError(w, http.StatusServiceUnavailable, "voice_storage_unavailable")
		return
	}
	v.refreshSocket(r.Context(), binding.Ref)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct{}{})
}

func (s *WebServer) voiceClient(ctx context.Context) (*appwire.Client, error) {
	ctx = context.WithValue(ctx, voicePresentationContextKey{}, true)
	transport, err := s.appRPC.NewInProcessTransport(ctx)
	if err != nil {
		return nil, err
	}
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (s *WebServer) voiceProvider(ctx context.Context, name string) (string, http.Header, error) {
	if s.auth == nil || s.auth.registry() == nil {
		return "", nil, errors.New("voice provider registry unavailable")
	}
	resolved, err := s.auth.registry().ResolveInstance(name)
	if err != nil {
		return "", nil, err
	}
	if !isOpenAIProtocol(resolved.Protocol) {
		return "", nil, errors.New("voice provider does not use an OpenAI transport")
	}
	u, err := url.Parse(resolved.Transport.BaseURL)
	if err != nil || u == nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", nil, errors.New("voice provider endpoint unavailable")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/realtime"
	u.RawPath, u.Fragment = "", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, err
	}
	for key, value := range resolved.Headers {
		req.Header.Set(key, value)
	}
	for key, value := range resolved.CredentialHeaders {
		req.Header.Set(key, value)
	}
	auth, ok := llm.AuthenticatorFor(resolved.Transport.Auth)
	if !ok {
		return "", nil, errors.New("voice provider authentication unavailable")
	}
	if err := auth.Apply(ctx, req, resolved); err != nil {
		return "", nil, err
	}
	return u.String(), req.Header, nil
}

// defaultOpenAIVoiceProvider names the instance voice uses when Arm names none:
// the configured default when it is an OpenAI instance, else the first OpenAI
// instance. A hub whose credentials live under another instance name still arms
// without the caller guessing that name.
func (s *WebServer) defaultOpenAIVoiceProvider() (string, error) {
	if s.auth == nil || s.auth.registry() == nil {
		return "", errors.New("voice provider registry unavailable")
	}
	r := s.auth.registry()
	defaultName, _, _ := r.DefaultInstance()
	return pickOpenAIVoiceProvider(defaultName, r.Instances())
}

func isOpenAIProtocol(protocol string) bool {
	return protocol == registry.ProtocolOpenAIChat || protocol == registry.ProtocolOpenAIResponses
}

// isOpenAIVoiceInstance reports whether inst is OpenAI itself. Other
// OpenAI-protocol instances (a Codex login, Groq, Lunaroute, OpenRouter) speak
// the chat protocol against an OpenAI-compatible base but not OpenAI's Realtime
// endpoint, even when their base is named "openai".
func isOpenAIVoiceInstance(inst registry.Instance) bool {
	if inst.ProviderID == "openai" {
		return true
	}
	u, err := url.Parse(inst.BaseURL)
	return err == nil && strings.EqualFold(u.Hostname(), "api.openai.com")
}

// pickOpenAIVoiceProvider chooses voice's instance from the hub's default and
// configured instances.
func pickOpenAIVoiceProvider(defaultName string, instances []registry.Instance) (string, error) {
	for _, inst := range instances {
		if inst.Name == defaultName && isOpenAIVoiceInstance(inst) {
			return inst.Name, nil
		}
	}
	for _, inst := range instances {
		if isOpenAIVoiceInstance(inst) {
			return inst.Name, nil
		}
	}
	return "", errors.New("voice provider unavailable: no OpenAI-transport instance")
}

func voiceOpaqueID() string {
	var value [32]byte
	_, _ = rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func decodeVoiceHTTP(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		voiceHTTPError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		voiceHTTPError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func voiceHTTPError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code string `json:"code"`
	}{Code: code})
}

func voiceTransportAllowed(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return false
	}
	endpoint, err := netip.ParseAddrPort(local.String())
	if err != nil {
		return false
	}
	private := func(ip netip.Addr) bool {
		ip = ip.Unmap()
		return ip.IsLoopback() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(ip)
	}
	return private(remote.Addr()) && private(endpoint.Addr())
}
