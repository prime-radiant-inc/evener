# `llm` — provider-agnostic LLM client

This package is the LLM core: a small `Client` that routes a `Request` to a
registered `ProviderAdapter`, the adapters for each vendor (`llm/providers/*`),
and the env-driven registry that wires them up (`env_registry.go`).

**Start with the architecture doc, not the code:**

- [`docs/llm-providers.md`](../docs/llm-providers.md) — the request lifecycle
  (model string → profile → `req.Provider` → `client.providers[name]` → adapter),
  the **provider-name == `profile.ID()` == adapter `Name()` == type** invariant
  and where it's load-bearing, the profile layer (`agent/profile.go`), and the
  per-vendor wire protocols (Responses vs Chat Completions vs Messages).
- [`docs/llm-provider-config-and-launch.md`](../docs/llm-provider-config-and-launch.md)
  — credentials store, the provider env-var reference, OpenAI OAuth, and how the
  hub spawns `serf serve` / `serf launch-check`.

Routing keys on `req.Provider` (= the profile id), not on a model-string prefix.
Adapter names are hardcoded constants; the default provider is the first
registered non-`NonDefaultEligible` adapter.
