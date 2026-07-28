# Kimi K3 Context Window Design

## Problem

Sessions using a configured `kimi-anthropic` instance and wire model `k3`
start with a 262,144-token context window. Kimi documents `k3` as a
1,048,576-token model. The provider's Anthropic-compatible `/v1/models`
response supplies model IDs and display names but no context-window field, so
Serf's live model enrichment cannot correct the provider-wide 262,144 default.

The vendored LiteLLM catalog does not currently contain `k3`; running
`scripts/refresh-model-catalog.sh --check` therefore cannot supply the missing
metadata.

## Design

Materialize Kimi Code's model-specific context metadata in
`serf_model_catalog_overrides.json`, the existing authority for models absent
from LiteLLM:

- `k3`: 1,048,576
- `k3-256k`: 262,144
- `kimi-for-coding`: 262,144
- `kimi-for-coding-highspeed`: 262,144

Build `kimi-anthropic` profiles from the matching embedded catalog entry when
one exists. Unknown models retain the existing 262,144 fallback. The wire model
ID is unchanged.

When the Web UI receives a live model entry without a context window, enrich
that missing field from the embedded catalog. A positive live context window
remains authoritative and is never overwritten.

## Verification

Deterministic tests must prove:

1. Resolving `kimi-anthropic-api/k3` produces a 1,048,576-token profile.
2. The 256K Kimi model IDs remain 262,144.
3. The embedded catalog carries all four Kimi Code model shapes.
4. Live Web UI model metadata fills a missing K3 context window from the
   catalog but preserves an explicitly advertised live value.

No default test may call Kimi or depend on credentials.
