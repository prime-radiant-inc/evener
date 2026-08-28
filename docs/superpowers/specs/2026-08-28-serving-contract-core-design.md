# Serving-Contract Core Design

Date: 2026-08-28
Status: approved for specification; implementation not started
Decision: clean redesign; no backward-compatibility requirement

## Summary

Evener currently treats an OpenAI-shaped provider as if the provider name determined the complete wire contract. That failed for Groq: the configured `groq` instance used the OpenAI Responses adapter, which emitted OpenAI-only request fields and a server-tool shape Groq rejected with HTTP 400.

This project introduces an Evener-owned serving-contract layer. It resolves a typed request contract before the first model call. The contract is scoped to the provider instance, wire model, and serving surface. It controls request construction; external model catalogs do not.

The first release solves request-shape safety. It does not ingest external catalogs, probe providers, display metadata, or retry a request through another protocol.

## Requirements

### Functional requirements

- Select a serving surface explicitly for every model: for example, OpenAI Chat Completions or OpenAI Responses.
- Represent one model on multiple surfaces without sharing surface-specific fields.
- Resolve provider, model, surface, adapter, and request contract before sending a request.
- Represent capability and field support as `supported`, `unsupported`, `unknown`, or `conflict`.
- Omit optional fields when support is unknown or unsupported.
- Keep request-field policy separate from capability claims. A capability claim never authorizes an adapter to emit an arbitrary field.
- Support provider-, model-, and surface-specific reasoning, tool, role, history, output-limit, and streaming rules.
- Make agent profile capabilities and adapter request shaping consume the same resolved contract.
- Fail closed on invalid or conflicting configuration.
- Keep provider destination and authentication under trusted user configuration and curated Evener code.

### Operational requirements

- Startup and default tests remain network-free.
- External catalogs are not required for runtime correctness.
- Request shaping is deterministic and testable with fake transports.
- The API log identifies the resolved adapter, surface, model, and contract revision.
- Unknown providers and models produce an actionable configuration error or a conservative contract; they never receive guessed optional fields.

## Non-goals

- Automatic protocol detection.
- Generic cross-protocol fallback or retry.
- Runtime catalog downloads.
- Generating an adapter from metadata.
- Making external metadata control URLs, authentication, headers, redirects, or request bodies.
- Pricing enforcement, UI presentation, or catalog freshness.
- Continuation support for an arbitrary serving surface.

Those concerns are separate follow-up projects.

## Terminology

### Model identity

A model identity contains:

- provider instance name;
- canonical model ID, when known;
- wire model ID;
- explicitly documented aliases.

An alias is an accepted name mapping. It is not evidence that two model IDs have equivalent capabilities.

### Serving variant

A serving variant is the executable selection unit:

```
provider instance + wire model ID + surface
```

It contains:

- a trusted adapter ID;
- an API family and version;
- an endpoint path selected by the adapter;
- an immutable request contract;
- the model limits and capabilities relevant to that surface.

The base URL and authentication remain instance configuration. A catalog cannot replace them.

### Request contract

A request contract is typed data, not `map[string]any`. It contains these groups:

- **field policy:** optional request fields allowed, omitted, or unknown;
- **capabilities:** tools, reasoning, vision, structured output, server tools, and modalities;
- **reasoning:** supported effort levels, wire mapping, summary/replay behavior, and encrypted-content support;
- **tools:** function-tool shape, strict-schema policy, tool choice, server-tool names, and parallel-call support;
- **conversation:** system/developer role, tool-result replay, continuation handles, and stateful fields;
- **generation:** output-token field, sampling fields, stop fields, and stream usage;
- **response:** stream event family, finish reasons, and response-shape requirements.

Each capability has four states:

- `supported`: the contract permits the feature;
- `unsupported`: the contract forbids the feature;
- `unknown`: no trusted claim exists, so optional use is forbidden;
- `conflict`: trusted sources disagree, so resolution fails until curated.

Required fields belong to the adapter contract and are not controlled by these optional-field states.

## Configuration model

The clean configuration schema is versioned independently of the current `providers.toml` shape. Old configuration is not migrated silently; it is rejected with an instruction to author the new form.

The conceptual shape is:

```toml
schema = 2
default = "groq"

[instances.groq]
adapter = "openai"
base_url = "https://api.groq.com/openai/v1"

[instances.groq.models."qwen/qwen3.8-27b"]
canonical_id = "qwen/qwen3.8-27b"
default_surface = "responses"

[instances.groq.models."qwen/qwen3.8-27b".surfaces.responses]
api = "openai-responses"
contract = "groq-responses"
```

Surface-specific compatibility belongs under the surface. The user may override typed fields, but cannot provide arbitrary JSON body fragments:

```toml
[instances.groq.models."qwen/qwen3.8-27b".surfaces.responses.compat]
supports_store = false
supports_include = false
server_search = "none"
```

The exact field names are implementation detail; the schema must preserve the following rules:

- an instance may provide a default surface;
- a model may select a different default surface;
- a model may define multiple named surfaces;
- each surface has independent compatibility;
- a surface cannot inherit fields from another surface;
- only allowlisted contract fields are configurable;
- base URL, authentication, and headers are never sourced from catalog claims.

## Authority and resolution

There is no global source precedence. Resolution is domain-specific.

### Executable behavior

For adapter, protocol, endpoint path, request fields, and transforms:

1. explicit trusted instance/model/surface configuration;
2. exact curated Evener serving profile;
3. curated adapter/surface default;
4. conservative default.

External catalogs are advisory inputs for later enrichment. They cannot select executable behavior without an allowlisted curated mapping.

### Capabilities and limits

For capabilities and limits:

1. explicit trusted surface override;
2. exact curated provider/model/surface profile;
3. conservative unknown.

The first project does not use LiteLLM or Models.dev in this resolution path. Later catalog ingestion may add claims, but claims must retain source, revision, and scope and cannot silently turn `unknown` into `supported`.

### Conflicts

Contradictory trusted values are a configuration error. Contradictory external claims are retained as a conflict and resolve to unknown for request construction until a curated override settles them.

## Curated profiles

The first release ships typed profiles for the existing supported surfaces and the incident case:

### OpenAI public Responses

Implement the native OpenAI Responses contract, including supported Responses continuation and reasoning fields where the adapter requires them. This is the desired contract for the native surface, not a promise to preserve accidental behavior from the old implementation.

### OpenAI-compatible Chat Completions

Implement the OpenAI-compatible Chat Completions contract: model-specific reasoning formats, output-token field, tool-result replay, strict-tool policy, stream usage, and cache controls are surface-scoped. The new contract defines the intended behavior; it does not preserve accidental legacy fields.

### Groq Responses

Use the Groq Responses API only when explicitly selected. The profile:

- omits `store`;
- omits `include` and encrypted-reasoning request fields unless separately proven;
- disables OpenAI `web_search` for Qwen;
- permits only Groq-documented server-tool names on models that support them;
- does not enable continuation merely because Responses is accepted;
- retains Groq’s model-specific reasoning and tool capabilities as separate claims.

### Unknown OpenAI-shaped surface

Use a conservative Responses or Chat contract selected by configuration. Emit required core fields only. Do not inherit OpenAI-only caching, server tools, encrypted reasoning, continuation, or provider options merely because a URL resembles an OpenAI endpoint.

## Runtime architecture

The serving resolver is a pure, network-free package shared by configuration loading, agent profile construction, and adapter construction. It returns an immutable serving variant or a typed error.

The client resolves the variant before dispatch. The adapter receives the variant’s contract and cannot silently substitute a different surface. The agent uses the same variant to decide whether to expose reasoning, tools, or provider-native web search.

The existing semantic request model remains provider-agnostic. Provider-specific behavior is applied only by the selected adapter under the resolved contract. Arbitrary provider options are removed from the execution path unless they are defined by an allowlisted contract field.

A missing variant has two valid outcomes:

- explicit configuration selects a known surface and its conservative contract can serve the request; or
- resolution fails before network dispatch with the instance, model, and missing contract named.

The runtime never sends a speculative first request to discover the answer.

## Breaking changes

This project intentionally has no backward-compatibility requirement.

- The provider configuration schema may move from instance-wide API style to model/surface variants.
- Existing configuration files may require explicit reauthoring.
- The internal adapter/profile interfaces may change to require a resolved serving variant.
- Duplicated provider-conditional decisions in agent and adapters are removed in favor of the shared resolver.
- Existing tests and fixtures are rewritten to the new schema rather than preserving legacy parsing behavior.

A deliberate compatibility shim may be added later if migration cost warrants it, but it is not part of this project.

## Testing and acceptance

All tests use fake transports or pure functions. No provider credentials or network access are allowed.

### Resolver tests

- exact instance/model/surface selection;
- provider default and model override selection;
- multiple surfaces for one wire model;
- surface isolation;
- typed allowlist validation;
- supported, unsupported, unknown, and conflict behavior;
- trusted configuration precedence;
- unknown model conservative resolution;
- invalid configuration fails before adapter dispatch.

### Request-shape tests

Golden request tests must cover:

- OpenAI public Responses baseline;
- OpenAI-compatible Chat baseline;
- Groq Responses with `store`, `include`, and OpenAI `web_search` absent;
- Groq function tools and reasoning controls in their documented shape;
- unknown Responses and Chat surfaces with optional fields omitted;
- tool history and stream request behavior;
- response and error parsing for each adapter.

### Cross-layer tests

- agent capability exposure equals adapter contract capability;
- a disabled server tool cannot enter the request through agent defaults;
- a surface switch cannot carry surface-specific continuation or reasoning state;
- API attempt records include instance, wire model, surface, adapter, and contract revision;
- existing provider behavior is tested under the new schema, not through legacy compatibility fixtures.

### Fuzz/property tests

- malformed configuration and contract values never panic;
- unknown values never enable optional fields;
- serialization is deterministic;
- no contract field can inject an unallowlisted body key, URL, or credential header.

The project is accepted only when these tests pass and the normal offline gates remain deterministic.

## Follow-up projects

### External metadata ingestion

Keep LiteLLM for broad limits, pricing, and capability enrichment. Add Models.dev as a supplemental provider/inventory source because it has useful provider/API-package mappings. Neither becomes protocol authority. Pin both snapshots, validate them, preserve license notices, and convert them into provenance-bearing claims.

### Inventory discovery

Extend documented `/models` responses to inventory and explicitly advertised metadata. Do not infer protocol or request-field support from an ordinary model list.

### Diagnostic probing

Add an explicit, disclosed doctor operation for minimal no-state requests. Store exact-scope evidence with TTL and config/adapter invalidation. Never run it implicitly.

### Protocol fallback

Only add provider-specific fallback after a provider documents non-execution or provides a valid idempotency guarantee. Rebuild the alternate request, make one attempt, and log both. Generic 400/404/422 responses remain non-fallback errors.

### Presentation

Expose selected surface, contract status, provenance, conflicts, and unknown capabilities in CLI/hub/web model views.

## Open decisions for implementation planning

1. Should a conservative unknown surface be selectable, or should unknown contracts always fail before dispatch?
2. Which exact contract fields are needed in v1 beyond Groq’s `store`, `include`, and server-tool restrictions?
3. Should surface selection be a model-level default plus named surfaces, or should every launch specify a surface explicitly?
4. Which current provider adapters are in the first curated-profile set?
5. What is the precise trusted boundary for project-local configuration versus user-global configuration?

These decisions must be resolved in the implementation plan. They do not justify adding catalog ingestion or runtime probing to the first project.
