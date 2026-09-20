== panel 9f587a89-dc28-4b84-a73f-8e4eb778b509 head f7bfc4dbe outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=19458 verdict=0 chars=1280
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=19459 verdict=0 chars=1531
-- member 2 pi/deepseek-4.1-flash type=default status=done job=19460 verdict=0 chars=3713
-- member 3 codex/glm-5.3-vision type=default status=failed job=19461 verdict= chars=0

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium
- **Location**: `mobile-native/src/nativePreferenceDrafts.ts:25-65`
- **Problem**: The unreadable/null markers are identified solely by their `kind` field. A valid JSON draft containing `kind: "storedNull"` or `kind: "unparseable"` can therefore be treated as a marker, causing `matchesStoredBytes` to consider distinct records equivalent and allowing a CAS delete or replace to affect a newer record.
- **Fix**: Brand parser-created markers with a private symbol or compare their original serialized bytes, so ordinary stored JSON cannot forge marker identity.

---

- **Severity**: Low
- **Location**: `mobile-native/src/NativePreferencesProvider.tsx:202-209`
- **Problem**: After `readDraftOutcome` succeeds, the recovery path performs another uncaught `storage.load()`. A failure on this second read escapes instead of returning a storage-unavailable result and can leave the provider’s offline error state stale.
- **Fix**: Return the already-read value from `readDraftOutcome` and decode it, or wrap the second read and update storage-unavailable state consistently.

## Summary

The change introduces shared draft checkpoint/CAS infrastructure, generation handling, transcript settings state, and native unreadable-draft recovery.

######## member 1 (codex default)
## Review Findings
- **Severity**: Medium
- **Location**: `appwire-client/typescript/transcriptDisplayConfig.ts:382` and `appwire-client/typescript/transcriptDisplayStore.ts:199`
- **Problem**: `fromWirePatchResponse` tolerates extra top-level keys but `fromWireDefault`/`fromWireDefaults` require exact keys, so a future hub field is accepted on PATCH success yet rejected as malformed on GET, conflict `current`, and post-apply `applied`. The comment claims the same forward-compatible posture, which is false.
- **Fix**: Make `fromWireDefault`/`fromWireDefaults` tolerate extra keys like `fromWirePatchResponse`/`fromWireChange`, or narrow the comment and add GET/conflict/applied extra-key cases.
---
- **Severity**: Low
- **Location**: `appwire-client/typescript/transcriptDisplayStore.ts:474`
- **Problem**: A well-formed PATCH reply that is stale because a broadcast advanced `hub[layout]` while the request was in flight throws `InvalidPatchResponseError` with the malformed-response message. The reply was valid when produced; reporting it as malformed misdiagnoses a race as a hub bug and sets `hubError`/`hubErrors` incorrectly.
- **Fix**: Treat `canonical.revision` below the current hub revision as superseded/stale (ignore via `applyHubDefault` staleness or return current) instead of throwing the malformed-response error.
## Summary
Adds the transcript display hub-defaults store with direct writes alongside draft-port refactors, but has forward-compatibility and stale-reply error-classification inconsistencies.


######## member 2 (pi default)
## Summary

The series extracts two shared SDK primitives out of `keybindingsStore.ts` into new modules — a generic checkpointed-draft **port/repository** (`draftCheckpointPort.ts`) with a generalized editor (`checkpointedDraftEditor.ts`), and a generic **settings-hub generation/retirement core** (`settingsHubGeneration.ts`) — then builds a second store (`transcriptDisplayStore.ts`) on them as the design oracle. Along the way it hardens the draft repository's compare-and-swap (`save`/`removeIf`/`discardClassified`/`replaceClassified`, `insertIfAbsent`, raw-identity tracking), plumbs a `generation` staleness stamp and `draftUnreadable` state through the keybindings store, adds an offline/store-free unreadable-draft recovery path to the native app, and adds wide test coverage.

I reviewed the final aggregate diff (not per-commit). The logic is dense but internally consistent; the settle/fence/CAS ordering and the unreadable-record recovery paths are each exercised by focused tests. I found one concrete defect.

---

**Severity: low** — `canonicalJson` is an unsound identity encoding for `undefined`

`appwire-client/typescript/draftCheckpointPort.ts:48-57`

`canonicalJson` is documented as producing "a canonical string encoding", is now a **public export** (`index.ts:144`, `@evener/appwire-client`), and is the comparison primitive used by `draftIdentityKey`/`matchesStoredBytes` and the in-memory draft fakes for compare-and-swap identity. It mishandles `undefined` in two ways:

- `return JSON.stringify(value)` returns the JS `undefined` (not a string) for `undefined`/function/symbol input, despite the declared `: string`. A consumer calling the public `canonicalJson(undefined)` gets `undefined` where the type promises a string. Internal callers happen to be safe (`draftIdentityKey` wraps it in a template literal), but the exported contract is false.
- The array branch, `` `[${value.map(canonicalJson).join(",")}]` ``, collapses an all-`undefined` element to an empty join: `Array.prototype.join` renders `undefined` as `""`, so `canonicalJson([undefined]) === canonicalJson([]) === "[]"`. Two structurally different values therefore compare equal through the very CAS identity check this function backs (a false `removeIf`/`replaceIf` match if any identity array ever contains `undefined`). Production JSON parsed from storage never contains `undefined`, so there is no current data-loss path, but the function's advertised invariant ("structurally equal values compare equal") is violated, and the function is no longer test-only.

Suggested fix: encode non-JSON primitives explicitly and return a definite string, e.g.

```ts
export function canonicalJson(value: unknown): string {
  if (value === undefined) return "\u0000undefined";   // distinct from both null and ""
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (value !== null && typeof value === "object") { /* …unchanged… */ }
  return JSON.stringify(value) ?? "\u0000undefined";
}
```

(Any sentinel that cannot collide with a JSON-encoded value works; the point is that the array branch receives a string and `[]` no longer collides with `[undefined]`.)

---

No other issues found. In particular, I checked the `settledWrite`/`settleWrite` CAS ordering (including the `savingAtReadStart` and replaced-checkpoint adoption paths), the `directWrite` reservation and its same-turn gates against `saveDraft`/`patchOverrides`, the `awaitingFirstPayload` reset across `begin`/`end`, the `UnreadableDraftError` vs genuine-port-failure distinction in `restoreDraft`, and the native `parseDraftBytes` markers against `matchesStoredBytes`; those paths are correct and covered by the added tests.

######## member 3 (codex default)
<no output; error: outage: agent: codex failed: exit status 1 (parse error: codex stream reported failure: exceeded retry limit, last status: 429 Too Many Requests)
stderr: 2026-09-18T20:49:57.239264Z ERROR codex_models_manager::manager: failed to refresh available models: stream disconnected before completion: failed to decode models response: missing field `models` at line 1 column 8101; body: {"object":"list","data":[{"id":"bge-rr-v2-m3","object":"model","created":0,"owned_by":"lunaroute","capabilities":{"embeddings":false,"rerank":true}},{"id":"deepseek-4.1-flash","object":"model","created":0,"owned_by":"lunaroute","context_window":1048576,"context_length":1048576,"max_input_tokens":1048576,"max_output_tokens":262144,"max_completion_tokens":262144,"capabilities":{"anthropic_messages":true,"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true},"client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"deepseek_v41","thinkingLevelMap":{"high":"high","low":"low","max":"high","medium":"medium","minimal":null,"off":null,"xhigh":"high"}}}},{"id":"deepseek-4.1-flash-background","object":"model","created":0,"owned_by":"lunaroute","context_window":1048576,"context_length":1048576,"max_input_tokens":1048576,"max_output_tokens":262144,"max_completion_tokens":262144,"capabilities":{"anthropic_messages":true,"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true},"client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"deepseek_v41","thinkingLevelMap":{"high":"high","low":"low","max":"high","medium":"medium","minimal":null,"off":null,"xhigh":"high"}}}},{"id":"emb-granite","object":"model","created":0,"owned_by":"lunaroute","capabilities":{"embeddings":true,"rerank":false}},{"id":"emb-nomic-code","object":"model","created":0,"owned_by":"lunaroute","capabilities":{"embeddings":true,"rerank":false}},{"id":"emb-nomic-moe","object":"model","created":0,"owned_by":"lunaroute","capabilities":{"embeddings":true,"rerank":false}},{"id":"emb-qwen3","object":"model","created":0,"owned_by":"lunaroute","capabilities":{"embeddings":true,"rerank":false}},{"id":"flux2-klein","object":"model","created":0,"owned_by":"lunaroute","capabilities":{"image_generation":true}},{"id":"glm-5.2-vision","object":"model","created":0,"owned_by":"lunaroute","context_window":524288,"context_length":524288,"max_input_tokens":524288,"max_output_tokens":131072,"max_completion_tokens":131072,"capabilities":{"anthropic_messages":true,"glmv_placeholder_neutralize":true,"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true},"client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"zai","thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":"medium","minimal":"minimal","off":null,"xhigh":"xhigh"}}}},{"id":"glm-5.2-vision-background","object":"model","created":0,"owned_by":"lunaroute","context_window":524288,"context_length":524288,"max_input_tokens":524288,"max_output_tokens":131072,"max_completion_tokens":131072,"capabilities":{"anthropic_messages":true,"glmv_placeholder_neutralize":true,"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true},"client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"zai","thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":"medium","minimal":"minimal","off":null,"xhigh":"xhigh"}}}},{"id":"glm-5.2-vision-flex","object":"model","created":0,"owned_by":"lunaroute","context_window":524288,"context_length":524288,"max_input_tokens":524288,"max_output_tokens":131072,"max_completion_tokens":131072,"capabilities":{"anthropic_messages":true,"glmv_placeholder_neutralize":true,"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true},"client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"zai","thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":"medium","minimal":"minimal","off":null,"xhigh":"xhigh"}}}},{"id":"glm-5.3","object":"model","created":0,"owned_by":"lunaroute","context_window":524288,"context_length":524288,"max_input_tokens":524288,"max_output_tokens":131072,"max_completion_tokens":131072,"capabilities":{"anthropic_messages":true,"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true,"vision_jpeg_transcode_png":true},"client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"zai","thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":"high","minimal":"low","off":"none","xhigh":"max"}}}},{"id":"glm-5.3-background","object":"model","created":0,"owned_by":"lunaroute","context_window":524288,"context_length":524288,"max_input_tokens":524288,"max_output_tokens":131072,"max_completion_tokens":131072,"capabilities":{"anthropic_messages":true,"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true,"vision_jpeg_transcode_png":true},"client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"zai","thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":"high","minimal":"low","off":"none","xhigh":"max"}}}},{"id":"glm-5.3-flash","object":"model","created":0,"owned_by":"lunaroute","context_window":524288,"context_length":524288,"max_input_tokens":524288,"max_output_tokens":163840,"max_completion_tokens":163840,"capabilities":{"anthropic_messages":true,"glmv_placeholder_neutralize":true,"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true,"vision_jpeg_transcode_png":true},"client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"zai","thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":"high","minimal":"low","off":"low","xhigh":"high"}}}},{"id":"glm-5.3-flash-background","object":"model","created":0,"owned_by":"lunaroute","context_window":524288,"context_length":524288,"max_input_tokens":524288,"max_output_tokens":163840,"max_completion_tokens":163840,"capabilities":{"anthropic_messages":true,"glmv_placeholder_neutralize":true,"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true,"vision_jpeg_transcode_png":true},"client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"zai","thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":"high","minimal":"low","off":"low","xhigh":"high"}}}},{"id":"glm-5.3-vision","object":"model","created":0,"owned_by":"lunaroute","context_window":524288,"context_length":524288,"max_input_tokens":524288,"max_output_tokens":131072,"max_completion_tokens":131072,"capabilities":{"anthropic_messages":true,"glmv_placeholder_neutralize":true,"image_generation":false,"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true,"vision_jpeg_transcode_png":true},"client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"zai","thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":"high","minimal":"low","off":"none","xhigh":"max"}}}},{"id":"glm-5.3-vision-background","object":"model","created":0,"owned_by":"lunaroute","context_window":524288,"context_length":524288,"max_input_tokens":524288,"max_output_tokens":131072,"max_completion_tokens":131072,"capabilities":{"anthropic_messages":true,"glmv_placeholder_neutralize":true,"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true,"vision_jpeg_transcode_png":true},"client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"zai","thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":"high","minimal":"low","off":"none","xhigh":"max"}}}},{"id":"hidream-o1","object":"model","created":0,"owned_by":"lunaroute","capabilities":{"image_generation":true}},{"id":"qwen-image-2512","object":"model","created":0,"owned_by":"lunaroute","capabilities":{"image_generation":true}},{"id":"qwen-image-edit-2511","object":"model","created":0,"owned_by":"lunaroute","capabilities":{"image_generation":true}}]}
>
