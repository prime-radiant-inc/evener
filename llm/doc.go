// Package llm is a provider-agnostic client for large language models.
//
// A single [Client] talks to any configured provider — OpenAI, Anthropic,
// Google, and OpenAI-compatible backends — through one uniform request/response
// model, so caller code is written once and runs against any of them. The wire
// model is the same everywhere: a [Request] carries a slice of [Message] values,
// each a sequence of [ContentPart] values, and the provider returns a [Response]
// with content, tool calls, and token [Usage].
//
// # Constructing a client
//
// Use [NewClient] with the provider registry the process loaded, which resolves
// an instance/model reference against the user's configuration, the curated
// overlay, and the embedded catalog:
//
//	r, err := registry.Load()
//	if err != nil {
//		return err
//	}
//	client := llm.NewClient(llm.WithRegistry(r))
//
// A client built without [WithRegistry] still resolves — against
// [EmbeddedRegistry], a hermetic snapshot with no user layer and no
// environment — so library callers need no configuration to shape a request.
//
// # Generating
//
// [Generate] runs a complete request — including any tool-call rounds — and
// returns the final [GenerateResult]. Build messages with [System], [Developer],
// [User], [Assistant], and [ToolResult]; describe callable tools with [Tool] and
// shape the output with [ResponseFormat] and [ToolChoice]:
//
//	res, err := llm.Generate(ctx, llm.GenerateOptions{
//		Client:   client,
//		Model:    "gpt-5.2",
//		Messages: []llm.Message{llm.User("Say hello.")},
//	})
//
// [GenerateObject] additionally constrains and parses the output into a JSON
// object against a schema. For incremental output, [Client.Stream] returns a
// [Stream] of [StreamEvent] values — text and reasoning deltas, tool calls,
// per-step usage, and a terminal FINISH or ERROR event.
//
// # Errors
//
// Provider failures are normalized to the [Error] interface, which exposes the
// provider, HTTP status, retryability, and any Retry-After. The concrete error
// types are unexported; branch on a failure with one of two orthogonal axes
// rather than type-asserting:
//
//   - [Classify] returns the retry disposition ([ErrorClassRetryable] or
//     [ErrorClassPermanent]).
//   - [Kind] returns the category ([ErrorKind]: [KindRateLimit],
//     [KindContextLength], [KindContentFilter], …). A 429 and a 503 share a
//     Class (retryable) but differ in Kind.
//
// Match cancellation and timeout with errors.Is: the taxonomy populates its
// Unwrap chain, so errors.Is(err, context.Canceled) and errors.Is(err,
// context.DeadlineExceeded) hold for user-cancelled and timed-out requests.
//
// # Protocol and adapter SPI
//
// Wire protocols are pluggable. A protocol package implements [Protocol] and
// registers itself with [RegisterProtocol]; the registry record behind an
// instance names which one serves it. A caller may also register a
// [ProviderAdapter] override with [Client.Register], which owns its instance
// name outright. The streaming and transport primitives either needs —
// [ChanStream], [ParseSSE], [Retry], and the AdapterTimeout helpers — are
// exported for that purpose. Application code uses the caller API above and
// can ignore this surface.
//
// # Concurrency
//
// Register all providers on a [Client] during setup; afterwards it issues
// requests without mutating its own state, so a fully-built client may be shared
// across goroutines. Each [Stream] is owned by a single consumer goroutine:
// range over [Stream.Events] and call [Stream.Close] when finished.
package llm
