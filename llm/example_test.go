package llm_test

import (
	"context"
	"fmt"

	"primeradiant.com/serf/llm"
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
	return nil, llm.ErrStreamUnsupported
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
