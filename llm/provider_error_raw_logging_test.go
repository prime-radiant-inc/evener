package llm_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/anthropic"
	"primeradiant.com/serf/llm/providers/glm"
	"primeradiant.com/serf/llm/providers/google"
	"primeradiant.com/serf/llm/providers/kimi"
	kimi_anthropic "primeradiant.com/serf/llm/providers/kimi_anthropic"
	"primeradiant.com/serf/llm/providers/minimax"
	_ "primeradiant.com/serf/llm/providers/ollama"
	"primeradiant.com/serf/llm/providers/openai"
	"primeradiant.com/serf/llm/providers/openaicompat"
	"primeradiant.com/serf/llm/providers/openrouter"
	openrouter_anthropic "primeradiant.com/serf/llm/providers/openrouter_anthropic"
)

const providerRawErrorHelperEnv = "SERF_PROVIDER_RAW_ERROR_LOGGING_HELPER"

func TestProviderHTTPErrorRawLogging(t *testing.T) {
	if os.Getenv(providerRawErrorHelperEnv) == "1" {
		t.Skip("subprocess helper is run directly by the parent test")
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestProviderHTTPErrorRawLoggingHelper", "-test.v")
	cmd.Env = append(os.Environ(),
		providerRawErrorHelperEnv+"=1",
		"SERF_LOG_RAW_HTTP=1",
		"XDG_STATE_HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("provider raw error logging helper failed: %v\n%s", err, out)
	}
}

func TestProviderHTTPErrorRawLoggingHelper(t *testing.T) {
	if os.Getenv(providerRawErrorHelperEnv) != "1" {
		t.Skip("helper runs only in subprocess with SERF_LOG_RAW_HTTP set before package init")
	}
	if !llm.RawBodyEnabled() {
		t.Fatal("RawBodyEnabled is false in helper subprocess")
	}

	type providerCase struct {
		name      string
		model     string
		newClient func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client
	}

	openAICompatBase := func(baseURL string) string { return strings.TrimRight(baseURL, "/") + "/v1" }
	anthropicBase := func(baseURL string) string { return strings.TrimRight(baseURL, "/") }

	cases := []providerCase{
		{
			name:  "openai",
			model: "gpt-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				return clientWithAdapter(&openai.Adapter{APIKey: "bad-key", BaseURL: anthropicBase(baseURL), Client: httpClient})
			},
		},
		{
			name:  "openai-chat-completions",
			model: "gpt-chat-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				return clientWithAdapter(openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
					Name:    "openai-chat-completions",
					BaseURL: openAICompatBase(baseURL),
					APIKey:  "bad-key",
				}))
			},
		},
		{
			name:  "anthropic",
			model: "claude-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				a, err := anthropic.NewForInstance(anthropic.AnthropicInstanceParams{Name: "anthropic", BaseURL: anthropicBase(baseURL), APIKey: "bad-key"})
				if err != nil {
					t.Fatalf("anthropic adapter: %v", err)
				}
				a.Client = httpClient
				return clientWithAdapter(a)
			},
		},
		{
			name:  "google",
			model: "gemini-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				a, err := google.NewForInstance(google.GoogleInstanceParams{Name: "google", BaseURL: anthropicBase(baseURL), APIKey: "bad-key"})
				if err != nil {
					t.Fatalf("google adapter: %v", err)
				}
				a.Client = httpClient
				return clientWithAdapter(a)
			},
		},
		{
			name:  "gemini",
			model: "gemini-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				a, err := google.NewForInstance(google.GoogleInstanceParams{Name: "gemini", BaseURL: anthropicBase(baseURL), APIKey: "bad-key"})
				if err != nil {
					t.Fatalf("gemini adapter: %v", err)
				}
				a.Client = httpClient
				return clientWithAdapter(a)
			},
		},
		{
			name:  "openai-compatible",
			model: "compat-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				a := openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{Name: "openai-compatible", BaseURL: openAICompatBase(baseURL), APIKey: "bad-key"})
				a.Client = httpClient
				return clientWithAdapter(a)
			},
		},
		{
			name:  "glm",
			model: "glm-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				a := glm.NewForInstance(glm.InstanceParams{Name: "glm", BaseURL: openAICompatBase(baseURL), APIKey: "bad-key"})
				a.Client = httpClient
				return clientWithAdapter(a)
			},
		},
		{
			name:  "kimi",
			model: "kimi-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				a := kimi.NewForInstance(kimi.InstanceParams{Name: "kimi", BaseURL: openAICompatBase(baseURL), APIKey: "bad-key"})
				a.Client = httpClient
				return clientWithAdapter(a)
			},
		},
		{
			name:  "ollama",
			model: "ollama-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				return clientWithProviderConfig(t, providercfg.InstanceConfig{
					Name:    "ollama",
					Type:    "ollama",
					BaseURL: openAICompatBase(baseURL),
					APIKey:  "bad-key",
				})
			},
		},
		{
			name:  "openrouter",
			model: "openrouter-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				a := openrouter.NewForInstance(openrouter.InstanceParams{Name: "openrouter", BaseURL: openAICompatBase(baseURL), APIKey: "bad-key"})
				a.Client = httpClient
				return clientWithAdapter(a)
			},
		},
		{
			name:  "kimi-anthropic",
			model: "kimi-anthropic-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				a := kimi_anthropic.NewForInstance(kimi_anthropic.InstanceParams{Name: "kimi-anthropic", BaseURL: anthropicBase(baseURL), APIKey: "bad-key"})
				a.Client = httpClient
				return clientWithAdapter(a)
			},
		},
		{
			name:  "minimax",
			model: "minimax-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				a := minimax.NewForInstance(minimax.InstanceParams{Name: "minimax", BaseURL: anthropicBase(baseURL), APIKey: "bad-key"})
				a.Client = httpClient
				return clientWithAdapter(a)
			},
		},
		{
			name:  "openrouter-anthropic",
			model: "openrouter-anthropic-test",
			newClient: func(t *testing.T, baseURL string, httpClient *http.Client) *llm.Client {
				a := openrouter_anthropic.NewForInstance(openrouter_anthropic.InstanceParams{Name: "openrouter-anthropic", BaseURL: anthropicBase(baseURL), APIKey: "bad-key"})
				a.Client = httpClient
				return clientWithAdapter(a)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var requests []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				requests = append(requests, r.URL.String()+" "+string(body))
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				switch {
				case strings.Contains(r.URL.Path, "generateContent"):
					_, _ = io.WriteString(w, `{"error":{"code":401,"message":"bad-key-provider-error","status":"UNAUTHENTICATED"}}`)
				case strings.Contains(r.URL.Path, "messages"):
					_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad-key-provider-error"}}`)
				default:
					_, _ = io.WriteString(w, `{"error":{"message":"bad-key-provider-error","type":"invalid_request_error","code":"invalid_api_key"}}`)
				}
			}))
			t.Cleanup(srv.Close)

			dir := t.TempDir()
			apiPath := filepath.Join(dir, "api.jsonl")
			rawPath := filepath.Join(dir, "api-raw.jsonl")
			logger, err := llm.NewAPILogger(apiPath)
			if err != nil {
				t.Fatalf("NewAPILogger: %v", err)
			}
			if err := logger.EnableRawLogging(rawPath); err != nil {
				t.Fatalf("EnableRawLogging: %v", err)
			}

			client := tc.newClient(t, srv.URL, srv.Client())
			client.Use(logger)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st, err := client.Stream(ctx, llm.Request{
				Provider: tc.name,
				Model:    tc.model,
				Messages: []llm.Message{llm.User("hi from " + tc.name)},
			})
			if st != nil {
				_ = st.Close()
			}
			if err == nil {
				t.Fatal("Stream succeeded, want provider auth error")
			}
			if err := logger.Close(); err != nil {
				t.Fatalf("close logger: %v", err)
			}

			mu.Lock()
			requestCount := len(requests)
			mu.Unlock()
			if requestCount != 1 {
				t.Fatalf("provider request count = %d, want 1", requestCount)
			}

			apiEntries := readJSONLines[llm.APILogEntry](t, apiPath)
			if len(apiEntries) != 1 {
				t.Fatalf("api.jsonl entries = %d, want 1", len(apiEntries))
			}
			if apiEntries[0].Error == "" {
				t.Fatalf("api.jsonl error is empty: %+v", apiEntries[0])
			}

			rawEntries := readJSONLines[llm.APIRawLogEntry](t, rawPath)
			if len(rawEntries) != 1 {
				t.Fatalf("api-raw.jsonl entries = %d, want 1", len(rawEntries))
			}
			raw := rawEntries[0]
			if raw.Mode != "stream" {
				t.Fatalf("raw mode = %q, want stream", raw.Mode)
			}
			if raw.Provider != tc.name {
				t.Fatalf("raw provider = %q, want %q", raw.Provider, tc.name)
			}
			if raw.Model != tc.model {
				t.Fatalf("raw model = %q, want %q", raw.Model, tc.model)
			}
			if strings.TrimSpace(raw.RequestBody) == "" {
				t.Fatal("raw request_body is empty")
			}
			if !strings.Contains(raw.ResponseBody, "bad-key-provider-error") {
				t.Fatalf("raw response_body missing provider error marker: %q", raw.ResponseBody)
			}
		})
	}
}

func clientWithAdapter(adapter llm.ProviderAdapter) *llm.Client {
	c := llm.NewClient()
	c.Register(adapter)
	return c
}

func clientWithProviderConfig(t *testing.T, inst providercfg.InstanceConfig) *llm.Client {
	t.Helper()
	c, err := llm.NewFromProviders(providercfg.Config{
		Default:   inst.Name,
		Instances: []providercfg.InstanceConfig{inst},
	})
	if err != nil {
		t.Fatalf("NewFromProviders(%s): %v", inst.Name, err)
	}
	return c
}

func readJSONLines[T any](t *testing.T, path string) []T {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close() //nolint:errcheck

	var out []T
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var item T
		if err := json.Unmarshal(sc.Bytes(), &item); err != nil {
			t.Fatalf("decode %s: %v\nline: %s", path, err, sc.Text())
		}
		out = append(out, item)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}
