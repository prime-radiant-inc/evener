# `llm` — provider-agnostic LLM client

This package is the LLM core: a small `Client` that routes a `Request` to a
registered `ProviderAdapter`, the adapters for each vendor (`llm/providers/*`),
and the env-driven registry that wires them up (`env_registry.go`).

**Start with the architecture doc, not the code:**

- [`docs/llm-providers.md`](../docs/llm-providers.md) — the request lifecycle
  (model string → profile → `req.Provider` → `client.providers[name]` → adapter),
  the **two-identity model** (instance **name** routes/identifies;
  behavior **tag** drives provider-conditional behavior) introduced in the
  PRI-1880 Phase 1a refactor, the profile layer (`agent/profile.go`), and the
  per-vendor wire protocols (Responses vs Chat Completions vs Messages).
- [`docs/llm-provider-config-and-launch.md`](../docs/llm-provider-config-and-launch.md)
  — credentials store, the provider env-var reference, OpenAI OAuth, and how the
  hub spawns `serf serve` / `serf launch-check`.

Routing keys on `req.Provider` (= the profile id / instance name), not on a
model-string prefix. The behavior tag (`profile.BehaviorTag()`, defined by
`internal/providerconfig`) is what provider-conditional behavior branches on, and
the `Client` stamps the instance name + tag onto responses and errors centrally.
The default provider is the first registered non-`NonDefaultEligible` adapter.
