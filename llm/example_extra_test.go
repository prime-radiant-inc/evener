package llm_test

import (
	"context"
	"errors"
	"fmt"

	"primeradiant.com/serf/llm"
)

// jsonAdapter is a minimal [llm.ProviderAdapter] that always replies with a
// fixed JSON object, so the GenerateObject example can run without a network
// call. Real adapters live under llm/providers.
type jsonAdapter struct{}

func (jsonAdapter) Name() string { return "json" }

func (jsonAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{
		Provider: "json",
		Model:    req.Model,
		Message:  llm.Assistant(`{"city":"Paris","population":2102650}`),
		Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
	}, nil
}

func (jsonAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// GenerateObject constrains the model to a JSON schema and parses the validated
// result into GenerateResult.Output.
func ExampleGenerateObject() {
	client := llm.NewClient()
	client.Register(jsonAdapter{})

	res, err := llm.GenerateObject(context.Background(), llm.GenerateObjectOptions{
		GenerateOptions: llm.GenerateOptions{
			Client:   client,
			Provider: "json",
			Model:    "json-1",
			Messages: []llm.Message{llm.User("Describe a city as JSON.")},
		},
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city":       map[string]any{"type": "string"},
				"population": map[string]any{"type": "integer"},
			},
			"required": []any{"city", "population"},
		},
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	obj := res.Output.(map[string]any)
	fmt.Println(obj["city"])
	// Output: Paris
}

// Kind reports the category of a provider error — the axis orthogonal to
// Classify's retry disposition. Here a 429 and a 503 share the retryable class
// but differ in kind.
func ExampleKind() {
	rateLimited := llm.ErrorFromHTTPStatus("openai", 429, "slow down", nil, nil)
	serverDown := llm.ErrorFromHTTPStatus("openai", 503, "unavailable", nil, nil)

	fmt.Println(llm.Kind(rateLimited), llm.Classify(rateLimited))
	fmt.Println(llm.Kind(serverDown), llm.Classify(serverDown))
	// Output:
	// rate_limit retryable
	// server retryable
}

// Error values from the package normalize provider failures behind the Error
// interface; match cancellation and deadlines with errors.Is.
func ExampleError() {
	err := llm.NewAbortError("user cancelled", context.Canceled)

	var e llm.Error
	if errors.As(err, &e) {
		fmt.Println("retryable:", e.Retryable())
	}
	fmt.Println("is canceled:", errors.Is(err, context.Canceled))
	// Output:
	// retryable: false
	// is canceled: true
}
