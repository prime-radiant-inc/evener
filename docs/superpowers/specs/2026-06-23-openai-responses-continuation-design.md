# OpenAI Responses Continuation Design

Date: 2026-06-23
Status: draft for Jesse review
Scope: OpenAI public `/v1/responses` and ChatGPT/Codex backend `/backend-api/codex/responses`

## 1. Problem

Serf already has most of the raw materials for Responses API continuation, but it does not use them as a coherent request path:

- `llm.Request` exposes `PreviousResponseID` and `ConversationID`.
- The OpenAI Responses adapter serializes `previous_response_id` and `conversation`.
- `fromResponses` maps the provider response `id` into `llm.Response.ID`.
- `appendAssistantTurn` persists `schema.Turn.ResponseID` on assistant turns.

The missing piece is session-level request shaping. `prepareModelRequest` expands full local history every round, and `toResponsesInput` serializes every historical assistant tool call again. That means a malformed tool call from an earlier assistant turn can be resent to the provider before the model sees the error tool result. The current provider-safe argument sanitizer is a necessary full-history fallback defense, but it is not Codex parity.

Codex's Responses path keeps function-call arguments raw, lets tool execution parse them, returns parse failures as tool outputs, and uses `previous_response_id` plus incremental input so prior provider output items are not resent. Serf should do the same for OpenAI Responses traffic where the active endpoint supports it.

## 2. Goals

- Enable OpenAI Responses continuation automatically only when `openai_responses_continuation=auto`, the request is eligible, the endpoint family is enabled in the support registry, and the endpoint family has proven continuation/storage semantics.
- Send only the new local turn delta after the selected assistant response anchor.
- Preserve local transcript history exactly: do not mutate old assistant tool calls, and do not rewrite malformed arguments in stored turns.
- Keep full-history replay as the safe fallback path for unsupported, expired, incompatible, or rejected continuation attempts.
- Keep the provider-safe historical tool-call sanitizer for full-history replay and non-continuation paths.
- Record enough request-shape diagnostics in `api.jsonl` and raw HTTP logs to determine whether a round used continuation, full history, or full-history fallback.
- Keep provider-side response storage an explicit user-facing launch/global setting rather than silently changing public OpenAI retention behavior.
- Treat correctness, Codex parity, request-payload reduction, and measured provider-token impact as the value proposition. The first implementation intentionally still expands local full history for fallback safety and context-pressure accounting; it is not a local CPU/serialization optimization. Provider billing/input-token reduction is not assumed, because provider-side prior state may still be billed as cached or effective input; live proof artifacts must record the observed provider token counts.
- Keep default tests deterministic. Live public OpenAI and Codex backend tests remain explicit opt-in.

Why this much scope:

- The minimal alternative is a public-OpenAI-only malformed-tool-call delta experiment with no durable anchor metadata, no storage-scope fingerprint, no default export redaction, and no Codex-ready shared contracts.
- That alternative is not safe to ship as a general session feature because `previous_response_id` turns provider response IDs into durable provider-state handles. Once Serf persists and reuses those handles, it needs auth/storage scoping, redaction, private local storage, fallback logging, and endpoint-family enablement gates from the first runtime-enabled endpoint.
- The current full-history sanitizer prevents provider validation failures during replay, but it does not provide Responses continuation semantics: the malformed assistant item is still locally resent on full-history paths, and request-payload reduction cannot be proven.
- The minimum acceptable V1-public win is semantic and observable: for a malformed-tool-call recovery turn, public OpenAI must accept a `responses_delta` request whose raw body contains only the resulting tool output plus `previous_response_id`, omits the malformed historical assistant item, and is net smaller than the paired full-history shadow after accounting for added continuation overhead such as `previous_response_id` and continuation-owned storage fields. Phase 12A-public must report omitted historical-item bytes, added continuation-overhead bytes, and net body-size delta separately. If Phase 12A-public cannot prove accepted anchor semantics and net request-payload reduction, Phase 12B-public does not land even if all deterministic plumbing works.
- Before Phase 0A starts, Jesse must explicitly approve choosing the V1-public cut in Section 14 over the minimal experiment. If that approval is not given, this spec remains design collateral and implementation should stop at the already-landed sanitizer/raw-logging work.
- Approval withheld means no continuation registry, launch setting, schema metadata, HMAC/redaction machinery, or runtime delta path ships from this spec. Any later minimal malformed-tool-call delta experiment must be scoped as a separate non-durable experiment that does not persist or reuse provider-state handles.

Endpoint rollout scope:

- This design covers both public OpenAI `/v1/responses` and the ChatGPT/Codex backend `/backend-api/codex/responses` so the shared session, logging, redaction, and fallback contracts do not have to be redesigned later.
- Public OpenAI may be the first runtime-enabled endpoint family after its registry entry has production-path proof.
- Codex backend request-shape discovery, deterministic fixtures, and shared adapter/session plumbing may land before Codex runtime enablement, but the Codex endpoint-family registry entry must remain `Enabled=false` until Phase 12A-codex records its production-path live proof and Phase 12B-codex enables that endpoint family.
- While the Codex registry entry is disabled, Codex backend sessions must use `full_history` and must not send continuation-owned `previous_response_id` or storage flags, even if shared delta-building code already exists.
- Public OpenAI implementation is intentionally included in V1 because Serf already exposes public Responses traffic and the shared logging/redaction/fallback contracts must be exercised there. Runtime enablement is still optional: Phase 12B-public may land only if the Phase 12A-public artifact shows provider acceptance, explicit invalid-anchor errors, real request-payload reduction, and no unacceptable provider-token/cost/quota behavior. Phase 12A artifacts must record concrete rollout thresholds such as eligible-hit-rate floor, prompt-cache hit-rate floor, storage-quota/error ceiling, provider-token/cost ceiling, and any rate-limit ceiling; if rollout diagnostics cross those artifact thresholds, flip the affected registry entry back to `Enabled=false` in a small 12C rollback commit.

Must-have driver and measurable target:

- The must-have driver is Responses continuation parity, first runtime-realized on public OpenAI after Phase 12B-public and then on the Codex backend only after Phase 12B-codex. In both endpoint families, after a malformed assistant tool call, Serf must be able to send only the resulting tool output via `previous_response_id` without resending the malformed assistant item. The existing full-history sanitizer remains necessary, but it does not prove the continuation path or Codex backend parity.
- Each endpoint-family live proof must show that the delta request payload omits all pre-anchor assistant/tool-call items present in the paired full-history shadow request, report the gross omitted serialized-item bytes separately from added continuation-overhead bytes, and show a net body-size reduction for the scripted proof conversation. If an endpoint family cannot show this request-payload reduction and accepted anchor semantics in Phase 12A, its Phase 12B-public or Phase 12B-codex enablement does not land.

Early discovery gates:

- Phase 0B is a go/no-go checkpoint for the full implementation path, not just a note-taking exercise. The opt-in live portion is non-blocking only for landing Phase 0A audits; it is blocking before Phases 1A-11 are treated as committed implementation work for a target endpoint family.
- If Phase 0B live discovery shows the target endpoint family rejects valid `previous_response_id` use, silently accepts invalid anchors as fresh context, rejects the required storage/co-presence shape with no documented adapter fallback, or cannot demonstrate net request-payload reduction on the scripted malformed-tool-call recovery probe, stop before the 1A-10 plumbing investment unless Jesse explicitly approves a narrower research branch.
- Phase 0B must also inventory real launch/profile usage for OpenAI Responses traffic with `SystemPromptAsUser=true`. If the target V1-public model/profile mix predominantly requires `SystemPromptAsUser`, then V1-public runtime enablement is blocked until that mode is removed/disabled for the target profiles, or Jesse explicitly accepts that continuation will fire only for the non-`SystemPromptAsUser` profile set.
- Phase 0B must record a rough eligible-hit-rate expectation before implementation: count or estimate how often the intended rollout traffic is blocked by `SystemPromptAsUser`, date-boundary prompt changes, unsupported item kinds, media/file/web-search inputs, reasoning items, model changes, or storage-scope mismatches. If that estimate is below the proposed Phase 12A eligible-hit-rate floor, stop before the 1A-10 plumbing investment unless Jesse approves continuing for parity rather than broad hit rate.

## 3. Non-Goals

- No broader Codex-client parity sweep beyond Responses API continuation and malformed tool-call replay.
- No server-side continuation for Chat Completions, OpenAI-compatible providers, Anthropic, Google/Gemini, Kimi, or OpenRouter.
- No migration of existing transcripts. Existing assistant turns without new response metadata remain valid history but are not required to become continuation anchors.
- No adapter-owned conversation memory. The OpenAI adapter remains stateless across requests.
- No continuation support when `SystemPromptAsUser` is enabled. That mode deliberately moves instructions into user content; for this design it is a full-history-only compatibility path and a deprecation/removal candidate for OpenAI Responses profiles that should use continuation.
- No dependency on live provider credentials in default tests.

## 4. Current Implementation Facts

Relevant existing files:

- `llm/types.go`: `Request.PreviousResponseID`, `Request.ConversationID`, and `Response.ID`.
- `llm/providers/openai/responses.go`: `buildRequestBody` defaults `store` to `false` and serializes `previous_response_id`; `toResponsesInput` expands full `req.Messages`; `fromResponses` reads raw response `id`.
- `agent/session.go`: `appendAssistantTurn` writes `ResponseID` onto `schema.Turn`.
- `agent/schema/turn.go`: `Turn.ResponseID` is persisted.
- `agent/session_model_call.go`: `prepareModelRequest`, `buildModelRequest`, and `expandHistory` always build a full local history request.
- `agent/provider/resolve.go` and `agent/provider/profile.go`: OpenAI `responses` style resolves to the full OpenAI profile; `chat_completions` style resolves to the OpenAI-compatible path.
- `llm/providers/openai/adapter.go`: the adapter can distinguish public Responses from the Codex backend by its endpoint path and stamps the dialed endpoint URL into `resp.Raw["endpoint_url"]`.

The decisive gap is that no agent/session code reads a prior `Turn.ResponseID` and assigns it to the next `llm.Request.PreviousResponseID`.

## 5. Architecture

Continuation is session-owned, not adapter-owned.

The session owns durable history, compaction, model fallback, retries, tool-result turns, and persisted assistant response IDs. It is the only layer with enough context to decide whether a `previous_response_id` anchor is safe and which local turns are new.

The OpenAI adapter remains stateless, but it owns canonical wire-shape introspection because only the adapter knows final defaults, endpoint filtering, and Codex-specific request normalization:

- serialize `previous_response_id` when present;
- serialize the `input` built from `req.Messages`;
- expose a pure continuation-capability/canonicalization method for the session to call before dispatch;
- stamp `endpoint_url` on successful responses;
- return provider errors with raw request/response bodies when raw logging is enabled.

The session records one of four request/attempt history modes:

| Mode | Meaning |
|---|---|
| `full_history` | Existing behavior: system prompt plus all expanded local history. |
| `responses_delta` | OpenAI Responses continuation: system prompt plus only local turns after the anchor; `PreviousResponseID` is set. |
| `full_history_fallback` | Retry after a continuation-specific rejection; same payload shape as `full_history`, with sanitizer active. |
| `chat_completions_fallback` | Adapter-internal fallback from Responses to Chat Completions, using full history. |

The mode is request metadata for logging and testing; it is not model-visible content.

`llm.Request` should gain explicit control-plane fields for the session-owned history shape:

```go
type HistoryMode string

const (
	HistoryModeFullHistory         HistoryMode = "full_history"
	HistoryModeResponsesDelta      HistoryMode = "responses_delta"
	HistoryModeFullHistoryFallback HistoryMode = "full_history_fallback"
	HistoryModeChatFallback        HistoryMode = "chat_completions_fallback"
)

type Request struct {
	// existing fields...
	HistoryMode                 HistoryMode          `json:"-"`
	Continuation                *ContinuationMetadata `json:"-"`
	FullHistoryFallbackMessages []Message            `json:"-"`
}
```

`HistoryMode` is one of the four modes above. `ContinuationMetadata` carries the selected anchor hash, delta count/kinds, endpoint family, request fingerprint, recomputed active-slice context marker, storage-scope fingerprint, and a storage-policy label for logging/tests. Storage-policy compatibility is enforced through `StorageScopeFingerprint`, not through request fingerprinting. The raw `previous_response_id` lives only on `llm.Request.PreviousResponseID` for the provider wire path and in sensitive local transcript/debug fields explicitly called out below. Each model round starts from one session-owned full-history expansion. The shadow context-pressure estimate reads that expansion, and `FullHistoryFallbackMessages` is cloned from it only when the selected path can fall back to Chat Completions. `FullHistoryFallbackMessages` being empty on a non-fallback-capable path does not mean the shadow estimate is unavailable. Provider-safe replay sanitization happens only inside adapter body builders.

The metadata shape should stay small and value-like:

```go
type ContinuationMetadata struct {
	// AttemptIndex is 1-based within an attempt group.
	AttemptIndex           int
	PreviousResponseIDHash string
	AnchorTurnIndex        int
	DeltaTurnCount         int
	DeltaTurnKinds         []string
	EndpointFamily         string
	RequestFingerprint     string
	ContextMarker           string
	StoragePolicyLabel      string
	StorageScopeFingerprint string
	ChatFallbackHistoryLen  int
}
```

The session model-call attempt coordinator owns `AttemptIndex`. It assigns a 1-based index immediately before each session-level provider dispatch, stamps that value into `ContinuationMetadata`, `llm.APILogContext`, and raw-log context, and exposes a session-owned "next attempt index" allocator to the adapter attempt-recorder callback for adapter-internal fallback attempts. The adapter must request/receive an assigned index from that callback; it must not mint indices independently. `ModelAttemptMetadata` only carries the already assigned index back for transcript persistence. Attempt totals are owned by `ModelAttemptMetadata` after dispatch, when retry/fallback classification can know the group size.

The provider capability method should return all provider-owned continuation facts from one place:

```go
type ResponsesContinuationPlan struct {
	Supported                            bool
	EndpointFamily                       string
	EndpointURL                          string
	RequestFingerprint                   string
	StorageScope                         ContinuationStorageScope
	CanFallbackToChat                    bool
	ContinuationErrorsBypassChatFallback bool
}

type ResponsesErrorClass string

const (
	ResponsesErrorContinuationRejected ResponsesErrorClass = "continuation_rejected"
	ResponsesErrorModelEndpoint        ResponsesErrorClass = "model_endpoint"
	ResponsesErrorTransient            ResponsesErrorClass = "transient"
	ResponsesErrorPermanentOther       ResponsesErrorClass = "permanent_other"
)

type ResponsesContinuationPlanner interface {
	PlanResponsesContinuation(req Request) (ResponsesContinuationPlan, error)
	ClassifyResponsesError(req Request, err error) ResponsesErrorClass
}
```

The concrete access path is through `llm.Client`, because session code does not own adapter instances directly:

```go
func (c *Client) PlanResponsesContinuation(req Request) (ResponsesContinuationPlan, bool, error)
func (c *Client) ClassifyResponsesError(req Request, err error) (ResponsesErrorClass, bool)
```

`Client` resolves `req.Provider` the same way `Complete`/`Stream` do, checks whether the selected adapter implements `ResponsesContinuationPlanner`, and returns `ok=false` when it does not. The session never reaches into the provider registry directly and never duplicates `buildRequestBody` or Codex field-filtering logic. This keeps the adapter stateless while making the adapter the single owner of provider-facing canonicalization.

Planner failure behavior:

- `ok=false` means the provider path is unsupported; use `full_history`.
- A planner/canonicalization error is non-fatal for the round; use `full_history` and log a diagnostic warning with the provider, model, and error.
- If planning fails, public OpenAI anchor-producing storage is disabled for that round: do not send `store:true` solely for continuation, and do not persist continuation-anchor metadata. Preserve an explicit user/provider storage setting only if it was set for a reason outside Responses continuation.
- Planner errors must not prevent the model call unless the full-history request itself cannot be built.

The session model-call wrapper must carry final attempt metadata separately from `llm.Response`:

```go
type ModelAttemptMetadata struct {
	HistoryMode        HistoryMode
	EndpointFamily     string
	EndpointURL        string
	RequestModel       string
	RequestFingerprint string
	StorageScopeFingerprint string
	ContextMarker       string
	// AttemptIndex is 1-based within an attempt group.
	AttemptIndex       int
	AttemptCount       int
	FinalAttemptCount  *int
}
```

`ModelAttemptMetadata.AttemptCount` is the attempt-group size known at record write time, or `0` when the group size is not known yet. `ModelAttemptMetadata.FinalAttemptCount` is the authoritative final group size once retry/fallback classification has completed; readers must prefer it over `AttemptCount` whenever it is present.

Use an explicit function boundary:

```go
appendAssistantTurn(resp llm.Response, finalAttempt ModelAttemptMetadata)
```

`appendAssistantTurn` must persist response-anchor metadata from the successful final attempt, not from the first request tried. Provider result fields come from `llm.Response`; request/continuation fields come from `ModelAttemptMetadata`. The session populates `ModelAttemptMetadata` from the dispatched request's `ContinuationMetadata` plus the final response, fallback classification, and endpoint facts from the attempt that actually succeeded.

| Turn field | Source |
|---|---|
| `ResponseID` | `resp.ID` only when `finalAttempt.EndpointFamily` is a Responses endpoint and the attempt completed successfully. |
| `ResponseIDHash` | Redacted hash of `resp.ID` from the local continuation HMAC utility whenever `ResponseID` is persisted for an anchor-eligible Responses turn. |
| `ResponseProvider` | `resp.Provider`. |
| `ResponseModel` | `resp.Model`. |
| `ResponseRequestModel` | resolved request model from `finalAttempt`. |
| `ResponseEndpoint` | `finalAttempt.EndpointURL` with `resp.Raw["endpoint_url"]` as a compatibility fallback. |
| `ResponseStorageScopeFingerprint` | `finalAttempt.StorageScopeFingerprint`. |
| `ResponseRequestFingerprint` | `finalAttempt.RequestFingerprint`. |
| `ResponseContextMarker` | `finalAttempt.ContextMarker`, which must be the literal `cont-ctx-v1` for V1 anchor-eligible attempts; empty means absent metadata. |

`ContinuationMetadata` is request-shaping input and request-log context. Persisted assistant anchor fields must be read from `ModelAttemptMetadata` for the successful final attempt, never by re-reading an earlier dispatched request's `ContinuationMetadata`. This keeps fallback success from persisting stale delta fingerprints, storage scopes, context markers, or attempt indices from a failed first attempt.

If `responses_delta` fails and `full_history_fallback` succeeds, the assistant turn receives the fallback attempt's fingerprint/scope. If adapter-internal fallback succeeds on Chat Completions, the assistant turn may keep ordinary response usage/text metadata, but it must not be eligible as a Responses continuation anchor. A response id observed during an incomplete stream must not become an eligible anchor unless the stream reaches a successful final response.

## 6. Response Metadata

`schema.Turn.ResponseID` is necessary but not sufficient for safe continuation. New assistant turns should also persist enough metadata to prove endpoint and request-shape compatibility:

```go
ResponseID                 string `json:"response_id,omitempty"`
ResponseIDHash             string `json:"response_id_hash,omitempty"`
ResponseProvider           string `json:"response_provider,omitempty"`
ResponseModel              string `json:"response_model,omitempty"`
ResponseRequestModel       string `json:"response_request_model,omitempty"`
ResponseEndpoint           string `json:"response_endpoint,omitempty"`
ResponseStorageScopeFingerprint string `json:"response_storage_scope_fingerprint,omitempty"`
ResponseRequestFingerprint string `json:"response_request_fingerprint,omitempty"`
ResponseContextMarker       string `json:"response_context_marker,omitempty"`
```

`ResponseStorageScopeFingerprint` is the storage-scope fingerprint string persisted on the turn; `ContinuationStorageScope` is the structured scope produced by the planner. Only the fingerprint string is compared for eligibility or persisted on transcript turns.

`appendAssistantTurn(resp, finalAttempt)` should populate these fields from the provider response and the final attempt wrapper:

- `ResponseID`: `resp.ID` only when `finalAttempt.EndpointFamily` is a successful Responses endpoint
- `ResponseIDHash`: redacted hash of `resp.ID` using the local continuation HMAC utility; if the HMAC secret is unavailable, still persist raw `ResponseID` as sensitive local transcript/debug data, leave `ResponseIDHash` empty, set `ResponseContextMarker` empty, and do not persist continuation-anchor eligibility metadata for that turn
- `ResponseProvider`: `resp.Provider`
- `ResponseModel`: `resp.Model`
- `ResponseRequestModel`: resolved request model from `finalAttempt`
- `ResponseEndpoint`: `finalAttempt.EndpointURL` with `resp.Raw["endpoint_url"]` as a compatibility fallback
- `ResponseStorageScopeFingerprint`: `finalAttempt.StorageScopeFingerprint`
- `ResponseRequestFingerprint`: `finalAttempt.RequestFingerprint`
- `ResponseContextMarker`: `finalAttempt.ContextMarker`, which must be the literal `cont-ctx-v1` for V1 anchor-eligible attempts; empty means the metadata is absent and the turn is not eligible as an anchor

Existing transcript turns that only have `ResponseID` are not continuation anchors by default. This avoids guessing whether an old id came from public OpenAI, the Codex backend, a fallback model, or a different endpoint.

Schema compatibility expectations:

- New `Turn` fields are optional `omitempty` fields. Old transcript readers must tolerate their absence.
- Old transcripts with only `response_id` remain readable and exportable but are not eligible anchors.
- New anchor-eligible Responses turns must include `ResponseIDHash` when they include `ResponseID`, so default exports do not depend on nearby `api_call` records to redact response handles.
- Older Serf versions that read newer transcripts should ignore unknown metadata fields; if an older version rewrites a transcript and drops those fields, newer versions must treat the affected turns as non-anchorable and use `full_history`.
- ATIF/export code should pass these fields through according to the export mode: default exports include redacted/hash provider-state handles plus non-secret metadata, while local diagnostic raw exports include raw provider-state handles only when explicitly requested.
- No transcript migration is required.

The fingerprint must be computed from provider-facing request fields after removing only fields that are expected to change for continuation:

- remove `input`;
- remove `previous_response_id`;
- remove only the endpoint-family-specific storage/retention fields listed in the table below;
- keep `instructions`;
- keep model, tools, tool choice, reasoning, include fields, response format, web search tools, provider options, prompt-cache controls, session/thread metadata other than `conversation`, and Codex backend metadata.
- do not include storage policy; storage policy belongs only to the storage scope fingerprint.

Invariant: request fingerprint compatibility proves only model/request shape. Storage compatibility is checked only through `ResponseStorageScopeFingerprint`; `store` and other continuation-owned retention controls must never affect request fingerprint compatibility, and excluding them from the request fingerprint must not skip the storage-scope check.

Storage/retention fields excluded from request fingerprinting must be listed per endpoint family. This table is the only source of truth for excluded storage/retention fields. Do not strip provider options by purpose, prefix, substring, or broad category; adding a field to this table plus deterministic tests is the only way to exclude it.

| Endpoint family | Excluded storage/retention wire fields |
|---|---|
| Public OpenAI `/v1/responses` | `store`, `conversation` |
| ChatGPT/Codex backend `/backend-api/codex/responses` | `conversation`; add any exact backend storage/retention field after endpoint discovery records one |

Canonicalization tests must cover every excluded key in this table. Any storage-like provider option not listed here remains in the request fingerprint until the design and tests add it explicitly.

`conversation` is classified as a provider-state handle, not model/request shape. It is excluded from request fingerprint compatibility and enforced only through `ContinuationStorageScope.ConversationIDHash`.

The Codex backend row is provisional until endpoint discovery records the exact storage/retention shape. When Phase 0B-discovery or Phase 12A-codex discovers a backend storage/retention field, the same change set must update this table, update canonicalization fixtures for that exact field, and bump the fingerprint version before the Codex endpoint family can be enabled.

System prompt / `instructions` stability:

- `instructions` remains in the request fingerprint. Continuation is eligible only when the effective system/developer instructions are deterministic for the same model, tool set, profile/config, visible context marker, and session state.
- If existing prompt construction includes volatile data such as wall-clock time, per-request counters, transient status text, or changing context-pressure hints, either move that data out of provider-visible instructions for continuation-eligible requests or normalize it through an explicit, tested prompt-canonicalization layer before fingerprinting.
- If deterministic instructions cannot be proven for a request, use `full_history`, do not add continuation-owned provider storage solely for anchor production, and log `continuation_instructions_unstable`.
- The current production prompt includes `agent/prompts/sections/environment.md.tmpl` with `WorkingDir`, `Platform`, `OSVersion`, `Today`, `Model`, and `KnowledgeCutoff`. V1 does not canonicalize `Today`: continuation may drop to `full_history` when the rendered date changes across a session boundary. Phase 3B-fingerprint must include a real production-prompt determinism test that renders the prompt with fixed values for all environment-template inputs and proves the fingerprint is stable, and a two-date fixture that proves the expected request-fingerprint mismatch or explicit normalization behavior before any future canonicalization is introduced. The same test must assert that `WorkingDir`, `Platform`, `OSVersion`, `Model`, and `KnowledgeCutoff` are stable inputs for an otherwise continuation-eligible request; if any of them can change mid-session for a real request path, that path uses `full_history` until normalized.

This mirrors Codex's incremental gate: a delta request may reuse server-side prior output only when the non-input request shape still matches the request that produced the anchor response.

Canonicalization rules:

- Compute the request fingerprint through the adapter's `PlanResponsesContinuation` result from the provider-facing body after adapter defaults, filtering, and normalization have been applied, then remove only the continuation-varying fields above.
- Canonicalize maps and object fields with stable key ordering.
- Normalize nil/omitted/default fields to the exact wire behavior the adapter will use. Pointer `false` and nil are equal only when the adapter serializes them identically.
- For the Codex backend, compute the fingerprint after Codex-specific unsupported fields have been filtered.
- Add a fingerprint version prefix so future canonicalization changes invalidate old anchors instead of silently reusing incompatible state.

The storage scope is separate from the request-shape fingerprint because it is a security boundary, not model input. The storage scope fingerprint must be computed from normalized endpoint/storage/auth identity:

- provider and endpoint family;
- normalized base URL and endpoint path;
- public OpenAI org/project identifier hashes;
- auth source (`api_key`, OAuth/Codex backend, or other explicit source);
- non-secret credential identity hash;
- ChatGPT account/workspace identifier hash for the Codex backend;
- conversation id hash when the request uses `ConversationID`;
- storage policy.

The auth/provider construction layer owns storage-scope construction. Session code must receive a sanitized, versioned value such as:

```go
type AuthScopeIdentity struct {
	Version        string
	AuthSource     string
	CredentialHash string
	AccountHash    string
	WorkspaceHash  string
}

type ContinuationStorageScope struct {
	Fingerprint        string
	HashVersion        string
	Provider           string
	EndpointFamily     string
	BaseURL            string
	Path               string
	AuthSource         string
	OrgIDHash          string
	ProjectIDHash      string
	AccountHash        string
	CredentialHash     string
	ConversationIDHash string
	StoragePolicy      string
}
```

OpenAI adapter construction must receive both raw credentials for transport and a sanitized `AuthScopeIdentity` for continuation planning. The raw credential stays in the transport path; planning must be split so the pure canonicalization/scope helper receives only the provider-facing body, normalized endpoint facts, storage field exclusion table, and sanitized `AuthScopeIdentity` / org/project hashes. `PlanResponsesContinuation` may build the body through adapter code, but it must delegate fingerprint/scope construction to that pure helper and must not pass raw transport credentials into it. Concretely:

- `OpenAIInstanceParams` and `Adapter` gain an `AuthScopeIdentity` field.
- API-key config loading computes `AuthSource="api_key"` and `CredentialHash` before adapter construction.
- OAuth/Codex auth resolution computes `AuthSource="oauth"` plus account/workspace hashes from stable account/workspace identifiers before adapter construction.
- OpenAI org/project config loading computes `OrgIDHash` and `ProjectIDHash`; raw org/project identifiers are transport/header inputs only and are not persisted in planner metadata/logs.
- OAuth access tokens, bearer tokens, refresh tokens, and raw API keys are never inputs to planner code and are never persisted in transcripts/logs.

Hash construction:

- Use a versioned HMAC-SHA256 identity such as `cont-scope-v1:<kind>:<base64url(hmac(scope_subkey, normalized_value))>`.
- Provider-state handle hashes use the parallel form `cont-handle-v1:<kind>:<base64url(hmac(redaction_subkey, normalized_handle))>`, where `kind` is `response_id`, `previous_response_id`, or `conversation_id`. Changing the handle hash version affects newly written/exported hashes only and must never require rewriting historical raw handles.
- `local_scope_secret` lives under the resolved private Serf state directory (`SERF_STATE_DIR` / XDG state home), outside per-session transcripts, in a dedicated continuation-secret file chosen in Phase 1B-secret. It is created with private file permissions, is never exported, and is not the provider credential.
- `local_scope_secret` is a local root secret, not the direct HMAC key. Phase 1B-secret must derive separate subkeys with distinct labels, for example `serf-continuation-scope-v1` for `ContinuationStorageScope.Fingerprint` / auth-scope hashes and `serf-continuation-redaction-v1` for redacted provider-handle export/display hashes. Rotating one derived-key version must not force rotating the other unless the root secret itself is compromised.
- API-key `CredentialHash` input is the normalized API key. OAuth `CredentialHash` input is a stable auth-record identity such as auth source plus account/workspace/subject identifiers, never an access token.
- `AccountHash`, `WorkspaceHash`, `OrgIDHash`, and `ProjectIDHash` inputs are normalized identifiers.
- `ContinuationStorageScope.Fingerprint` is a versioned canonical hash over endpoint family, normalized base URL/path, org/project hashes, `AuthScopeIdentity`, conversation id hash, and storage policy.

If `local_scope_secret` cannot be created or no durable private Serf state home is available, continuation must fail closed for that session: use `full_history`, do not send continuation-owned `store:true`, do not persist anchor eligibility metadata, and log a diagnostic such as `continuation_scope_secret_unavailable`. The model call itself may still proceed on the normal full-history path.

The session may compare and log the `Fingerprint`, endpoint family, storage policy, and hash version, but it must not hash raw secrets itself. Never persist raw API keys, bearer tokens, OAuth tokens, or full account identifiers in transcript fields. Changing hash construction or rotating `local_scope_secret` changes `HashVersion`/fingerprints and invalidates old anchors predictably; Serf should use `full_history` rather than trying to migrate those anchors. Continuation eligibility requires exact `ResponseStorageScopeFingerprint` match so a restored session cannot reuse `previous_response_id` across API keys, orgs/projects, ChatGPT accounts, conversations, base URLs, or proxies.

Context boundary membership is relative to the active visible history slice used for the next request. V1 computes it only from explicit boundary turns in `s.history`. Eligibility always recomputes active-slice boundary membership for both the candidate anchor and the current request, in live sessions and restored sessions alike. The persisted `ResponseContextMarker` is the literal `cont-ctx-v1`: only its presence and version (`== "cont-ctx-v1"`) gate whether a turn was written by the continuation-aware schema. It is a diagnostic breadcrumb and must not encode a boundary hash or slice origin. On restore, compute the active visible slice after `ResumeHistory` has selected `[last checkpoint/summary, ...subsequent turns]`, then recompute boundary membership from that restored slice. Do not compare a persisted marker value from the old slice origin to a freshly restored slice origin. A future rewrite path that changes effective context without emitting a boundary turn must land a persisted epoch offset or explicit boundary record in the same change; v1 must not use an unpersisted live-only counter for continuation.

## 7. Continuation Eligibility

A request is eligible for `responses_delta` only when all of these are true:

- The active profile is the full OpenAI Responses profile, not `openai-compatible` / Chat Completions style.
- The round is going to the OpenAI Responses adapter path, not a known Chat Completions fallback.
- The resolved launch/runtime setting is `openai_responses_continuation=auto`.
- The endpoint-family support registry entry for the current endpoint family is `Enabled=true`.
- `SystemPromptAsUser` is disabled. If it is enabled, Serf must use `full_history`; this design does not add a separate system-as-user delta builder.
- The visible history since the latest context boundary is text/tool-call/tool-result only for the first implementation. If prior image, document/file, web-search, provider-hosted reference inputs, provider-exposed reasoning items, encrypted reasoning blobs, or reasoning summary items appear before the anchor or in the delta, use `full_history` until endpoint-specific fixtures prove those item types can be safely continued and replayed. This item-kind gate owns reasoning-content detection even when reasoning content is attached to an assistant turn rather than represented as a separate turn kind.
- The latest assistant turn in the visible history slice is the candidate anchor and has a non-empty response id, response id hash, response endpoint metadata, storage scope, request fingerprint, and `ResponseContextMarker="cont-ctx-v1"` marking it as written by the continuation-aware schema. The persisted marker value is a presence/version gate only. If a newer assistant turn exists but is not anchor-eligible, use `full_history`; do not skip over it to select an older anchor.
- The anchor endpoint is compatible with the current endpoint family:
  - public OpenAI `/v1/responses` can continue only public OpenAI `/v1/responses`;
  - ChatGPT/Codex backend can continue only ChatGPT/Codex backend responses.
- The current resolved request model is compatible with the model recorded on the anchor attempt. The first implementation must require exact match after fallback resolution and any local model alias canonicalization. Do not use provider-returned `resp.Model` alone for compatibility; providers may return aliases or dated snapshots. Persist provider-returned `resp.Model` for diagnostics, but persist the resolved request model used for dispatch as the anchor compatibility value.
- The current non-input request fingerprint exactly matches the anchor turn's `ResponseRequestFingerprint`.
- The current storage scope fingerprint exactly matches the anchor turn's `ResponseStorageScopeFingerprint`.
- The endpoint storage policy can support continuation, and the launch configuration permits that policy. Public OpenAI continuation requires saved response state; anchor-producing public OpenAI requests and delta public OpenAI requests must opt into provider storage. Codex backend storage behavior must be treated as endpoint-specific: if it accepts and requires `store`, use the same storage policy; if it stores response state independently, keep the backend's accepted request shape and prove continuation with tests.
- The selected anchor is not older than the enabled endpoint family's maximum anchor age, measured from the persisted `schema.Turn.Timestamp` on the assistant anchor. If the anchor timestamp is missing or older than that bound, use `full_history` and let the successful full-history response create a fresh anchor when eligible.
- The anchor is after the latest context boundary. V1's recomputed active-slice boundary membership is the named implementation of that check: recompute it for the candidate anchor and current request, and require both to be in the active slice after the latest boundary. On restore, compare the recomputed active-slice membership described in Section 6, not any boundary meaning inferred from `ResponseContextMarker`. A context boundary is any `TurnCheckpoint`, `TurnSummary`, or future context-management turn that intentionally replaces, summarizes, or rewrites prior local context. A future persisted epoch-offset mechanism may add an independent mismatch case; V1 does not have one.
- No assistant output turn appears after the anchor. The delta may contain user input, tool results, and other explicitly supported non-assistant items only.
- The delta after the anchor is non-empty.

If any condition fails, Serf uses `full_history`.

If an endpoint rejects the storage policy required for continuation, Serf must mark that endpoint family as continuation-disabled for the current session/key described in failure handling and use `full_history` rather than repeatedly issuing doomed delta requests.

Eligibility computation is expected to be O(active visible history slice) per model round in V1 because it recomputes boundary membership, request fingerprint inputs, storage scope, and item-kind gates from the visible slice. This is acceptable for the first implementation because the model request already expands the same local history for fallback and shadow accounting. If rollout diagnostics show eligibility computation itself becoming material on long restored sessions, cache eligibility inputs with the same invalidation keys used for the shadow full-history estimate.

## 8. Anchor Selection

The anchor is the latest assistant turn in `historyTurns`, and it must satisfy continuation eligibility and appear after the latest context boundary. Serf must not skip over a newer non-anchorable assistant turn to continue from an older anchor, because the provider-side state behind the older `previous_response_id` does not contain that newer assistant output.

Anchor selection and assistant-turn persistence must be serialized per session under the same session history lock/turn-order discipline used for normal model calls. A request may only continue from committed assistant turns already present in `s.history`. In-flight or partially streamed responses are not eligible anchors. If future work introduces overlapping model calls inside one session, each call must reserve its history base and reject continuation if another call commits a newer turn before dispatch.

Implementation must first verify the current `processOneInput` / model-call path still serializes model calls per session and that `s.history` cannot be mutated between anchor selection and dispatch except by the same request. If that guarantee is not present, add the history-base reservation check in the first implementation before enabling continuation.

The request delta is the turn slice after the anchor:

```text
historyTurns[:anchor+1] = server-side prior response state
historyTurns[anchor+1:] = local delta to send
```

The delta is expanded through the same `expandHistory` logic used today, so tool-result aggregation and steering/checkpoint treatment stay consistent.

If the turn slice after the candidate anchor contains any assistant output turn, use `full_history`. The first implementation does not try to replay intervening assistant output inside a delta, and it must not strip such output while leaving later tool results behind.

Any `function_call_output` / tool-result item in the delta must reference a `call_id` owned by the selected anchor response's server-side state or by another supported item inside the same delta. If Serf cannot prove that linkage from local transcript ordering and call ids, use `full_history`; do not send orphaned tool results in a delta.

Anchor selection starts after the latest context boundary:

```text
USER_INPUT
ASSISTANT(response_id=resp_1)
CHECKPOINT or SUMMARY
USER_INPUT
ASSISTANT(response_id=resp_2)

eligible anchors: resp_2 only
```

This is stricter than only checking the delta. Continuing from `resp_1` would ask the provider to reuse the full pre-compaction server-side state while Serf's local history now contains a compacted replacement for that state.

Examples:

```text
USER_INPUT
ASSISTANT(response_id=resp_1, malformed tool_call)
TOOL_RESULTS(invalid arguments error)

next request:
  previous_response_id = resp_1
  input = TOOL_RESULTS only
```

```text
... ASSISTANT(response_id=resp_5)
USER_INPUT("next question")

next request:
  previous_response_id = resp_5
  input = USER_INPUT only
```

If the delta contains `TurnCheckpoint`, `TurnSummary`, or another context boundary, continuation is disabled for that request. V1 derives the boundary from existing checkpoint/summary turns. Any future context rewrite that lacks those markers must persist an epoch offset or explicit boundary record before it can participate in continuation; otherwise requests after that rewrite use `full_history`.

## 9. Anchor Production

Anchor production is separate from delta eligibility. A `full_history` request can create the first future anchor even when no previous anchor or non-empty delta exists.

Use a distinct predicate such as `canProduceResponsesAnchor(req, plan)` before adding continuation-owned storage to a full-history request or persisting anchor metadata. It is true only when all of these hold:

- the active request path is a full OpenAI Responses endpoint family, not Chat Completions or OpenAI-compatible mode;
- the launch setting is `auto`;
- the endpoint-family support registry is `Enabled=true`;
- `Client.PlanResponsesContinuation` succeeded and returned endpoint family, request fingerprint, storage scope, and endpoint support facts; storage policy is read from `ContinuationStorageScope.StoragePolicy`;
- the endpoint storage policy can create provider-side state for future `previous_response_id` use;
- continuation storage ownership can be represented with a `ContinuationStoreOverride` clone/sidecar, without mutating explicit user/provider storage settings;
- `SystemPromptAsUser` is disabled;
- visible history since the latest context boundary is text/tool-call/tool-result only for the first implementation;
- context marker is known for the visible history;
- `local_scope_secret` exists and transcript/API/raw-log sinks that would receive raw provider handles are private or can be hardened before dispatch.

If `canProduceResponsesAnchor` is false, dispatch the immutable base request, do not add continuation-owned `store:true` or storage provider options, and do not persist anchor eligibility metadata. A successful full-history response from that path may still persist ordinary assistant content and non-sensitive diagnostics.

If `canProduceResponsesAnchor` is true, the anchor-producing `full_history` round must still run the planner and carry continuation metadata through dispatch even though it does not set `PreviousResponseID`. The request or model-attempt sidecar for that `full_history` attempt carries the endpoint family, request fingerprint, storage scope fingerprint, storage policy, active-slice context marker, and assigned attempt index; previous-response hash, anchor index, and delta counts remain empty/zero. `appendAssistantTurn(resp, finalAttempt)` then has the same `ModelAttemptMetadata` fields it would have for a delta attempt and can persist `ResponseRequestFingerprint`, `ResponseStorageScopeFingerprint`, and `ResponseContextMarker` on the first anchor. Without this metadata carrier, the next round cannot become `responses_delta`.

## 10. Request Shape

For `responses_delta`, `buildModelRequest` still includes the current system prompt. The OpenAI adapter's `toResponsesInput` will separate system/developer messages into `instructions` and serialize only non-system delta messages as `input`.

The non-input request fields should remain the same as the full request would have used: model, tools, tool choice, reasoning effort, include fields, provider options, session/thread metadata, and Codex backend metadata. This follows the Codex pattern: previous server state supplies prior items, while the new request still declares the current request parameters.

Reasoning-model continuity follows the same rule. Prior reasoning items are assumed to live in provider-side state behind `previous_response_id`; Serf does not replay them locally in `responses_delta`. The request fingerprint keeps `reasoning` and `include` fields, so changing reasoning effort, encrypted-reasoning settings, or reasoning-summary inclusion disables continuation. If a provider exposes reasoning items as local transcript inputs before the anchor or inside the delta, use `full_history` until endpoint-specific fixtures prove those items can be continued safely.

Prompt-cache controls remain in the request fingerprint. `responses_delta` intentionally changes the locally sent prefix, so existing full-history prompt-cache hit rates may change; that is a rollout observation, not an eligibility shortcut. Do not remove prompt-cache controls from the fingerprint to chase cache reuse.

The request must not include old assistant `function_call` items in `responses_delta`; those items are already associated with the prior response id on the provider side.

V1 does not introduce new `ConversationID` population. The continuation handle is `previous_response_id`; `ConversationID` is copied only when it is already present from profile/config/provider-specific request construction or a future caller. The conversation scope logic is still required because an explicit `conversation` is a provider-state handle, not request shape. If both the selected anchor and current request have no conversation handle, they are compatible on this dimension. If either side has a conversation handle, both sides must have matching `ContinuationStorageScope.ConversationIDHash`; otherwise use `full_history`. Never send a delta request with a `previous_response_id` from one conversation and a different current `conversation` handle.

This means the `conversation` branch may be exercised only by tests or unusual profile/config paths in V1. It stays in the spec as a security boundary for already-present provider-state handles and for the Codex backend discovery path; it must not become an excuse to populate new conversations solely for continuation.

Phase 0B-discovery must cover the concrete request shape where `previous_response_id` and `conversation` are both present. If an endpoint rejects or ambiguously handles co-presence, its enabled runtime path must either omit the explicit `conversation` on delta requests through a documented adapter canonicalization rule, or keep continuation disabled for requests with `ConversationID`. Serf must not rely on storage-scope matching alone to send an unproven co-present handle shape.

Request shaping order:

```text
resolve launch/global config
  -> build full-history raw expanded request
  -> create a continuation candidate request through ContinuationStoreOverride when storage is needed
  -> call Client.PlanResponsesContinuation(candidate)
  -> select full_history or responses_delta
  -> for responses_delta, attach PreviousResponseID and delta Messages
  -> dispatch
  -> persist final-attempt metadata only from the successful attempt
```

Continuation storage ownership:

- Treat the full-history request built from session/config/profile state as immutable base input.
- Any `store:true` value or provider option added solely to create or maintain continuation anchors must be applied only by a continuation-storage helper that returns a cloned request plus an ownership record.
- The ownership record should identify each continuation-owned field/key, for example:

```go
type ContinuationStoreOverride struct {
	StoreSetByContinuation          bool
	ProviderOptionKeysByContinuation []string
	StoragePolicy                    string
}
```

- Do not infer ownership later by inspecting values. `Store=true` can come from explicit user/provider config or from continuation; only the sidecar ownership record distinguishes them.
- Clearing continuation-owned storage means discarding the owned clone and returning to the immutable base request, or applying the ownership record to remove only the fields/keys that helper added. Never strip explicit user/provider storage settings.
- A continuation-specific fallback that can become a new anchor must apply continuation storage to a fresh full-history fallback clone. An endpoint-level storage-policy rejection must retry using the immutable base request, with only explicit user/provider storage settings preserved.
- Planner failure, registry disabled, launch setting `off`, or missing storage eligibility must dispatch the immutable base request and must not send continuation-owned `store:true` or storage provider options.

Anchor-producing `full_history` requests must have `Store` set before planning and dispatch when the storage policy requires `store:true`. Treat that mutation as continuation-owned until planning succeeds. If planning fails or the request is not eligible to become an anchor, clear continuation-owned `Store` / storage provider options before dispatch so public OpenAI keeps its existing `store:false` behavior. A response from a request that was sent with public OpenAI `store:false` must not receive continuation-anchor metadata.

`SystemPromptAsUser` exception: when this flag is true, `buildModelRequest` may prepend the system prompt to the first user message or create a synthetic user message for the system prompt. Continuation must stay disabled in that mode. Do not add a parallel system-as-user delta builder in this design; if OpenAI Responses continuation needs to cover a profile that still depends on this flag, the implementation should first remove or disable `SystemPromptAsUser` for that profile and prove ordinary `instructions` are followed.

This is a material runtime gate, not a cosmetic non-goal. `SessionConfig.SystemPromptAsUser` exists for models that ignore the `instructions` parameter for some task-delegation shapes. Phase 0B must record which OpenAI Responses profiles in the intended V1-public rollout actually run with that flag. If the intended rollout traffic mostly has `SystemPromptAsUser=true`, Phase 12B-public is not a meaningful runtime enablement and must not land until the target profiles stop requiring system-as-user prompting, or until Jesse explicitly approves a limited non-system-as-user rollout.

Context and token accounting:

- The full-history expansion in the request-shaping order above is the single source for both fallback cloning and shadow context-pressure estimation. Do not run an independent second history expansion for the shadow estimate, and do not read the shadow estimate from `FullHistoryFallbackMessages`.
- Provider-reported usage remains the billing/actual-provider-usage record.
- Context pressure and compaction decisions must not use delta-only provider input usage as the sole signal. For `responses_delta`, the session must also compute a shadow full-history request estimate using the same history that `full_history` would have sent and record that as the context-pressure input size.
- Populate `FullHistoryFallbackMessages` lazily only when the selected adapter path can actually fall back to Chat Completions. Do not clone the full-history message slice on every delta-capable round merely for accounting; the shadow estimate already reads the single full-history expansion.
- If provider input usage is larger than the local estimate, use the larger value for pressure. If local full-history estimate is larger, use it for pressure while preserving provider usage for billing.
- API logs should expose both values when they differ, e.g. `provider_input_tokens` and `context_pressure_input_tokens`.
- If the full-history expansion cannot be serialized or estimated before dispatch, or if a history-base reservation check says the expansion is stale, use `full_history` for that round, do not send a delta request, and log `continuation_shadow_estimate_unavailable`. This avoids undercounting effective context pressure. This branch is not triggered merely because `FullHistoryFallbackMessages` is nil on a path that cannot fall back to Chat Completions.
- The first implementation must compute the shadow estimate once per model round from the already-expanded full history. If that becomes expensive on long restored sessions, cache by history length, context marker, system prompt hash, model, tool fingerprint, and request fingerprint, and invalidate on history append, context boundary, tool/config change, or model change.

Launch configuration and storage policy:

- Add a launch-time setting with a global default and per-launch UI override for OpenAI Responses continuation.
- Use one runtime/config key across direct CLI, `serf serve`, and hub-launched sessions: `openai_responses_continuation`, values `off` / `auto`.
- Wire that key through `SessionConfig` as the single runtime source of truth, expose it to direct CLI users as `--openai-responses-continuation=off|auto`, and allow `SERF_OPENAI_RESPONSES_CONTINUATION` only as launch-time input into the same setting. The hub UI/global default must write the same config field rather than maintaining a parallel UI-only preference.
- Persist the resolved launch value in `ConfigSnapshot` for session reproducibility.
- Restore precedence is: explicit resume override first, persisted `ConfigSnapshot.openai_responses_continuation` second, current global default only for new sessions or old restored sessions with no persisted value.
- Changing the global default affects new launches only. To disable continuation for a restored session whose snapshot says `auto`, the resume path must pass an explicit override or the UI must let the user change the restored launch setting before resume dispatch.
- When an explicit resume override is used, record the effective value in the resumed session's snapshot so later resumes are not ambiguous.
- Add `SERF_OPENAI_RESPONSES_CONTINUATION` to the envvars registry and update `docs/environment.md`, serve/hub environment help, and launch-setting docs so the CLI, server, and UI surfaces describe the same values and retention implications.
- The shipping default must preserve existing public OpenAI retention semantics. In practice, public OpenAI/API-key continuation is off until the global default or launch override is set to `auto`.
- `off` preserves current public OpenAI behavior: Serf must not send `previous_response_id` and must not change public OpenAI requests from `store:false` to `store:true` solely to create continuation anchors.
- `auto` authorizes Serf to use endpoint-specific provider-side response storage when all continuation eligibility checks pass.
- The UI/docs copy must say that public OpenAI continuation stores response state with OpenAI because that is a provider-side retention change from today's default.
- The same UI/docs surface must disclose that provider-side stored state and server-retained prior context can have cost implications even when Serf sends a smaller local request payload; Phase 12A artifacts record observed provider token counts instead of promising savings.
- User-facing docs must state that changing or not syncing the Serf state directory across machines changes `local_scope_secret`, invalidates continuation anchors, and makes restored sessions use `full_history` until they create new eligible anchors.

Public OpenAI storage policy when continuation is `auto`:

- `full_history` anchor-producing requests that are eligible to become future continuation anchors must send `store: true`.
- `responses_delta` requests must send `store: true` so their responses can become the next anchor.
- `full_history_fallback` uses the decision table below; do not rely on ad hoc caller judgment.

| Situation | `full_history_fallback` storage behavior | Can successful fallback become new anchor? |
|---|---|---|
| Continuation-specific rejection of selected anchor, endpoint family still enabled, planner succeeded for fallback request | Send `store:true` when continuation is `auto`. If that recovery attempt fails only because continuation-owned storage hit provider quota/limits, use the one storage-demotion retry with the immutable base request and no continuation-owned storage. | Yes for the stored fallback success; no for the storage-demotion retry. |
| Unknown/expired/incompatible `previous_response_id` for one anchor only | Send `store:true` when continuation is `auto`. If that recovery attempt fails only because continuation-owned storage hit provider quota/limits, use the one storage-demotion retry with the immutable base request and no continuation-owned storage. | Yes for the stored fallback success; no for the storage-demotion retry. |
| Endpoint rejects `previous_response_id` field or required storage policy | Clear continuation-owned `store:true`; disable endpoint family/key for the session. | No. |
| Planner/canonicalization failed before dispatch | Clear continuation-owned `store:true`. | No. |
| Launch setting is `off` or endpoint family support registry has `Enabled=false` | Keep current public OpenAI behavior (`store:false` unless explicitly user-set for another reason). | No. |
| Non-continuation permanent request error | Do not change storage solely because of the error. | No response; no anchor. |

Retention lifecycle:

- Disabling continuation stops future `previous_response_id` use and stops future public OpenAI `store:true` requests made solely for continuation.
- Disabling continuation does not delete response state already stored with the provider.
- User-facing docs must state that provider retention duration and deletion controls are provider-defined. Stored provider-state deletion is out of scope for this implementation: if OpenAI exposes a supported deletion API for stored Responses state, deletion should be a separate explicit user action and phase, not an automatic side effect of changing the launch setting.
- If a request sent continuation-owned provider storage and then was canceled, interrupted, or failed before Serf persisted a final anchor, provider-side stored state may exist without a local handle. Serf cannot clean that up in V1; it must log a diagnostic such as `continuation_orphaned_provider_state_possible` on incomplete stored attempts and disclose this retention case in the same user-facing storage copy.
- Stored local transcript anchors remain in the transcript but are ignored while continuation is off or storage scope no longer matches.
- Phase 12A must estimate steady-state stored-response creation rate for the target endpoint family, using the expected eligible-hit rate and typical hub/session volume, and compare it with any known provider storage quota, retention limit, or cleanup/deletion capability. If storage growth is unbounded relative to known limits, or provider quota behavior cannot be bounded by rollback thresholds, Phase 12B must not enable that endpoint family.

Codex backend storage policy:

- The implementation must not assume public OpenAI `store` semantics apply unchanged to `/backend-api/codex/responses`.
- Codex backend continuation remains disabled until endpoint discovery has recorded the accepted request shape, Phase 12A-codex has recorded the production-path live proof artifact, and Phase 12B-codex has enabled the Codex endpoint family. Public OpenAI continuation can ship independently after its own Phase 12A-public/12B-public proof and enablement while Codex remains gated.
- The first Codex-enabled implementation must cover the Codex backend request shape with deterministic tests and one opt-in live proof.
- If the Codex backend rejects `store: true` but supports `previous_response_id` anyway, Serf should continue using the backend-accepted storage shape and document that endpoint behavior in the live test log.
- If the Codex backend requires a new explicit provider-side storage flag, that flag is also gated by the continuation launch setting.

Endpoint-family enablement gate:

- Add a small release-time support registry keyed by endpoint family, for example:

```go
type ResponsesContinuationSupport struct {
	EndpointFamily          string
	StorageShapeProven      bool
	ProductionPathProven    bool
	Enabled                 bool
	MaxAnchorAgeSeconds     int64
	StorageShapeProofID     string
	ProductionPathProofID   string
}
```

- `auto` is necessary but not sufficient. Runtime continuation requires the launch setting to be `auto`, endpoint storage policy eligibility to pass, and `Enabled=true` for the endpoint family in this registry.
- Phase 0A-audits owns adding the registry with all production endpoint-family entries defaulting to `Enabled=false`. Phase 0B-discovery records adapter-level discovery proof in `StorageShapeProofID`/docs but does not set `Enabled=true`.
- An enabled endpoint-family entry must include a non-zero `MaxAnchorAgeSeconds`. Runtime must use `full_history` for anchors older than that bound or anchors without a usable timestamp. This is a conservative guard against long-idle sessions crossing a provider retention window, not the primary safety mechanism. Local `schema.Turn.Timestamp` can be affected by clock skew, cross-machine restores, or provider-side retention changes; Phase 12A's explicit classifiable-error proof for invalid/expired anchors remains the real safety gate.
- Phase 12A-public and Phase 12A-codex record production-path proof per endpoint family while leaving `Enabled=false`; Phase 12B-public or Phase 12B-codex flips `ProductionPathProven`, `ProductionPathProofID`, `MaxAnchorAgeSeconds`, and `Enabled` for one endpoint family in a small commit that references that endpoint family's proof artifact.
- Unsupported or unproven endpoint families use `full_history` and log a diagnostic reason such as `continuation_endpoint_not_enabled`.
- If a later release disables an endpoint family that was previously enabled, runtime immediately falls back to `full_history`, stops sending continuation-owned `store:true`, and logs `continuation_endpoint_disabled_by_release`. Existing local anchors remain in transcripts but are ignored until a future release re-enables that endpoint family and all compatibility checks still pass.
- Tests that need enabled endpoint-family behavior must use a test-only injected registry or fixture value. Provider credentials, env vars, or global defaults must not temporarily flip production registry entries to `Enabled=true`.

Chat Completions fallback safety:

- A delta-shaped `llm.Request` must never be passed directly into the Chat Completions fallback path. Chat Completions has no server-side prior response state, so a delta-only message list would lose context.
- The concrete implementation shape is to pass the paired raw expanded fallback history on the same request via `FullHistoryFallbackMessages`. The OpenAI adapter's immediate fallback (`streamResponses` returns 404/422) and empty-stream fallback must clone the request, replace `Messages` with `FullHistoryFallbackMessages`, clear `PreviousResponseID`, clear continuation metadata, and build the Chat Completions body from that clone. The Chat Completions body builder then applies provider-safe replay serialization.
- If `FullHistoryFallbackMessages` is empty, the session must disable continuation before calling a stream path that can fall back to Chat Completions.
- The fallback path must be covered by a test where Responses starts as `responses_delta`, then falls back to Chat Completions, and the Chat Completions body contains full history with provider-safe tool-call replay.

## 11. Failure Handling

Continuation is optimistic and has exactly one semantic continuation-recovery dispatch per primary provider/model round. The recovery dispatch may be a `full_history_fallback` retry, or an immutable-base retry without continuation-owned storage when the first attempt failed because provider-side storage quota/limits rejected continuation storage. If a `full_history_fallback` recovery attempt itself fails only because continuation-owned storage hit provider quota/limits, Serf may use one final storage-demotion retry with the immutable base request, no `previous_response_id`, and no continuation-owned storage. That demotion exists only to preserve today's safe full-history answer path; it cannot create a new anchor. After the semantic recovery plus optional storage-demotion retry, any further permanent continuation/storage error is surfaced or passed to the normal model-fallback policy. Serf must not keep chaining per-class continuation retries.

| Provider result | Action |
|---|---|
| Unknown, expired, incompatible, or invalid `previous_response_id` | Use the single continuation-recovery dispatch as `full_history_fallback`. |
| Conversation mismatch or cannot use previous response with this request | Use the single continuation-recovery dispatch as `full_history_fallback`. |
| Codex backend rejects `previous_response_id` or the continuation storage policy as unsupported | Use the single continuation-recovery dispatch as `full_history_fallback`; disable continuation for that endpoint shape for the current session/key. |
| Provider rejects continuation-owned storage because of provider-side storage quota/limit | If the semantic continuation-recovery dispatch has not been used, use that dispatch with the immutable base request and no continuation-owned storage. If the semantic recovery was a stored `full_history_fallback` and it failed only on storage quota, use the one storage-demotion retry. Otherwise surface the error. Disable continuation for that endpoint shape for the current session/key and surface a diagnostic that provider storage quota blocked continuation. |
| 429, 5xx, network timeout, canceled context | Use the existing retry/cancel behavior; do not switch history mode merely because the failure is transient. |
| Permanent non-continuation 400, including malformed full-history replay after fallback | Surface the provider error. |

Continuation enablement depends on provider behavior for unknown or expired anchors. Phase 12A-public and Phase 12A-codex must record a production-path proof that an invalid `previous_response_id` produces an explicit classifiable error for that endpoint family and must choose the endpoint family's maximum anchor age from provider documentation, an explicit provider guarantee, or a conservative proof-backed rollout decision. If an endpoint accepts an invalid anchor and processes the delta as fresh context, or if the proof cannot distinguish erroring from silent context truncation, that endpoint family remains `Enabled=false` until a separate detection-and-retry design is specified and tested. When usage fields are available, the proof artifact should also record provider input-token usage for the invalid-anchor attempt to help detect silent-drop behavior, but usage heuristics alone do not authorize enablement.

Continuation-specific classification should use structured provider error data when available and message substring matching only as a last resort. The classifier must be narrow: a generic schema validation error for request content is not a reason to retry full history unless it names the continuation field or previous response relationship.

Codex backend classifier fixtures are provisional until Phase 12A-codex records real rejection payloads. The same change set that records Codex continuation/storage rejection shapes must extend the classifier fixture table for those exact payloads before Phase 12B-codex can enable the Codex endpoint family. If Codex returns only ambiguous generic errors, the Codex registry entry stays disabled until a separate classifier design is specified and tested.

Responses-to-Chat fallback ordering:

- When `PreviousResponseID` is present, the OpenAI adapter must classify the Responses error before applying its existing 404/422 Chat Completions fallback rule.
- Continuation-specific errors bypass Chat Completions fallback and surface to the session-level `full_history_fallback` retry on the same Responses endpoint.
- Only model/endpoint incompatibility errors may enter Chat Completions fallback.
- Empty-stream fallback follows the same rule: if the request was a continuation attempt, the adapter must first determine whether the empty stream is a continuation rejection signal. If it cannot prove model/endpoint incompatibility, it surfaces the error to the session retry path instead of falling back to Chat Completions.

Retry ordering:

```text
primary model responses_delta
  -> continuation-specific permanent rejection
  -> same primary model full_history_fallback
  -> only if that attempt fails with a model-fallback-eligible non-continuation error:
       configured model fallback attempts
```

The fallback retry is per model attempt. If model fallback changes provider endpoint, model, non-input request fingerprint, or streaming endpoint path, continuation is disabled for the fallback attempt unless exact compatibility can be proven.

Continuation-disabled state:

- Unknown, expired, or incompatible `previous_response_id` invalidates the selected anchor only. A later full-history success can create a new eligible anchor.
- Endpoint-level rejection of the `previous_response_id` field or the storage policy disables continuation for the current session only, keyed by provider, normalized endpoint URL/path, storage scope fingerprint, storage policy, and stream/non-stream request path.
- Disabled state is not process-global, not persisted across sessions, and not shared across concurrent sessions.
- Storage-quota disabled state is also session-local in V1. Concurrent or later sessions using the same key may rediscover the quota once, but each session's failure is bounded by the semantic recovery plus optional storage-demotion retry. A shared cross-process quota signal is future work because it would need its own persistence, expiry, and credential-scope privacy design.
- Concurrent restored sessions may branch from the same stored `previous_response_id`. That is safe because Serf treats the provider-side anchor as read-only input, records each session's next successful anchor in that session's transcript, and keeps disabled state session-local.
- Concurrent sessions may also produce independent stored responses from the same local ancestor. Serf must treat chaining as explicit through the selected `previous_response_id`, not as implicit provider conversation memory. If an endpoint family later requires an implicit `conversation` chain, that behavior must be proven in its endpoint-family fixtures and enforced through `ConversationIDHash` before enablement.
- Store disabled endpoint-family/key state in the live `Session` model-call state, not in transcript turns and not in adapter globals. It lasts until the session process ends or the launch/config storage-policy key changes.
- Model fallback consults disabled state before selecting a delta attempt. If fallback changes provider, endpoint, model, or storage scope, that fallback attempt starts with no reused disabled entry, but continuation is still disabled unless the normal exact compatibility and registry checks pass.
- On session restore, disabled state starts empty; persisted anchor metadata, storage scope, fingerprint, and reconstructed context marker still gate whether a delta can be attempted.
- A launch-config change creates a new storage policy key; it must not reuse stale disabled state from a different policy.

Streaming cancellation and partial responses:

- A response id observed in a stream is tentative until the stream reaches the same success condition that would normally append the assistant turn.
- If the stream is canceled, interrupted, fails after headers, or ends before a final successful response, do not persist `ResponseID`, `ResponseIDHash`, `ResponseContextMarker`, storage scope, or request fingerprint as anchor metadata for that partial response.
- Raw HTTP logs may record the partially observed id when raw logging is enabled, but transcript/API records must mark the attempt incomplete and ineligible rather than creating an anchorable assistant turn.
- If the incomplete stream was sent with continuation-owned provider storage, record `continuation_orphaned_provider_state_possible` with the attempt metadata. The diagnostic must not include raw provider handles outside raw-local logs.
- If cancellation races with adapter-internal fallback, the final attempt metadata must be cleared unless the fallback also reaches a successful final response.

## 12. Logging and Diagnostics

The transcript `api_call` entries are the authoritative session-level record because they are ordered with turns and survive transcript export. The `llm.APILogger` `api.jsonl` / raw HTTP log is the authoritative wire-level record for provider request and response bodies. Both layers must carry the same attempt identity and history-mode metadata so a transcript finding can be matched to the raw body that proves it.

`api_call` records one line per provider attempt. A round that first tries `responses_delta` and then retries as `full_history_fallback` must leave two inspectable `api_call` records. A single final record that hides the failed delta attempt is not sufficient.

Attempt records use a stable grouping schema:

```json
{
  "round": 4,
  "attempt_group_id": "01...",
  "attempt_index": 1,
  "attempt_count": 0,
  "final_attempt_count": null,
  "history_mode": "responses_delta",
  "previous_response_id_hash": "cont-handle-v1:...",
  "conversation_id_hash": "cont-handle-v1:..."
}
```

Attempt records are append-only. `attempt_group_id`, `attempt_index`, `history_mode`, and the `attempt_count` value known at write time must match across transcript `api_call`, `llm.APILogger`, and raw HTTP entries for the same provider attempt. `attempt_count=0` permanently means "unknown at the time this record was emitted"; earlier records are never updated after fallback/retry classification completes. Phase 0A-audits must audit the current `llm.APILogger` write model and choose one final-count shape before Phase 5A starts: either the terminal attempt record carries `final_attempt_count`, or a separate group-summary record with the same `attempt_group_id` carries `final_attempt_count`. Phase 5A implements only the chosen shape. Tests in early phases should assert stable group/index fields and tolerate immutable `attempt_count=0`; retry/fallback phases own `final_attempt_count` semantics.

Serf should also expose an aggregate diagnostic counter or existing metrics/log-summary field for `responses_delta`, `full_history`, and `full_history_fallback` counts by endpoint family. Metric labels must stay low-cardinality: endpoint family, history mode, and coarse diagnostic reason are acceptable; attempt ids, response hashes, storage-scope fingerprints, and provider handles must stay in logs, not metric labels. This is for rollout visibility and hit-rate debugging; correctness still comes from per-attempt logs and deterministic tests.

Provider-state handle source of truth:

- `APICall.Request` must persist redacted provider-state handle fields for request-side handles: `previous_response_id_hash`, `conversation_id_hash`, and any future continuation handle hashes.
- `APICall.Response` must persist `id_hash` alongside existing local raw `ID`.
- New assistant turns persist `ResponseIDHash` alongside raw local `ResponseID` when they are anchor-eligible. Default ATIF/export reads request-side handle hashes from `api_call` records and response-side handle hashes from `Turn.ResponseIDHash`, falling back to `APICall.Response.id_hash` when the turn hash is absent but the API record exists.
- Secret rotation never rewrites persisted handle hashes; default export emits existing `ResponseIDHash` / `APICall.Response.id_hash` values as opaque redacted handles. For legacy transcripts missing persisted response-handle hashes, default export may compute a current-version redacted hash from raw local `ResponseID` only when `local_scope_secret` is available and must label the current hash version. If no current secret is available, omit the redacted response handle and emit a diagnostic such as `response_id_hash_unavailable`; never emit the raw provider handle in default mode.
- When newer transcripts already contain persisted handle hashes but the current `local_scope_secret` is missing or rotated, readers keep those hashes as opaque redacted identifiers for display/export but must not treat the turn as a continuation anchor unless the stored storage-scope hash version still matches the active scope rules.
- Doctor/export diagnostics should distinguish "hash unavailable" from "hash present but current `local_scope_secret` cannot validate anchor eligibility" so a user can see when secret rotation or state-dir replacement invalidated continuation anchors.
- Local diagnostic raw export may resolve `previous_response_id` by matching `previous_response_id_hash` to an earlier assistant turn's raw `ResponseID` in the same local transcript. If no local match exists and raw HTTP logging was not enabled, omit the raw previous handle and emit a diagnostic instead of guessing or scraping unrelated logs.

Adapter-internal endpoint fallback must not be hidden. The OpenAI adapter currently owns immediate Responses-to-Chat fallback and empty-stream fallback, so the logging contract needs a concrete handoff:

- Add an attempt recorder to `llm.APILogContext` or an equivalent context callback that adapters can call before they switch endpoints. The session owns the callback and all transcript writes; the adapter must never append transcript records directly.
- The callback receives a value-like attempt record containing `attempt_group_id`, `history_mode`, endpoint URL/family, request/response redacted handle hashes, request body snapshot or raw-log pointer, status/error, streaming flag, and finality/incomplete flags. It must not receive raw credentials.
- The session callback allocates the next 1-based `attempt_index`, returns or stamps it onto the adapter-reported record, serializes the record into transcript `api_call` records in dispatch order under the same turn-order discipline as normal `logAPICall`, and writes matching `llm.APILogger` / raw-log entries with the same attempt identity.
- The adapter records the failed/empty Responses attempt with its request body, endpoint, status/error, and original history mode before switching endpoints.
- The adapter records the successful Chat Completions fallback as a separate attempt with `history_mode=chat_completions_fallback`.
- The adapter propagates the final attempt metadata back to the session wrapper so transcript `api_call`, `llm.APILogger`, raw HTTP logs, and persisted assistant-turn metadata agree.

`HistoryModeChatFallback` is stamped only by this adapter fallback attempt recorder. Session request shaping must choose among `full_history`, `responses_delta`, and `full_history_fallback`; it must not independently emit `chat_completions_fallback` records.

Each attempt should identify request history mode and continuation metadata:

```json
{
  "history_mode": "responses_delta",
  "previous_response_id_hash": "cont-handle-v1:...",
  "delta_turn_count": 1,
  "delta_turn_kinds": ["TOOL_RESULTS"],
  "anchor_turn_index": 42,
  "anchor_response_endpoint": "https://api.openai.com/v1/responses"
}
```

Default transcript/API examples must use redacted provider-handle fields. Raw `previous_response_id`, `response_id`, and conversation ids appear only in raw HTTP request/response bodies or, after Phase 11, in the explicit `raw-local` diagnostic export mode.

For a continuation rejection followed by fallback, every attempt must be visible:

```text
round N attempt 1: history_mode=responses_delta, error=...
round N attempt 2: history_mode=full_history_fallback, previous_response_id omitted
optional round N attempt 3: history_mode=full_history, no continuation-owned storage, storage-demotion retry
```

When `SERF_LOG_RAW_HTTP=1`, the raw request body should prove the same behavior:

- `responses_delta` raw body includes `previous_response_id` and omits historical assistant `function_call` items.
- `full_history_fallback` raw body omits `previous_response_id` and includes provider-safe historical tool-call arguments.
- public OpenAI `store:true` appears only when launch configuration allows continuation; `store:false` remains visible when continuation is off.

Provider-state handles:

- `response_id`, `previous_response_id`, and conversation ids are sensitive provider-state handles.
- The local transcript may store raw `ResponseID` because continuation/resume requires it, but only when the transcript/API sinks are private or have been hardened. Transcript files that contain raw provider handles inherit the same sensitivity as local session state.
- Transcript files, raw HTTP logs, and local diagnostic exports that contain raw provider-state handles must be created with private user-only permissions (`0600` for files, `0700` for containing directories) or inside an already-private Serf state directory with equivalent effective permissions.
- On first open/write after this change, Serf should check the active transcript, API log, raw HTTP log, and containing directories that may contain raw provider handles. If the current user owns a too-permissive file or directory, tighten it to the private mode above before writing new raw provider handles.
- If Serf cannot harden an active transcript/API log that would receive raw provider handles, the model call may still proceed and ordinary assistant content may still be appended, but all raw provider-handle writes for that attempt must be suppressed: do not persist `Turn.ResponseID`, `APILogResponse.ID`, raw `previous_response_id`, raw conversation ids, or anchor eligibility metadata. Log a diagnostic and use `full_history` for future requests. If raw HTTP logging is enabled and its target cannot be made private, disable raw HTTP logging for that session and log a diagnostic rather than writing raw bodies to a permissive file.
- Symlinks, non-owned files/directories, hard-linked files, and world/group-searchable session directories must be treated as unsafe for raw provider-handle writes unless the hardening code can prove the final target is owned by the current user and privately permissioned. Do not chmod through an untrusted symlink.
- On Windows or any platform where POSIX `0600`/`0700` semantics cannot prove user-only access, use an ACL/equivalent check that proves only the current user and required system accounts can read the file. If Serf cannot prove an equivalent private ACL, use the same fail-closed behavior: suppress raw provider-handle writes, disable raw HTTP body logging, omit anchor eligibility metadata, and continue ordinary full-history operation with diagnostics.
- Existing historical raw response IDs are not removed in place. Default export and doctor/dashboard surfaces must treat older transcripts as sensitive local state and redact by default.
- Keep existing raw local fields such as `APILogResponse.ID` for transcript compatibility and resume diagnostics, but add separate redacted/hash fields for user-facing navigation. Doctor/dashboard consumers must prefer the redacted field by default and treat the raw field as sensitive local debug data.
- During the transition, default doctor/dashboard views must not display existing raw `APILogResponse.ID` or `Turn.ResponseID`. If a redacted hash is unavailable, omit the handle and show a diagnostic placeholder rather than falling back to raw.
- Structured continuation metadata should record a stable redacted handle/hash for navigation. Raw handles appear in raw HTTP body logs only when raw logging is explicitly enabled.
- Raw HTTP logs are opt-in and must document that they include provider-state handles.

V1-blocking versus deferrable support:

- Blocking before any endpoint-family enablement: private local file/directory handling on the supported launch platform, fail-closed raw-handle suppression when privacy cannot be proven, default export redaction, and transcript/API/raw-log attempt fields required to debug continuation.
- Deferrable beyond the first endpoint-family enablement: native Windows ACL implementation on platforms not in the first supported deployment set, rich dashboard presentation beyond omitting raw handles, and aggregate diagnostics as durable metrics. Until those land, non-POSIX targets must follow the fail-closed ACL rule above, dashboard surfaces must omit raw handles, and aggregate counts may be emitted through existing log-summary diagnostics.
- Retroactive hardening of historical transcripts that already contain raw `ResponseID` values is a separate security track. V1 blocks only on safe handling for new continuation-owned raw provider-handle writes and on default redaction for shareable exports. Existing historical transcripts remain sensitive local state; doctor/export surfaces should warn rather than making continuation depend on rewriting or fully hardening every old file.

- ATIF/export has two explicit modes:
  - default redacted export emits `response_id_hash`, `previous_response_id_hash`, `conversation_id_hash`, `response_endpoint`, `response_storage_scope_fingerprint`, `response_request_fingerprint`, and `response_context_marker`;
  - local diagnostic raw export additionally emits `response_id`, `previous_response_id`, and `conversation_id`.
- Phase 1C-export introduces a CLI/export option such as `--export-provider-handles=redacted|raw-local`, defaulting to `redacted`. In the V1-public cut before Phase 11, `raw-local` must be recognized but rejected with an explicit "not implemented until local diagnostic export" error; it must not silently fall back to redacted or emit partial raw handles. Hub/export UI must use the same enum once Phase 11 wires raw-local export.
- Phase 1C-export must audit known ATIF/export consumers before changing the default output shape from raw `response_id` to redacted handles. If any consumer depends on raw `response_id`, either Jesse must explicitly approve the breaking default-export change or Phase 1C-export must preserve a compatible field shape while still preventing raw provider handles in shareable default exports. The spec's intended security posture is redacted-by-default; the implementation must make the compatibility decision explicit in the Phase 1C-export proof artifact.

Surface matrix:

| Surface | Raw provider handles | Redacted/hash handles | Default | Permissions / audience |
|---|---|---|---|---|
| Local transcript turns | `Turn.ResponseID` for successful local Responses anchors. | Add hash fields for user-facing navigation when available. | Raw retained for resume. | Private Serf state; `0600` files or equivalent state-dir protection. |
| Transcript `api_call` | Existing local `APILogResponse.ID` may remain for compatibility. | `APICall.Request.previous_response_id_hash`, `conversation_id_hash`, `APICall.Response.id_hash`. | Hashes preferred by readers. | Private Serf state; raw fields treated as local debug only. |
| `llm.APILogger` `api.jsonl` | Existing local response IDs may remain. | Same attempt/group/hash fields as transcript. | Hashes preferred by readers. | Private local diagnostic log. |
| Raw HTTP log | Raw request/response bodies may include provider handles. | Optional hashes in wrapper metadata. | Off unless raw logging enabled. | Explicit opt-in, private file permissions. |
| Doctor/dashboard | Must not display raw handles by default. | Use hash/redacted fields. | Redacted. | User-facing local UI. |
| Default ATIF/export | No raw provider handles. | Hashes plus endpoint/fingerprint/context metadata. | Redacted. | Shareable diagnostic artifact. |
| Local diagnostic export | Raw handles allowed only with explicit `raw-local` after Phase 11. | Include hashes too. | Off. | Local-only, private permissions. |

Migration note: existing transcripts may already contain raw `ResponseID` fields. Treat them as sensitive local state after this change; docs and doctor/dashboard surfaces must say older transcripts may contain raw provider-state handles and should not be shared as public artifacts without redaction.

## 13. Provider-Safe Replay Sanitizer

The existing sanitizer is still required for full-history replay. It should be documented and named as replay serialization, not as the conceptual malformed-tool-call handler.

Normal malformed-tool-call handling remains:

```text
model emits raw malformed arguments
  -> Serf stores raw arguments
  -> tool execution parses/validates
  -> parse failure becomes an error tool result
  -> Responses continuation sends only the tool result
```

Full-history replay, including fallback and non-Responses providers, still needs provider-safe historical tool-call serialization so provider validation cannot block the model from seeing error results.

## 14. Implementation Phases

Implement in this order so each phase has a deterministic proof point before the next one depends on it:

Every phase proof must re-assert the substrate facts it depends on before relying on earlier assumptions. For example, a phase that threads assistant metadata must confirm the current `appendAssistantTurn` call sites it touches; a phase that relies on serialized model calls must cite the current `ProcessInput -> processOneInput` turn loop, queue behavior, or reservation guard it depends on. If the substrate changed, the phase either updates the proof and tests or stops until the dependency is re-scoped.

The first user-visible V1-public cut is public OpenAI runtime enablement: Phases 0A through 10, plus 12A-public and 12B-public, with 1C-export/1C-surfaces default redaction still blocking before any new provider-handle metadata can appear in shareable exports. Shared plumbing and deterministic Codex request-shape fixtures may land in this cut, but the Codex registry entry remains disabled until its own 12A/12B. Phase 11 local raw diagnostic export and 12A/12B-codex are follow-on unless Jesse explicitly pulls them into the same release.

| Phase | Work | Proof |
|---|---|---|
| 0A-audits | Add the endpoint-family support registry with production defaults `Enabled=false`, and audit per-session model-call serialization plus the `llm.APILogger` write model. | Deterministic tests prove `auto` plus `Enabled=false` still uses full history. The proof artifact has three separately checkable lines: registry defaults; serialization audit citing the concrete turn-loop/queue or lock/reservation evidence and an explicit `reservation required: yes|no` verdict; and logging audit with final-attempt-count shape. This phase does not enable runtime continuation by itself. |
| 0B-discovery | Add narrow adapter-level endpoint discovery fixtures and record the storage-shape matrix. | Public OpenAI and Codex backend each get deterministic request-shape checks plus opt-in live discovery for `store`/storage shape, valid `previous_response_id`, invalid/expired `previous_response_id`, `previous_response_id` plus `conversation` co-presence, scripted malformed-tool-call recovery payload sizes, and intended-rollout `SystemPromptAsUser` profile usage. The artifact records the expected runtime resolution for co-presence: provider accepts it, adapter omits one handle by a documented canonicalization rule, or continuation stays disabled for that shape. Live discovery is explicit opt-in and does not block 0A audits from landing, but the target endpoint family's artifact must exist before Phase 7 and before Phases 1A-11 are treated as committed implementation work. |
| 1A-schema | Add optional turn metadata, request control-plane fields, final-attempt metadata fields, and redacted provider-handle schema fields on API call records. | Old transcript fixtures still load; new metadata round-trips; old snapshots/transcripts with absent fields stay non-anchorable rather than failing. |
| 1A-attempt | Add the minimal single-attempt index allocator/stamper and thread final-attempt metadata through assistant persistence. | Single-attempt model calls stamp `AttemptIndex=1`; multi-attempt group logging and adapter fallback allocation remain in 5A/5B. The proof enumerates every `appendAssistantTurn` call site, including streaming, and records how each supplies or clears final-attempt metadata. |
| 1B-secret | Add the local continuation redaction/HMAC utility and create `local_scope_secret` with private permissions on the supported launch platform. | Secret creation uses private file permissions; missing or uncreatable secret fails closed to `full_history` with diagnostics and no continuation-owned storage. |
| 1B-posix | Harden transcript/API/raw-log file creation for new raw provider-handle writes on the supported POSIX path. | New raw-handle transcript/API/raw-log files use private permissions; too-permissive owned paths are tightened; non-owned, symlinked, hard-linked, or non-private paths fail closed with diagnostics and without new raw provider-handle writes. Non-POSIX ACL support remains fail-closed until implemented. |
| 1C-export | Add default ATIF/export redaction for provider handles and the shared `redacted|raw-local` export enum. | Request/response handle hashes are recorded without raw handle leakage in default exports; `raw-local` is recognized but rejected until Phase 11; compatibility audit records whether default raw `response_id` output changed or a compatible redacted shape was preserved. |
| 1C-surfaces | Add doctor/dashboard redaction for provider handles. | Doctor/dashboard views prefer redacted/hash handles and omit unavailable hashes rather than falling back to raw `APILogResponse.ID` or `Turn.ResponseID`. |
| 2A-config | Add runtime configuration for OpenAI Responses continuation and provider-side storage, wire direct CLI / `serf serve` / hub launch config to the same setting, and update `ConfigSnapshot` restore conversion. | Config tests prove direct CLI, `serf serve`, and hub-launched sessions resolve the same value; golden snapshots cover persisted values and old snapshots with no value; restore-precedence tests cover global `auto -> off`, global `off -> auto`, and explicit resume override. With endpoint support registry `Enabled=false` and planner/storage eligibility absent, `auto` still sends no `previous_response_id` and does not change public OpenAI from `store:false` to `store:true`; positive `store:true` assertions wait until planner/storage eligibility exists. |
| 2B-docs-help | Wire envvars registry and docs/help surfaces for the same launch setting. | `SERF_OPENAI_RESPONSES_CONTINUATION`, `docs/environment.md`, direct CLI help, serve help, and hub launch-setting docs describe the same values, defaults, restore behavior, and retention/cost implications. |
| 3A-auth | Add auth-scope identity propagation and the pure planner helper boundary. | Unit tests prove adapters receive sanitized `AuthScopeIdentity` produced with the Phase 1B-secret HMAC utility, and the pure planner helper cannot read raw credentials, bearer tokens, OAuth tokens, or raw org/project identifiers. Storage-scope fields on the planner result are present but zero/stubbed until Phase 4A. |
| 3B-fingerprint | Add `llm.Client` planner access, adapter-owned request fingerprint canonicalization, and production-prompt determinism checks. | Unit tests prove the session obtains request-shape fields through `Client.PlanResponsesContinuation` without duplicating adapter body-building logic. Tests render the real production system prompt with fixed `WorkingDir`, `Platform`, `OSVersion`, `Today`, `Model`, and `KnowledgeCutoff` values and prove stable fingerprints, then render two different `Today` values and prove the expected fingerprint mismatch or explicit normalization behavior. Codex fingerprint tests cover the fields known at this phase; later Codex storage-field discovery must update these tests and bump the fingerprint version before Phase 12B-codex. |
| 4A | Add storage-scope fingerprinting, storage-policy eligibility, and continuation storage override ownership. | Unit tests cover auth/storage-scope mismatch, `off -> auto`, `auto -> off`, disabled endpoint registry, storage field exclusion from request fingerprints, exact storage-scope enforcement, and preservation of explicit user/provider storage settings when continuation-owned storage is cleared. |
| 4B | Add context-boundary marker eligibility, restore-derived visible boundary membership, and `SystemPromptAsUser` disablement. | Unit tests cover old turns, checkpoint/summary boundaries, restore-derived visible boundary membership, missing context marker metadata, `SystemPromptAsUser` full-history behavior, and older-version transcript rewrites that drop optional metadata. |
| 4C | If Phase 0A's serialization audit requires it, add a history-base reservation check before later phases depend on anchor selection. If the audit proved serialization, add only a small regression test that records the audited guarantee. | If reservation is required, unit tests cover history mutation between anchor selection and dispatch, overlapping request rejection/reservation, and no anchor selection from in-flight turns. Phase 4D-i must not start until either the Phase 0A audit proved serialization or the reservation check exists. |
| 4D-i | Add the minimum fallback-capability guard, then prove anchor production in a fake-provider slice behind an injected enabled registry. | Entry requires Phase 3B planner/fingerprint, Phase 4A storage overrides/scope, Phase 4B epoch gating, and Phase 4C audit/reservation outcome. Before any real session delta dispatch can run, session code must prove the selected path cannot fall back to Chat Completions or must have populated `FullHistoryFallbackMessages`; otherwise it uses `full_history`. The scripted Responses-only fake has no Chat Completions fallback path and asserts that fact. A real `Session` wired only to that fake adapter with the production prompt template and fixed environment-template inputs sends an eligible full-history request with continuation-owned `store:true`, `AttemptIndex=1`, and persisted anchor metadata. This phase does not enable real OpenAI adapter delta dispatch. |
| 4D-ii | Extend the fake-provider slice so the next request consumes the Phase 4D-i anchor as a delta. | The second request goes through real session anchor selection and delta dispatch against the Responses-only fake adapter, and the fake sees `previous_response_id`, delta-only input for one new user turn, matching request/storage fingerprints, `AttemptIndex=1`, and no production registry enablement or live network. A separate real OpenAI adapter regression proves the same session shape still uses `full_history` when `CanFallbackToChat=true` and `FullHistoryFallbackMessages` is absent. Full fallback cloning, retry, multi-turn delta, and persistence-on-fallback edge cases remain in later phases. |
| 5A | Add attempt-group identity and minimal transcript/API/raw logging fields for session-owned provider attempts. | Tests prove session-level attempts produce ordered transcript `api_call` records plus matching `llm.APILogger`/raw-log entries with stable `attempt_group_id`, 1-based `attempt_index`, history mode, endpoint, and redacted handle fields before classifier, retry, or delta logic depends on those fields. Raw-log proof must be verified for continuation rejections delivered as stream errors, not only immediate HTTP errors. Single-attempt dispatch reuses the 1A-attempt stamping path rather than creating a parallel index mechanism. |
| 5B | Add the adapter-callable attempt recorder for endpoint fallback. | Tests prove the session-owned callback converts adapter-reported immediate and empty-stream Responses-to-Chat fallback attempts into separate ordered records, allocates attempt indices, and keeps transcript attempts matched to raw body attempts without letting the adapter write transcripts directly. |
| 6 | Complete `FullHistoryFallbackMessages` Chat Completions fallback cloning for fallback-capable adapter paths, while continuation itself still uses full history outside the Phase 4D-i/4D-ii fake-provider slice. | Tests drive a test-only delta-shaped request produced by the same request-shaping helper used by Phase 4D-ii and Phase 9, or a thin wrapper over it, through fallback-capable adapter paths and prove Chat Completions receives full-history fallback messages, never the delta request, before broad session-level delta selection can be enabled. |
| 7 | Add continuation-error classification and Chat fallback ordering. | Entry requires the target endpoint family's Phase 0B discovery artifact with real invalid/expired `previous_response_id` rejection payloads or a recorded conclusion that the endpoint family cannot continue. Tests prove those exact payloads classify as continuation rejection and bypass Chat fallback, while adjacent generic schema/content errors and model/endpoint incompatibility do not. Do not invent provider payload fixtures. |
| 8 | Add same-Responses-endpoint full-history retry before model fallback using a test-only delta-shaped request. | Tests use the same request-shaping helper or thin wrapper from Phase 6 and prove continuation-specific rejection retries once as `full_history_fallback` before configured model fallback for the adapter/session retry harness, without claiming real session anchor selection is complete. |
| 9 | Complete session-level `responses_delta` behavior beyond the Phase 4D-ii happy path and keep full-history sanitizer on replay paths. | The deliverable checklist is finite: real-session anchor selection wiring; real-path item-kind gate enforcement; `call_id` linkage validation; intervening-non-anchorable-assistant handling; media/provider-hosted/reasoning gating on the real path; and the Phase 8 retry repeated through real session anchor selection. Scripted provider tests prove malformed historical tool calls are not resent on delta, sanitized on full history, never sent delta-only into Chat Completions, and cover every checklist item. |
| 10 | Add context-pressure accounting for delta requests. | Tests prove `responses_delta` uses provider usage for billing but a shadow full-history estimate for compaction pressure. |
| 11 | Complete local diagnostic raw export and hub/export UI mode plumbing. | Default export behavior from Phase 1C-export remains redacted; explicit local diagnostic export emits raw provider-state handles only when requested through the shared `redacted|raw-local` enum. |
| 12A-public | Add the opt-in production-path live proof harness for public OpenAI and record a durable proof artifact, with the public OpenAI registry entry still disabled. | The public OpenAI live proof command is explicit opt-in and produces a reviewed artifact documenting endpoint storage shape, response-id reuse, two successful branches from one stored anchor, invalid/expired `previous_response_id` behavior, the proposed `MaxAnchorAgeSeconds`, observed status/latency, provider input-token counts for delta versus full-history on the same scripted conversation, observed prompt-cache hit-rate impact when the provider reports cache details, concrete rollback thresholds for eligible-hit-rate floor plus prompt-cache hit-rate floor plus storage-quota/error/cost/rate-limit ceilings, and any rate-limit behavior. Deterministic tests remain the semantic gate and runtime continuation remains disabled. |
| 12A-codex | Add the opt-in production-path live proof harness for the Codex backend and record a durable proof artifact, with the Codex backend registry entry still disabled. | The Codex backend live proof command is explicit opt-in and produces a reviewed artifact documenting endpoint storage shape, response-id reuse, two successful branches from one stored anchor, invalid/expired `previous_response_id` behavior, the proposed `MaxAnchorAgeSeconds`, observed status/latency, provider input-token counts for delta versus full-history on the same scripted conversation when usage fields are exposed, observed prompt-cache hit-rate impact when the provider reports cache details, concrete rollback thresholds for eligible-hit-rate floor plus prompt-cache hit-rate floor plus storage-quota/error/cost/rate-limit ceilings, any discovered storage/retention fields requiring a fingerprint-version update, and any rate-limit behavior. Deterministic tests remain the semantic gate and runtime continuation remains disabled. |
| 12B-public | Enable the public OpenAI registry entry in a small follow-up commit that references its matching Phase 12A-public proof artifact. | Review can verify the exact public OpenAI proof artifact, `ProductionPathProofID`, `MaxAnchorAgeSeconds`, numeric rollback thresholds, security-review sign-off for public OpenAI `store:false -> true` retention posture, and minimal `Enabled=true` registry flip without changing the Codex backend entry. 12B-public is rejected if the 12A-public artifact omits numeric thresholds. |
| 12B-codex | Enable the Codex backend registry entry in a small follow-up commit that references its matching Phase 12A-codex proof artifact. | Review can verify the exact Codex backend proof artifact, `ProductionPathProofID`, `MaxAnchorAgeSeconds`, numeric rollback thresholds, and minimal `Enabled=true` registry flip independently of the public OpenAI entry. 12B-codex is rejected if the 12A-codex artifact omits numeric thresholds. |
| 12C-rollback | If rollout diagnostics violate the matching Phase 12A artifact's expectations, disable the affected endpoint-family registry entry in a small rollback commit. | Review can verify the rollback changes only the affected endpoint family's `Enabled=false` state and diagnostic/proof reference, without deleting local anchors or changing user launch settings. |

## 15. Tests

Default tests must use fake providers, fake HTTP servers, or scripted adapters. Provider credentials alone must never trigger live requests.

Required deterministic coverage below is grouped by scenario, and each group names the owning implementation phase when it is not obvious from the phase table. A test must not be implemented before its owning phase unless it is explicitly marked as a disabled fixture or audit artifact for that phase.

- Tool call with malformed args followed by an error tool result (Phase 9):
  - request uses `previous_response_id`;
  - request input contains only the tool result;
  - historical malformed `function_call` is not resent.
  - the tool result's `call_id` is linked to a function call owned by the anchor response's provider-side state; an unprovable or orphaned `call_id` uses `full_history`.
- Normal next user turn after a stored OpenAI Responses id (Phase 4D-ii for fake path, Phase 9 for real session path):
  - request uses `previous_response_id`;
  - request input contains only the new user turn.
- Missing response metadata (Phase 4B):
  - request uses full history;
  - provider-safe replay sanitizer is active.
- Intervening non-anchorable assistant turn (Phase 9):
  - history contains an older eligible Responses anchor followed by a newer non-anchorable assistant turn and later user/tool-result input;
  - request uses `full_history` rather than selecting the older anchor;
  - full-history replay uses provider-safe serialization and does not orphan tool results.
- Request fingerprint mismatch (Phase 3B-fingerprint):
  - response id exists, but changed instructions/tools/tool choice/reasoning/provider options disable continuation.
  - the real production system prompt template, rendered with fixed `WorkingDir`, `Platform`, `OSVersion`, `Today`, `Model`, and `KnowledgeCutoff` values, produces stable fingerprints across otherwise identical requests.
  - the real production system prompt template rendered with two different `Today` values produces the expected fingerprint mismatch, unless a future explicit canonicalization layer normalizes it and has its own tests.
  - if any non-`Today` environment-template field can change mid-session for a real request path, that path uses `full_history` until an explicit canonicalization layer covers it.
  - deterministic prompt construction keeps stable `instructions` eligible for continuation when the request shape is otherwise unchanged.
  - volatile provider-visible instructions without a tested normalization layer use `full_history`, send no continuation-owned storage, and log `continuation_instructions_unstable`.
  - canonicalization treats map ordering and omitted/default fields deterministically.
  - Codex request fingerprints are computed after Codex-specific field filtering.
  - changed prompt-cache controls change the request fingerprint and disable continuation.
  - `store` and continuation-owned retention options never affect request fingerprint compatibility; storage compatibility is enforced only through storage scope.
  - `conversation` is excluded from request fingerprint compatibility and can disable continuation only through `ContinuationStorageScope.ConversationIDHash`.
- Model compatibility (Phase 7/9):
  - compatibility uses persisted resolved request model, not provider-returned `resp.Model` aliases;
  - changed resolved model disables continuation even when provider-returned aliases match.
- Storage scope mismatch (Phase 4A):
  - response id exists, but changed base URL, endpoint path, OpenAI org/project, API-key identity hash, ChatGPT account/workspace identity hash, conversation id, or storage policy disables continuation.
  - absent conversation handles on both the anchor and current request remain compatible.
  - an explicit current conversation with no matching anchor conversation hash disables continuation, and an anchor conversation hash with no current conversation disables continuation.
  - OpenAI adapter construction receives `AuthScopeIdentity`; planner code never hashes raw API keys, bearer tokens, OAuth tokens, or refresh tokens.
  - storage-scope fingerprints and provider-handle redaction hashes use distinct derived subkeys from `local_scope_secret`;
  - OpenAI org/project identifiers are hashed before planner/storage-scope metadata and are not persisted raw.
  - changing credential hash version or local scope secret invalidates old anchors and uses `full_history`.
  - missing or uncreatable `local_scope_secret` uses `full_history`, sends no continuation-owned `store:true`, and records `continuation_scope_secret_unavailable`.
  - public OpenAI `off -> auto` and `auto -> off` launch-config changes disable reuse of anchors produced under the previous policy.
  - public OpenAI and Codex backend storage scopes differ even when model and request fingerprint match.
  - session code receives a sanitized storage-scope fingerprint and never receives raw API keys or bearer/OAuth tokens for hashing.
- `SystemPromptAsUser` (Phase 4B):
  - request uses full history and does not set `previous_response_id`.
- Media/provider-hosted inputs (Phase 7/9):
  - prior image, document/file, web-search, or provider-hosted reference inputs disable continuation in the first implementation;
  - endpoint-specific fixtures are required before enabling continuation for those item types.
- Reasoning-model continuity (Phase 7/9):
  - unchanged reasoning/include settings remain continuation-eligible when all other checks pass;
  - changed reasoning effort, encrypted-reasoning settings, or reasoning-summary inclusion changes the request fingerprint and disables continuation;
  - provider-exposed reasoning items in local transcript history use `full_history` until endpoint-specific fixtures prove safe continuation.
  - reasoning content attached to an assistant turn is detected by the item-kind eligibility gate, not missed because it is not a separate turn kind.
- Public OpenAI storage policy (Phase 4A and Phase 12A/12B-public):
  - an eligible full-history public OpenAI Responses request under `auto` plus an injected enabled registry sends continuation-owned `store:true` and persists anchor metadata on success.
  - anchor-producing and delta public OpenAI Responses requests send `store:true` when continuation is enabled.
  - public OpenAI requests keep `store:false` and do not send `previous_response_id` when continuation/storage is disabled.
  - continuation-owned storage overrides are distinguishable from explicit user/provider storage settings and clearing an override never strips explicit settings.
- Runtime configuration (Phase 2A-config and Phase 2B-docs-help):
  - direct CLI, `serf serve`, env launch input, and hub launch UI all resolve through the same `openai_responses_continuation` runtime field.
  - envvars registry, `docs/environment.md`, serve/hub help, and launch-setting docs list `SERF_OPENAI_RESPONSES_CONTINUATION` with the same accepted values.
  - launch-setting docs disclose provider-side retention and possible provider-token/cost implications without promising billed-token savings.
  - `ConfigSnapshot` persists the resolved launch value; restore precedence is explicit override, then snapshot, then current global default only when no snapshot value exists.
  - resume tests cover global `auto -> off`, global `off -> auto`, and explicit resume override.
  - endpoint-family support registry defaults block continuation when `Enabled=false`, even if launch config is `auto`.
  - with registry disabled and planner/storage eligibility absent, `auto` sends no `previous_response_id` and keeps public OpenAI `store:false`.
  - deterministic tests use an injected test registry to enable endpoint families without mutating production defaults.
  - enabled test registry fixtures require non-zero `MaxAnchorAgeSeconds`; stale or timestamp-missing anchors use `full_history` and can be replaced by the next eligible full-history success.
- Phase 0A/0B audits and discovery:
  - the proof artifact records registry defaults, storage-shape matrix/discovery fixtures, per-session model-call serialization audit, and `llm.APILogger` write-model audit as separate line items;
  - the per-session model-call serialization audit cites concrete evidence from the current `ProcessInput -> processOneInput` turn loop, `session_queue.go` queue behavior, relevant lock discipline, or a new reservation guard, and records whether a history-base reservation is required before 4D-i;
  - the `llm.APILogger` write-model audit records the chosen `final_attempt_count` shape before 5A.
  - 0B discovery records valid and invalid/expired `previous_response_id` behavior before Phases 1A-11 are treated as a committed implementation path.
  - 0B discovery records whether each endpoint family accepts `previous_response_id` and `conversation` together; if it does not, deterministic fixtures prove the selected runtime resolution.
  - 0B discovery records intended-rollout `SystemPromptAsUser` profile usage and blocks V1-public runtime enablement when the target traffic mostly requires system-as-user prompts, until that mode is removed/disabled for the target profiles or Jesse approves a limited non-system-as-user rollout.
  - 0B discovery records the rough eligible-hit-rate expectation and the main blockers before the implementation path is committed.
  - 0B discovery reports gross omitted historical-item bytes, added continuation-overhead bytes, and net request body-size delta for the scripted malformed-tool-call recovery probe.
- Real session happy path (Phase 4D-i/4D-ii fake path, Phase 9 real path):
  - a real `Session` with the production prompt template and fixed environment-template inputs, an injected enabled registry, and a Responses-only fake adapter produces a stored full-history anchor with `ResponseRequestFingerprint`, `ResponseStorageScopeFingerprint`, `ResponseContextMarker`, and `AttemptIndex=1`, then a `responses_delta` request with `previous_response_id` and delta-only input.
  - the Phase 4D fake-adapter slice proves real session shaping only; a real OpenAI adapter path with Chat fallback capability still uses `full_history` until `FullHistoryFallbackMessages` fallback cloning is complete.
- Planner access (Phase 3A-auth and 3B-fingerprint):
  - session code obtains request fingerprints through `llm.Client.PlanResponsesContinuation`;
  - planner fingerprint/scope construction is performed by a pure helper that receives only provider-facing body data and sanitized identity/scope inputs, never raw transport credentials;
  - Phase 3B-fingerprint tests assert request-shape fields while storage-scope fields are present but zero/stubbed until Phase 4A;
  - unsupported providers and planner errors use `full_history` with diagnostics.
- Continuation-specific 400 (Phase 7/8/9):
  - Phase 7 uses real rejection payloads recorded by the target endpoint family's Phase 0B discovery artifact; guessed provider payload fixtures are not allowed.
  - at most one semantic continuation-recovery dispatch occurs per primary provider/model round, with only the storage-quota demotion exception below;
  - the semantic recovery dispatch uses `full_history_fallback` for continuation-anchor rejection or the immutable base request for first-attempt continuation-storage quota rejection;
  - if stored `full_history_fallback` fails only on continuation-owned storage quota, one final storage-demotion retry uses the immutable base request without continuation-owned storage and cannot create an anchor;
  - a second permanent continuation/storage failure after the semantic recovery plus optional demotion does not trigger another continuation retry;
  - all attempts are logged.
  - early attempt records keep immutable `attempt_count=0` when the group size was unknown at write time, and the terminal attempt or group summary records `final_attempt_count`.
  - `final_attempt_count` is exposed through `ModelAttemptMetadata.FinalAttemptCount` and matches across transcript `api_call`, `llm.APILogger`, and raw HTTP entries for the terminal attempt or group summary.
  - `attempt_index` is 1-based and stable across transcript `api_call`, `llm.APILogger`, and raw HTTP entries for the same provider attempt.
  - raw HTTP logging captures continuation rejection request/response bodies for stream-error paths as well as immediate HTTP errors.
  - Phase 9 repeats the retry through real session anchor selection and `responses_delta`, not only the Phase 8 test-only delta-shaped request.
  - Phase 9 covers real-session anchor selection wiring, real-path item-kind gate enforcement, `call_id` linkage validation, intervening-non-anchorable-assistant handling, and media/provider-hosted/reasoning gating on the real path.
- Adapter-internal endpoint fallback (Phase 5B/6/7):
  - Phase 6 and 8 test-only delta-shaped requests are constructed by the same request-shaping helper as Phase 4D-ii/9, or by a thin wrapper over that helper, so fallback/retry tests do not validate a parallel request shape.
  - when `previous_response_id` is present, continuation-specific 400/404/422 errors bypass Chat Completions fallback and surface to the session full-history retry path;
  - model/endpoint 404/422 errors without continuation semantics still enter Chat Completions fallback when fallback is otherwise eligible;
  - immediate 404/422 Responses-to-Chat fallback logs both endpoint attempts;
  - empty Responses stream fallback logs the empty Responses attempt and the Chat Completions attempt;
  - successful Chat Completions fallback is not persisted as an eligible Responses anchor.
- Provider-state handle redaction (Phase 1B-secret, 1B-posix, 1C-export, 1C-surfaces):
  - raw local transcript fields remain available for compatibility;
  - `cont-handle-v1:<kind>:<hmac>` hashes are produced with `local_scope_secret` for response id, previous response id, and conversation id handles;
  - `local_scope_secret` is stored under the resolved private Serf state directory outside per-session transcripts and is created with private permissions;
  - `Turn.ResponseIDHash`, `APICall.Request.previous_response_id_hash`, and `APICall.Response.id_hash` are the persisted sources for default exports;
  - default ATIF/export redaction is present in Phase 1C-export before new provider-handle metadata can appear in shareable artifacts;
  - `raw-local` is recognized but rejected until Phase 11 local diagnostic export is implemented;
  - default export omits response-handle hashes and emits a diagnostic, without raw provider handles, when a legacy transcript lacks persisted hashes and the local HMAC secret is unavailable;
  - transcript/raw-log files containing raw handles are private (`0600` files or equivalent private state-dir permissions);
  - owned too-permissive transcript/API/raw-log files are tightened before new raw handles are written; if hardening fails, continuation or raw HTTP logging fails closed with diagnostics;
  - symlinks, non-owned files/directories, hard-linked files, and non-private session directories suppress raw provider-handle writes unless hardening proves the final owned target is private;
  - non-POSIX platforms either prove an equivalent private ACL or follow the same fail-closed path without raw provider-handle writes;
  - default doctor/dashboard views omit raw `APILogResponse.ID` / `Turn.ResponseID` when redacted hashes are unavailable.
  - doctor/dashboard/export surfaces prefer stable redacted handles by default.
- Non-continuation 400 (Phase 7/8):
  - no history-mode switch;
  - error surfaces.
- Error-classifier fixtures (Phase 7 and Phase 12A-codex):
  - a fixture table covers known provider error payloads and strings that must classify as continuation rejection;
  - adjacent generic schema/content errors must not classify as continuation rejection.
  - Phase 12A-codex extends classifier fixtures with discovered Codex continuation/storage rejection payloads before Phase 12B-codex can enable Codex continuation.
- Model fallback (Phase 8/9):
  - same-model `full_history_fallback` happens before configured model fallback;
  - continuation is disabled on fallback when model or endpoint changes.
- Context compaction (Phase 4B):
  - continuation cannot select an anchor before the latest checkpoint or summary turn;
  - an anchor after the latest checkpoint or summary turn remains eligible when all other checks pass;
  - continuation is disabled when the delta contains checkpoint or summary turns;
  - V1 recomputed context marker is the implementation of active-slice boundary membership, not an independent numeric gate;
  - eligibility compares recomputed active-slice membership in live and restored sessions, not any boundary meaning inferred from `ResponseContextMarker`; a turn with `ResponseContextMarker="cont-ctx-v1"` and matching recomputed membership remains eligible.
- Session restore (Phase 4B and Phase 2A-config):
  - current context marker is reconstructed from transcript boundaries;
  - restored eligibility recomputes candidate-anchor and current-request boundary membership from the active visible slice and does not compare an old persisted marker value against a new slice origin;
  - continuation remains eligible after restore when anchor metadata and reconstructed boundary membership match;
  - continuation is disabled after restore when metadata is missing or the anchor predates the latest boundary.
- Context and token accounting (Phase 10):
  - `responses_delta` records provider usage for billing;
  - the shadow estimate is computed from the single full-history expansion for the round, not from a second expansion and not from `FullHistoryFallbackMessages`;
  - compaction pressure uses the larger of provider input usage and the local full-history shadow estimate;
  - API logs expose both values when they differ.
  - if the shadow full-history estimate cannot be computed before dispatch, the round uses `full_history`, sends no delta, and logs `continuation_shadow_estimate_unavailable`.
- Aggregate rollout diagnostics (Phase 10 and Phase 12A/12C):
  - existing metrics/log-summary plumbing, or a new narrow diagnostic counter if no suitable hook exists, records `responses_delta`, `full_history`, and `full_history_fallback` counts by endpoint family.
  - aggregate metric labels never include attempt ids, response hashes, storage-scope fingerprints, or raw provider handles.
  - rollout diagnostics expose storage-quota failure counts by endpoint family so operators can see clustering across hub sessions even though disabled state remains session-local.
  - rollout diagnostics include prompt-cache hit-rate observations when provider usage data exposes cache details, and 12C rollback compares them to the matching Phase 12A floor.
  - Phase 12A estimates steady-state stored-response creation rate and compares it to known provider quota/retention/deletion limits; 12B enablement is rejected when storage growth cannot be bounded by documented rollback thresholds.
- Successful attempt metadata (Phase 1A-attempt, Phase 5A/5B, Phase 9):
  - assistant-turn continuation metadata comes from the successful final attempt;
  - stale delta metadata is not persisted when fallback succeeds.
  - response ids observed during partial or failed streams are not persisted as eligible anchors.
  - incomplete attempts that were sent with continuation-owned provider storage log `continuation_orphaned_provider_state_possible` without leaking raw provider handles outside raw-local logs.
  - Phase 1A-attempt proof enumerates every `appendAssistantTurn` caller, including streaming, and verifies incomplete streams clear final-attempt metadata.
- ATIF/export (Phase 1C-export and Phase 11):
  - default export emits `response_id_hash`, `previous_response_id_hash`, `conversation_id_hash`, `response_endpoint`, `response_storage_scope_fingerprint`, `response_request_fingerprint`, and `response_context_marker`;
  - explicit local diagnostic export emits raw `response_id`, `previous_response_id`, and `conversation_id` when requested.
- Public OpenAI and Codex backend request shapes (Phase 0B-discovery and Phase 12A-codex):
  - tests cover `/v1/responses`;
  - tests cover `/backend-api/codex/responses`;
  - tests cover `previous_response_id` with no `conversation`, `conversation` with no `previous_response_id`, and both handles present;
  - if an endpoint's supported shape omits `conversation` on delta requests, tests prove the adapter omits only the documented handle and preserves storage-scope mismatch checks before dispatch.
  - Codex-specific unsupported fields remain filtered as they are today.
- Streaming and non-streaming paths (Phase 5A/5B, Phase 7/9):
  - streaming completion via the Codex backend preserves continuation metadata through `completeViaStream`;
  - empty Responses stream fallback to Chat Completions uses a full-history fallback request, not the delta request;
  - if no full-history fallback request is available, continuation is disabled before entering the stream path.
  - canceled, interrupted, or failed streams with partially observed response ids do not persist anchor metadata.
- Continuation-disabled state (Phase 8/9):
  - endpoint-level rejection stores disabled state in live session state only;
  - disabled state is reset on session restore and is keyed by provider, endpoint, storage scope, storage policy, and stream/non-stream path;
  - model fallback consults disabled state without persisting it or sharing it across sessions.

Live tests remain opt-in:

```sh
SERF_OPENAI_E2E=1 GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_E2E_OpenAIResponsesContinuation' -count=1 -v
SERF_OPENAI_CODEX_E2E=1 GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_E2E_CodexResponsesContinuation' -count=1 -v
```

Live tests prove endpoint acceptance, response-id reuse, two-branch behavior from one stored anchor, invalid/expired `previous_response_id` behavior, the proposed maximum anchor age, and observed provider token counts only. The semantic contract remains covered by deterministic tests.

## 16. Acceptance Criteria

- OpenAI Responses requests automatically use `previous_response_id` only when an eligible anchor exists, `openai_responses_continuation=auto`, and endpoint support registry `Enabled=true`.
- Phase 0A does not start until Jesse explicitly approves the V1-public cut over the minimal malformed-tool-call delta experiment.
- If Jesse withholds that approval, this spec ships no new continuation runtime work; the maintained state is the already-landed sanitizer/raw-logging path until a separately scoped experiment is approved.
- Continuation never skips over a newer non-anchorable assistant turn; any assistant output after the candidate anchor forces `full_history`.
- Malformed historical assistant tool calls are not resent on the normal Responses continuation path.
- Delta tool results must have provable `call_id` linkage to the selected anchor's server-side function calls or same-delta calls; otherwise Serf uses `full_history`.
- Eligible full-history requests can produce the first future anchor by sending continuation-owned storage and persisting anchor metadata after success.
- Public OpenAI continuation requests use a storage policy that actually preserves response state.
- Public OpenAI storage behavior changes only when the launch/global setting permits continuation.
- The first user-visible V1-public cut is public OpenAI runtime enablement: Phases 0A through 10 plus 12A-public/12B-public, with 1C-export/1C-surfaces default redaction still blocking shareable exports that include new provider-handle metadata.
- Public OpenAI may be runtime-enabled independently after proof; Codex backend continuation stays registry-disabled until its own endpoint discovery and production-path live proof pass, even if shared code paths already exist.
- Public OpenAI runtime enablement has a rollback criterion based on Phase 12A-public expectations: eligible-hit rate, prompt-cache hit rate when observable, provider storage quota failures, invalid-anchor behavior, and provider-token/cost observations.
- Phase 12A proof artifacts record numeric rollback thresholds for eligible-hit-rate floor plus prompt-cache hit-rate floor plus storage-quota/error/cost/rate-limit ceilings; 12C rollback references those thresholds.
- Phase 12B enablement is rejected if its matching Phase 12A proof omits numeric rollback thresholds; 12B-public also requires security-review sign-off for public OpenAI provider-side storage retention.
- Runtime enablement is not complete from this spec alone; each endpoint family needs its matching reviewed Phase 12A proof artifact before its Phase 12B registry flip can land.
- Runtime enablement also requires Phase 0B's rough eligible-hit-rate expectation to clear the proposed Phase 12A floor, unless Jesse explicitly accepts a parity-only rollout with narrow hit rate.
- Endpoint-family enablement records a non-zero maximum anchor age; stale or timestamp-missing anchors use `full_history` instead of relying on provider behavior for expired server-side state.
- Maximum anchor age is documented as a conservative local-clock guard; Phase 12A explicit rejection proof remains the safety gate for expired provider-side state.
- With endpoint registry disabled, `openai_responses_continuation=auto` does not send `previous_response_id` and does not change public OpenAI requests from `store:false` to `store:true`.
- Storage policy has one authoritative compatibility home: the storage scope fingerprint.
- Continuation-owned storage changes are tracked separately from explicit user/provider storage settings and are cleared without stripping explicit settings.
- Request fingerprint excludes continuation-owned storage controls; storage compatibility is enforced only through `ResponseStorageScopeFingerprint`.
- The auth/provider construction layer produces storage-scope fingerprints without exposing raw credentials to session code.
- OpenAI adapter construction receives sanitized `AuthScopeIdentity` alongside transport credentials; planner fingerprint/scope construction runs in a pure helper that cannot read raw credential material.
- OpenAI org/project identifiers are hashed before they enter continuation planner metadata, storage scope, logs, or transcripts.
- Credential-hash version changes or local scope-secret rotation invalidate old anchors predictably.
- Missing `local_scope_secret` or missing private state home fails closed to full history without continuation-owned provider storage.
- `local_scope_secret` lives under the resolved private Serf state directory outside per-session transcripts.
- Storage-scope fingerprints and provider-handle redaction hashes use distinct derived subkeys from `local_scope_secret` so redaction-key rotation and scope-key rotation can be reasoned about separately.
- Doctor/export diagnostics explain when secret rotation or state-dir replacement invalidated continuation anchor eligibility.
- The session reaches continuation planning through `llm.Client`, not direct adapter registry access or duplicated body builders.
- Planner/canonicalization failure falls back to full history and does not send public OpenAI `store:true` solely for continuation in that round.
- Anchor-producing `full_history` rounds run the planner and carry request fingerprint, storage scope fingerprint, context marker, storage policy, and `AttemptIndex=1` into `ModelAttemptMetadata` so the first successful Responses turn can become the next anchor.
- Every `appendAssistantTurn` caller supplies final-attempt metadata, and incomplete streaming paths clear metadata rather than persisting tentative response ids.
- Delta requests are used only when the non-input request fingerprint matches the anchor.
- Provider-visible `instructions` are either proven deterministic for continuation eligibility, normalized through tested canonicalization, or fail closed to `full_history` with `continuation_instructions_unstable`.
- The real production system prompt is covered by deterministic fingerprint tests, including fixed environment-template inputs, two-date mismatch or explicit canonicalization behavior, and explicit handling for any non-`Today` environment field that can change mid-session.
- Rollout diagnostics treat the production `Today` field as a practical eligibility limiter: sessions crossing a rendered-date boundary are expected to fall back to full history unless a future tested canonicalization layer changes that behavior.
- Prompt-cache controls remain part of the request fingerprint; continuation does not special-case prompt-cache hit-rate changes.
- Model compatibility uses the resolved request model persisted on the anchor attempt, not provider-returned aliases alone.
- Delta requests are used only when the storage/auth scope fingerprint exactly matches the anchor.
- `conversation` is a provider-state handle: it is excluded from request fingerprint compatibility and enforced only through the storage scope fingerprint.
- V1 does not populate `ConversationID` solely for continuation; absent conversation handles are compatible only when both the current request and anchor are absent, while one-sided or changed conversation handles disable continuation.
- `ConversationID` handling may be test-only or unusual-profile-only in V1; it exists to fail closed when a provider-state handle is already present, not to create new conversations for continuation.
- Runtime enablement requires Phase 0B discovery proof for the co-present `previous_response_id` plus `conversation` request shape, or deterministic adapter tests proving the documented omission/fallback rule for that endpoint family.
- Delta requests are used only from anchors after the latest context boundary and in the current context marker.
- V1 context marker is derived only from persisted boundary turns and duplicates active-slice boundary membership; non-boundary rewrites use `full_history` until a persisted epoch-offset or explicit boundary mechanism lands.
- Restored sessions reconstruct context marker from transcript boundaries and safely disable continuation when required metadata is missing.
- Context marker eligibility always compares recomputed active-slice boundary membership; persisted `ResponseContextMarker` only gates continuation-aware schema presence/version with the literal `cont-ctx-v1` and is not a boundary-membership eligibility value.
- Restored-session setting precedence is explicit resume override, then persisted `ConfigSnapshot`, then current global default only when no snapshot value exists.
- Disabled endpoint-family/key state is live session state only; it is not persisted, restored, or shared across sessions.
- Canceled, interrupted, or failed streams never persist partially observed response ids as continuation anchors.
- Incomplete attempts sent with continuation-owned provider storage log that provider-side orphaned state may exist and do not expose raw provider handles outside raw-local logs.
- Image, document/file, web-search, and provider-hosted reference inputs use full history until endpoint-specific continuation fixtures prove support.
- Reasoning-model continuation keeps reasoning/include settings in the request fingerprint and uses full history for provider-exposed local reasoning items until fixtures prove support.
- Provider-exposed reasoning content attached to assistant turns is detected by the item-kind eligibility gate and cannot silently pass as plain text/tool history.
- `SystemPromptAsUser` sessions use full history. This design does not add a dedicated system-as-user delta builder.
- Chat Completions fallback never receives a delta-only request.
- Adapter-internal Responses-to-Chat fallback records separate attempts and cannot create a Responses continuation anchor.
- Full-history fallback is still available and still provider-safe.
- Continuation-specific provider rejection retries exactly once as full history.
- Continuation recovery has one semantic dispatch budget per primary provider/model round; the only extra retry is the bounded storage-demotion retry that preserves a no-storage full-history answer when continuation-owned storage quota blocks recovery.
- Phase 0B discovery probes invalid or expired `previous_response_id` behavior early; endpoint-family enablement still requires Phase 12A proof that invalid or expired `previous_response_id` produces an explicit classifiable error. Silent-drop or unproven behavior keeps the registry entry disabled.
- Endpoint-family enablement requires Phase 12A proof that two responses can branch successfully from one stored anchor.
- Codex endpoint-family enablement requires Codex-specific continuation/storage rejection classifier fixtures from Phase 12A-codex.
- Continuation-specific full-history retry happens before configured model fallback.
- Non-continuation provider errors do not trigger history-mode fallback.
- Request history mode is visible in API logs and raw HTTP logs.
- Raw HTTP logging proof covers continuation rejections delivered as stream errors as well as immediate HTTP errors.
- Attempt logs remain append-only; final retry group size is exposed through `final_attempt_count`, not by rewriting earlier records.
- `final_attempt_count` has a concrete metadata home (`ModelAttemptMetadata.FinalAttemptCount`); Phase 0A chooses terminal-attempt field or group-summary record, and Phase 5A implements only that chosen shape consistently across transcript/API/raw-log records.
- `attempt_index` is 1-based and stable across all logs for the same provider attempt.
- Request-side previous-response handles are persisted as redacted hashes on `api_call` records so default exports do not need raw provider handles.
- Response-side provider handles are persisted as `Turn.ResponseIDHash` for new anchor-eligible turns; default exports never require raw response ids to redact new transcripts.
- If response-id hashing is unavailable, raw `ResponseID` may remain as sensitive local transcript/debug data, but anchor eligibility metadata must be omitted.
- Default ATIF/export redaction ships before new provider-handle metadata can appear in shareable artifacts; `raw-local` is recognized but rejected until Phase 11 implements local diagnostic raw export.
- Tests can enable endpoint families only through injected test registry fixtures; production registry defaults remain disabled until the corresponding Phase 12B-public or Phase 12B-codex enablement commit lands.
- Every phase proof rechecks the concrete substrate facts it relies on, such as current `appendAssistantTurn` call sites, current model-call serialization, or the reservation guard that replaced serialization.
- Delta request billing usage and context-pressure accounting are recorded separately when provider usage undercounts full effective context.
- The shadow full-history estimate is computed from the same full-history expansion that fallback would use; if that estimate is unavailable, Serf uses `full_history` instead of sending a delta request.
- Aggregate diagnostics expose continuation hit-rate inputs by endpoint family: `responses_delta`, `full_history`, and `full_history_fallback` counts, with no per-attempt or per-handle metric labels.
- Rollout diagnostics expose provider storage-quota failure clustering by endpoint family while keeping disabled state session-local in V1.
- History mode is represented with typed constants, not free-form strings.
- ATIF/export preserves the new response metadata fields when present, while redacting provider-state handles by default.
- Environment/help/docs surfaces describe `SERF_OPENAI_RESPONSES_CONTINUATION` consistently.
- User-facing docs disclose provider-side retention and possible provider-token/cost implications without promising billed-token savings.
- User-facing docs disclose that changing or not syncing the Serf state directory across machines changes `local_scope_secret`, invalidates continuation anchors, and causes full-history operation until new anchors are created.
- Raw provider-handle transcripts/logs are stored with private local permissions or equivalent private state-dir protection.
- If transcript/API/raw-log hardening fails, raw provider-handle fields are suppressed for that attempt; ordinary assistant content may still be written without anchor metadata.
- Shared-reader log tooling does not get an implicit compatibility carveout for raw provider handles; any multi-user raw-log access requires a separate explicit design, while default exports remain redacted.
- Existing owned permissive transcript/API/raw-log files are tightened before new raw handles are written; if hardening fails, continuation or raw HTTP logging fails closed with diagnostics.
- On non-POSIX platforms, raw provider-handle writes require an equivalent private ACL proof; otherwise Serf follows the same fail-closed path.
- Default doctor/dashboard views never display raw `APILogResponse.ID` or `Turn.ResponseID`; they omit unavailable hashes rather than falling back to raw handles.
- Concurrent restored sessions may branch from the same read-only provider anchor without sharing disabled state or overwriting each other's next anchor.
- Stored provider-state deletion is not implemented in this scope and must be a separate explicit user action/phase if a provider exposes supported deletion.
- Default `go test ./...` stays deterministic and does not require network, quota, model behavior, or credentials.

## 17. Open Questions Resolved by This Spec

- **Should this cover both public OpenAI and Codex backend?** Yes, both are in design scope. Public OpenAI may runtime-enable first after proof; Codex backend remains registry-disabled until its own production-path proof passes.
- **Should continuation be automatic?** Yes, when eligibility is proven, the launch/global setting permits the endpoint's storage policy, and the endpoint family has passed its required proof.
- **Should malformed tool calls be rewritten internally?** No. Raw history is preserved; only full-history provider replay uses safe serialization.
- **Should the adapter own continuation state?** No. The session owns state; the adapter owns wire format.
- **Should `SystemPromptAsUser` use continuation now?** No. It is full-history only in this design; remove/disable that mode for target OpenAI Responses profiles before expecting continuation there.
- **Can restored sessions branch from the same `previous_response_id`?** Yes. Provider anchors are treated as read-only input; each session writes its own next local anchor and disabled state stays session-local.
- **Does disabling continuation delete provider-stored response state?** No. Deletion is out of scope and requires a separate explicit user action/phase if the provider exposes a supported deletion API.
- **Do live proofs set rollout thresholds?** Yes. They record observed status, latency, rate-limit behavior, eligible-hit-rate floor, prompt-cache hit-rate floor when observable, storage-growth expectations, and cost/quota/error ceilings per endpoint family; deterministic tests remain the semantic gate.
