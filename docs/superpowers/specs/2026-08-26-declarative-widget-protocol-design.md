# Declarative widgets: versioned safe protocol design

- **Date:** 2026-08-26
- **Status:** proposed design; no runtime implementation
- **Issue:** `Refs #320`
- **Decision:** define a bounded protocol and threat model before renderer or interaction work.

## 1. Scope and non-goals

This document specifies the wire contract for model/tool-authored, declarative UI. It does not implement parsing, rendering, transport, persistence, or a new tool. A future implementation MUST land separately and MUST preserve the existing `ask_user` contract until parity is demonstrated.

The protocol has two deliberately different operations:

- **`render_widget`** is display-only. It may show state and links, but no event from it can unblock an agent turn or invoke a tool.
- **`request_input`** is interactive. It creates one addressed pending input request, yields the turn, and accepts only validated responses to that request. Interaction semantics are not implied by visual appearance.

Non-goals: arbitrary HTML/CSS/JavaScript, model-selected component modules, custom client code, network/RPC actions from a widget, credential/payment collection, arbitrary navigation, remote A2UI execution, workflow parking or timers, replacing `ask_user` in this PR, or closing #320. A2UI may be evaluated behind an adapter later; it is not this wire format.

## 2. Envelope and negotiation

The protocol name is `evener.widget`; the version is a major/minor integer, not a free-form string. A producer sends only a version advertised by the authenticated client/session. Major versions are incompatible; minor versions are additive and must have a defined fallback. The server validates before persistence or fan-out, and every client validates again before display.

Normative envelope (illustrative JSON; omitted optional fields have no effect):

```json
{
  "protocol": "evener.widget",
  "version": {"major": 1, "minor": 0},
  "kind": "render_widget",
  "widgetId": "w_01J...",
  "toolCallId": "tc_01J...",
  "sessionId": "s_01J...",
  "revision": 1,
  "createdAt": "2026-08-26T00:00:00Z",
  "expiresAt": null,
  "fallback": {"plainText": "A widget is available; see the transcript."},
  "root": {"type": "stack", "direction": "vertical", "children": [
    {"type": "text", "text": "Build completed", "style": "body"}
  ]}
}
```

`widgetId`, `toolCallId`, and `sessionId` are opaque server-generated IDs with strict alphabet/length limits. IDs are never supplied by model content. `revision` increases monotonically per widget. `fallback.plainText` is required for a valid widget and is the safe rendering for unknown version/component, unsupported capability, parse failure, or policy rejection. Clients MUST NOT guess meaning from unknown fields or component names.

Negotiation is an authenticated session capability: the client advertises supported major versions, minor versions, catalog hash/version, maximum envelope/component/text sizes, interaction support, and a11y features. The server selects the highest mutually supported version and catalog. No negotiation response grants new server capabilities; it only narrows what may be sent.

## 3. Finite catalog and typed data

Version 1 permits only these native nodes:

`stack` (vertical/horizontal), `text`, `heading`, `label`, `choice_group` (single/multi), `text_input`, `number_input`, `checkbox`, `button`, `divider`, `status`, and `link`.

Every node has a unique bounded `nodeId`, and a bounded child count/depth. Layout is structural, not CSS: fixed direction, spacing tokens, and size tokens only. A node's `value`, `options`, and `action` are typed by its catalog entry. There is no generic `props`, expression, template, data binding, event handler, or polymorphic JSON value.

Allowed values are null, boolean, finite number, bounded UTF-8 string, and arrays/objects only where the node schema explicitly permits them. Inputs declare a type, requiredness, length/range/pattern constraints from a small server-approved set, and an optional redacted display mode. Constraints are validation, not a promise that client-side checks are sufficient; the server revalidates all submitted values.

Actions are catalog tokens (`submit`, `cancel`, `link_open`) carrying only the addressed widget/node ID and typed values. They are not tool names, RPC methods, URLs, shell commands, JavaScript, or prompts. A `link` may contain only an HTTPS URL from a server-approved origin or an inert reference resolved by the server. No `javascript:`, `data:`, custom scheme, inline HTML, markdown-as-HTML, SVG, iframe, image URL, or CSS is accepted.

## 4. Limits and untrusted content

These are protocol defaults; deployment may lower them but MUST NOT raise them without a version change and threat review:

| Resource | Limit |
| --- | ---: |
| encoded envelope | 256 KiB |
| nodes | 256 |
| depth | 16 |
| children per container | 64 |
| text value | 4 KiB / 1,024 Unicode scalar values |
| options per choice group | 128 |
| option label/detail | 1 KiB each |
| input values in one response | 256 KiB total |
| actions per accepted mutation | 1 |
| widget revisions retained | 64 |

Reject duplicate IDs, cycles, non-finite numbers, invalid UTF-8, oversized fields, unknown required fields, and limits exceeded. Parsing is streaming or bounded before allocation. Truncation is not silent: render fallback and record a non-sensitive reason code.

All model-authored text is untrusted plain text. Escape it in every client context, preserve no executable markup, and render URLs as text unless the typed `link` policy allows them. Never interpolate text into HTML, attributes, CSS, shell commands, logs, prompts, or telemetry without context-appropriate encoding. Do not infer trusted labels, warnings, origins, or consent from model text.

## 5. Capabilities, routing, and authentication

A server-issued session capability binds a widget to `(tenant, session, turn, toolCallId, widgetId)` and the authenticated principal. The producer cannot broaden that binding. The server is the authority for catalog, action, origin, and authorization; web, TUI, and mobile independently enforce the same allowlist and limits.

Events route to the server using the exact `widgetId`, `revision`, `nodeId`, and `clientMutationId`. The server checks session/principal, pending state, selected protocol/catalog, action authorization, revision, and value schema before applying an action. A display-only widget has no accepting endpoint. Clients do not send events directly to tools or peers.

Transport authentication and CSRF/origin protections follow the existing session channel. A reconnect MUST re-authenticate and re-bind; a copied widget envelope or event is not portable between sessions. Authorization failures are indistinguishable from unknown/expired widgets to an untrusted client.

## 6. Replay, idempotency, and lifecycle

`clientMutationId` is a client-generated opaque unique ID, scoped to the authenticated session and widget. The server durably records accepted, rejected, and in-flight mutations long enough to make retries deterministic. Replaying the same ID with the same canonical payload returns the original result and performs no second action; reuse with a different payload is rejected. A different ID is not a retry.

Exactly one response resolves one `request_input` widget by default. A future explicitly versioned batch action may resolve a declared group, but clients MUST NOT treat “submit” as resolving the whole pending set. Concurrent clients race on the server's atomic pending transition: one accepted resolution wins, others receive `already_resolved` and retain their local state until the authoritative update arrives.

Lifecycle states are `pending`, `accepted`, `rejected`, `expired`, and `cancelled`. State transitions are monotonic and persisted with the transcript. Reconnect/cold attach replays the latest valid revision plus lifecycle state. Unknown version/catalog, malformed data, expired request, or unsupported interaction renders `fallback.plainText` with a non-actionable state. No timer or automatic answer is introduced by this design.

Errors are typed and safe: `invalid_widget`, `unsupported_version`, `unsupported_component`, `invalid_value`, `stale_revision`, `already_resolved`, `expired`, `unauthorized`, and `rate_limited`. UI displays a localized generic message and preserves fallback/transcript context; raw validation details, tokens, or server internals are not exposed.

## 7. Accessibility and clients

Every interactive node MUST have an accessible name, correct native semantic role, visible focus, keyboard operation, error association, and programmatic state. Tab order follows document order; buttons and choices work without pointer/hover; Enter/Space behavior is consistent with the native control; Escape only cancels when the request declares cancellation. Focus moves to the request heading/error on attach or submit failure without trapping users in an unbounded widget. Motion, contrast, text zoom, and reduced-motion settings are respected.

Layouts reflow at narrow widths, have no required horizontal scrolling, and expose no pointer-only action. TUI and mobile use the same typed semantics and fallback, with platform-native controls. Screen-reader labels and the fallback remain available when visual decoration is omitted. Accessibility is a release acceptance criterion, not a renderer-specific enhancement.

## 8. Compatibility and `ask_user` mapping

Existing `ask_user` remains authoritative. Its `DefAskUser` schema, interactive-root-only execution, pending-state reconstruction from transcript, fixed acknowledgement, web pending-set derivation, plain `[answers]` reply, TUI parser, and byte-compatible reply are unchanged by this document.

The compatibility adapter maps each legacy question to a `request_input` envelope: `question` becomes escaped `text`/`heading`; `options` become typed `choice_group` options; `multi_select` controls cardinality; `header` is the accessible name; and the existing answer serialization remains the server-side response. `why` is plain explanatory text. The adapter assigns server IDs and emits the required fallback. It MUST preserve multiple-question pending semantics until an explicit parity review decides otherwise. Legacy transcripts without envelopes continue to render through the existing parser.

Migration is staged: (1) schema/catalog fixtures and independent validators; (2) read-only `render_widget`; (3) interaction protocol with persistence/reconnect/idempotency; (4) `ask_user` adapter and web/TUI/mobile parity; (5) deprecation only after compatibility evidence. Each stage has a rollback to legacy rendering/transport. No stage silently interprets an unknown version.

## 9. Threat model and mitigations

| Threat / boundary | Required mitigation / evidence |
| --- | --- |
| XSS, HTML/JS/CSS injection | finite native catalog; plain-text escaping; reject markup, URLs, scripts, modules; client and server validation |
| phishing or deceptive consent | trusted chrome identifies agent-origin content; no model-controlled origin/brand/permission text; typed confirmation semantics; no credentials/payments |
| arbitrary tool/RPC/network execution | action enum only; server routes by bound widget ID; no tool name, URL, shell, or direct RPC in payload |
| privilege escalation / confused deputy | authenticated session capability; principal/tenant/tool-call binding; server authorization; fail closed |
| replay, double submit, cross-session copy | scoped `clientMutationId`, canonical-payload replay record, atomic lifecycle, session binding |
| stale/concurrent clients | monotonic revision and pending transition; authoritative update; `stale_revision`/`already_resolved` |
| render/parse DoS | pre-allocation byte/depth/node/text/option/action limits; one bounded action; rate limits; fallback |
| data exfiltration / sensitive logging | no remote assets; typed redaction; allowlisted URLs; audit reason codes and IDs only; payload access policy |
| accessibility-induced unsafe action | native semantics, keyboard/focus/error requirements, explicit cancel, parity testing across clients |
| client drift / downgrade | authenticated capability negotiation, catalog hash, major-version rejection, deterministic fallback, staged migration |
| untrusted producer or compromised client | backend is authoritative; every client independently validates; no client-to-client trust; audit reject reasons |

Privacy boundary: transcript persistence stores the validated envelope and lifecycle metadata under existing session retention/access controls; audit logs store actor, session/widget/tool IDs, version/catalog, action/result reason, and timestamps, but not input values unless an existing policy explicitly permits them. Redacted fields remain redacted in retries, diagnostics, and exports.

## 10. Traceable acceptance and threat checklist

This is a design acceptance checklist, not a claim that runtime tests exist.

- [ ] Protocol name/version, negotiation, catalog hash, fallback, stable IDs, and revision are normative.
- [ ] `render_widget` cannot unblock; `request_input` has addressed pending semantics.
- [ ] Catalog is finite; values/actions are typed; arbitrary props, code, HTML, CSS, scripts, URLs, and RPC are excluded.
- [ ] Server and each client enforce byte, depth, count, text, option, value, revision, and rate bounds before use.
- [ ] Session/principal/tool-call binding, authorization, origin/CSRF protections, replay/idempotency, and concurrent resolution are specified.
- [ ] Persistence, reconnect, cold attach, unknown version/component, lifecycle, fallback, and safe errors are specified.
- [ ] Web/TUI/mobile share semantics and meet keyboard, screen-reader, focus, zoom, contrast, and narrow-layout requirements.
- [ ] Privacy, redaction, audit boundaries, migration rollback, and legacy `ask_user` compatibility are explicit.
- [ ] Runtime follow-up work includes schema/fuzz, malicious-content/action, parity, persistence/reconnect, multi-client, retry, accessibility, and legacy-transcript evidence before deprecation.
- [ ] This PR adds no renderer, protocol endpoint, executable widget, runtime behavior, delivery claim, or issue closure.

## Rejected alternatives

Raw HTML/JS, model-selected React/component names, arbitrary JSON-schema UI, direct A2UI adoption, and “just replace the renderer” are rejected: they do not provide a bounded authority boundary and conflate display with pending-input lifecycle. A generic toolkit can be considered only behind this protocol's catalog, validation, capability, and fallback adapter.
