# Cross-Provider Fallbacks — Requirements

**Date:** 2026-08-07
**Status:** Requirements capture — needs its own brainstorm/design pass
**Origin:** Split out of `2026-08-07-provider-failure-feedback-design.md` v4
after adversarial review showed "lift the validation error" undersold a
spec-sized feature. Jesse's direction stands: serf should support
cross-provider fallbacks. This doc records what the review established so
the design pass starts from facts, not from v4's mistakes.

## Goal

A `model_fallbacks` entry on a different provider is a genuine escape route
when a provider's transport is unhealthy (see the parent spec's
`ProviderUnhealthyError`), and a valid target for permanent-class fallback.
Today `validateModelFallbackEntry` rejects cross-provider refs
(`agent/session_init.go:1127`) because "provider prompt/tool surfaces
differ" — the rejection is honest; the feature is making it unnecessary.

## Established constraints (each verified against code; each sank v4)

1. **History must be re-projected per fallback profile.** `expandHistory`
   runs once per round with a `replayScope` keyed to the primary profile
   (`agent/session_model_call.go:212-219`); the fallback loop reuses
   `req.Messages` verbatim. Cross-family filtering (`webSearchReplayEligible`,
   `thinkingReplayEligible`) never runs for the fallback, and the N4
   in-flight exemption passes the current turn's provider-raw
   thinking/web-search blocks through unfiltered — a deterministic 400 on
   the fallback provider. Design must plumb `historyTurns` (not just `req`)
   into the fallback path and define the in-flight exemption as
   same-family-only.
2. **The response path must use the answering profile.** Tool-call
   canonicalization uses `s.currentProfile().ToolNameMap()`
   (`agent/session_tools.go:230-231`; call sites in `session_lifecycle.go`
   and `session_stream.go`). Wire names differ per provider (google
   `shell→run_shell_command`, codex `shell→exec_command`), so a rescued
   round's tool calls fail registry dispatch unless the answering profile
   rides through response processing.
3. **`SetModel` is precedent for the checks, not the mechanism.** It is a
   locked, between-rounds, session-mutating switch: it rewrites
   `s.cachedSystemPrompt`, tool-defs cache, registry
   (`reapplyProviderSpecificTools`), context manager, and applies
   `WithCommunicateOverridesFrom` — and it *refuses* switches the history
   can't survive (`unrepresentableHistoryKinds`). A mid-round fallback
   needs pure, side-effect-free per-profile builders
   (`systemPromptFor(profile)`, `toolDefsFor(profile)`), must never touch
   session caches (the session continues on the primary), must layer
   communicate schema overrides, must handle `SystemPromptAsUser` fusion,
   and must *skip* entries whose profile can't represent current history
   (documents/audio) rather than 400.
4. **Meta-provider primaries need new ref semantics.** On
   openrouter/lunarouter tags, `anthropic/...` etc. are upstream
   namespaces (`prefixActionKeep`, `agent/provider/profile.go:601-638`) —
   the same transport, correctly not a provider switch. There is currently
   no syntax reaching a directly-configured instance from a meta-provider
   session, so the users who most need an escape (the motivating incident
   ran on lunarouter) cannot express one. Design needs an explicit
   instance-reference form (e.g. `instance:` prefix or configured-name
   precedence) with the tag-dependent parsing documented.
5. **Resolution ≠ callability.** `newFromProviders(allowPartial=true)`
   skips registering adapters for instances whose credentials fail
   (`llm/providers_config.go:100-111`); the runtime error is a Permanent
   `ConfigurationError` that would mask the primary's unhealthy verdict
   (last-error-wins, `session_model_call.go:770-771`). Validation must
   check the target instance has a registered, authenticated adapter; at
   runtime, fallback-unavailable errors must not become the round's
   terminal error. Also: with a nil `resolveProfile` (allowed,
   `agent/session.go:127`) a cross-provider ref silently degenerates to a
   bogus same-provider `WithModel` projection, and the loop discards
   resolver errors (`fbProfile, _, _` at `session_model_call.go:794`) —
   one nil away from a panic in the recovery path.
6. **Window and representability disparity.** Compaction targets the
   primary's window; a 1M-window primary falling back to a 200k profile
   413s every time (Permanent). Entries must be preflighted per round
   (window fit, `unrepresentableHistoryKinds`) and skipped with a
   diagnostic, and the parent spec's settlement gate must key its
   exclusions on the terminal error class so a fallback-induced
   context-length error doesn't discard the primary group's salvage.
7. **Eligibility must be stated per group, not per round.** The chain
   loop advances on any fallback failure; "skip same-provider entries"
   must be evaluated against a per-round set of providers declared
   unhealthy (a fallback provider can go unhealthy too), or chains with
   two entries on one provider re-create the grind. The matrix must also
   carry the existing rows honestly: permanent-class walks the chain;
   retry-after-declined retryables walk the chain (kata r128) — v4's
   "retryables never fall back (unchanged)" misstated current behavior.
8. **Post-success residency must be defined.** Today fallback success
   updates only the round's request/attempt metadata; the next round
   returns to the primary. Against a hard-down primary that means
   re-grinding the early-stop bound every round of a multi-round turn.
   Turn-scoped stickiness (remember the unhealthy verdict or the winning
   fallback for the remainder of the turn) is the cheapest coherent
   answer; whatever is chosen, the per-round cost must be stated.
9. **Responses-continuation state is provider-specific.**
   `responsesContinuationModelFallbackRequest` falls back to the delta
   message slice when `FullHistoryFallbackMessages` is empty
   (`session_model_call.go:888-901`) — silent context loss cross-provider.
   The fallback request must always regenerate messages from
   `historyTurns` (constraint 1 forces this anyway).
10. **Mixed-round settlement wording.** When the primary failed
    consume-phase and the fallback failed open-phase, steering must
    describe the consume-phase group's shape and provider (with a clause
    noting the fallback also failed), not the last group's — otherwise the
    likely failure mode (unauthenticated fallback) produces steering about
    a zero-attempt config error.

## Interface to the parent spec

The parent spec's eligibility gate treats `ProviderUnhealthyError` as
"settle immediately" via an extension point. This feature implements the
extension: unhealthy → try entries that are (a) cross-provider per the new
ref semantics, (b) callable, (c) representable, (d) not already declared
unhealthy this round. The salvage recorder already spans groups and needs
only provider provenance added.

## Non-goals (unless the design pass argues otherwise)

- Cross-provider fallback for open-phase retryable errors other than the
  existing kata r128 exception.
- Session-permanent migration to the fallback provider (that is `SetModel`,
  which exists).
