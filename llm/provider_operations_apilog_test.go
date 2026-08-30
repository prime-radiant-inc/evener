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

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/registry"
)

// providerOperationCase names one instance the provider-operation tests
// drive: the curated provider it inherits from and the model its counting
// calls name.
type providerOperationCase struct {
	name  string
	base  string
	model string
}

var modelListProviderOperations = []providerOperationCase{
	{name: "openai", base: "openai", model: "gpt-5.5"},
	{name: "anthropic", base: "anthropic", model: "claude-sonnet-4-5"},
	{name: "google", base: "google", model: "gemini-2.5-flash"},
}

var tokenCountProviderOperations = modelListProviderOperations

func TestClientProviderOperationsWriteCanonicalAPILog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	tests := []struct {
		name         string
		instance     providerOperationCase
		pathSuffix   string
		responseBody string
		requestModel string
		call         func(context.Context, *llm.Client, string, string) error
	}{
		{
			name: "openai model list", instance: modelListProviderOperations[0],
			pathSuffix: "/models", responseBody: `{"data":[{"id":"gpt-test"}]}`, requestModel: "*",
			call: listModels,
		},
		{
			name: "anthropic model list", instance: modelListProviderOperations[1],
			pathSuffix: "/models", responseBody: `{"data":[{"id":"claude-test"}],"has_more":false}`, requestModel: "*",
			call: listModels,
		},
		{
			name: "google model list", instance: modelListProviderOperations[2],
			pathSuffix: "/models", responseBody: `{"models":[{"name":"models/gemini-test","supportedGenerationMethods":["generateContent"]}]}`, requestModel: "*",
			call: listModels,
		},
		{
			name: "openai input token count", instance: tokenCountProviderOperations[0],
			pathSuffix: "/responses/input_tokens", responseBody: `{"input_tokens":7}`, requestModel: "gpt-5.5",
			call: countInputTokens,
		},
		{
			name: "anthropic input token count", instance: tokenCountProviderOperations[1],
			pathSuffix: "/messages/count_tokens", responseBody: `{"input_tokens":8}`, requestModel: "claude-sonnet-4-5",
			call: countInputTokens,
		},
		{
			name: "google input token count", instance: tokenCountProviderOperations[2],
			pathSuffix: ":countTokens", responseBody: `{"totalTokens":9}`, requestModel: "gemini-2.5-flash",
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

			client, provider := newProviderOperationClient(t, test.instance, server.URL)
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
			if err := test.call(ctx, client, provider, test.instance.model); err != nil {
				t.Fatalf("provider call: %v", err)
			}

			logPath := filepath.Join(stateDir, "sessions", "session-provider-operation.api.jsonl")
			if sessionID == "" {
				logPath = filepath.Join(stateDir, "sessions", "unattributed.api.jsonl")
			}
			attempt, settlement := oneProviderOperationAttempt(t, logPath)
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
			if strings.Contains(string(persisted), "provider-secret") {
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
	if _, err := client.Models(context.Background(), "local-only"); err == nil {
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

	client, provider := newProviderOperationClient(t, tokenCountProviderOperations[2], server.URL)
	logger, logPath := attachProviderOperationLogger(t, client)
	defer func() { _ = logger.Close() }()

	_, callErr := client.CountInputTokens(context.Background(), llm.Request{
		Provider: provider, Model: "gemini-2.5-flash", Messages: []llm.Message{llm.User("count this")},
	})
	if callErr == nil {
		t.Fatal("CountInputTokens error = nil")
	}
	var providerErr llm.Error
	if !errors.As(callErr, &providerErr) {
		t.Fatalf("CountInputTokens error type = %T, want llm.Error", callErr)
	}
	if providerErr.Provider() != provider || llm.Kind(callErr) != llm.KindAccessDenied {
		t.Fatalf("provider error = provider %q, kind %q", providerErr.Provider(), llm.Kind(callErr))
	}

	attempt, settlement := oneProviderOperationAttempt(t, logPath)
	if attempt.Outcome != apilog.AttemptProviderReject || settlement.Outcome != apilog.AttemptProviderReject {
		t.Fatalf("attempt/settlement outcomes = %q/%q, want provider_rejection/provider_rejection", attempt.Outcome, settlement.Outcome)
	}
	if attempt.ErrorClass != llm.KindAccessDenied.String() {
		t.Fatalf("attempt error class = %q, want %q", attempt.ErrorClass, llm.KindAccessDenied)
	}
}

// TestClientModelListDecodeFailurePreservesObservedResponse pins what a
// listing whose body will not decode leaves behind: the bytes the exchange
// observed, recorded verbatim, and a decode failure on both the attempt and
// the settlement. The protocol reads the whole body before decoding it, so
// "observed" here is the entire response.
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

			if _, err := client.Models(context.Background(), provider); err == nil {
				t.Fatal("Models error = nil")
			}

			attempt, settlement := oneProviderOperationAttempt(t, logPath)
			loggedBody, err := apilog.DecodeBody(attempt.Response.Body)
			if err != nil {
				t.Fatalf("DecodeBody(response): %v", err)
			}
			if !bytes.Equal(loggedBody, responseBody) {
				t.Fatalf("logged response bytes = %d, want the %d observed wire bytes", len(loggedBody), len(responseBody))
			}
			if !attempt.Response.Body.Exact {
				t.Fatal("a fully observed response body must be marked exact")
			}
			if attempt.Outcome != apilog.AttemptDecodeFail || settlement.Outcome != apilog.AttemptDecodeFail {
				t.Fatalf("attempt/settlement outcomes = %q/%q, want response_decoding_failure", attempt.Outcome, settlement.Outcome)
			}
		})
	}
}

// TestClientTokenCountDecodeConditionsPreserveResultsAndForensicFailures pins
// the two verdicts a token count can reach when the body does not decode
// cleanly. A truncated read splits: the caller still gets the count the body
// carried, the attempt records the read failure, and the public settlement
// stays successful. A body that never decodes has no count to hand back, so
// the call is an error — the protocols report the gap rather than returning
// the zero the adapters used to call exact.
func TestClientTokenCountDecodeConditionsPreserveResultsAndForensicFailures(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	responses := []struct {
		name string
		body []byte
		// truncate promises more bytes than are written, so the read fails
		// after the whole body has been observed.
		truncate   bool
		wantTokens int
		wantErr    bool
	}{
		{name: "read failure after complete JSON", body: []byte(`{"input_tokens":17,"totalTokens":17}`), truncate: true, wantTokens: 17},
		{name: "body never decodes", body: []byte("not-json"), wantErr: true},
	}

	for _, providerCase := range tokenCountProviderOperations {
		for _, responseCase := range responses {
			t.Run(providerCase.name+"/"+responseCase.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if responseCase.truncate {
						w.Header().Set("Content-Length", "4096")
					}
					_, _ = w.Write(responseCase.body)
				}))
				t.Cleanup(server.Close)

				client, provider := newProviderOperationClient(t, providerCase, server.URL)
				logger, logPath := attachProviderOperationLogger(t, client)
				defer func() { _ = logger.Close() }()

				count, callErr := client.CountInputTokens(context.Background(), llm.Request{
					Provider: provider, Model: providerCase.model, Messages: []llm.Message{llm.User("count this")},
				})
				if responseCase.wantErr {
					if callErr == nil {
						t.Fatalf("CountInputTokens = %+v, want an error for a body that does not decode", count)
					}
					if count.Exact || count.Tokens != 0 {
						t.Fatalf("CountInputTokens = %+v, want no count beside the error", count)
					}
				} else {
					if callErr != nil {
						t.Fatalf("CountInputTokens returned an error for an observed body: %v", callErr)
					}
					if count.Tokens != responseCase.wantTokens || !count.Exact || count.Source != llm.TokenCountSourceProvider {
						t.Fatalf("CountInputTokens = %+v, want %d exact provider tokens", count, responseCase.wantTokens)
					}
				}

				attempt, settlement := oneProviderOperationAttempt(t, logPath)
				loggedBody, err := apilog.DecodeBody(attempt.Response.Body)
				if err != nil {
					t.Fatalf("DecodeBody(response): %v", err)
				}
				if !bytes.Equal(loggedBody, responseCase.body) {
					t.Fatalf("logged response body = %q, want the observed wire body %q", loggedBody, responseCase.body)
				}
				if attempt.Response.Body.Exact == responseCase.truncate {
					t.Fatalf("logged body exact = %t for a %s", attempt.Response.Body.Exact, responseCase.name)
				}
				if attempt.Outcome != apilog.AttemptDecodeFail || attempt.ErrorMessage == "" {
					t.Fatalf("attempt = %+v, want an observed decode failure", attempt)
				}
				wantSettlement := apilog.AttemptSuccess
				if responseCase.wantErr {
					wantSettlement = apilog.AttemptDecodeFail
				}
				if settlement.Outcome != wantSettlement {
					t.Fatalf("settlement = %+v, want %q", settlement, wantSettlement)
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

	client, provider := newProviderOperationClient(t, modelListProviderOperations[0], server.URL)
	logger, logPath := attachProviderOperationLogger(t, client)
	defer func() { _ = logger.Close() }()
	group := llm.NewAPIAttemptGroup("ag_caller_owned_provider_operation")
	ctx := llm.WithAPIAttemptGroup(context.Background(), group)

	if _, err := client.Models(ctx, provider); err != nil {
		t.Fatalf("Models: %v", err)
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
	if _, err := client.Models(context.Background(), provider); err != nil {
		t.Fatalf("Models: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
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
		if strings.HasSuffix(request.URL.Path, "/models") {
			http.Redirect(w, request, "/redirected-models", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"model"}]}`)
	}))
	t.Cleanup(server.Close)

	client, provider := newProviderOperationClient(t, modelListProviderOperations[0], server.URL)
	logger, logPath := attachProviderOperationLogger(t, client)
	defer func() { _ = logger.Close() }()
	if _, err := client.Models(context.Background(), provider); err != nil {
		t.Fatalf("Models: %v", err)
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

// newProviderOperationClient builds a client whose registry holds one
// instance of the named curated provider, pointed at baseURL.
func newProviderOperationClient(t *testing.T, test providerOperationCase, baseURL string) (*llm.Client, string) {
	t.Helper()
	provider := "provider-operation-" + test.name
	r := fixtureRegistry(t, baseURL, map[string]registry.Provider{
		provider: {Base: test.base, APIKey: "provider-secret", Transport: registry.Transport{BaseURL: baseURL}},
	})
	return llm.NewClient(llm.WithRegistry(r)), provider
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

func listModels(ctx context.Context, client *llm.Client, provider, _ string) error {
	_, err := client.Models(ctx, provider)
	return err
}

func countInputTokens(ctx context.Context, client *llm.Client, provider, model string) error {
	_, err := client.CountInputTokens(ctx, llm.Request{
		Provider: provider, Model: model, Messages: []llm.Message{llm.User("count this")},
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
