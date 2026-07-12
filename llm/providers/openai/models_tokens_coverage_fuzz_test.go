//go:build serffuzz

package openai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

type modelsTokensCoverageTransport func(*http.Request) (*http.Response, error)

func (fn modelsTokensCoverageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func modelsTokensCoverageResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func FuzzOpenAIModelsTokensCoverage(f *testing.F) {
	for scenario := byte(0); scenario < 11; scenario++ {
		f.Add(scenario)
	}

	f.Fuzz(func(t *testing.T, scenario byte) {
		switch scenario % 11 {
		case 0:
			old := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = old })
			http.DefaultTransport = modelsTokensCoverageTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("method = %s, want GET", req.Method)
				}
				return modelsTokensCoverageResponse(http.StatusOK, `{"data":[]}`), nil
			})
			a := &Adapter{APIKey: "key", BaseURL: "https://models.invalid"}
			if _, err := a.ListModels(context.Background()); err != nil {
				t.Fatalf("ListModels with default client: %v", err)
			}
			if a.Client == nil {
				t.Fatal("ListModels did not initialize Client")
			}

		case 1:
			a := &Adapter{BaseURL: ":", Client: &http.Client{Transport: modelsTokensCoverageTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("malformed URL reached transport")
				return nil, nil
			})}}
			if _, err := a.ListModels(context.Background()); err == nil {
				t.Fatal("ListModels accepted malformed URL")
			}

		case 2:
			want := errors.New("models transport failed")
			a := &Adapter{BaseURL: "https://models.invalid", Client: &http.Client{Transport: modelsTokensCoverageTransport(func(*http.Request) (*http.Response, error) {
				return nil, want
			})}}
			if _, err := a.ListModels(context.Background()); !errors.Is(err, want) {
				t.Fatalf("ListModels error = %v, want %v", err, want)
			}

		case 3:
			levels := codexReasoningEfforts([]codexReasoningLevel{{Effort: " low "}, {Effort: ""}, {Effort: "low"}, {Effort: "high"}})
			if len(levels) != 2 || levels[0] != "low" || levels[1] != "high" {
				t.Fatalf("reasoning efforts = %v", levels)
			}
			if codexHasTool([]string{"  File_Search  "}, " SEARCH ") != true {
				t.Fatal("codexHasTool missed normalized substring")
			}
			if codexHasTool([]string{"computer"}, "search") {
				t.Fatal("codexHasTool reported absent tool")
			}
			info := (codexModelListEntry{
				Slug:               "gpt-covered",
				MaxOutputTokens:    8192,
				SupportsSearchTool: true,
			}).modelInfo()
			if info.MaxOutputTokens == nil || *info.MaxOutputTokens != 8192 {
				t.Fatalf("MaxOutputTokens = %v", info.MaxOutputTokens)
			}
			if info.SupportsWebSearch == nil || !*info.SupportsWebSearch {
				t.Fatalf("SupportsWebSearch = %v", info.SupportsWebSearch)
			}

		case 4:
			a := &Adapter{ResponsesPath: defaultCodexResponses}
			if _, err := a.CountInputTokens(context.Background(), llm.Request{}); !errors.Is(err, llm.ErrInputTokenCountUnsupported) {
				t.Fatalf("Codex CountInputTokens error = %v", err)
			}

		case 5:
			old := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = old })
			http.DefaultTransport = modelsTokensCoverageTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost {
					t.Fatalf("method = %s, want POST", req.Method)
				}
				return modelsTokensCoverageResponse(http.StatusOK, `{"input_tokens":12}`), nil
			})
			a := &Adapter{APIKey: "key", BaseURL: "https://tokens.invalid"}
			got, err := a.CountInputTokens(context.Background(), llm.Request{Model: "gpt-test"})
			if err != nil || got.Tokens != 12 {
				t.Fatalf("CountInputTokens = %+v, %v", got, err)
			}
			if a.Client == nil {
				t.Fatal("CountInputTokens did not initialize Client")
			}

		case 6:
			choice := llm.ToolChoice{Mode: "named"}
			a := &Adapter{}
			if _, err := a.CountInputTokens(context.Background(), llm.Request{ToolChoice: &choice}); err == nil {
				t.Fatal("CountInputTokens accepted invalid tool choice")
			}
			if _, err := a.buildInputTokenCountBody(llm.Request{ToolChoice: &choice}); err == nil {
				t.Fatal("buildInputTokenCountBody accepted invalid tool choice")
			}

		case 7:
			a := &Adapter{BaseURL: "https://tokens.invalid", Client: &http.Client{Transport: modelsTokensCoverageTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("unmarshalable body reached transport")
				return nil, nil
			})}}
			req := llm.Request{ProviderOptions: map[string]any{"openai": map[string]any{"bad": func() {}}}}
			if _, err := a.CountInputTokens(context.Background(), req); err == nil {
				t.Fatal("CountInputTokens marshaled a function")
			}

		case 8:
			a := &Adapter{BaseURL: ":", Client: &http.Client{Transport: modelsTokensCoverageTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("malformed URL reached transport")
				return nil, nil
			})}}
			if _, err := a.CountInputTokens(context.Background(), llm.Request{}); err == nil {
				t.Fatal("CountInputTokens accepted malformed URL")
			}

		case 9:
			want := errors.New("token transport failed")
			a := &Adapter{BaseURL: "https://tokens.invalid", Client: &http.Client{Transport: modelsTokensCoverageTransport(func(*http.Request) (*http.Response, error) {
				return nil, want
			})}}
			if _, err := a.CountInputTokens(context.Background(), llm.Request{}); err == nil || !strings.Contains(err.Error(), want.Error()) {
				t.Fatalf("CountInputTokens error = %v, want wrapped %v", err, want)
			}

		case 10:
			a := &Adapter{BaseURL: "https://tokens.invalid", Client: &http.Client{Transport: modelsTokensCoverageTransport(func(*http.Request) (*http.Response, error) {
				resp := modelsTokensCoverageResponse(http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`)
				resp.Header.Set("Retry-After", "1")
				return resp, nil
			})}}
			if _, err := a.CountInputTokens(context.Background(), llm.Request{}); err == nil || !strings.Contains(err.Error(), "slow down") {
				t.Fatalf("CountInputTokens HTTP error = %v", err)
			}
		}
	})
}
