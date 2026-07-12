//go:build serffuzz

package cmdutil

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

type coverageRoundTripper func(*http.Request) (*http.Response, error)

func (f coverageRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingReader) Close() error             { return nil }

// FuzzCmdutilCoverage is a deterministic union seed for dependency-bound
// cmdutil branches. All HTTP is intercepted before it reaches a transport.
func FuzzCmdutilCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		oldClient := http.DefaultClient
		oldRaw := apiRawBodyEnabled
		oldFromEnv := newClientFromEnv
		oldAvailable := newClientFromAvailableProviders
		t.Cleanup(func() {
			http.DefaultClient = oldClient
			apiRawBodyEnabled = oldRaw
			newClientFromEnv = oldFromEnv
			newClientFromAvailableProviders = oldAvailable
		})
		apiRawBodyEnabled = func() bool { return true }
		closeLog, err := AttachAPILogger(llm.NewClient(), t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = closeLog()
		_, _ = ResolveReasoningEffort("none", "")

		responses := []struct {
			status int
			body   io.ReadCloser
			err    error
		}{
			{err: errors.New("offline")},
			{status: http.StatusUnauthorized, body: io.NopCloser(strings.NewReader("no"))},
			{status: http.StatusOK, body: failingReader{}},
			{status: http.StatusOK, body: io.NopCloser(strings.NewReader("{"))},
			{status: http.StatusOK, body: io.NopCloser(strings.NewReader(`{"data":[{"id":"other","context_length":1}]}`))},
			{status: http.StatusOK, body: io.NopCloser(strings.NewReader(`{"data":[{"id":"wanted","context_length":42}]}`))},
		}
		for _, response := range responses {
			response := response
			http.DefaultClient = &http.Client{Transport: coverageRoundTripper(func(req *http.Request) (*http.Response, error) {
				if response.err != nil {
					return nil, response.err
				}
				return &http.Response{StatusCode: response.status, Body: response.body, Header: make(http.Header)}, nil
			})}
			_ = queryModelContextWindow("kimi", "wanted", "https://offline.invalid", "key", map[string]string{"X-Test": "yes"})
		}
		for _, tc := range []struct {
			provider, base, key string
			headers             map[string]string
		}{
			{"ollama", "x", "k", nil},
			{"openai-compatible", "", "", nil},
			{"openai-compatible", "://bad", "", nil},
			{"openrouter", "", "", nil},
			{"openrouter", "", "", map[string]string{"Authorization": "token"}},
		} {
			_ = queryModelContextWindow(tc.provider, "m", tc.base, tc.key, tc.headers)
		}
		t.Setenv(envvars.OpenRouterAPIKey.Name, "key")
		_ = queryModelContextWindow("openrouter", "m", "https://offline.invalid", "", nil)

		t.Setenv("CMDUTIL_COVERAGE_KEY", "secret")
		cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{
			Name: "custom", Type: "openai", APIStyle: "chat-completions", BaseURL: "https://offline.invalid",
			APIKey: "$CMDUTIL_COVERAGE_KEY", Models: map[string]providercfg.ModelConfig{"m": {ContextWindow: 7}},
			Headers: map[string]string{"Authorization": "$CMDUTIL_MISSING", "X-Bad": "$CMDUTIL_MISSING", "X-Good": "ok"},
		}}}
		_, _, _, _ = instanceEndpoint(cfg, "custom")
		_ = instanceConfiguresContextWindow(cfg, "missing", "m")
		if _, err := ResolveProfileWithLiveWindow(cfg, "missing/m"); err == nil {
			t.Fatal("missing instance resolved")
		}

		badState := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(badState, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envvars.SERFStateDir.Name, badState)
		_, _ = seedConfigFromEnv()
		_, _ = MaterializeProvidersConfig(filepath.Join(badState, "providers.toml"))
		_, _, _ = LoadProviderConfig()
		_, _, _, _ = LoadClient()
		newClientFromEnv = func(...llm.EnvOption) (*llm.Client, error) { return nil, errors.New("factory failed") }
		_, _ = seedConfigFromEnv()
		_, _ = MaterializeProvidersConfig(filepath.Join(t.TempDir(), "providers.toml"))
		t.Setenv(envvars.SERFProvidersConfig.Name, filepath.Join(t.TempDir(), "absent.toml"))
		_, _, _ = LoadProviderConfig()
		newClientFromEnv = oldFromEnv
		newClientFromAvailableProviders = func(providercfg.Config, ...llm.EnvOption) (*llm.Client, []error, error) {
			return nil, nil, errors.New("adapter failed")
		}
		_, _, _, _ = LoadClient()
		newClientFromAvailableProviders = oldAvailable

		validRoot := t.TempDir()
		t.Setenv(envvars.SERFStateDir.Name, validRoot)
		t.Setenv(envvars.OllamaBaseURL.Name, "http://ollama.invalid")
		t.Setenv(envvars.OllamaHost.Name, "http://host.invalid")
		_, _ = seedConfigFromEnv()
		t.Setenv(envvars.OllamaBaseURL.Name, "")
		_, _ = seedConfigFromEnv()
		_, _ = MaterializeProvidersConfig(filepath.Join(validRoot, "providers.toml"))

		invalidConfig := filepath.Join(validRoot, "invalid.toml")
		if err := os.WriteFile(invalidConfig, []byte("["), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envvars.SERFProvidersConfig.Name, invalidConfig)
		_, _, _ = LoadProviderConfig()
		_, _, _, _ = LoadClient()

		cpu := filepath.Join(t.TempDir(), "cpu")
		stopCPU, err := StartCPUProfile(cpu)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = StartCPUProfile(filepath.Join(t.TempDir(), "second"))
		stopCPU()
		trace := filepath.Join(t.TempDir(), "trace")
		stopTrace, err := StartTrace(trace)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = StartTrace(filepath.Join(t.TempDir(), "second-trace"))
		stopTrace()

		t.Setenv(envvars.SERFStateDir.Name, "")
		t.Setenv(envvars.XDGConfigHome.Name, "")
		t.Setenv("HOME", "")
		_ = DefaultStateRoot()
		_ = DefaultConfigRoot()
	})
}
