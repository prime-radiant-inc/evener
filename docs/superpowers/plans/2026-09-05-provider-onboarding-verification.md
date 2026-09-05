# Provider onboarding verification

Verified on 2026-09-05 in the isolated `codex/provider-onboarding` worktree. Production changes through `c89388d21` were reviewed with no remaining correctness blockers.

## Automated checks

- `make merge-approval-gate`: passed, including lint, embedded frontend/runtime build, and the full Go and frontend test suite.
- `make vet`: passed.
- `make test-web-browser`: passed all five layout, overflow, shell, spawn, and transcript-scroll guards.
- Focused behavioral suites covered missing/configured/implicit-keyless providers, reconnect and credential removal, preserved draft and path, keyless connection-test recovery, stale credential reads, open model-picker refresh, API-key and OAuth editors, cancellation, and deferred response races.
- An intermediate frontend run overlapped dialog source/test edits and failed those new regressions. The subsequent canonical gate ran the completed sources and passed.

## Live product exercise

Used the built hub and agent binary with a fresh temporary profile, separate state directory, random loopback ports, and a scripted OpenAI-compatible HTTP provider. No existing hub configuration was changed.

1. Opening the empty hub routed to the normal composer with an inline Connect provider action and disabled Start. The implicit Ollama entry with no running server did not suppress setup.
2. Entered a prompt and a new project path before opening setup. A rejected test key produced recoverable connection feedback; replacing it and explicitly testing succeeded. The prompt and path remained intact.
3. Selected the connected model, used the existing Create & start confirmation, and verified the directory, project navigation entry, and completed agent response `onboarding-live-ok`.
4. Removed the last credential through AppWire and observed setup return without any first-run reset.
5. Inspected the composer and connection dialog at 390 × 844. The final dialog omits the healthy configuration-file path from its warnings.

The first scripted completion used bare text and correctly hit Evener's communicate-tool requirement. The corrected fixture completed the turn; that fixture correction required no product change.

## Real external provider

A separate temporary profile selectively reused the existing OpenRouter credential without displaying it or modifying its source. Both a direct HTTP request and a full Evener agent turn using `openrouter-corp/openai/gpt-4.1-mini` returned `onboarding-real-provider-ok`; the agent exited successfully after one turn. The temporary credential copy was removed.

The key's credit allowance rejected the model's default 32K output reservation. The successful smoke test used a profile-local 256-token output cap. No credit or account limits were changed.

OAuth redirect/device flows were exercised with deterministic AppWire responses, not live external account sign-ins. Browser acceptance used the isolated local hub; no production deployment was performed.
