# Serving-Contract Core Design

Date: 2026-08-28
Status: revised after adversarial review; implementation not started
Decision: clean redesign; no backward-compatibility requirement

## Summary

Evener currently treats an OpenAI-shaped provider as if the provider name determined the complete wire contract. That failed for Groq: the configured instance used the OpenAI Responses adapter, which emitted OpenAI-only request fields and a server-tool shape Groq rejected with HTTP 400.

This project introduces an Evener-owned serving-contract layer. It resolves a typed request contract before the first model call. The executable unit is a serving variant, scoped to a provider instance, wire model, and serving surface. The variant controls request construction; external model catalogs do not.

The first release solves request-shape safety for every retained adapter surface. It does not ingest external catalogs, probe providers, display metadata, or retry a request through another protocol.

## Requirements

### Functional requirements

- Resolve exactly one serving surface before every model request. A launch may name the surface explicitly; otherwise resolution must follow the deterministic default rule defined below.
- Represent one wire model on multiple surfaces without sharing surface-specific fields or state.
- Resolve provider, model, surface, adapter, endpoint path, authentication scope, and request contract before network dispatch.
- Represent capability and field support as `supported`, `unsupported`, `unknown`, or `conflict`.
- Omit optional fields only when omission preserves caller semantics. If a requested semantic feature cannot be represented, fail before dispatch.
- Support provider-, model-, and surface-specific reasoning, tool, role, history, output-limit, modality, and streaming rules.
- Make agent profile capabilities and adapter request shaping consume the same resolved variant.
- Fail closed on invalid, ambiguous, or conflicting configuration.
- Bind credentials to the selected trusted origin and instance. A custom origin must never inherit a first-party provider credential by adapter type alone.
- Preserve provenance on provider-native history artifacts so a surface cannot replay another surface's raw state without validation.

### Operational requirements

- Startup and default tests remain network-free.
- External catalogs are not required for runtime correctness.
- Request shaping is deterministic and testable with fake transports.
- API logs identify the resolved adapter, surface, wire model, and contract revision without recording secrets.
- Unknown surface contracts fail before dispatch. Unknown model metadata may use a known surface's required core contract, but optional capabilities remain unknown.
- A revised adapter or contract revision cannot silently reinterpret persisted continuation or provider-native state.

## Non-goals

- Automatic protocol detection.
- Generic cross-protocol fallback or retry.
- Runtime catalog downloads.
- Generating an adapter from metadata.
- Making external metadata control URLs, authentication, headers, redirects, or request bodies.
- Pricing enforcement, UI presentation, or catalog freshness.
- Continuation support for an arbitrary serving surface.
- Silent dropping of caller-requested semantic features.

Those concerns are separate follow-up projects.

## Terminology

### Model identity

A model identity contains:

- provider instance name;
- canonical model ID, when known;
- wire model ID;
- explicitly documented aliases.

An alias is a one-way accepted-name mapping to a canonical and wire ID. It is not evidence that two model IDs have equivalent capabilities.

Exact wire IDs take precedence over aliases. Alias cycles, duplicate aliases within one instance, and aliases that resolve to different wire IDs fail configuration. An alias never crosses provider instances unless explicitly configured there.

### Serving surface

A serving surface is a named API contract such as:

- `openai-completions`;
- `openai-responses`;
- `anthropic-messages`;
- `google-generative-ai`.

A surface identifies the adapter family and API version. It does not authorize a destination or credential. The instance owns the trusted base URL and authentication binding.

### Serving variant

A serving variant is the executable selection unit:

```
provider instance + wire model ID + surface
```

It contains:

- a curated adapter ID and implementation revision;
- an API family and version;
- an endpoint path selected by the adapter;
- an authentication scope reference;
- an immutable request contract;
- the model limits and capabilities relevant to that surface;
- a stable effective-contract revision.

The base URL and authentication remain trusted instance configuration. A catalog cannot replace them.

### Request contract

A request contract is typed data, not `map[string]any`. It contains these groups:

- **field policy:** each optional field's state and omission behavior;
- **capabilities:** tools, reasoning, vision, structured output, server tools, and modalities;
- **reasoning:** supported effort levels, wire mapping, summary/replay behavior, and encrypted-content support;
- **tools:** function-tool shape, strict-schema policy, tool choice, server-tool names, and parallel-call support;
- **conversation:** system/developer role, tool-result replay, continuation handles, and stateful fields;
- **generation:** output-token field, sampling fields, stop fields, and stream usage;
- **response:** stream event family, finish reasons, and response-shape requirements.

Every capability and optional field has four states:

- `supported`: the contract permits the feature or field;
- `unsupported`: the contract forbids it;
- `unknown`: no trusted claim exists, so optional use is forbidden;
- `conflict`: equally authoritative claims disagree; resolution fails.

A higher-priority explicit value replaces a lower-priority default. Two incompatible values at the same trusted layer are a conflict. External-source conflicts remain diagnostic claims and never authorize a request.

A field policy also declares whether a requested value is:

- **semantic:** omission changes the requested operation and must fail before dispatch;
- **optional:** omission preserves semantics and is allowed when unsupported or unknown;
- **required-by-adapter:** the adapter always supplies it as part of its validated core shape.

For example, dropping a prompt-cache optimization may be optional; dropping a requested structured-output schema, tool, modality, stop sequence, or output cap is semantic and must fail unless the contract explicitly defines an equivalent representation.

## Configuration model

The clean configuration schema is versioned independently of the current `providers.toml` shape. Old configuration is not migrated silently; it is rejected with an instruction to author the new form.

The conceptual shape is:

```toml
schema = 2
default = "groq"

[instances.groq]
adapter = "openai"
base_url = "https://api.groq.com/openai/v1"
credential = "groq-api-key"
default_surface = "openai-responses"

[instances.groq.models."qwen/qwen3.8-27b"]
canonical_id = "qwen/qwen3.8-27b"
default_surface = "openai-responses"

[instances.groq.models."qwen/qwen3.8-27b".surfaces.openai-responses]
contract = "groq-responses"

[instances.groq.models."qwen/qwen3.8-27b".surfaces.openai-responses.compat]
include = "unsupported"
store = "unsupported"
server_search = "unsupported"
```

The exact TOML names are implementation detail; the schema must preserve these invariants:

- every model resolves a surface through explicit launch selection or a deterministic authored default;
- a model may define multiple named surfaces;
- each surface has independent compatibility and state rules;
- a surface cannot inherit fields from another surface;
- only allowlisted contract fields are configurable;
- base URL, authentication, and headers are never sourced from catalog claims;
- arbitrary JSON body fragments and arbitrary adapter IDs are rejected;
- alias mappings are scoped to an instance and resolve deterministically.

### Surface selection

The selection algorithm is fixed:

1. explicit launch surface;
2. model `default_surface`;
3. instance `default_surface`;
4. exactly one surface defined for the model;
5. otherwise fail with an ambiguous-or-missing-surface error.

Auxiliary calls, model switches, forks, and resume use the same algorithm only when they create a new request identity. A resumed session uses its persisted serving-variant identity and does not silently re-resolve a changed default.

### Trust and credential binding

Only user-global/state-root provider configuration is loaded implicitly. A project-local provider configuration is never discovered automatically. An explicitly supplied configuration path requires:

- current-user ownership;
- a regular file, not a symlink;
- no group/world write permission;
- a parent directory that is not group/world writable;
- explicit user selection when the path is outside the default state root.

The resolved credential reference is bound to the instance and normalized origin. First-party provider credentials and OAuth records are valid only for their curated first-party origins. A custom base URL requires an instance-scoped credential or explicit credential header. Missing or mismatched binding fails before transport. Redirects cannot carry credentials across origins.

## Authority and resolution

There is no global source precedence. Resolution is domain-specific.

### Executable behavior

For adapter, protocol, endpoint path, request fields, and transforms:

1. explicit trusted instance/model/surface configuration;
2. exact curated Evener serving profile;
3. curated adapter/surface default;
4. conservative default or pre-dispatch failure, according to field policy.

External catalogs cannot select executable behavior without an allowlisted curated mapping.

### Capabilities and limits

For capabilities and limits:

1. explicit trusted surface override;
2. exact curated provider/model/surface profile;
3. conservative unknown.

A field marked `unknown` never emits an optional provider-specific request field. A field marked `unsupported` never emits it. A `conflict` fails resolution.

The first project does not use LiteLLM or Models.dev in this execution path. Later catalog ingestion may add claims, but claims must retain source, revision, retrieval time, and scope and cannot silently turn `unknown` into `supported`.

### Pricing and aliases

Pricing remains informational and is outside this project. Later pricing claims must preserve currency, units, region, service tier, effective date, and unknown state; missing price is not zero.

Aliases are resolved before profile lookup. Exact IDs beat aliases. A duplicate or ambiguous alias fails closed. The resolved variant records both the requested alias and final wire ID for diagnostics.

## Curated profiles

The first release must enumerate every retained adapter. Each row has one or more typed surfaces and baseline request/response/error/stream tests.

| Adapter family | Surface(s) in v1 | Contract source |
|---|---|---|
| OpenAI | `openai-responses`, `openai-completions` | Curated Evener adapter contracts |
| OpenAI-compatible | `openai-completions` | Curated compatible contract plus model/surface overrides |
| Anthropic | `anthropic-messages` | Curated Anthropic contract |
| Google/Gemini | `google-generative-ai` | Curated Gemini contract |
| Kimi | Kimi's current chat surface | Curated Kimi contract |
| Kimi Anthropic | `anthropic-messages` variant | Curated Kimi-Anthropic contract |
| MiniMax | MiniMax's current chat surface | Curated MiniMax contract |
| OpenRouter | OpenAI-compatible chat surface | Curated OpenRouter contract |
| OpenRouter Anthropic | `anthropic-messages` variant | Curated OpenRouter-Anthropic contract |

If an adapter cannot be given a v1 contract and tests, it is explicitly removed from the new configuration schema; it is not silently accepted outside the resolver.

### OpenAI public Responses

Implement the native OpenAI Responses contract, including supported Responses continuation and reasoning fields where the adapter requires them. This is the desired contract for the native surface, not a promise to preserve accidental behavior from the old implementation.

### OpenAI-compatible Chat Completions

Implement the OpenAI-compatible Chat Completions contract: model-specific reasoning formats, output-token field, tool-result replay, strict-tool policy, stream usage, and cache controls are surface-scoped. The new contract defines intended behavior; it does not preserve accidental legacy fields.

### Groq Responses

Use the Groq Responses API only when explicitly selected. The profile:

- omits `store`;
- omits `include` and encrypted-reasoning request fields unless separately proven;
- disables OpenAI `web_search` for Qwen;
- permits only Groq-documented server-tool names on models that support them;
- does not enable continuation merely because Responses is accepted;
- retains Groq's model-specific reasoning and tool capabilities as separate claims.

### Unknown model on a known surface

A model unknown to the curated catalog may use a configured known surface's required core contract. Its optional capabilities remain unknown. The agent must not expose optional server tools or semantic features without a positive claim. If the caller explicitly requests an unknown semantic feature, resolution fails before dispatch.

### Unknown surface or adapter

An unknown surface or adapter is never executable. Resolution fails before network dispatch with the instance, model, requested surface, and missing contract named.

## Runtime architecture

The serving resolver is a pure, network-free package shared by configuration loading, agent profile construction, session persistence, and adapter construction. It returns an immutable serving variant or a typed error.

The client resolves the variant before dispatch. The adapter receives the variant and cannot silently substitute a different surface. The agent uses the same variant to decide whether to expose reasoning, tools, modalities, or provider-native web search.

The semantic request remains separate from the serving variant. The adapter translates the semantic request under the contract. It validates semantic feature requirements before constructing bytes:

- unsupported or unknown requested modalities fail before transport;
- unsupported or unknown requested structured output fails before transport;
- unsupported or unknown requested tools, stop sequences, output caps, or continuation state fail before transport unless the contract defines an equivalent;
- optional optimizations may be omitted;
- persisted provider-native artifacts are validated against their source and target surfaces before replay.

The first release removes the existing implicit Responses-to-Chat fallback path
from the OpenAI adapter. An empty Responses stream, a generic provider error, or
a transport failure produces one surfaced failure and exactly one recorded
attempt. No alternate endpoint is tried.

The API-attempt record includes the variant identity and effective-contract revision. Contract resolution, validation, and serialization are separate observable phases so a future failure identifies the boundary.

### Session identity

Session metadata persists:

- instance name;
- wire model ID and canonical model ID;
- selected surface and API version;
- effective-contract revision;
- credential-scope fingerprint, never the credential;
- continuation/state compatibility marker.

Resume requires the exact variant and contract revision to remain available. If they do not, resume fails with an explicit migration/recovery error. A curated-code upgrade may invalidate a revision only through a declared revision transition; it cannot silently reinterpret old provider state.

### History provenance

Provider-native content parts that carry raw wire artifacts, including server-tool calls and encrypted reasoning, carry their source variant or an equivalent provenance marker. Before replay, the target contract either:

- accepts the artifact's source and target shape;
- applies a typed, documented translation; or
- fails before transport.

Raw provider artifacts are never passed through solely because they are valid JSON.

## Breaking changes

This project intentionally has no backward-compatibility requirement.

- The provider configuration schema may move from instance-wide API style to model/surface variants.
- Existing configuration files may require explicit reauthoring.
- Internal adapter/profile interfaces may require a resolved serving variant.
- Duplicated provider-conditional decisions in agent and adapters are removed in favor of the shared resolver.
- Existing tests and fixtures are rewritten to the new schema.
- Providers without a v1 serving contract are rejected rather than run through an implicit legacy path.

A compatibility shim may be added later if migration cost warrants it, but it is not part of this project.

## Testing and acceptance

All tests use fake transports or pure functions. No provider credentials or network access are allowed.

### Resolver tests

- explicit launch, model-default, instance-default, single-surface, and missing/ambiguous surface selection;
- exact instance/model/surface variant identity;
- multiple surfaces for one wire model and surface isolation;
- typed allowlist validation;
- supported, unsupported, unknown, and conflict states for both capabilities and fields;
- same-layer conflict versus higher-priority override;
- unknown surface and unknown adapter fail before dispatch;
- unknown model on a known surface uses only its defined core contract;
- alias exact-match precedence, one-way mapping, duplicate/cycle rejection, and wire-ID recording;
- custom-origin credential binding, credential mismatch, symlink/permission rejection, and cross-origin redirect rejection;
- persisted variant identity and resume failure when the revision is unavailable.

### Request-shape tests

Golden request tests cover every retained adapter/surface in the table above. The OpenAI-shaped set includes:

- OpenAI public Responses baseline;
- OpenAI-compatible Chat baseline;
- Groq Responses with `store`, `include`, and OpenAI `web_search` absent;
- Groq function tools and reasoning controls in their documented shape;
- unknown-model Requests on known surfaces with optional fields omitted;
- no automatic Responses-to-Chat request after an empty Responses stream or provider error;
- surface-switch history containing a prior server-tool artifact;
- image, document, and audio requests on unsupported/unknown surfaces fail with zero transport dispatch;
- explicit structured-output, tool, stop, output-cap, and continuation requests fail rather than being silently weakened when unavailable;
- response and error parsing for each retained adapter.

### Cross-layer tests

- agent capability exposure equals adapter contract capability;
- a disabled server tool cannot enter a request through agent defaults or persisted history;
- a surface switch cannot carry surface-specific continuation or reasoning state;
- API attempt records include instance, wire model, surface, adapter, and contract revision;
- every registered adapter is either represented in the v1 table or rejected before dispatch.

### Fuzz/property tests

- malformed configuration and contract values never panic;
- unknown values never enable optional fields;
- serialization is deterministic;
- no contract field can inject an unallowlisted body key, URL, or credential header;
- provider-native artifacts from one surface cannot serialize onto an incompatible surface;
- semantic request features are never silently dropped.

The project is accepted only when these tests pass and the normal offline gates remain deterministic.

## Follow-up projects

### External metadata ingestion

Keep LiteLLM for broad limits, pricing, and capability enrichment. Add Models.dev as a supplemental provider/inventory source because it has useful provider/API-package mappings. Neither becomes protocol authority. Pin both snapshots, validate them, preserve license notices, and convert them into provenance-bearing claims. Catalog claims remain advisory and cannot directly execute.

### Inventory discovery

Extend documented `/models` responses to inventory and explicitly advertised metadata. Do not infer protocol or request-field support from an ordinary model list.

### Diagnostic probing

Add an explicit, disclosed doctor operation for minimal no-state requests. Store exact-scope evidence with TTL and config/adapter invalidation. Never run it implicitly.

### Protocol fallback

Only add provider-specific fallback after a provider documents non-execution or provides a valid idempotency guarantee. Rebuild the alternate request, make one attempt, and log both. Generic 400/404/422 responses remain non-fallback errors.

### Presentation

Expose selected surface, contract status, provenance, conflicts, and unknown capabilities in CLI/hub/web model views.

## Implementation boundary

This spec defines a clean serving-contract core, not a catalog project. The first implementation plan should partition the work into these independently testable units:

1. trusted config and serving-variant schema;
2. pure resolution and field-state validation;
3. adapter contract integration and semantic request validation;
4. agent/session profile and persisted-variant wiring;
5. curated adapter profiles and request-shape goldens.

External catalog ingestion, `/models` enrichment, diagnostic probing, UI presentation, and protocol fallback require separate designs and approvals.
