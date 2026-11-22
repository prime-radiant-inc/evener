package tokenauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/auth/githubcopilot"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/registry"
)

// copilotRecord is a complete, currently-valid record for tests.
func copilotRecord(instance, access, refresh string, obtained time.Time) githubcopilot.AuthRecord {
	return githubcopilot.AuthRecord{
		Instance:      instance,
		Scheme:        githubcopilot.AuthScheme,
		Version:       1,
		AccessToken:   access,
		RefreshToken:  refresh,
		TokenType:     "bearer",
		Scope:         "read:user offline_access",
		ObtainedAt:    obtained,
		AccessExpiry:  obtained.Add(time.Hour),
		RefreshExpiry: obtained.Add(90 * 24 * time.Hour),
		CopilotToken:  access,
		CopilotExpiry: obtained.Add(time.Hour),
		AccountID:     "acct-1",
	}
}

func saveCopilotRecord(t *testing.T, stateDir, instance, access, refresh string) githubcopilot.AuthRecord {
	t.Helper()
	record := copilotRecord(instance, access, refresh, time.Now())
	if err := githubcopilot.SaveAuth(stateDir, instance, record); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	return record
}

// rewriteCopilotRecord runs inside an httptest handler goroutine, where
// t.Fatalf would exit only that goroutine; t.Errorf is safe there.
func rewriteCopilotRecord(t *testing.T, stateDir, instance, access, refresh string) {
	if err := githubcopilot.SaveAuth(stateDir, instance, copilotRecord(instance, access, refresh, time.Now())); err != nil {
		t.Errorf("rewrite record: %v", err)
	}
}

func copilotRes() registry.Resolved {
	return registry.Resolved{
		Instance:   "github-copilot",
		Credential: registry.Credential{Source: "oauth"},
		Transport:  registry.Transport{Auth: registry.AuthOAuthGitHubCopilot},
	}
}

func TestCopilotApplySetsValidatedHeaders(t *testing.T) {
	stateDir := t.TempDir()
	saveCopilotRecord(t, stateDir, "github-copilot", "copilot-token", "refresh-1")

	oldVersion := ClientVersion
	ClientVersion = "1.2.3"
	t.Cleanup(func() { ClientVersion = oldVersion })

	req, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
	if err := (&Copilot{StateDir: stateDir}).Apply(context.Background(), req, copilotRes()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer copilot-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != "evener/1.2.3" {
		t.Fatalf("User-Agent = %q, want evener/1.2.3", got)
	}
	if got := req.Header.Get("X-GitHub-Api-Version"); got != "2026-08-01" {
		t.Fatalf("X-GitHub-Api-Version = %q", got)
	}
	for name, want := range map[string]string{
		"Copilot-Integration-Id": githubcopilot.IntegrationID,
		"Editor-Version":         githubcopilot.EditorVersion,
		"Editor-Plugin-Version":  githubcopilot.EditorPluginVersion,
	} {
		if got := req.Header.Get(name); got != want {
			t.Fatalf("%s = %q, want the live-proven compatibility value %q", name, got, want)
		}
	}
}

func TestCopilotApplyReplacesPreexistingPiUserAgent(t *testing.T) {
	stateDir := t.TempDir()
	saveCopilotRecord(t, stateDir, "github-copilot", "tok", "refresh")

	oldVersion := ClientVersion
	ClientVersion = ""
	t.Cleanup(func() { ClientVersion = oldVersion })

	req, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
	req.Header.Set("User-Agent", githubcopilot.PiUserAgent)
	if err := (&Copilot{StateDir: stateDir}).Apply(context.Background(), req, copilotRes()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := req.Header.Get("User-Agent"); got != "evener/dev" {
		t.Fatalf("User-Agent = %q, want the Evener identity to replace a preexisting Pi agent", got)
	}
}

func TestCopilotApplySignInErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, stateDir string)
	}{
		{name: "missing record", setup: func(t *testing.T, stateDir string) {}},
		{name: "corrupt record", setup: func(t *testing.T, stateDir string) {
			path := githubcopilot.AuthFilePath(stateDir, "github-copilot")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateDir := t.TempDir()
			tt.setup(t, stateDir)
			req, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
			err := (&Copilot{StateDir: stateDir}).Apply(context.Background(), req, copilotRes())
			if !errors.Is(err, llm.ErrSignInRequired) {
				t.Fatalf("Apply error = %v, want ErrSignInRequired", err)
			}
			var cfg *llm.ConfigurationError
			if !errors.As(err, &cfg) {
				t.Fatalf("Apply error = %T, want *llm.ConfigurationError", err)
			}
			if cfg.ProviderInstance != "github-copilot" || !strings.Contains(cfg.Message, "evener github-copilot login --instance github-copilot") {
				t.Fatalf("sign-in error = %q instance %q", cfg.Message, cfg.ProviderInstance)
			}
			if got := req.Header.Get("Authorization"); got != "" {
				t.Fatalf("Authorization = %q, want none for a missing credential", got)
			}
		})
	}
}

func TestCopilotStateRootIsolation(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	saveCopilotRecord(t, rootA, "github-copilot", "token-a", "refresh-a")
	saveCopilotRecord(t, rootB, "github-copilot", "token-b", "refresh-b")

	apply := func(stateDir string) string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
		if err := ScopedCopilot(stateDir).Apply(context.Background(), req, copilotRes()); err != nil {
			t.Fatalf("Apply(%s): %v", stateDir, err)
		}
		return req.Header.Get("Authorization")
	}
	if got, want := apply(rootA), "Bearer token-a"; got != want {
		t.Fatalf("root A Authorization = %q, want %q", got, want)
	}
	if got, want := apply(rootB), "Bearer token-b"; got != want {
		t.Fatalf("root B Authorization = %q, want %q", got, want)
	}
}

func TestCopilotAndCodexRecordsAreNotInterchangeable(t *testing.T) {
	stateDir := t.TempDir()
	copilotPath := githubcopilot.AuthFilePath(stateDir, "github-copilot")
	if err := os.MkdirAll(filepath.Dir(copilotPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	codexRecord := `{"auth_scheme":"oauth-openai-codex","version":1,"instance":"github-copilot","access_token":"codex-token","refresh_token":"r","token_type":"bearer","scope":"openid","obtained_at":"2026-01-01T00:00:00Z","access_expiry":"2027-01-01T00:00:00Z","refresh_expiry":"2027-01-01T00:00:00Z","account_id":"acct"}`
	if err := os.WriteFile(copilotPath, []byte(codexRecord), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
	err := (&Copilot{StateDir: stateDir}).Apply(context.Background(), req, copilotRes())
	if !errors.Is(err, llm.ErrSignInRequired) {
		t.Fatalf("Copilot accepted a Codex record: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q from a Codex record", got)
	}

	codexRecordPath := authopenai.AuthFilePath(stateDir, "openai-codex")
	if err := os.MkdirAll(filepath.Dir(codexRecordPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copilotJSON := `{"auth_scheme":"oauth-github-copilot","version":1,"instance":"openai-codex","access_token":"copilot-token","refresh_token":"r","token_type":"bearer","scope":"read:user","obtained_at":"2026-01-01T00:00:00Z","access_expiry":"2027-01-01T00:00:00Z","refresh_expiry":"2027-01-01T00:00:00Z","account_id":"acct"}`
	if err := os.WriteFile(codexRecordPath, []byte(copilotJSON), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	codexReq, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	codex := &Codex{StateDir: stateDir}
	if err := codex.Apply(context.Background(), codexReq, registry.Resolved{Instance: "openai-codex", Credential: registry.Credential{Source: "oauth"}, Transport: registry.Transport{Auth: registry.AuthOAuthOpenAICodex}}); err == nil {
		t.Fatal("Codex accepted a Copilot record")
	}
	if got := codexReq.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q from a Copilot record", got)
	}
}

// TestScopedOAuthAuthenticatorsUseSeparateStateRoots proves one request context
// can carry a Codex scope and a Copilot scope from different state roots, and
// that each scheme resolves only its own record.
func TestScopedOAuthAuthenticatorsUseSeparateStateRoots(t *testing.T) {
	codexDir := codexState(t, "openai-codex", "acct_codex")
	copilotDir := t.TempDir()
	saveCopilotRecord(t, copilotDir, "github-copilot", "copilot-token", "refresh")

	ctx := llm.WithScopedAuthenticator(context.Background(), registry.AuthOAuthOpenAICodex, ScopedCodex(codexDir))
	ctx = llm.WithScopedAuthenticator(ctx, registry.AuthOAuthGitHubCopilot, ScopedCopilot(copilotDir))

	codexAuth := llm.AuthenticatorForContext(ctx, registry.AuthOAuthOpenAICodex)
	if codexAuth == nil {
		t.Fatal("Codex scheme resolved no scoped authenticator")
	}
	codexReq, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if err := codexAuth.Apply(ctx, codexReq, codexRes("openai-codex")); err != nil {
		t.Fatalf("Codex Apply: %v", err)
	}
	if got := codexReq.Header.Get("Authorization"); got != "Bearer stale" {
		t.Fatalf("Codex Authorization = %q, want its own state root's record", got)
	}
	if got := codexReq.Header.Get("ChatGPT-Account-ID"); got != "acct_codex" {
		t.Fatalf("Codex account header = %q", got)
	}

	copilotAuth := llm.AuthenticatorForContext(ctx, registry.AuthOAuthGitHubCopilot)
	if copilotAuth == nil {
		t.Fatal("Copilot scheme resolved no scoped authenticator")
	}
	copilotReq, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
	if err := copilotAuth.Apply(ctx, copilotReq, copilotRes()); err != nil {
		t.Fatalf("Copilot Apply: %v", err)
	}
	if got := copilotReq.Header.Get("Authorization"); got != "Bearer copilot-token" {
		t.Fatalf("Copilot Authorization = %q, want its own state root's record", got)
	}
}

func TestCopilotPrepareRequestInteractionHeaders(t *testing.T) {
	tests := []struct {
		name      string
		req       llm.Request
		wantID    string
		wantIniti string
	}{
		{name: "session id wins", req: llm.Request{SessionID: "sess-1", ThreadID: "thread-1", Messages: []llm.Message{llm.User("hi")}}, wantID: "sess-1", wantIniti: "user"},
		{name: "thread id fallback", req: llm.Request{ThreadID: "thread-1", Messages: []llm.Message{llm.User("hi")}}, wantID: "thread-1", wantIniti: "user"},
		{name: "no identity", req: llm.Request{Messages: []llm.Message{llm.User("hi")}}, wantIniti: "user"},
		{name: "assistant tail is agent", req: llm.Request{Messages: []llm.Message{llm.User("hi"), llm.Assistant("ok")}}, wantIniti: "agent"},
		{name: "tool result is agent", req: llm.Request{Messages: []llm.Message{llm.User("hi"), llm.ToolResultNamed("call-1", "tool", "done", false)}}, wantIniti: "agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpReq, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
			if err := (&Copilot{}).PrepareRequest(context.Background(), httpReq, nil, tt.req, copilotRes()); err != nil {
				t.Fatalf("PrepareRequest: %v", err)
			}
			if got := httpReq.Header.Get("X-Interaction-Id"); got != tt.wantID {
				t.Fatalf("X-Interaction-Id = %q, want %q", got, tt.wantID)
			}
			if got := httpReq.Header.Get("X-Initiator"); got != tt.wantIniti {
				t.Fatalf("X-Initiator = %q, want %q", got, tt.wantIniti)
			}
			if got := httpReq.Header.Get("Openai-Intent"); got != "conversation-edits" {
				t.Fatalf("Openai-Intent = %q", got)
			}
			if got := httpReq.Header.Get("X-Interaction-Type"); got != "conversation-agent" {
				t.Fatalf("X-Interaction-Type = %q", got)
			}
		})
	}
}

func TestCopilotPrepareRequestVisionHeader(t *testing.T) {
	image := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "describe"},
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("png"), MediaType: "image/png"}},
	}}
	toolImage := llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c", Content: "shot", ImageData: []byte("png"), ImageMediaType: "image/png"}},
	}}
	tests := []struct {
		name   string
		msgs   []llm.Message
		wanted string
	}{
		{name: "image part", msgs: []llm.Message{image}, wanted: "true"},
		{name: "tool result image", msgs: []llm.Message{toolImage}, wanted: "true"},
		{name: "no image", msgs: []llm.Message{llm.User("hi")}, wanted: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpReq, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
			httpReq.Header.Set("Copilot-Vision-Request", "stale")
			if err := (&Copilot{}).PrepareRequest(context.Background(), httpReq, nil, llm.Request{Messages: tt.msgs}, copilotRes()); err != nil {
				t.Fatalf("PrepareRequest: %v", err)
			}
			if got := httpReq.Header.Get("Copilot-Vision-Request"); got != tt.wanted {
				t.Fatalf("Copilot-Vision-Request = %q, want %q", got, tt.wanted)
			}
		})
	}
}

func TestCopilotAuthScopeAndGeneration(t *testing.T) {
	stateDir := t.TempDir()
	record := saveCopilotRecord(t, stateDir, "github-copilot", "tok", "refresh-1")
	copilot := &Copilot{StateDir: stateDir}

	account, workspace, err := copilot.AuthScope(context.Background(), copilotRes())
	if err != nil {
		t.Fatalf("AuthScope: %v", err)
	}
	if account != "acct-1" || workspace != "" {
		t.Fatalf("AuthScope = %q %q", account, workspace)
	}

	before, err := copilot.AuthGeneration(context.Background(), copilotRes())
	if err != nil {
		t.Fatalf("AuthGeneration: %v", err)
	}
	if before == "" {
		t.Fatal("AuthGeneration is empty")
	}
	if strings.Contains(before, record.RefreshToken) || strings.Contains(before, record.AccessToken) {
		t.Fatalf("AuthGeneration leaks a token: %q", before)
	}
	again, err := copilot.AuthGeneration(context.Background(), copilotRes())
	if err != nil || again != before {
		t.Fatalf("stable generation = %q %v, want %q", again, err, before)
	}
	saveCopilotRecord(t, stateDir, "github-copilot", "tok-2", "refresh-2")
	after, err := copilot.AuthGeneration(context.Background(), copilotRes())
	if err != nil {
		t.Fatalf("AuthGeneration after save: %v", err)
	}
	if after == before {
		t.Fatal("generation did not change after SaveAuth")
	}
}

func TestCopilotSchemeIsRegistered(t *testing.T) {
	auth, ok := llm.AuthenticatorFor(registry.AuthOAuthGitHubCopilot)
	if !ok {
		t.Fatal("oauth-github-copilot is not registered")
	}
	if auth != llm.Authenticator(DefaultCopilot) {
		t.Fatalf("registered auth = %T, want DefaultCopilot", auth)
	}
}

// --- client 401 rotation retry -------------------------------------------

type copilotServer struct {
	t        *testing.T
	stateDir string

	mu       sync.Mutex
	requests []string // Authorization headers
	respond  func(w http.ResponseWriter, n int)
}

func (s *copilotServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Header.Get("Authorization"))
	n := len(s.requests)
	s.mu.Unlock()
	s.respond(w, n)
}

func (s *copilotServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func newCopilotClient(t *testing.T, srvURL string) *llm.Client {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"copilot": {
				Base:     "openai",
				Protocol: registry.ProtocolOpenAIChat,
				Surface:  registry.SurfaceGeneric,
				APIKey:   "unused-key",
				Transport: registry.Transport{
					Auth: registry.AuthOAuthGitHubCopilot, BaseURL: srvURL,
					Endpoint: "/chat/completions", StreamEndpoint: "/chat/completions",
					ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported,
				},
				Models: map[string]registry.Model{"m": {ID: "m"}},
			},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return llm.NewClient(llm.WithRegistry(r))
}

func completeOnce(t *testing.T, client *llm.Client, ctx context.Context) error {
	t.Helper()
	_, err := client.Complete(ctx, llm.Request{Model: "m", Provider: "copilot", Messages: []llm.Message{llm.User("hi")}})
	return err
}

func writeChatCompletion(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":{"message":"bad credentials","type":"authentication_error"}}`))
}

func TestCopilotClientRetries401OnceWhenRecordGenerationChanged(t *testing.T) {
	stateDir := t.TempDir()
	saveCopilotRecord(t, stateDir, "copilot", "old-token", "refresh-a")
	srv := &copilotServer{t: t, stateDir: stateDir}
	srv.respond = func(w http.ResponseWriter, n int) {
		if n == 1 {
			rewriteCopilotRecord(t, stateDir, "copilot", "new-token", "refresh-b")
			writeUnauthorized(w)
			return
		}
		writeChatCompletion(w)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	client := newCopilotClient(t, ts.URL)
	ctx := llm.WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, ScopedCopilot(stateDir))
	if err := completeOnce(t, client, ctx); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	seen := srv.seen()
	if len(seen) != 2 {
		t.Fatalf("requests = %d (%v), want 2", len(seen), seen)
	}
	if seen[1] != "Bearer new-token" {
		t.Fatalf("second Authorization = %q, want the rotated token", seen[1])
	}
}

func TestCopilotClientDoesNotRetry401WhenRecordUnchanged(t *testing.T) {
	stateDir := t.TempDir()
	saveCopilotRecord(t, stateDir, "copilot", "old-token", "refresh-a")
	srv := &copilotServer{t: t, stateDir: stateDir}
	srv.respond = func(w http.ResponseWriter, _ int) { writeUnauthorized(w) }
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	client := newCopilotClient(t, ts.URL)
	ctx := llm.WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, ScopedCopilot(stateDir))
	err := completeOnce(t, client, ctx)
	if llm.Kind(err) != llm.KindAuthentication {
		t.Fatalf("Complete error = %v (%v), want authentication", err, llm.Kind(err))
	}
	if got := len(srv.seen()); got != 1 {
		t.Fatalf("requests = %d, want 1: a 401 without a record change must not retry", got)
	}
}

func TestCopilotClientDoesNotRetry401OnOtherSchemes(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeUnauthorized(w)
	}))
	t.Cleanup(ts.Close)

	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"bearer": {
				Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
				APIKey: "k",
				Transport: registry.Transport{Auth: registry.AuthBearer, BaseURL: ts.URL,
					Endpoint: "/chat/completions", ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported},
				Models: map[string]registry.Model{"m": {ID: "m"}},
			},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	_, err = client.Complete(context.Background(), llm.Request{Model: "m", Provider: "bearer", Messages: []llm.Message{llm.User("hi")}})
	if llm.Kind(err) != llm.KindAuthentication {
		t.Fatalf("Complete error = %v, want authentication", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1: 401 is not globally retried", got)
	}
}

func TestCopilotClientRetries401OnlyOnce(t *testing.T) {
	stateDir := t.TempDir()
	saveCopilotRecord(t, stateDir, "copilot", "token-0", "refresh-0")
	srv := &copilotServer{t: t, stateDir: stateDir}
	srv.respond = func(w http.ResponseWriter, n int) {
		rewriteCopilotRecord(t, stateDir, "copilot", "token-"+string(rune('0'+n)), "refresh-"+string(rune('0'+n)))
		writeUnauthorized(w)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	client := newCopilotClient(t, ts.URL)
	ctx := llm.WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, ScopedCopilot(stateDir))
	err := completeOnce(t, client, ctx)
	if llm.Kind(err) != llm.KindAuthentication {
		t.Fatalf("Complete error = %v, want authentication", err)
	}
	if got := len(srv.seen()); got != 2 {
		t.Fatalf("requests = %d, want exactly 2: one rotation retry, never a second", got)
	}
}

func TestCopilotClientDoesNotReplayAnExposedStream(t *testing.T) {
	stateDir := t.TempDir()
	saveCopilotRecord(t, stateDir, "copilot", "old-token", "refresh-a")
	srv := &copilotServer{t: t, stateDir: stateDir}
	srv.respond = func(w http.ResponseWriter, n int) {
		rewriteCopilotRecord(t, stateDir, "copilot", "new-token", "refresh-b")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"error\":{\"message\":\"bad credentials\",\"type\":\"authentication_error\",\"code\":401}}\n\n"))
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	client := newCopilotClient(t, ts.URL)
	ctx := llm.WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, ScopedCopilot(stateDir))
	st, err := client.Stream(ctx, llm.Request{Model: "m", Provider: "copilot", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream open: %v", err)
	}
	defer st.Close()
	var sawDelta, sawAuthError bool
	for ev := range st.Events() {
		switch {
		case ev.Type == llm.StreamEventTextDelta && ev.Delta == "hi":
			sawDelta = true
		case ev.Type == llm.StreamEventError && llm.Kind(ev.Err) == llm.KindAuthentication:
			sawAuthError = true
		}
	}
	if !sawDelta || !sawAuthError {
		t.Fatalf("stream events: delta=%v authError=%v, want both", sawDelta, sawAuthError)
	}
	if got := len(srv.seen()); got != 1 {
		t.Fatalf("requests = %d, want 1: an exposed stream is never replayed", got)
	}
}
