package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/auth/openai/oaitest"
	_ "primeradiant.com/serf/llm/providers/openai"
	_ "primeradiant.com/serf/llm/providers/openrouter"
)

func TestLaunchCheckReportsProtocolAndValidatedModel(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runLaunchCheck([]string{
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
	err := runLaunchCheck([]string{
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
	err := runLaunchCheck([]string{
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
	err := runLaunchCheck([]string{
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
	err := runLaunchCheck([]string{
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

type launchCheckTimeoutError struct{}

func (launchCheckTimeoutError) Error() string   { return "timeout" }
func (launchCheckTimeoutError) Timeout() bool   { return true }
func (launchCheckTimeoutError) Temporary() bool { return true }

func TestLaunchCheckRejectsUnsupportedProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runLaunchCheck([]string{
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
	err := runLaunchCheck([]string{
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

func TestLaunchCheckDispatchesFromTopLevel(t *testing.T) {
	var stdout, stderr bytes.Buffer
	handled, label, err := dispatchCLICommand([]string{
		"launch-check",
		"--protocol", appwire.ProtocolVersion,
		"--model", "openrouter/free",
		"--json",
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("dispatchCLICommand: %v", err)
	}
	if !handled {
		t.Fatal("dispatchCLICommand handled=false, want true")
	}
	if label != "serf launch-check" {
		t.Fatalf("label=%q, want serf launch-check", label)
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
