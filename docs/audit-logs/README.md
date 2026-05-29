# Audit logs — historical, not maintained

These are **point-in-time spec-compliance audits from 2026-02-11** (by Claude
Opus 4.6) against `coding-agent-loop-spec.md` and `unified-llm-spec.md`. They
record what passed/failed/gapped at that moment.

They are **stale** as architecture reference: among other drift, they cite the
old `internal/llm/...` paths from before the package moved to top-level `llm/`.
Do not treat them as a description of how the code works today.

For the current implementation, see:

- [`../llm-providers.md`](../llm-providers.md) — LLM provider architecture
  (routing, profiles, adapters, wire protocols).
- [`../llm-provider-config-and-launch.md`](../llm-provider-config-and-launch.md)
  — credentials, OAuth, and the hub launch/spawn model.

Kept for the historical record only.
