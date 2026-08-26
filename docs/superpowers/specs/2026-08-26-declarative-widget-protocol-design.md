# Declarative widgets: versioned safe protocol design

- **Date:** 2026-08-26
- **Status:** proposed design; no runtime implementation
- **Issue:** `Refs #320`
- **Decision:** define a bounded protocol and threat model before renderer or interaction work.

## 1. Scope and non-goals

This document specifies the wire contract for model/tool-authored declarative UI. It does not implement parsing, rendering, transport, persistence, or a new tool. A future implementation MUST land separately and MUST preserve the existing `ask_user` contract until parity is demonstrated.

The protocol has two deliberately different operations:

- **`render_widget`** is display-only. It may show state and links, but no event from it can unblock an agent turn or invoke a tool.
- **`request_input`** is interactive. It creates one addressed pending input request, yields the turn, and accepts only validated responses to that request. Interaction semantics are not implied by visual appearance.

Non-goals: arbitrary HTML/CSS/JavaScript, model-selected component modules, custom client code, network/RPC actions from a widget, credential/payment collection, arbitrary navigation, remote A2UI execution, workflow parking or timers, replacing `ask_user` in this PR, or closing #320. A2UI may be evaluated behind an adapter later; it is not this wire format.

## 2. Wire conventions and v1 grammar

The protocol name is `evener.widget`. All messages are UTF-8 JSON objects (RFC 8259), with no duplicate keys. JSON `null`, booleans, strings, arrays, and objects are the only values. Strings must be valid Unicode scalar sequences; controls U+0000--U+001F are rejected. Numbers MUST be finite base-10 JSON numbers, integral where the schema says integer, and within signed 64-bit range; NaN and Infinity are rejected. Timestamps are RFC 3339 UTC with seconds (`YYYY-MM-DDTHH:MM:SSZ`). IDs are server-generated except `clientMutationId`; each is ASCII `[A-Za-z0-9_:-]{1,64}` and never contains secrets.

The following compact grammar is normative. `?` means optional, `*` means zero or more, and `+` means one or more. Objects reject unknown fields unless a row says otherwise; v1 has no extension fields. Missing required fields, wrong types, duplicate IDs, and extra fields are `invalid_widget`. An implementation MUST validate the whole object before allocation beyond the stated limits.

**Envelope:**

```text
Envelope = {
  protocol:"evener.widget", version:Version, kind:Kind,
  widgetId:Id, toolCallId:Id, sessionId:Id, revision:Int,
  createdAt:Timestamp, expiresAt:Timestamp|null,
  fallback:Fallback, root:Node,
  groupId?:Id, allowCancel?:Bool, sensitivity?:SensitivityMap
}
Version = {major:Int(1), minor:Int(0)}
Kind = "render_widget" | "request_input"
Fallback = {plainText:String(1..4096)}
SensitivityMap = {inputNodeId: "public"|"personal"|"secret"}
```

`widgetId`, `toolCallId`, and `sessionId` are required and server-issued. `revision` is an integer 1..2^63-1. `groupId`, if present, is server-issued and only legal on `request_input`; `allowCancel` defaults false and is only legal on `request_input`; `sensitivity` is only legal on `request_input` and keys input node IDs. `root` is one tree, not a list.

**Nodes:** each node is an object with required `type` and `nodeId:Id`; the fields below are the complete per-type fields (all other fields are rejected). `children` is legal only on `stack`.

| Type | Required fields | Optional fields and legality |
| --- | --- | --- |
| `stack` | `type,nodeId,direction,children` | `direction:"vertical"|"horizontal"`; `spacing?:"none"|"small"|"medium"`; children are any display node, or input nodes only in `request_input` |
| `text` | `type,nodeId,text:String(1..4096)` | `style?:"body"|"muted"|"code"`; no markup |
| `heading` | `type,nodeId,text:String(1..4096)` | `level?:1|2|3` |
| `label` | `type,nodeId,text:String(1..4096),for:Id` | none; `for` must name an input node |
| `choice_group` | `type,nodeId,options:[Option](1..128),multiple:Bool` | `required?:Bool`, `value?:[OptionId]` (display only may show value) |
| `text_input` | `type,nodeId,label:String(1..1024)` | `required?:Bool`, `placeholder?:String(0..1024)`, `maxLength?:Int(0..4096)`, `value?:String(0..4096)` |
| `number_input` | `type,nodeId,label:String(1..1024)` | `required?:Bool`, `min?:Number`, `max?:Number`, `step?:Number`, `value?:Number` |
| `checkbox` | `type,nodeId,label:String(1..1024)` | `required?:Bool`, `value?:Bool` |
| `button` | `type,nodeId,label:String(1..1024),action:ActionName` | `style?:"primary"|"secondary"|"cancel"` |
| `divider` | `type,nodeId` | none |
| `status` | `type,nodeId,text:String(1..4096),tone:"info"|"success"|"warning"|"error"` | none |
| `link` | `type,nodeId,label:String(1..1024),url:String(1..2048)` | none; URL policy below |

`Option = {optionId:Id,label:String(1..1024),detail?:String(0..1024),recommended?:Bool}`. Option IDs are unique within a group. `ActionName = "submit"|"cancel"`; `link_open` is a renderer-local navigation of a validated `link`, never a server action. `root` may contain only `stack,text,heading,label,choice_group,divider,status,link` for `render_widget`; `request_input` additionally permits `text_input,number_input,checkbox,button`. `label.for` and all action references must resolve. `button` and every input are illegal in `render_widget`. Inputs and actions are illegal outside `request_input`. A submit/cancel event is legal only when `nodeId` names a `button` whose declared `action` equals the event action; a `nodeId` naming an input or another button is `invalid_value` with `invalidNodeIds` as defined below. `SensitivityMap` must have exactly one entry for each input node, and no entry for a non-input node; omitted sensitivity defaults to `public`.

**Client event:** only `request_input` accepts this object:

```text
Event = {
  protocol:"evener.widget", version:Version, kind:"event",
  sessionId:Id, widgetId:Id, revision:Int, nodeId:Id,
  action:"submit"|"cancel", values:Values,
  clientMutationId:MutationId
}
Values = {inputNodeId:TypedValue}  // exactly declared input IDs; no extras
MutationId = [A-Za-z0-9_:-]{1,64}
```

`TypedValue` is a string, finite number, boolean, null, or an array of option IDs, exactly as declared by its input. `submit` requires all `required` inputs and `cancel` requires `{}`. One event has exactly one action and is limited to 256 KiB. `value` is legal only for `public` inputs; personal/secret inputs MUST omit it and start empty. A producer that supplies a sensitive prefill is rejected before persistence.

**Server result/error:**

```text
Result = {protocol:"evener.widget", version:Version, kind:"result",
  sessionId:Id, widgetId:Id, revision:Int, clientMutationId:MutationId,
  status:"accepted"|"duplicate", lifecycle:"accepted"|"cancelled",
  values?:Values, groupStatus?:"complete"}
Error = {protocol:"evener.widget", version:Version, kind:"error",
  sessionId:Id, widgetId:Id, revision:Int, clientMutationId?:MutationId,
  code:ErrorCode, lifecycle:"pending"|"rejected"|"accepted"|"expired"|"cancelled",
  currentRevision?:Int, invalidNodeIds?:[Id](1..16), fallback:Fallback}
ErrorCode = "invalid_widget"|"unsupported_version"|"unsupported_catalog"|
  "unsupported_component"|"invalid_value"|"stale_revision"|"already_resolved"|
  "expired"|"cancelled"|"unauthorized"|"rate_limited"|"idempotency_conflict"
```

A result never includes sensitive values (the producer receives them through the existing protected tool/session path); `values` is present only for non-sensitive fields and only where the negotiated policy permits it. Errors include only the bounded `invalidNodeIds` list for safe schema diagnostics; they include no values, token, URL, or internal detail. Responses are server-authenticated and addressed to the same session/widget.

**Capability advertisement:**

```text
Capabilities = {protocol:"evener.widget", supported:[{
  major:Int(1), minors:[Int(0..255)], catalogs:[Catalog],
  maxEnvelopeBytes:Int, maxEventBytes:Int, maxIdBytes:Int, maxFallbackBytes:Int,
  maxNodes:Int, maxDepth:Int, maxChildren:Int, maxTextBytes:Int,
  maxTextScalars:Int, maxOptionCount:Int, maxOptionLabelBytes:Int,
  maxOptionDetailBytes:Int, maxInputBytes:Int, maxActionsPerEvent:Int,
  maxRevisions:Int,
  maxEventsPerWidgetPerMinute:Int, maxEventsPerSessionPerMinute:Int,
  burstEvents:Int, interaction:Bool,
  a11y:["keyboard","screenReader","reflow","textZoom"]
}], clientId:Id}
Catalog = {id:String(1..64), revision:Int(1..255), sha256:Hex64}
```

Capability messages are authenticated, not model content. Every `max*` field is required, positive, and cannot exceed its corresponding protocol limit (`maxOptionLabelBytes` and `maxOptionDetailBytes` each cover their respective 1 KiB field; `maxActionsPerEvent` covers the one-action rule); every rate field is required and cannot exceed the corresponding protocol rate. `clientId` is server/session-issued. These schemas are v1's complete vocabulary, not illustrative JSON. A client that omits any field is not v1-compatible. If a producer encounters an unsupported optional field from a minor version, it MUST reject that version as `unsupported_version` and use the version-independent fallback; v1 itself permits no unknown fields.

## 3. Negotiation and deterministic fallback

A server-supported ordered list is configured per deployment; order is explicit, highest preference first. To negotiate, filter client entries to the same `protocol`, intersect major versions, then intersect minor versions and catalog identifiers. Selection is the first server `(major,minor,catalog)` tuple for which the client advertises that major, includes that minor, and advertises a catalog with the exact `id`, `revision`, and `sha256`. Catalog entries are sorted by server order, then client order. No major downgrade is implicit; a minor is never assumed compatible merely because it is numerically lower. A minor marked additive by the protocol registry may be selected only if the client explicitly advertises it.

The effective limit for each byte/count/rate is `min(protocol limit, deployment limit, client-advertised limit)`; because all v1 capability limits are required, there is no missing-limit default. The smaller effective limit is selected before producing an envelope. Values above it cause deterministic fallback rather than truncation. A catalog ID/revision/hash mismatch is `unsupported_catalog`; no-common major/minor is `unsupported_version`; no common interactive capability means `request_input` is not sent and the server uses the legacy/plain fallback. For example, a v1.1 envelope containing an unknown `tooltip` field is rejected by a v1.0 client with `unsupported_version` and the fallback carrier; it is never silently ignored.

`Fallback` is a version-independent carrier: the transport always permits the ASCII-tagged record `EVENER-FALLBACK/1\n` followed by a 4-byte big-endian UTF-8 byte length (1..4096), then plain text bytes. A client that cannot parse JSON MUST locate and display this carrier as escaped text; it MUST NOT inspect or execute the rejected envelope. Independently byte-checked example: the 40-byte UTF-8 text `Widget unavailable; see the transcript.!` is carried as `EVENER-FALLBACK/1\n\x00\x00\x00\x28Widget unavailable; see the transcript.!` (`0x28` = 40 payload bytes; prefix 18 + length 4 + payload 40 = 62 bytes total). A JSON envelope's `fallback.plainText` is copied into this carrier by the server before version/catalog negotiation. If the carrier itself is malformed, display the fixed localized string `Interactive content unavailable.` and record only the reason code.

## 4. Limits and untrusted content

These protocol maxima cannot be raised without a version change and threat review: encoded envelope 256 KiB; event 256 KiB; nodes 256; depth 16; children 64; text 4 KiB/1,024 Unicode scalars; options 128; option label/detail 1 KiB; input values 256 KiB total; one action/event; 64 revisions. Rate limits are 30 accepted or rejected events per widget per minute and 120 per session per minute, with a burst of 10; excess returns `rate_limited` and does not mutate state. Parsing is bounded before allocation.

All model-authored text is untrusted plain text. Escape it in every client context; preserve no executable markup; render URLs as text unless the typed `link` policy allows them. URLs must be HTTPS, valid, <=2048 bytes, and match a server-approved origin; reject `javascript:`, `data:`, custom schemes, inline HTML, markdown-as-HTML, SVG, iframe, image URL, CSS, expressions, templates, bindings, and modules. Never infer trusted labels, warnings, origins, or consent from model text.

## 5. Capabilities, routing, and authentication

A server-issued session capability binds a widget to `(tenant, session, turn, toolCallId, widgetId)` and the authenticated principal. Events route only to the server using exact session/widget/revision/node/mutation IDs. The server checks principal, pending state, selected protocol/catalog, action authorization, revision, and value schema before applying an action. Display-only widgets have no accepting endpoint. Reconnect re-authenticates and re-binds; a copied envelope/event is not portable. CSRF/origin protections follow the existing session channel. Unauthorized, cross-session, or unknown IDs fail closed as `unauthorized` without revealing existence.

## 6. Replay, lifecycle, and atomic effects

Canonical payload encoding for idempotency is UTF-8 JSON with lexicographically sorted object keys, array order preserved, no insignificant whitespace, and numbers rendered in the shortest RFC 8259 decimal form that round-trips to the same finite value; strings use JSON escaping. Before encoding, every `public` value remains its typed JSON value; every `personal`/`secret` value is replaced by `HMAC-SHA-256(K_session, UTF8(type + "\\0" + canonicalValue))` represented as lowercase 64-hex text. The one and only stored idempotency representation is `HMAC-SHA-256(K_idempotency, canonical Event with those substitutions)`, plus the substitution version `hmac-v1`; no plaintext SHA-256 digest is stored.

The idempotency key is `(tenant, sessionId, widgetId, clientMutationId)`. In one durable transaction, under a unique constraint, the server checks the key; if absent, verifies pending/revision/authorization/schema, inserts `in_flight` with digest and expiry, applies only the lifecycle transition and an outbox row, then commits. A separate authenticated outbox worker delivers the tool effect using an idempotency key `(tenant, toolCallId, widgetId, clientMutationId)` accepted by the tool boundary; it marks the outbox row delivered only after acknowledgement. Thus lifecycle commit and intent recording are atomic, and delivery is at-least-once at the transport boundary but exactly-once at the tool effect boundary. If the worker or process crashes, it retries the same outbox key; it never creates a second intent. If the tool boundary cannot provide deduplication, the request remains `in_flight` and fails closed rather than claiming acceptance. After delivery acknowledgement, the transaction records `accepted` and result; validation/rejection records the result without an outbox effect. Existing keys with the same digest return the stored result without effect; another digest returns `idempotency_conflict` without effect.

Idempotency records are retained until the later of widget expiry plus 24 hours or session expiry plus 24 hours, capped at 30 days from the record's `createdAt` epoch (the cap origin). They are then deleted only with the widget/session tombstone. After eviction, a mutation ID is not reusable: if its tombstone is unavailable, the server rejects the request as `expired`/`unauthorized` before any tool effect (fail closed), never treating it as new. Widget revisions are retained through the same window, capped at 64; post-eviction reconnect gets fallback only.

Lifecycle is `pending -> accepted|cancelled|expired`; `rejected` is a non-terminal mutation result while the request remains `pending`. `expired` is server-enforced at `expiresAt` (or session expiry); no timer invents an answer. The following effects are normative:

| Condition | Result/state/effect | Retry rule |
| --- | --- | --- |
| valid submit, pending/current revision | `accepted`, request terminal; outbox-backed exactly-once tool delivery; Result has `lifecycle:"accepted"` | same ID returns `duplicate` with accepted result; other IDs Error has `lifecycle:"accepted"` and `already_resolved` |
| valid cancel with `allowCancel` | `cancelled`, request terminal; Result has `lifecycle:"cancelled"`; no tool delivery | same ID returns `duplicate` with cancelled result; other IDs Error has `lifecycle:"cancelled"` and `already_resolved` |
| missing/invalid value | `invalid_value`, remains `pending`, returns current revision | corrected payload may use a new mutation ID; same ID only retries identical rejection |
| stale revision | `stale_revision`, remains pending, returns `currentRevision`; no effect | refresh then new ID; same ID repeats rejection |
| unauthorized/session mismatch | `unauthorized`, no existence/state disclosure, no effect | re-authenticate; mutation ID cannot be reused across sessions |
| duplicate digest | stored result, no effect | deterministic replay |
| digest mismatch | `idempotency_conflict`, no effect | new ID only after correction |
| expiry/cancel/race loser | `expired`/`cancelled`/`already_resolved`, terminal state unchanged | no retry can mutate |
| rate limit | `rate_limited`, remains pending, no effect | wait for server-provided retry window; new ID does not bypass quota |

Concurrent valid events use one atomic compare-and-set from `pending`; one wins and all losers observe the terminal state. Reconnect/cold attach replays the latest valid revision, lifecycle, and version-independent fallback. Errors are localized generic messages and preserve no secrets.

## 7. Multi-control `ask_user` compatibility

Existing `ask_user` remains authoritative: its `DefAskUser` schema, interactive-root-only execution, pending reconstruction from transcript, fixed acknowledgement, web pending-set derivation, plain `[answers]` user message, TUI parser, and byte-compatible reply remain unchanged.

One legacy `ask_user` tool call maps to **one** `request_input` envelope, with one `groupId` equal to the server-owned tool-call group. Each legacy question maps, in source-array order, to a `heading`/`text` plus exactly one `choice_group` (or declared input control); `header` is its label, `options` preserve order and labels/details, `multi_select` controls array cardinality, and `why` is plain text. The envelope contains all question controls and one submit/cancel action. There are no separate request widgets and no generic batch action.

Submit is an atomic aggregation: all required controls validate together; any invalid control leaves the whole group pending and returns one `invalid_value` result with bounded `invalidNodeIds`; no partial tool delivery occurs. A successful submit serializes answers in original question order using the existing byte-compatible `[answers]` format and delivers exactly one legacy reply. For the concrete questions `[{"header":"Color","question":"Pick","options":[{"label":"Red"},{"label":"Blue"}],"multi_select":false},{"header":"Tags","question":"Choose","options":[{"label":"A"},{"label":"B"}],"multi_select":true}]`, selections Red and A+B serialize exactly as UTF-8 bytes `["Red","A","B"]` (hex `5b 22 52 65 64 22 2c 22 41 22 2c 22 42 22 5d`) in the existing user-message path. Cancel delivers exactly the existing cancellation bytes `[]` (hex `5b 5d`) atomically. A race or expiry delivers no partial reply; existing pending questions remain reconstructible until the group terminal state. One `clientMutationId` covers the complete group payload, so retries cannot produce a second legacy reply. Legacy transcripts without envelopes continue through the existing parser. Migration stages are schema/fixtures, read-only rendering, interaction persistence, adapter/parity, then deprecation only after evidence and rollback capability.

## 8. Sensitive values and privacy boundary

An input's `sensitivity` is one of `public`, `personal`, or `secret`; it is a storage/transport policy, not merely visual masking. `secret` permits only text/number/choice/checkbox values needed by the declared action and forbids credentials, payment instruments, private keys, and unrestricted free-form collection as a non-goal. A masked control MUST still announce its purpose and type accessibly without exposing its value.

On submit, the authenticated client sends values over the existing protected session channel. The server validates them, encrypts sensitive values with envelope encryption under the session/tenant key, and stores ciphertext in a restricted mutation record. The canonical idempotency digest canonicalizes a keyed HMAC of each sensitive value (key held by the server), never plaintext; equality therefore supports replay without disclosure. Public values may be stored as ordinary validated JSON; personal/secret values are ciphertext plus type, length, and HMAC metadata. Transport logs never include values.

The transcript stores the envelope, sensitivity map, lifecycle, and a redacted response marker; it stores no personal/secret plaintext. The protected tool handoff decrypts only for the authorized originating tool execution, in memory, and does not echo values into assistant text. Mutation records and ciphertext follow session retention but are hard-deleted at session expiry plus 24 hours and at most 30 days; keys are destroyed on tenant retention deletion. Audit records contain actor/session/widget/tool IDs, version/catalog, sensitivity class, digest ID, action/result code, and timestamps, never values or ciphertext.

Retries return only the stored status/result and redacted marker. Diagnostics, crash reports, previews, notification payloads, and exports omit ciphertext, plaintext, HMACs, and sensitive lengths where that could identify content; exports include the envelope with `value: "[REDACTED]"`, lifecycle, and reason codes. Access requires the existing tenant/session authorization and is audited; support/debug access is not implicit. Display masking alone never provides storage or transport secrecy.

## 9. Accessibility and clients

Every interactive node MUST have an accessible name, native semantic role, visible focus, keyboard operation, error association, and programmatic state. Tab order follows document order; buttons/choices work without pointer/hover; Enter/Space behavior is native and consistent; Escape cancels only when declared. Focus moves to the request heading or error on attach/submit failure without an unbounded trap. Motion, contrast, text zoom, and reduced motion are respected.

Layouts reflow at narrow widths with no required horizontal scrolling or pointer-only action. TUI and mobile use the same typed semantics and fallback with platform-native controls. Screen-reader labels and fallback remain available when decoration is omitted.

## 10. Threat model and mitigations

| Threat / boundary | Required mitigation / evidence |
| --- | --- |
| XSS, HTML/JS/CSS injection | finite schema; plain-text escaping; reject markup, scripts, modules, URLs/assets; server/client validation |
| phishing or deceptive consent | trusted chrome identifies agent content; no model-controlled origin/permission text; no credentials/payments |
| arbitrary tool/RPC/network execution | action enum and bound widget only; no tool name, URL action, shell, or direct RPC |
| privilege escalation / confused deputy | authenticated capability; principal/tenant/tool binding; server authorization; fail closed |
| replay, double submit, cross-session copy | scoped key, canonical digest, durable unique transaction, tombstone, session binding |
| stale/concurrent clients | revision/CAS; explicit loser states; authoritative replay |
| render/parse DoS | pre-allocation limits, bounded event/rate, fallback |
| sensitive data exfiltration/logging | encryption/HMAC, redacted transcript/audit/exports, restricted tool handoff, no remote assets |
| accessibility-induced unsafe action | native semantics, keyboard/focus/error requirements, parity fixtures |
| client drift/downgrade | ordered negotiation, exact catalog hash, no implicit major/minor downgrade, version-independent fallback |
| untrusted producer/compromised client | backend authority; every client validates independently; no client-to-client trust |

## 11. Concrete acceptance fixtures for future runtime PRs

These are normative Given/When/Then fixtures, not claims that this design PR implements them. A future stage cannot advance until its listed fixtures pass on server, web, TUI, and mobile where applicable.

| ID | Given | When | Then |
| --- | --- | --- | --- |
| BND-1 (server, web/TUI/mobile) | JSON envelope fixture has a 256 KiB encoded body, a 16-deep `stack`, 256 nodes, 128 options, and text exactly 4,096 bytes/1,024 scalars | validate, then add one byte/node/level/option | first is accepted; each added boundary returns Error `invalid_widget`, `lifecycle:"rejected"`, and the 62-byte fallback carrier whose length field is `00 00 00 28` |
| SEC-1 (server, web/TUI/mobile) | `{"type":"text","nodeId":"n1","text":"<img src=x onerror=1>"}`, a `link` URL `javascript:alert(1)`, `https://evil.example`, and a button action `tool.exec` | validate/render and attempt activation | `invalid_widget`/fallback; no markup, script, network request, tool action, or origin outside approved HTTPS occurs |
| NEG-1 (server plus each client) | server tuples `[(1,1,cA),(1,0,cB)]`; client tuples `[(1,0,cB),(1,1,cWrong)]`, exact `cB` hash matches | negotiate | choose `(1,0,cB)`; replace hash with one byte difference and result is `unsupported_catalog` plus exact fallback carrier |
| NEG-2 (server plus each client) | client advertises only v2, then advertises v1.1 with unknown `tooltip:"x"`; server offers only v1.0 | negotiate/parse | no downgrade or field guessing; `unsupported_version` plus fallback carrier in both cases |
| AUTH-1 (server, web/TUI/mobile event clients) | authenticated session A sends an Event with session/widget IDs from B and altered tenant | submit | Error `unauthorized` with no existence/state detail; no read, lifecycle mutation, or tool effect |
| ID-1 (server and all event clients) | exact Event JSON `{"action":"submit","clientMutationId":"m1","nodeId":"b1","revision":1,"values":{"i1":"x"}}` | submit it twice | one outbox intent/tool effect; second Result has `status:"duplicate",lifecycle:"accepted"` and identical non-sensitive fields |
| ID-2 (server and all event clients) | first Event above is rejected for missing required `i2`; resend same `m1` with `i2:"changed"` | retry | Error `idempotency_conflict`, `lifecycle:"pending"`, no effect; corrected payload requires a new ID |
| RACE-1 (server, web/TUI/mobile) | two authorized clients submit the same current revision with distinct IDs in one transaction race | submit concurrently | one Result `accepted`; loser Error `already_resolved,lifecycle:"accepted"`; one outbox delivery and no duplicate reply |
| RECON-1 (server, web/TUI/mobile) | accepted outbox exists; client disconnects before Result, then reconnects; repeat after tombstone eviction | cold attach/retry | replay accepted Result and no second effect; post-eviction retry is `expired`/`unauthorized` and never new |
| LEG-1 (server, web/TUI/mobile) | exact two-question fixture from §7, selections Red and A+B | submit all controls | one atomic group and exact bytes `["Red","A","B"]` (`5b 22 52 65 64 22 2c 22 41 22 2c 22 42 22 5d`); cancel is `[]` (`5b 5d`); invalid one produces `invalidNodeIds` and no bytes |
| PRIV-1 (server, web/TUI/mobile) | secret `text_input` omits initial `value`, submits `s3cr3t`, then retries/diagnoses/exports | inspect envelope, transcript, outbox, audit, diagnostics, export | only authorized tool handoff sees plaintext; all other surfaces show `[REDACTED]`; stored canonical event uses `hmac-v1` substitution and ciphertext, never plaintext |
| A11Y-1 (web/TUI/mobile; server checks metadata) | exact request tree `heading(h1) -> label(l1 for i1) -> text_input(i1) -> button(b1 submit)` at 320 CSS px; keyboard sends Tab, Enter | attach, focus, submit missing value, cancel | focus order `h1,i1,b1`; roles `heading,textbox,button`; announcement `Pick: required`; error announcement `Pick is required`; no horizontal scroll/pointer-only action; fallback is readable |

## 12. Traceable delivery checklist

- [ ] Implement schema/validator fixtures BND, SEC, NEG and bounded fuzzing before rendering.
- [ ] Implement authenticated routing, atomic idempotency/lifecycle, RACE/RECON, and rate-limit fixtures before interaction.
- [ ] Implement sensitive storage/access/export tests PRIV before accepting sensitive controls.
- [ ] Implement one-envelope legacy adapter and LEG golden serialization before deprecating `ask_user`.
- [ ] Implement web/TUI/mobile keyboard, screen-reader, reflow, and fallback checks A11Y before parity sign-off.
- [ ] This PR adds no renderer, protocol endpoint, executable widget, runtime behavior, delivery claim, or issue closure.

## Rejected alternatives

Raw HTML/JS, model-selected React/component names, arbitrary JSON-schema UI, direct A2UI adoption, and “just replace the renderer” are rejected: they do not provide a bounded authority boundary and conflate display with pending-input lifecycle. A generic toolkit can be considered only behind this protocol's catalog, validation, capability, and fallback adapter.
