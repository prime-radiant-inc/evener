package openai

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// FuzzOpenAIAdapterConfigProgram covers the adapter's deterministic config,
// identity, continuation-planning, header, and small collection helpers.
func FuzzOpenAIAdapterConfigProgram(f *testing.F) {
	f.Add("work", " key ", " https://api.example.test/// ", "org", "project", "conversation", true)
	f.Add("", "", "", "", "", "", false)

	f.Fuzz(func(t *testing.T, name, key, base, org, project, conversation string, store bool) {
		if len(name)+len(key)+len(base)+len(org)+len(project)+len(conversation) > 1<<16 {
			t.Skip()
		}
		// Environment values and filesystem paths cannot contain NUL bytes.
		clean := func(value string) string { return strings.ReplaceAll(value, "\x00", "") }
		name, key, base, org, project, conversation = clean(name), clean(key), clean(base), clean(org), clean(project), clean(conversation)
		stateHome := t.TempDir()
		hasher := llm.NewContinuationHasher([]byte("adapter-config-coverage"))

		params := OpenAIInstanceParams{
			Name: name, APIKey: key, BaseURL: base, OrgID: org, ProjectID: project,
			StateHome: stateHome, ContinuationHasher: hasher,
			Headers: map[string]string{"X-Custom": "custom", "Authorization": "wrong"},
		}
		a, err := NewForInstance(params)
		if strings.TrimSpace(key) == "" {
			if !errors.Is(err, errNoCredentials) || a != nil {
				t.Fatalf("empty key: adapter=%v err=%v", a, err)
			}
		} else {
			if err != nil || a == nil {
				t.Fatalf("API-key construction: adapter=%v err=%v", a, err)
			}
			if a.Name() == "" || a.Client == nil || a.ResponsesPath != defaultResponsesPath {
				t.Fatalf("incomplete adapter: %+v", a)
			}

			req, reqErr := http.NewRequest(http.MethodPost, "https://example.test", nil)
			if reqErr != nil {
				t.Fatal(reqErr)
			}
			a.setHeaders(req)
			if req.Header.Get("Authorization") != "Bearer "+strings.TrimSpace(key) {
				t.Fatalf("provider authorization did not win: %q", req.Header.Get("Authorization"))
			}

			plan, planErr := a.PlanResponsesContinuation(llm.Request{
				Model: "gpt-5.4", Messages: []llm.Message{llm.User("hello")},
				ConversationID: conversation, Store: &store,
			})
			if planErr != nil || plan.RequestFingerprint == "" || plan.StorageScopeFingerprint == "" {
				t.Fatalf("continuation plan=%+v err=%v", plan, planErr)
			}
		}

		withoutHasher, noHashErr := NewForInstance(OpenAIInstanceParams{Name: name, APIKey: "key"})
		if noHashErr != nil || withoutHasher == nil {
			t.Fatalf("optional hasher: adapter=%v err=%v", withoutHasher, noHashErr)
		}
		if _, err := withoutHasher.PlanResponsesContinuation(llm.Request{Model: "gpt-5.4"}); !errors.Is(err, llm.ErrContinuationSecretUnavailable) {
			t.Fatalf("missing hasher error=%v", err)
		}

		codex := &Adapter{APIKey: "oauth", ResponsesPath: defaultCodexResponses, ContinuationHasher: hasher}
		if _, err := codex.PlanResponsesContinuation(llm.Request{Model: "gpt-5.4", Store: &store, ConversationID: conversation}); err != nil {
			t.Fatalf("Codex continuation plan: %v", err)
		}
		codexReq, _ := http.NewRequest(http.MethodPost, "https://example.test", nil)
		codex.setHeaders(codexReq)
		if codexReq.Header.Get("originator") == "" || codexReq.Header.Get("User-Agent") == "" {
			t.Fatalf("missing Codex defaults: %v", codexReq.Header)
		}
		codex.DefaultHeaders = map[string]string{"originator": "caller", "User-Agent": "caller-agent"}
		codex.ChatGPTAccountID = "account"
		codexReq, _ = http.NewRequest(http.MethodPost, "https://example.test", nil)
		codex.setRequestHeaders(codexReq, llm.Request{Model: "gpt-5.6", SessionID: " session ", ThreadID: " thread "})
		codex.setRequestHeaders(codexReq, llm.Request{Model: "gpt-5.4"})
		publicReq, _ := http.NewRequest(http.MethodPost, "https://example.test", nil)
		aWithNoCodex := &Adapter{APIKey: "key", OrgID: org, ProjectID: project}
		aWithNoCodex.setRequestHeaders(publicReq, llm.Request{})

		badChoice := llm.ToolChoice{Mode: "invalid"}
		if _, err := codex.PlanResponsesContinuation(llm.Request{Model: "gpt-5.4", ToolChoice: &badChoice}); err == nil {
			t.Fatal("continuation plan accepted invalid tool choice")
		}
		if _, err := codex.PlanResponsesContinuation(llm.Request{Model: "gpt-5.4", ProviderOptions: map[string]any{
			"openai": map[string]any{"unmarshalable": func() {}},
		}}); err == nil {
			t.Fatal("continuation plan accepted unmarshalable fingerprint body")
		}

		_ = (&Adapter{}).Name()
		_, _ = authScopeForAPIKey(nil, key)
		_, _ = authScopeForAPIKey(hasher, key)
		_, _ = authScopeForOAuth(nil, org, project)
		_, _ = authScopeForOAuth(hasher, org, project)
		_, _ = hashOpenAIScopeIdentifier(nil, "org_id", org)
		_, _ = hashOpenAIScopeIdentifier(hasher, "org_id", "  ")
		_, _ = hashOpenAIScopeIdentifier(hasher, "org_id", org)

		_, _ = responsesStoragePolicyForPlan(llm.ResponsesEndpointFamilyOpenAICodex, nil)
		_, _ = responsesStoragePolicyForPlan(llm.ResponsesEndpointFamilyOpenAIPublic, map[string]any{"store": true})
		_, _ = responsesStoragePolicyForPlan(llm.ResponsesEndpointFamilyOpenAIPublic, map[string]any{"store": "true"})
		_ = normalizedResponsesBaseURL(" ", llm.ResponsesEndpointFamilyOpenAICodex)
		_ = normalizedResponsesBaseURL(" ", llm.ResponsesEndpointFamilyOpenAIPublic)
		_ = normalizedResponsesBaseURL(base, llm.ResponsesEndpointFamilyOpenAIPublic)
		_ = normalizedResponsesPath("")
		_ = normalizedResponsesPath(" /custom ")

		oldVersion := ClientVersion
		ClientVersion = " "
		_ = defaultUserAgent()
		ClientVersion = oldVersion
		_ = defaultUserAgent()
		if got := mergeStringMaps(nil, map[string]string{}, map[string]string{"a": "1"}, map[string]string{"a": "2"}); got["a"] != "2" {
			t.Fatalf("merge override=%v", got)
		}
		_ = mergeStringMaps()
		values := appendUniqueString(nil, "x")
		values = appendUniqueString(values, "x")
		_ = appendUniqueString(values, "y")

		t.Setenv(envvars.OpenAIAPIKey.Name, " env-key ")
		t.Setenv(envvars.OpenAIBaseURL.Name, base)
		t.Setenv(envvars.OpenAIOrgID.Name, org)
		t.Setenv(envvars.OpenAIProjectID.Name, project)
		t.Setenv(envvars.OpenAIChatGPTBaseURL.Name, "")
		if fromEnv, err := NewFromEnv(Config{}, Config{StateHome: " "}, Config{StateHome: stateHome}); err != nil || fromEnv == nil {
			t.Fatalf("NewFromEnv: adapter=%v err=%v", fromEnv, err)
		}
		if _, err := instanceParamsFromConfig(name, base, key, stateHome); err != nil {
			t.Fatalf("instanceParamsFromConfig: %v", err)
		}
		if adapter, configured, err := openAIEnvAdapterFactory(llm.EnvConfig{StateHome: stateHome}); err != nil || !configured || adapter == nil {
			t.Fatalf("env factory configured: adapter=%v configured=%v err=%v", adapter, configured, err)
		}
		instanceConfig := providercfg.InstanceConfig{Name: name, BaseURL: base, APIKey: "instance-key", Headers: map[string]string{"X-Instance": "yes"}}
		if adapter, err := openAIInstanceAdapterFactory(instanceConfig, stateHome); err != nil || adapter == nil {
			t.Fatalf("instance factory configured: adapter=%v err=%v", adapter, err)
		}
		if adapter, err := openAIInstanceAdapterFactory(providercfg.InstanceConfig{Name: name}, stateHome); !errors.Is(err, errNoCredentials) || adapter != nil {
			t.Fatalf("instance factory unconfigured: adapter=%v err=%v", adapter, err)
		}

		stateFile := filepath.Join(t.TempDir(), "state-file")
		if err := os.WriteFile(stateFile, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := continuationHasherForStateHome(stateFile); err == nil {
			t.Fatal("continuationHasherForStateHome accepted a file as state home")
		}
		if _, err := instanceParamsFromConfig(name, base, key, stateFile); err == nil {
			t.Fatal("instanceParamsFromConfig accepted a file as state home")
		}
		if _, err := NewFromEnv(Config{StateHome: stateFile}); err == nil {
			t.Fatal("NewFromEnv accepted a file as state home")
		}
		if adapter, configured, err := openAIEnvAdapterFactory(llm.EnvConfig{StateHome: stateFile}); err == nil || !configured || adapter != nil {
			t.Fatalf("env factory state error: adapter=%v configured=%v err=%v", adapter, configured, err)
		}
		if adapter, err := openAIInstanceAdapterFactory(instanceConfig, stateFile); err == nil || adapter != nil {
			t.Fatalf("instance factory state error: adapter=%v err=%v", adapter, err)
		}
		t.Setenv(envvars.OpenAIAPIKey.Name, "")
		if adapter, configured, err := openAIEnvAdapterFactory(llm.EnvConfig{StateHome: t.TempDir()}); err != nil || configured || adapter != nil {
			t.Fatalf("env factory unconfigured: adapter=%v configured=%v err=%v", adapter, configured, err)
		}
		t.Setenv(envvars.OpenAIAPIKey.Name, " env-key ")
		if got, err := NewForInstance(OpenAIInstanceParams{StateHome: stateFile, APIKey: "key"}); err == nil || got != nil {
			t.Fatalf("NewForInstance accepted state file: adapter=%v err=%v", got, err)
		}

		oauthHome := t.TempDir()
		oauthDir := authopenai.DefaultStateDirWithStateHome(oauthHome)
		record := authopenai.AuthRecord{
			Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
			ObtainedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), TokenType: "Bearer", Scope: "openid",
			AccessToken: "oauth-access", RefreshToken: "oauth-refresh", Expiry: time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC),
			AccountID: org, WorkspaceID: project,
		}
		if err := authopenai.SaveAuth(oauthDir, name, record); err != nil {
			t.Fatalf("SaveAuth: %v", err)
		}
		oauthAdapter, err := NewForInstance(OpenAIInstanceParams{
			Name: name, StateHome: oauthHome, ChatGPTBaseURL: base, ContinuationHasher: hasher,
		})
		if err != nil || oauthAdapter == nil || oauthAdapter.ResponsesPath != defaultCodexResponses {
			t.Fatalf("OAuth construction: adapter=%v err=%v", oauthAdapter, err)
		}

		originalServiceFactory := newRuntimeAuthService
		originalScopeHash := hashContinuationScopeValue
		originalStorageHash := hashContinuationStorageScope
		defer func() {
			newRuntimeAuthService = originalServiceFactory
			hashContinuationScopeValue = originalScopeHash
			hashContinuationStorageScope = originalStorageHash
		}()

		injected := errors.New("adapter config injected error")
		for _, service := range []*adapterConfigAuthService{
			{statusErr: injected},
			{statuses: []authopenai.AuthStatus{{SignedIn: true, Source: authopenai.AuthSourceOAuth}}, resolveErr: injected},
			{statuses: []authopenai.AuthStatus{{SignedIn: true, Source: authopenai.AuthSourceOAuth}}, credentials: authopenai.RuntimeCredentials{BearerToken: "token"}, statusErrAfter: injected},
		} {
			newRuntimeAuthService = func(*http.Client) runtimeAuthService { return service }
			if got, err := NewForInstance(OpenAIInstanceParams{StateHome: stateHome, ContinuationHasher: hasher}); err == nil || got != nil {
				t.Fatalf("injected auth failure: adapter=%v err=%v", got, err)
			}
		}
		newRuntimeAuthService = func(*http.Client) runtimeAuthService { return &adapterConfigAuthService{} }

		for _, failedKind := range []string{"credential", "org_id", "project_id"} {
			hashContinuationScopeValue = func(h *llm.ContinuationHasher, kind, value string) (string, error) {
				if kind == failedKind {
					return "", injected
				}
				return originalScopeHash(h, kind, value)
			}
			if got, err := NewForInstance(OpenAIInstanceParams{APIKey: "key", OrgID: "org", ProjectID: "project", ContinuationHasher: hasher}); err == nil || got != nil {
				t.Fatalf("injected %s hash failure: adapter=%v err=%v", failedKind, got, err)
			}
		}
		hashContinuationScopeValue = func(h *llm.ContinuationHasher, kind, value string) (string, error) {
			if kind == "account" || kind == "workspace" || kind == "credential" {
				return "", injected
			}
			return originalScopeHash(h, kind, value)
		}
		for _, ids := range [][2]string{{"account", "workspace"}, {"", "workspace"}, {"", ""}} {
			if _, err := authScopeForOAuth(hasher, ids[0], ids[1]); err == nil {
				t.Fatalf("authScopeForOAuth accepted injected failure for %q/%q", ids[0], ids[1])
			}
		}
		newRuntimeAuthService = func(*http.Client) runtimeAuthService {
			return &adapterConfigAuthService{
				statuses: []authopenai.AuthStatus{
					{SignedIn: true, Source: authopenai.AuthSourceOAuth, AccountID: "account"},
					{SignedIn: true, Source: authopenai.AuthSourceOAuth, AccountID: "account"},
				},
				credentials: authopenai.RuntimeCredentials{BearerToken: "token"},
			}
		}
		if got, err := NewForInstance(OpenAIInstanceParams{StateHome: stateHome, ContinuationHasher: hasher}); err == nil || got != nil {
			t.Fatalf("OAuth scope hash failure: adapter=%v err=%v", got, err)
		}

		hashContinuationScopeValue = func(h *llm.ContinuationHasher, kind, value string) (string, error) {
			if kind == "conversation_id" {
				return "", injected
			}
			return originalScopeHash(h, kind, value)
		}
		if _, err := codex.PlanResponsesContinuation(llm.Request{Model: "gpt-5.4", ConversationID: "conversation"}); err == nil {
			t.Fatal("continuation plan accepted injected conversation hash failure")
		}
		hashContinuationScopeValue = originalScopeHash
		hashContinuationStorageScope = func(*llm.ContinuationHasher, llm.ContinuationStorageScope) (string, error) {
			return "", injected
		}
		if _, err := codex.PlanResponsesContinuation(llm.Request{Model: "gpt-5.4"}); err == nil {
			t.Fatal("continuation plan accepted injected storage hash failure")
		}
	})
}

type adapterConfigAuthService struct {
	statuses       []authopenai.AuthStatus
	statusCalls    int
	statusErr      error
	statusErrAfter error
	credentials    authopenai.RuntimeCredentials
	resolveErr     error
}

func (s *adapterConfigAuthService) Status(string, string) (authopenai.AuthStatus, error) {
	if s.statusErr != nil {
		return authopenai.AuthStatus{}, s.statusErr
	}
	if s.statusCalls > 0 && s.statusErrAfter != nil {
		return authopenai.AuthStatus{}, s.statusErrAfter
	}
	if s.statusCalls >= len(s.statuses) {
		s.statusCalls++
		return authopenai.AuthStatus{}, nil
	}
	status := s.statuses[s.statusCalls]
	s.statusCalls++
	return status, nil
}

func (s *adapterConfigAuthService) ResolveRuntimeCredentials(context.Context, string, string) (authopenai.RuntimeCredentials, error) {
	return s.credentials, s.resolveErr
}
