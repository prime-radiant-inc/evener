# OpenAI-Compatible Stream Response ID — Design Specification

**Status:** Approved for implementation
**Date:** 2026-07-11
**Decision owner:** Drew

## Summary

Preserve the Chat Completions response identifier carried by OpenAI-compatible
SSE chunks on the final `llm.Response`. This makes streaming behavior match the
existing non-streaming mapper and allows downstream local exporters to retain
provider handles when explicitly configured to do so.

## Problem

`chatCompletionChunk` already decodes the top-level `id` field, and real
OpenRouter streams include the same `gen-...` identifier on every chunk.
`decodeStream` currently retains model, finish reason, usage, content, and tool
calls, but never copies `chunk.ID` into the final response. The agent therefore
stores an empty assistant-turn response ID, and an explicitly enabled
`raw-local` ATIF export has no provider handle to emit.

The non-streaming Chat Completions path already maps the response ID. This is a
streaming parity bug, not a new provider feature.

## Design

Track the latest non-empty `chatCompletionChunk.ID` while decoding the stream
and assign it to `finalResp.ID` on `[DONE]`. Empty chunk IDs do not erase a
previously observed ID. The rest of the stream event and response shape remains
unchanged.

The existing captured OpenRouter SSE fixture is the regression oracle: its
chunks contain one stable generation ID. Extend the replay test to require that
exact ID on the final response. The test remains deterministic and performs no
network request beyond its local `httptest` server.

## Failure behavior

- Streams with no ID continue to produce an empty response ID; Serf does not
  invent one.
- A malformed chunk remains subject to the existing skip behavior.
- Provider-handle export stays redacted by default. This change only makes the
  ID available to consumers that already hold the in-memory response; raw ATIF
  export still requires the explicit `raw-local` mode.

## Security

The identifier is operational metadata, not response content. It is retained
in Serf's existing assistant-turn field and follows existing transcript and
ATIF redaction controls. No prompt, completion, tool argument, credential, or
raw HTTP body is newly persisted.

## Testing and rollout

1. Extend the captured OpenRouter replay test and observe the expected empty-ID
   failure.
2. Implement the one-field decoder propagation and run the focused package
   tests.
3. Run `go test ./...`, `go vet ./...`, and the repository's formatting/lint
   checks.
4. Commit and push the exact Serf SHA.
5. Update the eval appliance's immutable Serf pin, rebuild through managed
   preparation, and verify both source SHA and binary provenance before a paid
   smoke.

## Rollback

Revert the Serf commit and rebuild the prior immutable pin. No stored artifact
or config migration is required.
