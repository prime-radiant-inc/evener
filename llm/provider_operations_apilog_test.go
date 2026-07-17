package llm_test

import (
	"bytes"
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

type providerOperationCase struct {
	name         string
	providerType providercfg.Type
	apiStyle     providercfg.APIStyle
}

var modelListProviderOperations = []providerOperationCase{
	{name: "openai", providerType: "openai", apiStyle: providercfg.StyleResponses},
	{name: "anthropic", providerType: "anthropic"},
	{name: "google", providerType: "google"},
	{name: "openai compatible", providerType: "openai", apiStyle: providercfg.StyleChatCompletions},
}

var tokenCountProviderOperations = []providerOperationCase{
	{name: "openai", providerType: "openai", apiStyle: providercfg.StyleResponses},
	{name: "anthropic", providerType: "anthropic"},
	{name: "google", providerType: "google"},
	{name: "kimi", providerType: "kimi"},
}

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
				ctx = llm.WithAPILogContext(ctx, sessionID)
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
			if !bytes.Equal(requestBody, wireRequestBody) {
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

func TestClientModelListDecodeFailurePreservesObservedResponse(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	responseBody := append([]byte("!malformed-model-list\n"), bytes.Repeat([]byte("unread-tail-"), 1024)...)

	for _, test := range modelListProviderOperations {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(responseBody)
			}))
			t.Cleanup(server.Close)

			client, provider := newProviderOperationClient(t, test, server.URL)
			logger, logPath := attachProviderOperationLogger(t, client)
			defer func() { _ = logger.Close() }()

			if _, err := client.ListModels(context.Background(), provider); err == nil {
				t.Fatal("ListModels error = nil")
			}

			attempt, settlement := oneProviderOperationAttempt(t, logPath)
			loggedBody, err := apilog.DecodeBody(attempt.Response.Body)
			if err != nil {
				t.Fatalf("DecodeBody(response): %v", err)
			}
			if len(loggedBody) == 0 || len(loggedBody) >= len(responseBody) || !bytes.Equal(loggedBody, responseBody[:len(loggedBody)]) {
				t.Fatalf("logged response bytes = %d, want the observed prefix of %d wire bytes", len(loggedBody), len(responseBody))
			}
			if attempt.Response.Body.Exact {
				t.Fatal("partially observed response body marked exact")
			}
			if attempt.Outcome != apilog.AttemptDecodeFail || settlement.Outcome != apilog.AttemptDecodeFail {
				t.Fatalf("attempt/settlement outcomes = %q/%q, want response_decoding_failure", attempt.Outcome, settlement.Outcome)
			}
		})
	}
}

func TestClientTokenCountDecodeConditionsPreserveResultsAndForensicFailures(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	responses := []struct {
		name          string
		body          []byte
		truncateAfter bool
		wantTokens    int
	}{
		{name: "malformed JSON", body: []byte("not-json")},
		{name: "response read error", body: []byte(`{"input_tokens":17,"totalTokens":17,"data":{"total_tokens":17}}`), truncateAfter: true, wantTokens: 17},
	}

	for _, providerCase := range tokenCountProviderOperations {
		for _, responseCase := range responses {
			t.Run(providerCase.name+"/"+responseCase.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if responseCase.truncateAfter {
						w.Header().Set("Content-Length", "4096")
					}
					_, _ = w.Write(responseCase.body)
				}))
				t.Cleanup(server.Close)

				client, provider := newProviderOperationClient(t, providerCase, server.URL)
				logger, logPath := attachProviderOperationLogger(t, client)
				defer func() { _ = logger.Close() }()

				count, callErr := client.CountInputTokens(context.Background(), llm.Request{
					Provider: provider, Model: "test-model", Messages: []llm.Message{llm.User("count this")},
				})
				if callErr != nil {
					t.Fatalf("CountInputTokens returned merge-base-compatible response condition: %v", callErr)
				}
				if count.Tokens != responseCase.wantTokens || !count.Exact || count.Source != llm.TokenCountSourceProvider {
					t.Fatalf("CountInputTokens = %+v, want %d exact provider tokens", count, responseCase.wantTokens)
				}

				attempt, settlement := oneProviderOperationAttempt(t, logPath)
				loggedBody, err := apilog.DecodeBody(attempt.Response.Body)
				if err != nil {
					t.Fatalf("DecodeBody(response): %v", err)
				}
				if !bytes.Equal(loggedBody, responseCase.body) {
					t.Fatalf("logged response body = %q, want exact partial wire body %q", loggedBody, responseCase.body)
				}
				if attempt.Response.Body.Exact == responseCase.truncateAfter {
					t.Fatalf("logged response exact = %t, truncateAfter = %t", attempt.Response.Body.Exact, responseCase.truncateAfter)
				}
				if attempt.Outcome != apilog.AttemptDecodeFail || attempt.ErrorMessage == "" || settlement.Outcome != apilog.AttemptSuccess {
					t.Fatalf("attempt/settlement = %+v/%+v, want observed decode failure and successful public result", attempt, settlement)
				}
			})
		}
	}
}

func TestClientProviderOperationCallerOwnsSettlement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"model"}]}`)
	}))
	t.Cleanup(server.Close)

	client, provider := newProviderOperationClient(t, modelListProviderOperations[3], server.URL)
	logger, logPath := attachProviderOperationLogger(t, client)
	defer func() { _ = logger.Close() }()
	group := llm.NewAPIAttemptGroup("ag_caller_owned_provider_operation")
	ctx := llm.WithAPIAttemptGroup(context.Background(), group)

	if _, err := client.ListModels(ctx, provider); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	records := readAPILogRecords(t, logPath)
	if len(records) != 1 {
		t.Fatalf("records before caller settlement = %d, want one attempt", len(records))
	}
	group.SettleResult(ctx, nil)
	records = readAPILogRecords(t, logPath)
	if len(records) != 2 {
		t.Fatalf("records after caller settlement = %d, want attempt plus one settlement", len(records))
	}
	settlement := records[1].(apilog.APIAttemptGroupSettlement)
	if settlement.AttemptGroupID != group.ID || settlement.FinalAttemptCount != 1 {
		t.Fatalf("settlement = %+v, want caller group %q with one attempt", settlement, group.ID)
	}
}

func TestClientAnthropicModelListPaginationSharesOneSettlement(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Query().Get("after_id") == "first" {
			_, _ = io.WriteString(w, `{"data":[{"id":"second"}],"has_more":false}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"first"}],"has_more":true,"last_id":"first"}`)
	}))
	t.Cleanup(server.Close)

	client, provider := newProviderOperationClient(t, modelListProviderOperations[1], server.URL)
	logger, logPath := attachProviderOperationLogger(t, client)
	defer func() { _ = logger.Close() }()
	models, err := client.ListModels(context.Background(), provider)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if requests != 2 || len(models) != 2 {
		t.Fatalf("requests/models = %d/%d, want 2/2", requests, len(models))
	}
	records := readAPILogRecords(t, logPath)
	if len(records) != 3 {
		t.Fatalf("canonical records = %d, want two attempts and one settlement", len(records))
	}
	first := records[0].(apilog.APIAttemptRecord)
	second := records[1].(apilog.APIAttemptRecord)
	settlement := records[2].(apilog.APIAttemptGroupSettlement)
	if first.AttemptGroupID != second.AttemptGroupID || second.AttemptIndex != 2 || settlement.FinalAttemptID != second.AttemptID || settlement.FinalAttemptCount != 2 {
		t.Fatalf("attempts/settlement = %+v / %+v / %+v", first, second, settlement)
	}
}

func TestClientModelListRedirectRecordsEachHTTPAttempt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/models" {
			http.Redirect(w, request, "/redirected-models", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"model"}]}`)
	}))
	t.Cleanup(server.Close)

	client, provider := newProviderOperationClient(t, modelListProviderOperations[3], server.URL)
	logger, logPath := attachProviderOperationLogger(t, client)
	defer func() { _ = logger.Close() }()
	if _, err := client.ListModels(context.Background(), provider); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	records := readAPILogRecords(t, logPath)
	if len(records) != 3 {
		t.Fatalf("canonical records = %d, want redirect attempt, final attempt, and settlement", len(records))
	}
	first := records[0].(apilog.APIAttemptRecord)
	second := records[1].(apilog.APIAttemptRecord)
	settlement := records[2].(apilog.APIAttemptGroupSettlement)
	if !strings.HasSuffix(first.Request.Endpoint, "/models") || !strings.HasSuffix(second.Request.Endpoint, "/redirected-models") {
		t.Fatalf("redirect endpoints = %q then %q", first.Request.Endpoint, second.Request.Endpoint)
	}
	if settlement.FinalAttemptID != second.AttemptID || settlement.FinalAttemptCount != 2 {
		t.Fatalf("settlement = %+v, want redirected call to settle after two attempts", settlement)
	}
}

func newProviderOperationClient(t *testing.T, test providerOperationCase, baseURL string) (*llm.Client, string) {
	t.Helper()
	provider := "provider-operation-" + strings.ReplaceAll(test.name, " ", "-")
	client, err := llm.NewFromProviders(providercfg.Config{
		Default: provider,
		Instances: []providercfg.InstanceConfig{{
			Name: provider, Type: test.providerType, APIStyle: test.apiStyle,
			BaseURL: baseURL, APIKey: "provider-secret",
		}},
	})
	if err != nil {
		t.Fatalf("NewFromProviders: %v", err)
	}
	return client, provider
}

func attachProviderOperationLogger(t *testing.T, client *llm.Client) (*llm.APILogger, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "api.jsonl")
	logger, err := llm.NewAPILogger(logPath)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	client.Use(logger)
	return logger, logPath
}

func oneProviderOperationAttempt(t *testing.T, logPath string) (apilog.APIAttemptRecord, apilog.APIAttemptGroupSettlement) {
	t.Helper()
	records := readAPILogRecords(t, logPath)
	if len(records) != 2 {
		t.Fatalf("canonical records = %d, want one attempt and one settlement", len(records))
	}
	attempt, ok := records[0].(apilog.APIAttemptRecord)
	if !ok {
		t.Fatalf("first canonical record = %T, want APIAttemptRecord", records[0])
	}
	if attempt.Response == nil {
		t.Fatal("canonical attempt response is nil")
	}
	settlement, ok := records[1].(apilog.APIAttemptGroupSettlement)
	if !ok {
		t.Fatalf("second canonical record = %T, want APIAttemptGroupSettlement", records[1])
	}
	return attempt, settlement
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
