package launchcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/auth/openai/oaitest"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/openai"
	_ "primeradiant.com/serf/llm/providers/openrouter"
)

func TestLaunchCheckReportsProtocolAndValidatedModel(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "openrouter/free",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Protocol string `json:"protocol"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if out.Protocol != appwire.ProtocolVersion || out.Provider != "openrouter" || out.Model != "free" {
		t.Fatalf("launch check output=%+v", out)
	}
}

func TestLaunchCheckListsLiveModelsFromConfiguredProviders(t *testing.T) {
	configureLaunchCheckOpenAIModels(t, `{"data":[{"id":"gpt-live"},{"id":"text-embedding-3-small"}]}`)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--models",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Models []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"models"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if len(out.Models) != 1 || out.Models[0].Provider != "openai" || out.Models[0].Model != "gpt-live" {
		t.Fatalf("models=%+v", out.Models)
	}
}

func TestLaunchCheckReportsModelEnumerationDiagnostics(t *testing.T) {
	configureLaunchCheckOpenAIModelStatus(t, http.StatusForbidden, `{"error":"forbidden"}`)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--models",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Diagnostics []struct {
			Provider string `json:"provider"`
			Source   string `json:"source"`
			Title    string `json:"title"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if len(out.Diagnostics) == 0 {
		t.Fatalf("diagnostics=%+v", out.Diagnostics)
	}
	var found bool
	for _, got := range out.Diagnostics {
		if got.Provider == "openai" {
			found = true
			if got.Source != "provider" || got.Title == "" || !strings.Contains(got.Message, "403") {
				t.Fatalf("diagnostic=%+v", got)
			}
		}
	}
	if !found {
		t.Fatalf("diagnostics missing openai entry: %+v", out.Diagnostics)
	}
}

func TestLaunchCheckListsModelsWhenOneConfiguredProviderCannotInitialize(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-live"}]}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(cfgPath, []byte(`schema = 1
default = "openai"

[instances.anthropic]
type = "anthropic"

[instances.openai]
type = "openai"
base_url = "`+srv.URL+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.toml"), []byte(`schema = 1
[providers.openai]
api_key = "test-key"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERF_PROVIDERS_CONFIG", cfgPath)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--models",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}

	var out struct {
		Models []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"models"`
		Diagnostics []struct {
			Provider string `json:"provider"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if len(out.Models) != 1 || out.Models[0].Provider != "openai" || out.Models[0].Model != "gpt-live" {
		t.Fatalf("models=%+v", out.Models)
	}
	var foundAnthropic bool
	for _, diag := range out.Diagnostics {
		if diag.Provider == "anthropic" {
			foundAnthropic = true
			if diag.Message == "" {
				t.Fatalf("anthropic diagnostic has empty message: %+v", diag)
			}
		}
	}
	if !foundAnthropic {
		t.Fatalf("diagnostics missing anthropic entry: %+v", out.Diagnostics)
	}
}

func TestLaunchCheckModelDiagnosticRedactsEnvSecrets(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "sk-launch-secret")
	diag := launchCheckModelDiagnostic("openai", errors.New("provider rejected credential sk-launch-secret in https://sk-launch-secret@example.test"))
	if strings.Contains(diag.Message, "sk-launch-secret") {
		t.Fatalf("diagnostic leaked secret: %+v", diag)
	}
	if !strings.Contains(diag.Message, "[redacted]") {
		t.Fatalf("diagnostic did not redact secret: %+v", diag)
	}
}

func TestLaunchCheckRejectsModelMissingFromLiveProviderList(t *testing.T) {
	configureLaunchCheckOpenAIModels(t, `{"data":[{"id":"gpt-live"}]}`)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "openai/gpt-stale",
		"--json",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected stale model rejection")
	}
	if !strings.Contains(err.Error(), "model openai/gpt-stale is not available") {
		t.Fatalf("error=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q, want empty on failure", stdout.String())
	}
}

func TestLaunchCheckAcceptsModelWhenProviderCannotEnumerateModels(t *testing.T) {
	configureLaunchCheckOpenAIModelStatus(t, http.StatusForbidden, `{"error":"forbidden"}`)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "openai/gpt-5.5",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if out.Provider != "openai" || out.Model != "gpt-5.5" {
		t.Fatalf("launch check output=%+v", out)
	}
}

func TestLaunchCheckModelListUnavailableClassifiesTransientFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "context deadline",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "provider deadline string",
			err:  errors.New(`Get "https://chatgpt.com/backend-api/codex/models?client_version=0.0.0": context deadline exceeded`),
			want: true,
		},
		{
			name: "dns lookup failure",
			err:  errors.New(`Get "https://example.test/v1/models": dial tcp: lookup example.test: no such host`),
			want: true,
		},
		{
			name: "connection refused",
			err:  errors.New(`Get "http://localhost:11434/v1/models": dial tcp [::1]:11434: connect: connection refused`),
			want: true,
		},
		{
			name: "net timeout",
			err:  launchCheckTimeoutError{},
			want: true,
		},
		{
			name: "definite model failure",
			err:  errors.New("model openai/gpt-stale is not available"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := launchCheckModelListUnavailable(tt.err); got != tt.want {
				t.Fatalf("launchCheckModelListUnavailable(%v)=%v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestLaunchCheckModelVisibleFiltersByBehaviorTag verifies that
// launchCheckModelVisible filters models by the behavior tag passed in, not by a
// literal provider instance name. A renamed openrouter instance (e.g.
// "or-work") that is mapped to tag "openrouter" must be filtered the same way
// as the canonical "openrouter" instance name — i.e. hidden when not in the
// catalog or the catalog entry lacks SupportsTools.
func TestLaunchCheckModelVisibleFiltersByBehaviorTag(t *testing.T) {
	cat := llm.EmbeddedModelCatalog()

	// With the canonical tag "openrouter", a model absent from the catalog is hidden.
	if launchCheckModelVisible("openrouter", "not-in-catalog-xyz", cat) {
		t.Error("model not in catalog should be hidden when behaviorTag is openrouter")
	}
	// With a non-openrouter tag, any non-media model is visible.
	if !launchCheckModelVisible("some-other-tag", "not-in-catalog-xyz", cat) {
		t.Error("model should be visible when behaviorTag is not openrouter")
	}
}

type launchCheckTimeoutError struct{}

func (launchCheckTimeoutError) Error() string   { return "timeout" }
func (launchCheckTimeoutError) Timeout() bool   { return true }
func (launchCheckTimeoutError) Temporary() bool { return true }

func TestLaunchCheckRejectsUnsupportedProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "missing/free",
		"--json",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("error=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q, want empty on failure", stdout.String())
	}
}

func TestLaunchCheckRejectsProtocolMismatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", "serf-appwire-v0",
		"--model", "openrouter/free",
		"--json",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected protocol mismatch")
	}
	if !strings.Contains(err.Error(), "unsupported appwire protocol") {
		t.Fatalf("error=%v", err)
	}
}

// TestLaunchCheckAcceptsConfigInstanceModel verifies that when
// SERF_PROVIDERS_CONFIG points to a valid config file, launch-check resolves
// a custom instance name (e.g. "work2") without requiring credentials.
func TestLaunchCheckAcceptsConfigInstanceModel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(cfgPath, []byte(`schema = 1
default = "work"
[instances.work]
type = "openai"
[instances.work2]
type = "openai"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERF_PROVIDERS_CONFIG", cfgPath)
	oaitest.IsolateOpenAIAuth(t)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "work2/gpt-5.2",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if out.Provider != "work2" || out.Model != "gpt-5.2" {
		t.Fatalf("launch check output=%+v", out)
	}
}

// TestLaunchCheckAcceptsCompatInstanceModel verifies that a chat-completions
// instance with a custom base_url is also accepted by the config path.
func TestLaunchCheckAcceptsCompatInstanceModel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(cfgPath, []byte(`schema = 1
default = "work"
[instances.work]
type = "openai"
[instances.compat-x]
type = "openai"
api_style = "chat-completions"
base_url = "https://example.test/v1"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERF_PROVIDERS_CONFIG", cfgPath)
	oaitest.IsolateOpenAIAuth(t)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "compat-x/some-model",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if out.Provider != "compat-x" || out.Model != "some-model" {
		t.Fatalf("launch check output=%+v", out)
	}
}

// TestLaunchCheckRejectsUnknownInstanceFromConfig verifies that a model ref
// naming an instance not present in the providers.toml is rejected.
func TestLaunchCheckRejectsUnknownInstanceFromConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(cfgPath, []byte(`schema = 1
default = "work"
[instances.work]
type = "openai"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERF_PROVIDERS_CONFIG", cfgPath)
	oaitest.IsolateOpenAIAuth(t)

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{
		"--protocol", appwire.ProtocolVersion,
		"--model", "missing/some-model",
		"--json",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected unknown instance error")
	}
	if !strings.Contains(err.Error(), "unknown instance") {
		t.Fatalf("error=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q, want empty on failure", stdout.String())
	}
}

func configureLaunchCheckOpenAIModels(t *testing.T, body string) {
	t.Helper()
	configureLaunchCheckOpenAIModelStatus(t, http.StatusOK, body)
}

func configureLaunchCheckOpenAIModelStatus(t *testing.T, status int, body string) {
	t.Helper()
	// Isolate from any stored OAuth / OpenAI env vars on the dev machine so
	// the test's OPENAI_API_KEY + OPENAI_BASE_URL win deterministically.
	oaitest.IsolateOpenAIAuth(t)
	for _, key := range []string{
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY",
		"GLM_API_KEY",
		"GROK_API_KEY",
		"KIMI_API_KEY",
		"MINIMAX_API_KEY",
		"OPENROUTER_API_KEY",
	} {
		t.Setenv(key, "")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OLLAMA_BASE_URL", srv.URL+"/missing")
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("OLLAMA_API_KEY", "")
}
