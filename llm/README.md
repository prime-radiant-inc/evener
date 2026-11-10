# `llm` — provider-agnostic LLM client

This package is the LLM core: a `Client` that resolves an `instance/model`
reference against the provider registry and runs the request over whichever
wire protocol that instance speaks.

**Start with the architecture doc, not the code:**

- [`docs/llm-providers.md`](../docs/llm-providers.md) — the five nouns
  (protocol, transport, provider, model, surface), how the layers merge, and
  the request lifecycle.
- [`docs/llm-provider-config-and-launch.md`](../docs/llm-provider-config-and-launch.md)
  — credentials store, the provider env-var reference, OpenAI OAuth, and how
  the hub spawns `evener serve` / `evener launch-check`.

## The pieces

**`llm/registry`** merges three layers — the models.dev catalog, the curated
overlay, and the user's `providers.toml` — into one `registry.Resolved` per
`instance/model` reference (`registry.Load`, then `(*Registry).Resolve`).
`Resolved` carries the transport, protocol id, credential, and the `Caps`
merged across every layer. Everything downstream reads `Resolved`: the
request builders, the CLI, and the agent's `agent/provider.Profile`.

**Protocol packages** implement the four wire formats on top of the HTTP
plumbing shared in `llm/providers/internal/protocolhttp`:

| Package | Protocol id |
|---|---|
| `llm/providers/chatcompletions` | `openai-chat` |
| `llm/providers/responses` | `openai-responses` |
| `llm/providers/anthropic` | `anthropic` |
| `llm/providers/google` | `google` |

Each registers itself at init with `RegisterProtocol`, and auth schemes
register separately with `RegisterAuthenticator`. `llm/providers/all` is the
single blank import that pulls in the whole set plus the `tokenauth`
authenticators (`gcp-adc`, `oauth-openai-codex`).

**Dispatch** normalizes `req.Provider` to an instance name and resolves it.
An adapter registered under that name with `Client.Register` serves the
request in place of the protocol; the registry record shapes the request
either way. A client built without `WithRegistry` still resolves — against
`EmbeddedRegistry`, a hermetic snapshot with no user layer and no
environment.

**`ShapeRequest`** is the one place request-level shaping happens: clear the
reasoning controls a row disables, clamp effort to the row's ladder, apply
`MaxOutputTokens` when the request has none, drop sampling parameters the row
won't take, gate the prompt-cache fields.

**Continuation planning** (`Client.PlanResponsesContinuation`) decides whether
a Responses request may continue from a stored `previous_response_id`, and
under which storage scope, so a session falls back to full history when it
may not.

Routing keys on the instance name, never on a model-string prefix or a
base-URL guess — a gateway in front of the vendor makes URL sniffing wrong.

## Streaming and retry commitment

`StreamGenerate` retries a stream attempt only while it has not committed
content: an open failure, a 429/5xx, or a stream that truncated before its
finish event. A nonempty text or reasoning delta commits the attempt, as do
nonempty tool arguments on a start, delta or end event, a nonnil object delta,
and a content-bearing passthrough (below). An empty text delta and a
control-only frame publish nothing, so they keep the attempt retryable.

Nonempty text deltas commit an attempt. A `PROVIDER_EVENT` passthrough commits one only
when the adapter sets `StreamEvent.ContentBearing`, because the adapter is the
layer that knows which raw frame carries model output rather than lifecycle or
control traffic. The criterion is "a retry would republish it": if the caller
has observed the frame's content, the attempt is committed. The Responses
decoder classifies the frames it knows carry content (`response.refusal.*`,
audio and transcript deltas, unmappable tool-call fragments) and leaves
lifecycle frames unclassified, so a control-only stream that truncates still
retries. The guarantee is scoped to the adapter: a custom adapter that emits
its own `Raw` payloads must set the flag to get the same no-duplication
behavior (#4941).
