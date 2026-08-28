# Provider and Model Contract Program Design

Date: 2026-08-28
Status: draft for review; implementation not started
Decision: clean redesign; no backward-compatibility requirement

## Purpose

Evener needs more than a model list. It needs a trustworthy answer to four separate questions:

1. Which model identity is being requested?
2. Which provider serving surface will answer it?
3. Which request and response contract is legal on that surface?
4. What limits, capabilities, economics, and evidence describe that exact serving variant?

The current system answers these questions with provider-name defaults, an instance-wide API style, and a broad LiteLLM snapshot. That arrangement permits a provider such as Groq to receive an OpenAI Responses request containing fields and server tools it does not accept.

This design splits the work into independently shippable projects. The first project is the serving-contract core described in `docs/superpowers/specs/2026-08-28-serving-contract-core-design.md`. This document defines the program around it: trusted configuration, curated contracts, external metadata, inventory discovery, diagnostics, presentation, and any future fallback.

## Product requirements

### Hard requirements

- Every model request resolves one explicit serving surface before network dispatch.
- Request shaping is governed by an Evener-owned typed contract scoped to the provider instance, wire model, surface, API version, and deployment/entitlement scope.
- Optional fields are emitted only when the selected contract permits them.
- Unknown is distinct from unsupported, and neither enables optional behavior.
- A caller-requested semantic feature that cannot be represented fails before dispatch instead of being silently dropped.
- Provider destination, authentication, credential headers, and redirects are controlled only by trusted configuration and curated code.
- Agent capability exposure and adapter request construction consume the same resolved serving variant.
- Provider-native history and continuation state cannot cross surface boundaries without provenance validation.
- Runtime correctness does not depend on an external catalog or live provider discovery.
- Default tests are deterministic and offline.
- Metadata claims retain source, revision, scope, retrieval time, and confidence.
- Errors identify the provider instance, wire model, surface, contract, and failing phase without exposing credentials.

### Secondary requirements

- Support one wire model on multiple serving surfaces.
- Enrich context limits, output limits, modalities, reasoning levels, tools, structured output, aliases, and pricing.
- Show users why a model/surface was selected and which capabilities remain unknown.
- Permit explicit diagnostic checks without making ordinary startup expensive or nondeterministic.
- Provide a path to provider-specific fallback only when a provider proves that retrying another surface is safe.

### Non-goals

- Universal protocol introspection.
- Automatic adapter generation from catalog data.
- Treating model capability flags as proof of request-field compatibility.
- Runtime catalog downloads as a correctness dependency.
- Catalog-controlled URLs, authentication, headers, redirects, or arbitrary request bodies.
- Price enforcement from third-party estimates.
- Generic retry of LLM POSTs across protocols.
- Silent semantic downgrade to make a request succeed.

## Core model

### Model identity

A model identity is a name-resolution object:

- provider instance;
- canonical model ID, if known;
- wire model ID;
- aliases explicitly documented for that instance;
- requested name and resolved name for diagnostics.

Aliases are one-way mappings. Exact wire IDs beat aliases. Duplicate, cyclic, ambiguous, or cross-instance aliases fail closed. An accepted alias does not imply equivalent limits or capabilities.

### Provider integration

A provider integration identifies how a configured instance can be served:

- curated adapter ID and implementation revision;
- trusted origin and credential binding;
- available serving surfaces;
- provider-level defaults;
- model inventory and model-ID translation rules.

The integration is not the same thing as a model contract. A provider may serve one model through several APIs with different capabilities and fields.

### Serving variant

A serving variant is the executable identity:

```
provider instance + wire model ID + serving surface + API version
```

It contains:

- model identity;
- adapter ID and implementation revision;
- surface/API family and version;
- endpoint path selected by the adapter;
- credential-scope reference;
- immutable request/response contract;
- exact limits and capabilities for that surface;
- effective contract revision.

The normalized base URL and authentication come from trusted instance configuration. External metadata may describe an integration, but it cannot change the destination that receives a prompt.

### Contract

The contract is typed and immutable. It has independent policy for:

- optional request fields;
- semantic feature requirements;
- modalities and attachments;
- tools and server tools;
- tool choice and strict schemas;
- reasoning controls and wire-level mapping;
- roles, history replay, and continuation handles;
- generation fields, output caps, stop sequences, and sampling;
- streaming usage and event grammar;
- response shape and finish reasons.

Each capability and optional field has one of four states:

- `supported` — use is permitted;
- `unsupported` — use is forbidden;
- `unknown` — no trusted claim exists, so optional use is forbidden;
- `conflict` — equally authoritative claims disagree, so resolution fails.

Each field also declares its semantic class:

- `semantic` — omission changes caller intent and must fail before dispatch;
- `optional` — omission preserves intent;
- `required` — the adapter must emit it as part of the surface's core request.

This prevents a generic “omit unsupported fields” rule from silently dropping a requested tool, schema, modality, output cap, or stop condition.

### Evidence and claims

A claim is a statement about one field of one serving variant. It carries:

- field path;
- value and state;
- source class;
- source revision or content hash;
- retrieval/observation time;
- scope, including instance, origin, model, surface, API version, region, deployment, and entitlement where relevant;
- confidence and review/expiry metadata.

Sources are not interchangeable. A catalog estimate, provider documentation claim, explicit user override, and successful probe have different authority and failure modes.

## Source strategy

### Curated typed Evener registry: executable authority

Evener's typed registry is authoritative for:

- adapter selection;
- supported surface families;
- request and response transformation;
- credential policy;
- safe endpoint paths;
- field policies and semantic-loss behavior;
- provider-specific server-tool and reasoning dialects.

The registry is code-reviewed, allowlisted, and versioned with the adapter implementation. A catalog cannot instantiate a new adapter or apply an arbitrary body transform.

### LiteLLM: broad enrichment source

Keep the vendored LiteLLM snapshot as the broad enrichment source for:

- model inventory breadth;
- context and output limits;
- pricing dimensions;
- common capability flags;
- aliases and provider-qualified model identifiers where present;
- endpoint hints such as `mode` and `supported_endpoints`.

LiteLLM's catalog is an advisory map, not a portable Evener wire contract. Its fields describe LiteLLM's model/provider knowledge, while important compatibility behavior also lives in LiteLLM provider transformation code. The snapshot is MIT-licensed under the repository's non-enterprise terms, but model and provider licenses remain separate.

### Models.dev: provider/inventory enrichment source

Use a pinned Models.dev snapshot as a supplemental source for:

- normalized provider/model inventory;
- provider API base metadata;
- adapter/package integration hints such as `npm`;
- modalities, reasoning options, limits, dates, and simple pricing.

Models.dev's `npm` field names an AI SDK package, not an Evener adapter. Its provider/model shape is sparse and cannot authorize an Evener request field. Translate it through an allowlisted Evener mapping. Models.dev is MIT-licensed; model/provider licenses remain separate.

Do not replace LiteLLM with Models.dev. LiteLLM is broader for the enrichment already used by Evener. Models.dev is cleaner for provider-oriented inventory and integration hints. Both are useful inputs to one normalized claim pipeline; neither is executable authority.

### Provider documentation

Official provider documentation informs curated registry entries. Documentation references are stored with the curated profile and reviewed when API versions or beta features change. “OpenAI-compatible” is not treated as “OpenAI-equivalent.”

### Runtime sources

A provider's `/models` response is an inventory source unless that provider explicitly documents richer fields. An ordinary model-list response does not establish protocol, tool, reasoning, cache, or request-field support.

An opt-in diagnostic probe is runtime evidence for the exact request shape it exercised. A successful response proves acceptance of that request, not that every related field is semantically honored. Failed probes distinguish authentication, quota, outage, moderation, invalid model, and unsupported feature; only the last can affect a capability claim.

## Authority and precedence

There is no global source precedence. Resolution is field-specific.

| Domain | Authority order | Rule |
|---|---|---|
| Origin, credentials, headers, redirects | trusted explicit config → curated registry | External sources cannot write executable routing or authentication. |
| Adapter and serving surface | explicit trusted selection → curated registry | Catalog protocol hints are advisory only. |
| Request transforms | typed adapter contract → curated provider/model/surface delta → explicit typed override | Arbitrary body passthrough is forbidden. |
| Capabilities | trusted surface override → curated documentation/profile → exact positive runtime evidence → external claim | Preserve unknown, unsupported, and conflict. |
| Limits | account/deployment config → curated serving value → exact provider field → external estimate | Context, input, and output are separate. |
| Pricing | account contract/rate sheet → curated rate → external estimate | Preserve units, currency, region, tier, effective date, and unknown. |
| Aliases | explicit alias → provider-documented alias → curated alias | Accepted IDs do not prove equivalence. |
| Inventory | provider `/models` → pinned catalog | Inventory cannot alter executable contract. |

Within a trusted layer, incompatible values are a configuration conflict. A higher-priority explicit value replaces a lower-priority default. External conflicts remain diagnostic and resolve to unknown for request construction. No source can silently turn unknown into supported.

## Trust boundary

Only user-global/state-root provider configuration is discovered implicitly. Project-local provider configuration is not discovered automatically.

An explicitly selected configuration path must be:

- owned by the current user;
- a regular file, not a symlink;
- not group/world writable;
- located under a non-group/world-writable parent;
- explicitly selected when outside the default state root.

A credential reference is bound to its instance and normalized origin. First-party API keys and OAuth records are valid only for curated first-party origins. A custom origin requires an instance-scoped credential or explicit credential header. Missing or mismatched binding fails before transport. Redirects cannot forward credentials across origins.

Project-local configuration may later be supported only through an explicit trust/consent mechanism. It must never become an ambient source of destinations or credentials merely because Evener entered a repository.

## Project decomposition

Each project below has one contract, one acceptance boundary, and an explicit non-scope. Projects can be reviewed and landed independently.

### Project 0: incident mitigation

**Contract:** Configure Groq to use the known-safe Chat Completions surface until the serving-contract core supports an explicit Groq Responses profile.

**Acceptance:** The local Groq configuration no longer sends the rejected Responses request. No application code or catalog behavior changes.

**Non-scope:** General metadata, config schema, discovery, diagnostics, and fallback.

### Project 1: serving-contract core

**Contract:** Resolve one immutable serving variant and use it for agent capability exposure, request validation, adapter shaping, history replay, and session identity.

**Required behavior:**

- clean model/surface configuration;
- deterministic surface selection;
- typed field and capability states;
- semantic-loss errors before transport;
- trusted credential/origin binding;
- provenance-aware history replay;
- exact contract revision persisted with session identity;
- curated profiles for every retained adapter surface.

**Acceptance:** Fake-transport request goldens prove legal OpenAI, compatible Chat, Groq, Anthropic, Google, Kimi, MiniMax, and OpenRouter surfaces. Unknown surfaces and adapters fail before dispatch. Unknown model metadata on a known surface emits only required core fields. The full first-slice acceptance suite is specified in `2026-08-28-serving-contract-core-design.md`.

**Non-scope:** External catalog ingestion, `/models` enrichment, live probes, UI, and cross-protocol fallback.

### Project 2: normalized metadata and offline catalog pipeline

**Contract:** Import pinned LiteLLM and Models.dev snapshots into provenance-bearing Evener claims without changing executable routing or request behavior.

**Pipeline:**

1. Fetch exact upstream revisions in a maintainer-controlled refresh operation.
2. Validate JSON/TOML shape, entry counts, license notices, and required fields.
3. Parse each source into separate claims rather than merging source objects.
4. Normalize identity, limits, modalities, capabilities, pricing, aliases, `mode`, endpoint hints, and provider integration hints.
5. Apply field-specific conflict rules.
6. Overlay reviewed Evener claims and materialize a generated artifact.
7. Emit a human-readable drift/conflict report.

**Acceptance:** Runtime uses only the pinned generated artifact. A source update is reproducible from its revision/hash. Removed or changed upstream entries produce a reviewable report. External data cannot alter adapter, URL, auth, or request-body behavior. Unknown fields survive normalization as unknown.

**Non-scope:** Live fetching at startup, automatic protocol selection, arbitrary provider adapters, and direct catalog-to-body passthrough.

### Project 3: provider inventory discovery

**Contract:** Query a configured provider's documented inventory endpoint and publish available wire IDs and explicitly advertised metadata.

**Behavior:**

- inventory is keyed by instance and origin;
- credentials use the instance binding from Project 1;
- a failed refresh retains the last good inventory with stale status;
- a successful inventory may add/remove selectable wire IDs;
- absent capability fields remain unknown;
- inventory cannot change protocol, request fields, auth, or destination.

**Acceptance:** Fake providers cover authentication, pagination, malformed payloads, stale retention, explicit capability fields, duplicate IDs, and provider-specific endpoint paths. No inventory response changes a serving contract without a curated mapping.

**Non-scope:** Inference calls, protocol probes, semantic capability inference, and fallback.

### Project 4: opt-in diagnostics and evidence cache

**Contract:** Let a user explicitly test a selected serving variant and record exact-scope evidence without changing runtime behavior implicitly.

**Probe rules:**

- explicit doctor/preflight command or UI action;
- clear disclosure that the call may cost money, consume quota, be logged, or trigger moderation;
- minimal non-sensitive input;
- no tools, uploads, provider state, continuation handles, cache fields, or background mode;
- smallest practical output cap;
- one feature per probe where possible;
- no probe on ordinary startup or default tests.

**Evidence key:**

```
instance + normalized URL/path + wire model + surface/API version
+ adapter revision + deployment/region/entitlement
+ relevant headers/options + stream mode + exact feature/schema hash
+ non-secret credential-scope fingerprint
```

Positive evidence expires. Negative capability evidence expires separately. Authentication, quota, outage, moderation, timeout, and transport failures never become negative capability claims. Configuration, adapter, API-version, and credential-scope changes invalidate evidence.

**Acceptance:** Probes require explicit consent, never persist secrets, retain exact scope and failure class, do not rewrite providers configuration, and never affect default runtime decisions without a reviewed override.

**Non-scope:** Automatic probing, arbitrary prompts, continuation proof, and protocol fallback.

### Project 5: metadata presentation

**Contract:** Expose the resolved serving variant and its claims so users can understand why a request will or will not be sent.

**Presentation:**

- selected provider instance, model, surface, adapter, and contract revision;
- supported, unsupported, unknown, and conflict states;
- source and age of important claims;
- stale inventory/evidence indicators;
- semantic features that will fail before dispatch;
- explicit overrides and their scope;
- provider documentation links.

**Acceptance:** CLI, hub, and web views render the same typed descriptor. Presentation never invents capabilities and never changes resolver output.

**Non-scope:** Contract resolution, request shaping, catalog refresh, and live probes.

### Project 6: provider-specific protocol fallback

**Contract:** Permit one rebuilt alternate request only for a provider/surface transition whose error proves the first request was not applied or whose idempotency is documented.

Fallback requires all of:

- provider-specific structured evidence of wrong surface;
- no response event or output byte;
- provider guarantee of non-execution or valid cross-surface idempotency;
- semantic intersection of both surfaces;
- rebuilt request through the alternate adapter;
- one alternate attempt maximum;
- durable logging of both attempts and the reason.

Generic 400/404/422, timeout, connection reset, auth, quota, moderation, model-not-found, partial stream, or empty answer never triggers fallback. Responses-only state, server tools, uploads, background work, and continuation never downgrade silently.

**Acceptance:** Provider-specific fake tests prove allowed and disallowed transitions, exact one-attempt behavior, semantic-feature rejection, and API-log visibility.

**Non-scope:** This project is not part of the initial serving-contract release.

## Runtime data flow

```text
trusted config + curated registry
        │
        ├── instance/origin/credential validation
        ├── model alias resolution
        ├── explicit surface selection
        └── serving-variant resolver
                    │
                    ├── agent capability/profile projection
                    ├── adapter request contract
                    ├── session variant identity
                    └── API-log contract identity

pinned LiteLLM/Models.dev claims ──> advisory enrichment only
provider /models inventory       ──> selectable IDs + explicit fields only
opt-in diagnostic evidence       ──> scoped claims + report only
```

The resolver has no network dependency. The adapter cannot replace the variant after resolution. The agent cannot expose a feature the adapter contract forbids. A provider-native history artifact cannot cross surfaces without a typed translation or a pre-dispatch error.

## Persistence and invalidation

Session metadata stores the selected instance, canonical/wire model IDs, surface, API version, effective-contract revision, credential-scope fingerprint, and continuation/state compatibility marker. Resume requires that exact variant and revision. A missing or incompatible revision produces a recovery/migration error.

Catalog snapshots, inventory records, runtime evidence, and session identity have separate storage and invalidation policies:

- catalog artifact: replaced only by a reviewed generated refresh;
- inventory: per instance/origin, stale-retained on transient refresh failure;
- evidence: exact scope, bounded TTL, invalidated by relevant revisions;
- session identity: immutable for the session unless an explicit model/surface switch creates a new request identity.

No credential, prompt, raw authorization header, or secret-bearing catalog field is stored in evidence or API-log identity.

## Rollout and gates

1. Apply incident mitigation for Groq.
2. Land Project 1 with curated contracts and deterministic request goldens.
3. Land Project 2 as an offline enrichment pipeline; keep its claims advisory.
4. Land Project 3 for documented inventory only.
5. Land Project 4 for explicit diagnostics and scoped evidence.
6. Land Project 5 for explainability.
7. Consider Project 6 only after a concrete provider supplies non-execution/idempotency evidence.

Every project must pass the normal Go tests, vet, lint, generated-output checks, secret scan, and deterministic test policy. Live provider checks remain explicit opt-in and separate from default gates.

## Program acceptance criteria

The full program is complete only when:

- every selectable model resolves to an explicit serving variant;
- every retained adapter surface has a typed contract and baseline wire tests;
- no unknown or external claim can enable an optional field or redirect traffic;
- semantic request features fail clearly rather than disappearing;
- credentials cannot cross their bound origin;
- provider-native state cannot cross incompatible surfaces;
- catalogs are pinned, reproducible, licensed, and advisory;
- inventory does not masquerade as capability proof;
- diagnostics are explicit, scoped, and non-authoritative by default;
- fallback is absent unless a provider-specific proof justifies it;
- session resume pins the serving variant and contract revision;
- CLI/hub/web presentation agrees with the resolver;
- default tests and startup require no provider credentials, network, quota, or current model behavior.

## Decisions for implementation plans

The following are architectural decisions, not implementation placeholders:

- Do not replace LiteLLM with Models.dev. Use both as pinned enrichment sources with different roles.
- Curated typed Evener profiles own executable behavior.
- The serving variant, not the bare model or provider name, is the request identity.
- Surface selection is deterministic: launch override, model default, instance default, exactly one defined surface, then failure.
- Unknown surface/adapter fails before dispatch. Unknown model metadata may use a known surface's required core only.
- No automatic protocol probing or cross-protocol fallback ships in the serving-contract core.
- No backward-compatibility layer constrains the clean configuration redesign.
- Any later change to these decisions requires a separate design review.
