# Live Model Listing Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `ListModels()` to provider adapters so `/model` in the TUI shows an interactive picker of available models.

**Architecture:** Optional `ModelLister` interface on adapters (matching existing `Closer`/`Initializer` pattern), `GET /models` server endpoint, and Bubble Tea model picker in the TUI.

**Tech Stack:** Go, net/http, httptest, Bubble Tea (charmbracelet/bubbletea + bubbles)

---

### Task 1: Add `ModelLister` interface and `Client.ListModels`

**Files:**
- Modify: `llm/client.go` (add interface + client method)
- Create: `llm/client_listmodels_test.go`

**Step 1: Write the failing test**

Create `llm/client_listmodels_test.go`:

```go
package llm

import (
	"context"
	"testing"
)

type stubLister struct {
	stubAdapter
	models []ModelInfo
	err    error
}

func (s *stubLister) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return s.models, s.err
}

type stubAdapter struct{}

func (s *stubAdapter) Name() string                                          { return "stub" }
func (s *stubAdapter) Complete(ctx context.Context, req Request) (Response, error) { return Response{}, nil }
func (s *stubAdapter) Stream(ctx context.Context, req Request) (Stream, error)     { return nil, nil }

func TestClient_ListModels_Delegates(t *testing.T) {
	c := NewClient()
	c.Register(&stubLister{
		models: []ModelInfo{{ID: "m1", Provider: "stub"}, {ID: "m2", Provider: "stub"}},
	})

	models, err := c.ListModels(context.Background(), "stub")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if models[0].ID != "m1" {
		t.Fatalf("models[0].ID = %q, want m1", models[0].ID)
	}
}

func TestClient_ListModels_NotImplemented(t *testing.T) {
	c := NewClient()
	c.Register(&stubAdapter{})

	_, err := c.ListModels(context.Background(), "stub")
	if err == nil {
		t.Fatal("expected error for adapter that doesn't implement ModelLister")
	}
}

func TestClient_ListModels_UnknownProvider(t *testing.T) {
	c := NewClient()
	_, err := c.ListModels(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/ -run TestClient_ListModels -v`
Expected: FAIL — `ModelLister` and `Client.ListModels` don't exist yet.

**Step 3: Write minimal implementation**

Add to `llm/client.go`, after the `ToolChoiceSupporter` interface block (~line 140):

```go
// ModelLister is implemented by adapters that can list available models from
// the provider API.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}
```

Add method to Client, after `SupportsToolChoice` (~line 188):

```go
// ListModels returns available models from the named provider. The adapter
// must implement the ModelLister interface.
func (c *Client) ListModels(ctx context.Context, provider string) ([]ModelInfo, error) {
	if c == nil {
		return nil, &ConfigurationError{Message: "client is nil"}
	}
	provider = normalizeProviderName(provider)
	a, ok := c.providers[provider]
	if !ok {
		return nil, &ConfigurationError{Message: fmt.Sprintf("unknown provider: %s", provider)}
	}
	lister, ok := a.(ModelLister)
	if !ok {
		return nil, &ConfigurationError{Message: fmt.Sprintf("provider %s does not support listing models", provider)}
	}
	return lister.ListModels(ctx)
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/ -run TestClient_ListModels -v`
Expected: PASS

**Step 5: Run full llm package tests**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/ -v`
Expected: All PASS (no regressions)

**Step 6: Commit**

```
git add llm/client.go llm/client_listmodels_test.go
git commit -m "feat: add ModelLister interface and Client.ListModels"
```

---

### Task 2: Implement `ListModels` on the OpenAI adapter

**Files:**
- Modify: `llm/providers/openai/adapter.go` (add `ListModels` method)
- Modify: `llm/providers/openai/adapter_test.go` (add test)

**Step 1: Write the failing test**

Add to `llm/providers/openai/adapter_test.go`:

```go
func TestAdapter_ListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Verify auth header is set.
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id": "gpt-4o", "object": "model", "owned_by": "openai"},
				{"id": "gpt-4o-mini", "object": "model", "owned_by": "openai"},
				{"id": "text-embedding-3-small", "object": "model", "owned_by": "openai"},
				{"id": "dall-e-3", "object": "model", "owned_by": "openai"},
				{"id": "tts-1", "object": "model", "owned_by": "openai"},
				{"id": "whisper-1", "object": "model", "owned_by": "openai"},
				{"id": "ft:gpt-4o:my-org:custom:id", "object": "model", "owned_by": "user"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	// Should filter out embedding, dall-e, tts, whisper models.
	// Should keep: gpt-4o, gpt-4o-mini, and the fine-tune.
	ids := make(map[string]bool)
	for _, m := range models {
		ids[m.ID] = true
	}
	if !ids["gpt-4o"] {
		t.Error("missing gpt-4o")
	}
	if !ids["gpt-4o-mini"] {
		t.Error("missing gpt-4o-mini")
	}
	if ids["text-embedding-3-small"] {
		t.Error("should filter out embedding model")
	}
	if ids["dall-e-3"] {
		t.Error("should filter out dall-e model")
	}
	if ids["tts-1"] {
		t.Error("should filter out tts model")
	}
	if ids["whisper-1"] {
		t.Error("should filter out whisper model")
	}
	// Verify provider field is set.
	for _, m := range models {
		if m.Provider != "openai" {
			t.Errorf("model %s: provider = %q, want openai", m.ID, m.Provider)
		}
	}
	// Verify sorted by ID.
	for i := 1; i < len(models); i++ {
		if models[i].ID < models[i-1].ID {
			t.Errorf("models not sorted: %s before %s", models[i-1].ID, models[i].ID)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/openai/ -run TestAdapter_ListModels -v`
Expected: FAIL — `ListModels` not defined.

**Step 3: Write minimal implementation**

Add to `llm/providers/openai/adapter.go`:

```go
func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	a.setHeaders(httpReq)

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var models []llm.ModelInfo
	for _, m := range result.Data {
		if skipOpenAIModel(m.ID) {
			continue
		}
		models = append(models, llm.ModelInfo{
			ID:          m.ID,
			Provider:    "openai",
			DisplayName: m.ID,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// skipOpenAIModel returns true for non-chat models (embeddings, images, audio).
func skipOpenAIModel(id string) bool {
	lower := strings.ToLower(id)
	prefixes := []string{"text-embedding", "dall-e", "tts-", "whisper", "davinci", "babbage", "embedding"}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) || strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/openai/ -run TestAdapter_ListModels -v`
Expected: PASS

**Step 5: Run full openai package tests**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/openai/ -v`
Expected: All PASS

**Step 6: Commit**

```
git add llm/providers/openai/adapter.go llm/providers/openai/adapter_test.go
git commit -m "feat: implement ListModels on OpenAI adapter"
```

---

### Task 3: Implement `ListModels` on the Anthropic adapter

**Files:**
- Modify: `llm/providers/anthropic/adapter.go`
- Modify: `llm/providers/anthropic/adapter_test.go`

**Step 1: Write the failing test**

Add to `llm/providers/anthropic/adapter_test.go`:

```go
func TestAdapter_ListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("x-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": [
				{"id": "claude-sonnet-4-6-20250514", "display_name": "Claude Sonnet 4.6", "type": "model"},
				{"id": "claude-haiku-4-5-20251001", "display_name": "Claude Haiku 4.5", "type": "model"}
			],
			"has_more": false
		}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if models[0].ID != "claude-haiku-4-5-20251001" {
		t.Errorf("models[0].ID = %q, want claude-haiku-4-5-20251001 (sorted)", models[0].ID)
	}
	if models[0].DisplayName != "Claude Haiku 4.5" {
		t.Errorf("models[0].DisplayName = %q, want 'Claude Haiku 4.5'", models[0].DisplayName)
	}
	for _, m := range models {
		if m.Provider != "anthropic" {
			t.Errorf("model %s: provider = %q, want anthropic", m.ID, m.Provider)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/anthropic/ -run TestAdapter_ListModels -v`
Expected: FAIL

**Step 3: Write minimal implementation**

Add to `llm/providers/anthropic/adapter.go`:

```go
func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range a.DefaultHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]llm.ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		displayName := m.DisplayName
		if displayName == "" {
			displayName = m.ID
		}
		models = append(models, llm.ModelInfo{
			ID:          m.ID,
			Provider:    "anthropic",
			DisplayName: displayName,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/anthropic/ -run TestAdapter_ListModels -v`
Expected: PASS

**Step 5: Run full anthropic package tests**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/anthropic/ -v`
Expected: All PASS

**Step 6: Commit**

```
git add llm/providers/anthropic/adapter.go llm/providers/anthropic/adapter_test.go
git commit -m "feat: implement ListModels on Anthropic adapter"
```

---

### Task 4: Implement `ListModels` on the Google adapter

**Files:**
- Modify: `llm/providers/google/adapter.go`
- Modify: `llm/providers/google/adapter_test.go`

**Step 1: Write the failing test**

Add to `llm/providers/google/adapter_test.go`:

```go
func TestAdapter_ListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"models": [
				{
					"name": "models/gemini-2.5-flash",
					"displayName": "Gemini 2.5 Flash",
					"supportedGenerationMethods": ["generateContent", "countTokens"]
				},
				{
					"name": "models/gemini-2.5-pro",
					"displayName": "Gemini 2.5 Pro",
					"supportedGenerationMethods": ["generateContent", "countTokens"]
				},
				{
					"name": "models/text-embedding-004",
					"displayName": "Text Embedding 004",
					"supportedGenerationMethods": ["embedContent"]
				}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "test-key", BaseURL: srv.URL, Client: srv.Client()}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	// Should filter out embedding model (no generateContent support).
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	// Model ID should strip the "models/" prefix.
	if models[0].ID != "gemini-2.5-flash" {
		t.Errorf("models[0].ID = %q, want gemini-2.5-flash", models[0].ID)
	}
	if models[0].DisplayName != "Gemini 2.5 Flash" {
		t.Errorf("models[0].DisplayName = %q", models[0].DisplayName)
	}
	for _, m := range models {
		if m.Provider != "google" {
			t.Errorf("model %s: provider = %q, want google", m.ID, m.Provider)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/google/ -run TestAdapter_ListModels -v`
Expected: FAIL

**Step 3: Write minimal implementation**

Add to `llm/providers/google/adapter.go`:

```go
func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	u, err := url.Parse(a.BaseURL + "/v1beta/models")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("key", a.APIKey)
	q.Set("pageSize", "1000")
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range a.DefaultHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var models []llm.ModelInfo
	for _, m := range result.Models {
		if !supportsGenerateContent(m.SupportedGenerationMethods) {
			continue
		}
		id := strings.TrimPrefix(m.Name, "models/")
		displayName := m.DisplayName
		if displayName == "" {
			displayName = id
		}
		models = append(models, llm.ModelInfo{
			ID:          id,
			Provider:    "google",
			DisplayName: displayName,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func supportsGenerateContent(methods []string) bool {
	for _, m := range methods {
		if m == "generateContent" {
			return true
		}
	}
	return false
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/google/ -run TestAdapter_ListModels -v`
Expected: PASS

**Step 5: Run full google package tests**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/google/ -v`
Expected: All PASS

**Step 6: Commit**

```
git add llm/providers/google/adapter.go llm/providers/google/adapter_test.go
git commit -m "feat: implement ListModels on Google adapter"
```

---

### Task 5: Implement `ListModels` on the OpenAI-compatible adapter

**Files:**
- Modify: `llm/providers/openaicompat/adapter.go`
- Modify: `llm/providers/openaicompat/adapter_test.go`

**Step 1: Write the failing test**

Add to `llm/providers/openaicompat/adapter_test.go`:

```go
func TestAdapter_ListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id": "llama3.1:latest", "object": "model"},
				{"id": "codellama:latest", "object": "model"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{BaseURL: srv.URL, Client: srv.Client()}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	for _, m := range models {
		if m.Provider != "openai-compatible" {
			t.Errorf("model %s: provider = %q, want openai-compatible", m.ID, m.Provider)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/openaicompat/ -run TestAdapter_ListModels -v`
Expected: FAIL

**Step 3: Write minimal implementation**

Add to `llm/providers/openaicompat/adapter.go`:

```go
func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range a.DefaultHeaders {
		httpReq.Header.Set(k, v)
	}
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]llm.ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, llm.ModelInfo{
			ID:          m.ID,
			Provider:    "openai-compatible",
			DisplayName: m.ID,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/openaicompat/ -run TestAdapter_ListModels -v`
Expected: PASS

**Step 5: Run full openaicompat package tests**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./llm/providers/openaicompat/ -v`
Expected: All PASS

**Step 6: Commit**

```
git add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/adapter_test.go
git commit -m "feat: implement ListModels on OpenAI-compatible adapter"
```

---

### Task 6: Add `GET /models` server endpoint

**Files:**
- Modify: `server/server.go`
- Modify: `server/server_test.go`

**Step 1: Write the failing test**

Add to `server/server_test.go`:

```go
func TestModelsEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{})

	srv.SetListModelsFunc(func(ctx context.Context) ([]ModelsResponseItem, error) {
		return []ModelsResponseItem{
			{ID: "gpt-4o", DisplayName: "gpt-4o"},
			{ID: "gpt-4o-mini", DisplayName: "gpt-4o-mini"},
		}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}

	var resp ModelsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("got %d models, want 2", len(resp.Models))
	}
	if resp.Models[0].ID != "gpt-4o" {
		t.Errorf("models[0].id = %q", resp.Models[0].ID)
	}
}

func TestModelsEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d, want 503", w.Code)
	}
}

func TestModelsEndpoint_Error(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetListModelsFunc(func(ctx context.Context) ([]ModelsResponseItem, error) {
		return nil, fmt.Errorf("upstream error")
	})

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status code: got %d, want 502", w.Code)
	}
}

func TestModelsEndpoint_MethodNotAllowed(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/models", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code: got %d, want 405", w.Code)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./server/ -run TestModelsEndpoint -v`
Expected: FAIL — types and handler don't exist.

**Step 3: Write minimal implementation**

Add to `server/server.go`:

In the `Server` struct, add field:
```go
listModelsFunc func(context.Context) ([]ModelsResponseItem, error)
```

In `NewServer`, add route:
```go
s.mux.HandleFunc("/models", s.handleModels)
```

Add types and handler:
```go
// ModelsResponseItem is a single model entry in the GET /models response.
type ModelsResponseItem struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// ModelsResponse is the JSON response for GET /models.
type ModelsResponse struct {
	Models []ModelsResponseItem `json:"models"`
}

// SetListModelsFunc sets the function called by GET /models.
func (s *Server) SetListModelsFunc(fn func(context.Context) ([]ModelsResponseItem, error)) {
	s.mu.Lock()
	s.listModelsFunc = fn
	s.mu.Unlock()
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	fn := s.listModelsFunc
	s.mu.RUnlock()

	if fn == nil {
		http.Error(w, "model listing not available", http.StatusServiceUnavailable)
		return
	}

	models, err := fn(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ModelsResponse{Models: models})
}
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./server/ -run TestModelsEndpoint -v`
Expected: PASS

**Step 5: Run full server package tests**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./server/ -v`
Expected: All PASS

**Step 6: Commit**

```
git add server/server.go server/server_test.go
git commit -m "feat: add GET /models server endpoint"
```

---

### Task 7: Wire `ListModels` in serve command

**Files:**
- Modify: `cmd/serf/serve.go`

**Step 1: Wire the callback**

In `cmd/serf/serve.go`, after `srv.SetModelFunc(sess.SetModel)` (line 104), add:

```go
srv.SetListModelsFunc(func(ctx context.Context) ([]server.ModelsResponseItem, error) {
	models, err := client.ListModels(ctx, profile.ID())
	if err != nil {
		return nil, err
	}
	items := make([]server.ModelsResponseItem, len(models))
	for i, m := range models {
		items[i] = server.ModelsResponseItem{
			ID:          m.ID,
			DisplayName: m.DisplayName,
		}
	}
	return items, nil
})
```

This requires adding `"context"` to imports if not already present. The `profile` variable is already in scope from the serve setup. Note: `profile.ID()` returns the provider name (e.g. "openai", "anthropic", "google").

**Step 2: Verify it compiles**

Run: `cd /Users/jesse/prime-radiant/serf && go build ./cmd/serf/`
Expected: Build succeeds.

**Step 3: Commit**

```
git add cmd/serf/serve.go
git commit -m "feat: wire ListModels into serve command"
```

---

### Task 8: Add model picker to TUI

**Files:**
- Create: `cmd/serf-tui/model_picker.go`
- Create: `cmd/serf-tui/model_picker_test.go`
- Modify: `cmd/serf-tui/input.go` (add `fetchModels` command)
- Modify: `cmd/serf-tui/model.go` (handle picker state + messages)

This is the largest task. The model picker is an inline overlay in the existing TUI — not a separate Bubble Tea program like `sessionPicker`. It renders inside the viewport area and intercepts key events when active.

**Step 1: Write the test for modelPicker**

Create `cmd/serf-tui/model_picker_test.go`:

```go
package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelPicker_FilterAndSelect(t *testing.T) {
	items := []modelPickerItem{
		{id: "gpt-4o", display: "gpt-4o"},
		{id: "gpt-4o-mini", display: "gpt-4o-mini"},
		{id: "o3", display: "o3"},
	}
	p := newModelPicker(items, "gpt-4o", 80)

	// Type "mini" to filter.
	for _, ch := range "mini" {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	filtered := p.(modelPicker).filtered()
	if len(filtered) != 1 {
		t.Fatalf("expected 1 filtered result, got %d", len(filtered))
	}
	if filtered[0].id != "gpt-4o-mini" {
		t.Errorf("filtered[0] = %q, want gpt-4o-mini", filtered[0].id)
	}

	// Press enter to select.
	result, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mp := result.(modelPicker)
	if mp.selected != "gpt-4o-mini" {
		t.Errorf("selected = %q, want gpt-4o-mini", mp.selected)
	}
}

func TestModelPicker_Escape(t *testing.T) {
	items := []modelPickerItem{{id: "m1", display: "m1"}}
	p := newModelPicker(items, "", 80)

	result, _ := p.Update(tea.KeyMsg{Type: tea.KeyEscape})
	mp := result.(modelPicker)
	if !mp.cancelled {
		t.Error("expected cancelled=true on escape")
	}
}

func TestModelPicker_ActiveHighlight(t *testing.T) {
	items := []modelPickerItem{
		{id: "gpt-4o", display: "gpt-4o"},
		{id: "gpt-4o-mini", display: "gpt-4o-mini"},
	}
	p := newModelPicker(items, "gpt-4o-mini", 80)

	// The view should contain an indicator for the active model.
	view := p.View()
	if !contains(view, "active") && !contains(view, "*") && !contains(view, "gpt-4o-mini") {
		t.Errorf("view should show active model indicator")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && stringContains(s, substr)
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

**Step 2: Create model picker implementation**

Create `cmd/serf-tui/model_picker.go`:

```go
package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type modelPickerItem struct {
	id      string
	display string
}

// modelPicker is an inline Bubble Tea model for selecting from a list of models.
// It supports type-to-filter, arrow key navigation, enter to select, esc to cancel.
type modelPicker struct {
	items     []modelPickerItem
	active    string // currently active model (highlighted differently)
	filter    string
	cursor    int
	width     int
	selected  string // set on enter
	cancelled bool   // set on esc
	done      bool
}

func newModelPicker(items []modelPickerItem, activeModel string, width int) modelPicker {
	return modelPicker{
		items:  items,
		active: activeModel,
		width:  width,
	}
}

func (m modelPicker) Init() tea.Cmd { return nil }

func (m modelPicker) filtered() []modelPickerItem {
	if m.filter == "" {
		return m.items
	}
	lower := strings.ToLower(m.filter)
	var out []modelPickerItem
	for _, item := range m.items {
		if strings.Contains(strings.ToLower(item.id), lower) ||
			strings.Contains(strings.ToLower(item.display), lower) {
			out = append(out, item)
		}
	}
	return out
}

func (m modelPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape, tea.KeyCtrlC:
			m.cancelled = true
			m.done = true
			return m, nil
		case tea.KeyEnter:
			filtered := m.filtered()
			if len(filtered) > 0 && m.cursor < len(filtered) {
				m.selected = filtered[m.cursor].id
			}
			m.done = true
			return m, nil
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyDown:
			filtered := m.filtered()
			if m.cursor < len(filtered)-1 {
				m.cursor++
			}
		case tea.KeyBackspace:
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
				m.cursor = 0
			}
		case tea.KeyRunes:
			m.filter += string(msg.Runes)
			m.cursor = 0
		}
	}
	return m, nil
}

func (m modelPicker) View() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	filterStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	activeTag := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	b.WriteString(titleStyle.Render("Select model"))
	b.WriteString("\n")

	filterText := m.filter
	if filterText == "" {
		filterText = dimStyle.Render("type to filter...")
	} else {
		filterText = filterStyle.Render(filterText)
	}
	b.WriteString(fmt.Sprintf("Filter: %s", filterText))
	b.WriteString("\n\n")

	filtered := m.filtered()
	if len(filtered) == 0 {
		b.WriteString(dimStyle.Render("  No matching models."))
		b.WriteString("\n")
	} else {
		// Show at most 15 items, centered around cursor.
		maxVisible := 15
		start := 0
		if len(filtered) > maxVisible {
			start = m.cursor - maxVisible/2
			if start < 0 {
				start = 0
			}
			if start+maxVisible > len(filtered) {
				start = len(filtered) - maxVisible
			}
		}
		end := start + maxVisible
		if end > len(filtered) {
			end = len(filtered)
		}

		for i := start; i < end; i++ {
			item := filtered[i]
			cursor := "  "
			style := normalStyle
			if i == m.cursor {
				cursor = "> "
				style = cursorStyle
			} else if item.id == m.active {
				style = activeStyle
			}
			line := cursor + style.Render(item.display)
			if item.id != item.display && item.display != "" {
				line += "  " + dimStyle.Render(item.id)
			}
			if item.id == m.active {
				line += "  " + activeTag.Render("(active)")
			}
			b.WriteString(line)
			b.WriteString("\n")
		}

		if len(filtered) > maxVisible {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  ... %d models total", len(filtered))))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("↑/↓ navigate  enter select  esc cancel"))
	return b.String()
}
```

**Step 3: Run picker test**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./cmd/serf-tui/ -run TestModelPicker -v`
Expected: PASS

**Step 4: Add `fetchModels` HTTP command to `input.go`**

Add to `cmd/serf-tui/input.go`:

```go
type modelsResult struct {
	models []modelPickerItem
	err    error
}

func fetchModels(addr string) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("http://%s/models", addr)
		resp, err := http.Get(url)
		if err != nil {
			return modelsResult{err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return modelsResult{err: fmt.Errorf("server returned %d", resp.StatusCode)}
		}
		var result struct {
			Models []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return modelsResult{err: err}
		}
		items := make([]modelPickerItem, len(result.Models))
		for i, m := range result.Models {
			display := m.DisplayName
			if display == "" {
				display = m.ID
			}
			items[i] = modelPickerItem{id: m.ID, display: display}
		}
		return modelsResult{models: items}
	}
}
```

**Step 5: Wire picker into TUI model**

Modify `cmd/serf-tui/model.go`:

Add field to `model` struct:
```go
picker *modelPicker // non-nil when model picker is active
```

In the `/model` case (where `args == ""`), change from showing usage text to fetching models:
```go
case "model":
	m.input.Reset()
	if args == "" {
		m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: "Fetching available models..."})
		m.refreshViewport()
		cmds = append(cmds, fetchModels(m.addr))
		return m, tea.Batch(cmds...)
	}
	m.messages = append(m.messages, chatMessage{Kind: msgSystem, Text: fmt.Sprintf("Switching to model %s...", args)})
	m.refreshViewport()
	cmds = append(cmds, sendModel(m.addr, args))
	return m, tea.Batch(cmds...)
```

Handle `modelsResult` message in the Update switch:
```go
case modelsResult:
	if msg.err != nil {
		m.messages = append(m.messages, chatMessage{
			Kind: msgSystem,
			Text: fmt.Sprintf("Could not fetch models: %s\nUse /model <name> to switch manually.", msg.err),
		})
		m.refreshViewport()
		return m, nil
	}
	if len(msg.models) == 0 {
		m.messages = append(m.messages, chatMessage{
			Kind: msgSystem,
			Text: "No models available from provider.",
		})
		m.refreshViewport()
		return m, nil
	}
	picker := newModelPicker(msg.models, m.sessionModel, m.width)
	m.picker = &picker
	// Remove the "Fetching..." message.
	if len(m.messages) > 0 && m.messages[len(m.messages)-1].Text == "Fetching available models..." {
		m.messages = m.messages[:len(m.messages)-1]
	}
	m.refreshViewport()
	return m, nil
```

Intercept key events when picker is active. At the TOP of the `tea.KeyMsg` case (before the existing switch), add:
```go
if m.picker != nil {
	updated, cmd := m.picker.Update(msg)
	p := updated.(modelPicker)
	m.picker = &p
	if p.done {
		m.picker = nil
		if p.selected != "" && p.selected != m.sessionModel {
			m.messages = append(m.messages, chatMessage{
				Kind: msgSystem,
				Text: fmt.Sprintf("Switching to model %s...", p.selected),
			})
			m.refreshViewport()
			return m, sendModel(m.addr, p.selected)
		}
		m.refreshViewport()
	}
	return m, cmd
}
```

In the `View()` method, render the picker when active. In the viewport content section, add before the viewport render:
```go
if m.picker != nil {
	// Show picker in place of the viewport content.
	pickerView := m.picker.View()
	// ... render pickerView in the viewport area
}
```

The exact integration depends on how `View()` is structured — the picker replaces the last portion of the viewport.

**Step 6: Update help text**

In `cmd/serf-tui/input.go`, update the `/model` help line:
```go
"  /model     Switch model (picker) or /model <name>",
```

**Step 7: Run all TUI tests**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./cmd/serf-tui/ -v`
Expected: All PASS

**Step 8: Commit**

```
git add cmd/serf-tui/model_picker.go cmd/serf-tui/model_picker_test.go cmd/serf-tui/input.go cmd/serf-tui/model.go
git commit -m "feat: add interactive model picker to TUI /model command"
```

---

### Task 9: Integration test and final verification

**Files:**
- No new files — run full test suite and manual verification.

**Step 1: Run full test suite**

Run: `cd /Users/jesse/prime-radiant/serf && go test ./... 2>&1 | tail -30`
Expected: All packages PASS.

**Step 2: Build binaries**

Run: `cd /Users/jesse/prime-radiant/serf && go build ./cmd/serf/ && go build ./cmd/serf-tui/`
Expected: Both build successfully.

**Step 3: Commit any remaining changes**

If any adjustments were needed during integration, commit them.
