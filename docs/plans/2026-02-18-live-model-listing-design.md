# Live Model Listing Design

## Problem

The serf TUI's `/model` command requires users to type the exact model ID. There's no way to discover which models are available for the current provider. The embedded LiteLLM catalog has model metadata but may be stale and includes models the user's API key can't access.

## Solution

Add live model listing via each provider's API, exposed through the existing optional-interface pattern. The TUI gets an interactive picker when `/model` is invoked without arguments.

## Architecture

### LLM Layer

New optional interface in `llm/client.go`:

```go
type ModelLister interface {
    ListModels(ctx context.Context) ([]ModelInfo, error)
}
```

Follows the same pattern as `Closer`, `Initializer`, and `ToolChoiceSupporter`. The `Client` gets a convenience method:

```go
func (c *Client) ListModels(ctx context.Context, provider string) ([]ModelInfo, error)
```

This type-asserts the adapter and delegates. Returns `ConfigurationError` if the adapter doesn't implement `ModelLister`.

### Per-Provider Implementation

| Provider | Endpoint | Filter |
|----------|----------|--------|
| OpenAI | `GET {base}/v1/models` | Keep models owned by openai/system, exclude embeddings/tts/whisper/dall-e |
| Anthropic | `GET {base}/v1/models` | All returned models (API only returns chat models) |
| Google | `GET {base}/v1beta/models` | Filter to `generateContent` supported methods |
| OpenAI-compat | `GET {base}/v1/models` | Return all (no reliable way to filter) |

Each adapter reuses its existing HTTP client, base URL, and auth headers. Responses are parsed into `[]ModelInfo` with at minimum the `ID` and `Provider` fields populated. Additional fields (context window, capabilities) are populated when the API provides them.

### Server Layer

New endpoint: `GET /models`

- Resolves the current session's provider
- Calls `client.ListModels(ctx, provider)`
- Returns JSON: `{"models": [{"id": "gpt-4o", "display_name": "gpt-4o", ...}]}`
- On error: returns HTTP 502 with error message

### TUI Layer

When `/model` is typed with no arguments:

1. TUI sends `GET {addr}/models` to the server
2. While waiting, shows "Fetching available models..."
3. On success, enters a Bubble Tea `list.Model` picker with:
   - Fuzzy filtering (type to narrow)
   - Current model highlighted/marked
   - Enter to select, Esc to cancel
4. On selection, sends existing `POST /model` request
5. On error, shows fallback message: "Could not fetch models: {error}. Use /model <name> to switch manually."

When `/model <name>` is typed (existing behavior), it works unchanged.

## Error Handling

- Network failures or auth errors from the list endpoint return the error to the TUI, which shows it as a system message and reminds the user of manual `/model <name>` syntax.
- Adapters that don't implement `ModelLister` cause the server to return an appropriate error.

## Non-Goals

- Caching the model list (can add later if latency is a concern)
- Cross-provider model listing (only lists models for the active provider)
- Enriching live results with embedded catalog metadata (can add later)
