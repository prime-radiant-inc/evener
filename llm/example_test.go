package llm_test

import (
	"context"
	"errors"
	"fmt"

	"primeradiant.com/evener/llm"
)

// exampleAdapter is a minimal [llm.ProviderAdapter] used by the examples: it
// returns a fixed assistant reply with no network call. Real adapters live under
// llm/providers and register themselves via the SPI (see the package overview).
type exampleAdapter struct{}

func (exampleAdapter) Name() string { return "example" }

func (exampleAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{Provider: "example", Model: req.Model, Message: llm.Assistant("Hello from the model.")}, nil
}

func (exampleAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	st := llm.NewChanStream(func() {})
	go func() {
		defer st.CloseSend()
		st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
		for _, chunk := range []string{"Hello", " from", " the", " model."} {
			st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: chunk})
		}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish})
	}()
	return st, nil
}

// Generate runs a complete request against a registered provider and returns the
// final assistant text.
func ExampleGenerate() {
	client := llm.NewClient()
	client.Register(exampleAdapter{})

	res, err := llm.Generate(context.Background(), llm.GenerateOptions{
		Client:   client,
		Provider: "example",
		Model:    "example-1",
		Messages: []llm.Message{llm.User("Say hello.")},
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(res.Text)
	// Output: Hello from the model.
}

// Classify maps a provider error to a retry class — preferred over
// type-asserting concrete error types.
func ExampleClassify() {
	err := llm.ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)
	fmt.Println(llm.Classify(err))
	// Output: retryable
}

// Client.Stream returns a Stream of incremental events; range over its Events
// channel for the deltas and Close it when done.
func ExampleClient_Stream() {
	client := llm.NewClient()
	client.Register(exampleAdapter{})

	st, err := client.Stream(context.Background(), llm.Request{
		Provider: "example",
		Model:    "example-1",
		Messages: []llm.Message{llm.User("Say hello.")},
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer st.Close() //nolint:errcheck

	for ev := range st.Events() {
		if ev.Type == llm.StreamEventTextDelta {
			fmt.Print(ev.Delta)
		}
	}
	fmt.Println()
	// Output: Hello from the model.
}

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
		Client:   client,
		Provider: "json",
		Model:    "json-1",
		Messages: []llm.Message{llm.User("Describe a city as JSON.")},
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

	if e, ok := errors.AsType[llm.Error](err); ok {
		fmt.Println("retryable:", e.Retryable())
	}
	fmt.Println("is canceled:", errors.Is(err, context.Canceled))
	// Output:
	// retryable: false
	// is canceled: true
}
