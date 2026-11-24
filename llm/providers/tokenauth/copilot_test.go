package tokenauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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

// copilotVisionRes is the resolved live-proven Haiku row: the account
// advertises image input for it, so an image may ride this route.
func copilotVisionRes() registry.Resolved {
	res := copilotRes()
	res.ModelID = "claude-haiku-5.5"
	res.Caps = registry.Caps{InputModalities: []string{"text", "image"}}
	return res
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
			if cfg.ProviderInstance != "github-copilot" {
				t.Fatalf("sign-in error instance = %q, want github-copilot", cfg.ProviderInstance)
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
		res    registry.Resolved
	}{
		{name: "image part", msgs: []llm.Message{image}, wanted: "true", res: copilotVisionRes()},
		{name: "tool result image", msgs: []llm.Message{toolImage}, wanted: "true", res: copilotVisionRes()},
		{name: "no image", msgs: []llm.Message{llm.User("hi")}, wanted: "", res: copilotRes()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpReq, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
			httpReq.Header.Set("Copilot-Vision-Request", "stale")
			if err := (&Copilot{}).PrepareRequest(context.Background(), httpReq, nil, llm.Request{Messages: tt.msgs}, tt.res); err != nil {
				t.Fatalf("PrepareRequest: %v", err)
			}
			if got := httpReq.Header.Get("Copilot-Vision-Request"); got != tt.wanted {
				t.Fatalf("Copilot-Vision-Request = %q, want %q", got, tt.wanted)
			}
		})
	}
}

// A Copilot row that advertises text only must refuse an attachment before the
// wire sees it: the account answers that request with an image-not-supported
// 400. The refusal is a configuration error naming the route so the caller can
// switch models and resend, and the caller's own message slice is left intact.
func TestCopilotPrepareRequestRefusesImageForTextOnlyModel(t *testing.T) {
	image := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "describe"},
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("png"), MediaType: "image/png"}},
	}}
	req := llm.Request{Model: "gpt-5.6-sol", Messages: []llm.Message{image}}
	httpReq, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
	res := copilotRes()
	res.ModelID = "gpt-5.6-sol"
	res.Caps = registry.Caps{InputModalities: []string{"text"}}

	err := (&Copilot{}).PrepareRequest(context.Background(), httpReq, nil, req, res)
	var configErr *llm.ConfigurationError
	if !errors.As(err, &configErr) {
		t.Fatalf("PrepareRequest error = %v, want a configuration error", err)
	}
	if configErr.ProviderInstance != res.Instance {
		t.Fatalf("ProviderInstance = %q, want %q", configErr.ProviderInstance, res.Instance)
	}
	if got := httpReq.Header.Get("Copilot-Vision-Request"); got != "" {
		t.Fatalf("Copilot-Vision-Request = %q, want it unset on a refused route", got)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 2 || req.Messages[0].Content[1].Image == nil {
		t.Fatal("the caller's attachment was mutated; it must survive the refusal for a retry")
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
	return newCopilotClientAt(t, srvURL, t.TempDir())
}

// newCopilotClientAt builds the client over an explicit registry state root,
// the root its Copilot record lives under.
func newCopilotClientAt(t *testing.T, srvURL, stateRoot string) *llm.Client {
	t.Helper()
	return newCopilotClientModelsAt(t, srvURL, stateRoot, registry.AuthOAuthGitHubCopilot, map[string]registry.Model{
		"m": {ID: "m"},
	})
}

// newCopilotClientModelsAt builds a client over one instance with the given
// auth scheme and model rows, so a route can carry an image only when its row
// advertises image input.
func newCopilotClientModelsAt(t *testing.T, srvURL, stateRoot, auth string, models map[string]registry.Model) *llm.Client {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(stateRoot),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"copilot": {
				Base:     "openai",
				Protocol: registry.ProtocolOpenAIChat,
				Surface:  registry.SurfaceGeneric,
				APIKey:   "unused-key",
				Transport: registry.Transport{
					Auth: auth, BaseURL: srvURL,
					Endpoint: "/chat/completions", StreamEndpoint: "/chat/completions",
					ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported,
				},
				Models: models,
			},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return llm.NewClient(llm.WithRegistry(r))
}

func imageRequest(model string) llm.Request {
	return llm.Request{Model: model, Provider: "copilot", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "describe"},
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("png"), MediaType: "image/png"}},
	}}}}
}

// The text-only refusal must happen before the request leaves the process: the
// account's image-not-supported 400 is the failure this prevents.
func TestCopilotClientRefusesTextOnlyImageBeforeSending(t *testing.T) {
	stateDir := t.TempDir()
	saveCopilotRecord(t, stateDir, "copilot", "token", "refresh")
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeChatCompletion(w)
	}))
	t.Cleanup(ts.Close)

	client := newCopilotClientAt(t, ts.URL, stateDir)
	ctx := llm.WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, ScopedCopilot(stateDir))
	_, err := client.Complete(ctx, imageRequest("m"))
	var configErr *llm.ConfigurationError
	if !errors.As(err, &configErr) {
		t.Fatalf("Complete error = %v, want a configuration error", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want 0: a text-only row must be refused before send", got)
	}
}

// A live-proven image row keeps its route: the image is serialized and the
// compatibility header still says the request carries vision input.
func TestCopilotClientSendsImageForImageCapableModel(t *testing.T) {
	stateDir := t.TempDir()
	saveCopilotRecord(t, stateDir, "copilot", "token", "refresh")
	var requests atomic.Int32
	var vision atomic.Bool
	var body atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		vision.Store(r.Header.Get("Copilot-Vision-Request") == "true")
		raw, _ := io.ReadAll(r.Body)
		body.Store(string(raw))
		writeChatCompletion(w)
	}))
	t.Cleanup(ts.Close)

	client := newCopilotClientModelsAt(t, ts.URL, stateDir, registry.AuthOAuthGitHubCopilot, map[string]registry.Model{
		"claude-haiku-5.5": {ID: "claude-haiku-5.5", Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
	})
	ctx := llm.WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, ScopedCopilot(stateDir))
	if _, err := client.Complete(ctx, imageRequest("claude-haiku-5.5")); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	if !vision.Load() {
		t.Fatal("Copilot-Vision-Request missing on an image-capable route")
	}
	if got := body.Load().(string); !strings.Contains(got, `"image_url"`) {
		t.Fatalf("request body does not carry the image: %s", got)
	}
}

// Non-Copilot routes are untouched: a bearer image request still goes out
// without the Copilot compatibility header.
func TestBearerImageRequestStillSends(t *testing.T) {
	var requests atomic.Int32
	var vision atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		vision.Store(r.Header.Get("Copilot-Vision-Request") != "")
		writeChatCompletion(w)
	}))
	t.Cleanup(ts.Close)

	client := newCopilotClientModelsAt(t, ts.URL, t.TempDir(), registry.AuthBearer, map[string]registry.Model{
		"vision-model": {ID: "vision-model", Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
	})
	if _, err := client.Complete(context.Background(), imageRequest("vision-model")); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	if vision.Load() {
		t.Fatal("a bearer route must not carry the Copilot vision header")
	}
}

// isolateDefaultCopilot pins the process-global Copilot authenticator to an
// empty root, so a client that leaked to the global default could not find the
// test machine's real record. Tests using it must not run parallel.
func isolateDefaultCopilot(t *testing.T) {
	t.Helper()
	previous := DefaultCopilot.StateDir
	DefaultCopilot.StateDir = t.TempDir()
	t.Cleanup(func() { DefaultCopilot.StateDir = previous })
}

func completeOnce(ctx context.Context, t *testing.T, client *llm.Client) error {
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
	if err := completeOnce(ctx, t, client); err != nil {
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
	err := completeOnce(ctx, t, client)
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
	err := completeOnce(ctx, t, client)
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

// TestCopilotClientAuthenticatesFromItsRegistryStateRoot proves a client built
// with llm.NewClient(WithRegistry(r)) authenticates against r's state root with
// no caller-supplied scoped authenticator: two clients naming one instance in
// separate roots each send their own record's bearer token.
func TestCopilotClientAuthenticatesFromItsRegistryStateRoot(t *testing.T) {
	isolateDefaultCopilot(t)
	roots := []string{t.TempDir(), t.TempDir()}
	saveCopilotRecord(t, roots[0], "copilot", "token-a", "refresh-a")
	saveCopilotRecord(t, roots[1], "copilot", "token-b", "refresh-b")

	var mu sync.Mutex
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		writeChatCompletion(w)
	}))
	t.Cleanup(ts.Close)

	for i, root := range roots {
		if err := completeOnce(context.Background(), t, newCopilotClientAt(t, ts.URL, root)); err != nil {
			t.Fatalf("client at root %d Complete: %v", i, err)
		}
	}
	if want := []string{"Bearer token-a", "Bearer token-b"}; !slices.Equal(seen, want) {
		t.Fatalf("Authorization headers = %v, want %v: each client must use its own root's record", seen, want)
	}
}

// TestCopilotClientListsModelsFromItsRegistryStateRoot proves model listing
// takes the same route: each client lists with the record under its own
// registry root and no caller-supplied scoped authenticator.
func TestCopilotClientListsModelsFromItsRegistryStateRoot(t *testing.T) {
	isolateDefaultCopilot(t)
	roots := []string{t.TempDir(), t.TempDir()}
	saveCopilotRecord(t, roots[0], "copilot", "token-a", "refresh-a")
	saveCopilotRecord(t, roots[1], "copilot", "token-b", "refresh-b")

	var mu sync.Mutex
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m","supported_endpoints":["/chat/completions"],"model_picker_enabled":true,"capabilities":{"family":"gpt","supports":{"tool_calls":true}}}]}`))
	}))
	t.Cleanup(ts.Close)

	for i, root := range roots {
		listing, err := newCopilotClientAt(t, ts.URL, root).Models(context.Background(), "copilot")
		if err != nil {
			t.Fatalf("client at root %d Models: %v", i, err)
		}
		if !listing.Live || !listing.Usable {
			t.Fatalf("client at root %d listing = live %v usable %v, want the account roster", i, listing.Live, listing.Usable)
		}
	}
	if want := []string{"Bearer token-a", "Bearer token-b"}; !slices.Equal(seen, want) {
		t.Fatalf("Authorization headers = %v, want %v: each client must list with its own root's record", seen, want)
	}
}

// TestCopilotClientKeepsExplicitScopedAuthenticator proves the registry binding
// never displaces a caller's scheme-scoped authenticator.
func TestCopilotClientKeepsExplicitScopedAuthenticator(t *testing.T) {
	isolateDefaultCopilot(t)
	registryRoot := t.TempDir()
	saveCopilotRecord(t, registryRoot, "copilot", "registry-token", "refresh-registry")
	overrideRoot := t.TempDir()
	saveCopilotRecord(t, overrideRoot, "copilot", "override-token", "refresh-override")

	var mu sync.Mutex
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		writeChatCompletion(w)
	}))
	t.Cleanup(ts.Close)

	client := newCopilotClientAt(t, ts.URL, registryRoot)
	ctx := llm.WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, ScopedCopilot(overrideRoot))
	if err := completeOnce(ctx, t, client); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if want := []string{"Bearer override-token"}; !slices.Equal(seen, want) {
		t.Fatalf("Authorization headers = %v, want %v: the explicit scope must win", seen, want)
	}
}
