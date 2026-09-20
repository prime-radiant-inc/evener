== panel 4ee2027b-0591-4921-a88c-25c277638d3f head dea5d5a3e outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=22655 verdict=1 chars=182
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=22656 verdict=0 chars=1044
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=22657 verdict=0 chars=2200

######## member 0 (codex default)
No issues found.

Summary: The change safely centralizes rejection payload decoding and makes transcript display default decoding forward-compatible with additional top-level fields.

######## member 1 (codex default)
## Review Findings
- **Severity**: Low
- **Location**: `appwire-client/typescript/errors.ts:60`
- **Problem**: `wireRejectionPayload` indexes `(error.data as Record<string, unknown>)[key]` without verifying `data` is an object; a `WireError` with matching `evenerErrorInfo` but non-object/missing `data` would throw instead of returning `undefined`.
- **Fix**: Guard like `mutationErrorData` does in the same file: return `undefined` when `error.data` is not a non-null object before indexing.
---
- **Severity**: Low
- **Location**: `appwire-client/typescript/transcriptDisplayConfig.ts:367`
- **Problem**: Comment justifies leniency by reference to `fromWirePatchResponse` `{layout, revision, config}` check, but no such function exists in the repo.
- **Fix**: Correct the comment to reference the actual existing precedent or remove the dangling reference.
## Summary
Extracts a shared rejection-payload helper and relaxes transcript display decoders for forward compatibility with one unsafe data access and one dangling comment reference.


######## member 2 (pi default)
## Summary

The commit extracts the shared rejection-payload helper into `appwire-client/typescript/errors.ts` as `wireRejectionPayload` (re-exported from `index.ts`) and rewires `keybindingsStore.ts`'s private `rejectionPayload` onto it, with new unit tests in `errors.test.ts`. It also relaxes `fromWireDefault`/`fromWireDefaults` to ignore unknown top-level fields (dropping the `hasExactKeys` check) and adds tests asserting that forward-compatible behavior. The extraction is behavior-preserving for keybindings, and the relaxation matches the already-lenient decoding in `mobile-native/src/nativePreferences.ts` (`decodeTranscript`/`decodeTranscriptPatch` only check the known fields), so the aggregate change is coherent.

---

**Severity: low** — `appwire-client/typescript/transcriptDisplayConfig.ts` (comment above `fromWireDefault`, around lines 369–374)

The new doc comment justifies the relaxation by claiming it mirrors "`fromWirePatchResponse`'s own `{layout, revision, config}` check already takes" a forward-compatible posture. No `fromWirePatchResponse` exists anywhere in the repository (the symbol appears only in this comment). The actual PATCH-response validation is inline in `cmd/evener-hub/frontend/src/stores/transcriptDisplay.ts` (`patchHubDefault`, the `exactResponse` check) and is *strict*: it requires `Object.keys(resultRecord).length === 3`, so a PATCH response carrying any extra top-level field still throws `InvalidPatchResponseError("Hub returned malformed transcript display PATCH response")`.

What goes wrong: the stated goal of accepting a future hub field "here too, not just on a PATCH reply" is not actually achieved for the PATCH reply — a hub that adds a top-level field to the PATCH response still breaks the primary save path, while GET/conflict/post-apply now tolerate it. The false rationale also misdirects a maintainer to a symbol that does not exist, obscuring where the counterpart validation really lives.

Suggested fix: either relax the frontend `exactResponse` check the same way (validate the three known fields, ignore extras) so the two decoders genuinely agree, or rewrite the comment to name the real location and drop the parity claim.
