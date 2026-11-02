# Google PDF document input — issue #4463

## Scope and acceptance

A Google/Gemini profile whose own catalog row declares `pdf` input accepts a
user-message `ContentDocument`. The request builder emits a valid Gemini media
part. A PDF read through the vision side-channel reaches that Google route,
including a return to the session model after a refused configured route.
The original bytes and history are not rewritten.

This change is confined to the Google request builder, document capability
helpers, their tests, and this spec. The isolated lane is
`issue-4463-google-documents`, based on main `48a6703dc790cd665bb574c6a411afe6ea0796d2`
merged as `0fe677218911fb7a6e3c435daf91030f7a6fca83` before this spec.
The issue's acceptance criteria authorize this bounded implementation without
an interactive approval round.

## Exact request mapping

For a `RoleUser` message, preserve the order of text, image and document parts.
For a non-nil `DocumentData` with nonempty `Data`, produce exactly:

```json
{"inlineData":{"mimeType":"application/pdf","data":"<base64 of original bytes>"}}
```

Use standard base64 encoding. Trim the declared `MediaType`; a missing or blank
value defaults to `application/pdf`. Preserve a nonblank declared media type,
as the existing document builders do, without claiming additional model
modalities. Bytes take precedence when both `Data` and `URL` are present.
`FileName` is metadata, not a Gemini `Part` field, and is not emitted.

For a URL-only document, emit
`{"fileData":{"mimeType":"application/pdf","fileUri":"<trimmed URL>"}}`,
with the same media-type rule. The URI must be a Gemini-supported file URI;
the builder neither uploads nor fetches it and does not read local paths.
Nil document pointers and documents with neither bytes nor a nonblank URI
contribute no part, matching the existing image/document builder conventions.
Neighboring text parts survive.

Assistant-message `ContentDocument` remains unsupported and returns a typed
`llm.ConfigurationError`. This is intentional: this issue adds document input,
not generated-document replay. Test that decision explicitly rather than
letting it inherit the old combined audio/document switch accidentally.
Audio behavior remains unchanged in both roles.

## Capabilities and routing

`llm.DeliversUserMessageDocuments` returns true for `ProtocolGoogle` as well as
Anthropic and Responses. It remains false for Chat Completions and unknown
protocols. `Profile.AcceptsDocumentInput` continues to require the model's own
`pdf` modality plus the adapter delivery fact. Image-only, synthesized and
renamed profiles must not acquire PDF support merely because the protocol can
serialize it.

The existing side-channel and cheap-model refusal/retry helpers consume that
profile fact. No route redesign is needed. Cover both an ordinary Google PDF
description and recovery from a refused configured route back to a PDF-capable
Google session. Keep `vision_model=off`, explicit route selection, fallback
handling and image behavior unchanged.

## Tool results and double delivery — included, not deferred

Keep the existing public contract: `DeliversToolResultMedia` is image-only,
and `RowSeesToolResultMedia` does not mark PDF tool results as natively seen.
Close the mismatch at the Google writer rather than broadening the capability
contract in this fix. A tool result with a non-image media type, including
`application/pdf`, contributes its ordinary `functionResponse` name and JSON
response but no binary `parts`. The existing side-channel describes the PDF
through the newly supported user-document mapping. Thus the serving request
contains its tool response and description, not a second inline copy of the
PDF. This also applies when the model lacks multimodal function responses:
a document is not an image and must not trigger the image-only capability
error. The original tool-result bytes remain in history.

Tool-result images retain their existing behavior: blank media type defaults
to `image/png`; image media ride inside `functionResponse.parts` only with
`MultimodalToolResults`, and unsupported image delivery still errors.
Trim and lowercase the claimed tool-result media type for classification;
preserve the existing image wire values. Token estimates must stop billing
Google tool-result document bytes that no longer ride on the wire.

This trades accidental native PDF function responses for one consistent
side-channel contract across supported protocols. Native Google tool-result
PDF delivery is not part of this PR, but the double-delivery bug is fully fixed
here, so no follow-up is required to complete #4463. There is no change to
Anthropic or Responses serialization or to explicit vision overrides.

## Non-goals and preservation

No provider-wide capability redesign, catalog changes, audio support,
generated-document replay, file uploads, local-document I/O, PDF parsing or
sniffing, size/page-limit enforcement, new client state, or live provider tests.
No original attachment, durable history, credentials or provider settings are
modified. Existing provider limits and errors remain provider-owned. Shared
helper comments are updated where needed; unrelated findings become issues,
not edits in this PR.

## Verification

Use deterministic, red-first tests at the external provider boundary or the
real builder, not live Gemini calls and not error-message wording assertions.
Before implementation, observe the new user-document request fail with
`ConfigurationError`, Google document capability/profile assertions fail,
Google PDF refusal fallback fail, tool-document omission fail, and the
Google document billing expectation fail.

Tests cover:

- User PDF bytes, standard base64, media type/default, mixed-part ordering,
  bytes-over-URI precedence, URL-only mapping and nil/empty documents.
- Explicit assistant document refusal and unchanged audio refusal.
- PDF tool results on cap-enabled and cap-disabled Google rows: text/JSON
  response survives, no binary parts, no image-cap refusal; image behavior is
  preserved.
- Google delivery helper and real profile PDF facts, with negative image-only,
  synthesized/renamed cases, and document tool-result delivery remaining false.
- Real side-channel routing to a Google session and recovery to it after a
  configured route refusal, with user-message PDF bytes preserved. A Google
  transport test must inspect the final request body so the side-channel's
  document acceptance is not proved only by a permissive fake adapter.
- Tool-result PDF estimates equal text-only estimates for Google while image
  estimates remain unchanged.

Run focused commands (selectors may be extended for the named new tests):

```sh
go test ./llm/providers/google -count=1
go test ./llm -run 'TestDeliversUserMessageDocuments|TestRowSeesToolResultMedia|TestEstimateMessagesInputTokensForResolved_BillsToolResultDocumentsOnlyWhereTheyRide' -count=1
go test ./agent/provider -run 'TestProfileDocumentInputPredicate|TestProfileImageInputPredicates|TestSeesToolResultMediaRequiresTheModelsOwnRow' -count=1
go test ./agent -run 'TestDescribeImage_.*(Google|PDF)' -count=1
make build
```

CI owns the full suite; do not run it locally. Freeze this spec and run two
independent competing PAR reviewers before implementing. Run implementation
PAR, simplify-code, commit and branch RoboRev review. Fetch and merge main
again before the final push if it moved. Shepherd the PR against main; admin
squash only with all required checks green and every RoboRev finding low or
conclusively refuted. Verify GitHub's merge state and SHA before cleanup.

## Review amendment — document history compatibility

The current-head review found another document capability helper still using
the old Google rejection: `unrepresentableHistoryKinds` in
`agent/session_set_model.go`. Keep this helper inside the bounded document
capability scope rather than changing model-switch policy generally.

When scanning a user-message document, reuse
`llm.DeliversUserMessageDocuments(protocol)` to recognize representable input.
A session containing a user PDF must switch to a Google target successfully
without changing its history or attachment bytes. Keep the existing protocol
restrictions for audio and for non-user documents: Google assistant documents
remain unrepresentable, and Chat Completions still cannot carry user documents.
Do not change catalog membership, fallback selection or persistence policy.

Add red-first `SetModel` coverage for Google user-document success and history
preservation. Keep the rejection coverage strict by testing Google assistant
documents, Chat Completions user documents, and audio. The existing
byte-identical rejection test must use an assistant document for Google now.
Run these focused model-switch checks with the other commands above:

```sh
go test ./agent -run 'TestSetModel_.*(Document|Audio|Rejection)|TestUnrepresentable' -count=1
```

Freeze and independently PAR-review this amendment before implementation, then
review its implementation diff and renew current-head branch/PR review and CI.

## Sources

- GitHub issue [#4463](https://github.com/prime-radiant-inc/evener/issues/4463)
  supplies the accepted Gemini `inlineData` PDF behavior.
- `llm/providers/google/request.go`: user/model part mapping and function
  responses.
- `llm/providers/anthropic/request.go`, `anthropicDocumentBlock`: document
  byte precedence, default media type and empty-part convention.
- `llm/token_count.go`: delivery predicates and media billing.
- `agent/provider/profile.go`: own-row document modality gate.
- `agent/session_tools.go` and `agent/internal/cheapmodel/caller.go`: vision
  route, refusal fallback and retry ownership.
- `agent/session_set_model.go`: document-history compatibility preflight.
- Product subsystem map S15/S16 and `docs/developing-evener/testing.md`:
  provider ownership and deterministic verification boundary.
