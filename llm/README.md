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

## High-level streaming retries

`StreamGenerate` retries a transient stream failure only if no nonempty content
has been forwarded to its `StreamResult.Events()` consumer. Text, reasoning,
object deltas, and tool-call arguments (whether delivered on start, delta, or
end) commit the attempt: a later truncation produces a terminal stream error
instead of replaying content into the same event channel. Empty deltas and
control/start events without content do not prevent retry. This is the
high-level library path; callers using `RetryStream` directly can explicitly
opt into retry-after-partial with a reset callback.

## Completed streaming tool rounds

After `StreamGenerate` completes, `StreamResult.Steps()` and `TotalUsage()`
retain every completed tool round even if a later stream open fails, a later
stream truncates, or the caller cancels. The tool results remain in their
completed steps and their usage counts toward the total. `Response()` still
returns the terminal error with no successful final response for an errored
turn; an unfinished later response does not add a step or usage. Successful
final completions and `StopWhen` retain their usual accounting.
