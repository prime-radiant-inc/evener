package llm_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
	"primeradiant.com/serf/llm/providercfg"
)

func TestClientProviderOperationsWriteCanonicalAPILog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	tests := []struct {
		name         string
		providerType providercfg.Type
		apiStyle     providercfg.APIStyle
		pathSuffix   string
		responseBody string
		requestModel string
		call         func(context.Context, *llm.Client, string) error
	}{
		{
			name: "openai model list", providerType: "openai", apiStyle: providercfg.StyleResponses,
			pathSuffix: "/v1/models", responseBody: `{"data":[{"id":"gpt-test"}]}`, requestModel: "*",
			call: func(ctx context.Context, client *llm.Client, provider string) error {
				_, err := client.ListModels(ctx, provider)
				return err
			},
		},
		{
			name: "anthropic model list", providerType: "anthropic",
			pathSuffix: "/v1/models", responseBody: `{"data":[{"id":"claude-test"}],"has_more":false}`, requestModel: "*",
			call: func(ctx context.Context, client *llm.Client, provider string) error {
				_, err := client.ListModels(ctx, provider)
				return err
			},
		},
		{
			name: "google model list", providerType: "google",
			pathSuffix: "/v1beta/models", responseBody: `{"models":[{"name":"models/gemini-test","supportedGenerationMethods":["generateContent"]}]}`, requestModel: "*",
			call: func(ctx context.Context, client *llm.Client, provider string) error {
				_, err := client.ListModels(ctx, provider)
				return err
			},
		},
		{
			name: "openai compatible model list", providerType: "openai", apiStyle: providercfg.StyleChatCompletions,
			pathSuffix: "/models", responseBody: `{"data":[{"id":"compat-test"}]}`, requestModel: "*",
			call: func(ctx context.Context, client *llm.Client, provider string) error {
				_, err := client.ListModels(ctx, provider)
				return err
			},
		},
		{
			name: "openai input token count", providerType: "openai", apiStyle: providercfg.StyleResponses,
			pathSuffix: "/v1/responses/input_tokens", responseBody: `{"input_tokens":7}`, requestModel: "test-model",
			call: countInputTokens,
		},
		{
			name: "anthropic input token count", providerType: "anthropic",
			pathSuffix: "/v1/messages/count_tokens", responseBody: `{"input_tokens":8}`, requestModel: "test-model",
			call: countInputTokens,
		},
		{
			name: "google input token count", providerType: "google",
			pathSuffix: "/v1beta/models/test-model:countTokens", responseBody: `{"totalTokens":9}`, requestModel: "test-model",
			call: countInputTokens,
		},
		{
			name: "kimi input token count", providerType: "kimi",
			pathSuffix: "/tokenizers/estimate-token-count", responseBody: `{"data":{"total_tokens":10}}`, requestModel: "test-model",
			call: countInputTokens,
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var wireRequestBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if !strings.HasSuffix(request.URL.Path, test.pathSuffix) {
					t.Errorf("request path = %q, want suffix %q", request.URL.Path, test.pathSuffix)
				}
				wireRequestBody, _ = io.ReadAll(request.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.responseBody)
			}))
			t.Cleanup(server.Close)

			provider := "provider-operation-" + strings.ReplaceAll(test.name, " ", "-")
			client, err := llm.NewFromProviders(providercfg.Config{
				Default: provider,
				Instances: []providercfg.InstanceConfig{{
					Name: provider, Type: test.providerType, APIStyle: test.apiStyle,
					BaseURL: server.URL, APIKey: "provider-secret",
					Headers:           map[string]string{"X-Provider-Instance": provider},
					CredentialHeaders: map[string]string{"X-Secret-Header": "custom-secret"},
				}},
			})
			if err != nil {
				t.Fatalf("NewFromProviders: %v", err)
			}

			stateDir := t.TempDir()
			logger, err := llm.NewSessionAPILogger(stateDir)
			if err != nil {
				t.Fatalf("NewSessionAPILogger: %v", err)
			}
			t.Cleanup(func() { _ = logger.Close() })
			client.Use(logger)

			sessionID := ""
			ctx := context.Background()
			if i != 0 {
				sessionID = "session-provider-operation"
				ctx = llm.WithAPILogContext(ctx, sessionID, 0)
			}
			if err := test.call(ctx, client, provider); err != nil {
				t.Fatalf("provider call: %v", err)
			}

			logPath := filepath.Join(stateDir, "sessions", "session-provider-operation.api.jsonl")
			if sessionID == "" {
				logPath = filepath.Join(stateDir, "sessions", "unattributed.api.jsonl")
			}
			records := readAPILogRecords(t, logPath)
			if len(records) != 2 {
				t.Fatalf("canonical records = %d, want one attempt and one settlement", len(records))
			}
			attempt, ok := records[0].(apilog.APIAttemptRecord)
			if !ok {
				t.Fatalf("first canonical record = %T, want APIAttemptRecord", records[0])
			}
			settlement, ok := records[1].(apilog.APIAttemptGroupSettlement)
			if !ok {
				t.Fatalf("second canonical record = %T, want APIAttemptGroupSettlement", records[1])
			}
			if attempt.ProviderInstance != provider || attempt.RequestModel != test.requestModel {
				t.Errorf("attempt provider/model = %q/%q, want %q/%q", attempt.ProviderInstance, attempt.RequestModel, provider, test.requestModel)
			}
			if attempt.Request.Method != http.MethodGet && attempt.Request.Method != http.MethodPost {
				t.Errorf("attempt method = %q", attempt.Request.Method)
			}
			requestBody, err := apilog.DecodeBody(attempt.Request.Body)
			if err != nil {
				t.Fatalf("DecodeBody(request): %v", err)
			}
			if string(requestBody) != string(wireRequestBody) {
				t.Errorf("logged request body = %q, wire request body = %q", requestBody, wireRequestBody)
			}
			if attempt.Response == nil {
				t.Fatal("attempt response is nil")
			}
			responseBody, err := apilog.DecodeBody(attempt.Response.Body)
			if err != nil {
				t.Fatalf("DecodeBody(response): %v", err)
			}
			if string(responseBody) != test.responseBody {
				t.Errorf("logged response body = %q, want %q", responseBody, test.responseBody)
			}
			if attempt.Outcome != apilog.AttemptSuccess || settlement.Outcome != apilog.AttemptSuccess {
				t.Errorf("attempt/settlement outcomes = %q/%q, want success/success", attempt.Outcome, settlement.Outcome)
			}
			if settlement.AttemptGroupID != attempt.AttemptGroupID || settlement.FinalAttemptID != attempt.AttemptID || settlement.FinalAttemptCount != 1 {
				t.Errorf("settlement = %+v, want final attempt %+v", settlement, attempt)
			}

			persisted, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("ReadFile(API log): %v", err)
			}
			if strings.Contains(string(persisted), "provider-secret") || strings.Contains(string(persisted), "custom-secret") {
				t.Fatal("canonical API log contains credential material")
			}
		})
	}
}

func TestClientLocalProviderOperationsDoNotWriteCanonicalRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	logger, err := llm.NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	client := llm.NewClient()
	client.Register(&localOnlyAdapter{})
	client.Register(&unsupportedCountAdapter{})
	client.Use(logger)

	if _, err := client.CountInputTokens(context.Background(), llm.Request{Provider: "local-only", Messages: []llm.Message{llm.User("missing model")}}); err == nil {
		t.Fatal("invalid token-count request succeeded")
	}
	if count, err := client.CountInputTokens(context.Background(), llm.Request{
		Provider: "local-only", Model: "m", Messages: []llm.Message{llm.User("local estimate")},
	}); err != nil || count.Exact {
		t.Fatalf("local token estimate = %+v, %v", count, err)
	}
	if count, err := client.CountInputTokens(context.Background(), llm.Request{
		Provider: "unsupported-count", Model: "m", Messages: []llm.Message{llm.User("unsupported")},
	}); err != nil || count.Exact {
		t.Fatalf("unsupported provider token estimate = %+v, %v", count, err)
	}
	if _, err := client.ListModels(context.Background(), "local-only"); err == nil {
		t.Fatal("unsupported model listing succeeded")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(API log): %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("API log size = %d, want no invented records", info.Size())
	}
}

func TestClientProviderOperationFailurePreservesErrorAndSettlement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"permission denied"}}`)
	}))
	t.Cleanup(server.Close)

	provider := "counting-instance"
	client, err := llm.NewFromProviders(providercfg.Config{
		Default: provider,
		Instances: []providercfg.InstanceConfig{{
			Name: provider, Type: "google", BaseURL: server.URL, APIKey: "provider-secret",
		}},
	})
	if err != nil {
		t.Fatalf("NewFromProviders: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "api.jsonl")
	logger, err := llm.NewAPILogger(logPath)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	client.Use(logger)

	_, callErr := client.CountInputTokens(context.Background(), llm.Request{
		Provider: provider, Model: "test-model", Messages: []llm.Message{llm.User("count this")},
	})
	if callErr == nil {
		t.Fatal("CountInputTokens error = nil")
	}
	var providerErr llm.Error
	if !errors.As(callErr, &providerErr) {
		t.Fatalf("CountInputTokens error type = %T, want llm.Error", callErr)
	}
	if providerErr.Provider() != provider || providerErr.BehaviorTag() != "google" || llm.Kind(callErr) != llm.KindAccessDenied {
		t.Fatalf("provider error = provider %q, tag %q, kind %q", providerErr.Provider(), providerErr.BehaviorTag(), llm.Kind(callErr))
	}

	records := readAPILogRecords(t, logPath)
	if len(records) != 2 {
		t.Fatalf("canonical records = %d, want one attempt and one settlement", len(records))
	}
	attempt := records[0].(apilog.APIAttemptRecord)
	settlement := records[1].(apilog.APIAttemptGroupSettlement)
	if attempt.Outcome != apilog.AttemptProviderReject || settlement.Outcome != apilog.AttemptProviderReject {
		t.Fatalf("attempt/settlement outcomes = %q/%q, want provider_rejection/provider_rejection", attempt.Outcome, settlement.Outcome)
	}
	if attempt.ErrorClass != llm.KindAccessDenied.String() {
		t.Fatalf("attempt error class = %q, want %q", attempt.ErrorClass, llm.KindAccessDenied)
	}
}

func countInputTokens(ctx context.Context, client *llm.Client, provider string) error {
	_, err := client.CountInputTokens(ctx, llm.Request{
		Provider: provider, Model: "test-model", Messages: []llm.Message{llm.User("count this")},
	})
	return err
}

func readAPILogRecords(t *testing.T, path string) []apilog.APILogRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open(API log): %v", err)
	}
	defer func() { _ = file.Close() }()

	decoder := apilog.NewDecoder(file, 4<<20)
	var records []apilog.APILogRecord
	for {
		record, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			return records
		}
		if err != nil {
			t.Fatalf("decode API log: %v", err)
		}
		records = append(records, record)
	}
}

type localOnlyAdapter struct{}

func (*localOnlyAdapter) Name() string { return "local-only" }
func (*localOnlyAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}
func (*localOnlyAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) { return nil, nil }

type unsupportedCountAdapter struct{ localOnlyAdapter }

func (*unsupportedCountAdapter) Name() string { return "unsupported-count" }
func (*unsupportedCountAdapter) CountInputTokens(context.Context, llm.Request) (llm.InputTokenCount, error) {
	return llm.InputTokenCount{}, llm.ErrInputTokenCountUnsupported
}
