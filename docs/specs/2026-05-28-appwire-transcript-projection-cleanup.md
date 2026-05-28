# AppWire Transcript Projection Cleanup

## Problem

Serf has two transcript-to-AppWire projection paths:

- `server/appwire_runtime.go` for the daemon AppWire server.
- `cmd/serf-hub/app_rpc.go` for hub RPC reads of past/local transcripts.

They duplicate the transcript prelude logic and closely mirror the JSONL replay
loop. This already caused drift: the synthetic `Tools (...)` transcript block
shows only tool names in both places, even though users need to inspect the full
tool definitions that were presented to the LLM.

## Goals

- Make the transcript prelude a single shared implementation.
- Show the full tool definitions from the first LLM request in the `Tools (...)`
  stanza: name, description, JSON schema parameters, and strictness.
- Preserve backward compatibility for old transcripts that only contain
  `tool_names`.
- Keep web UI, TUI, live notification replay, and past transcript reads on the
  same AppWire `Turn`/`ThreadItem` shapes.
- Reduce duplicate transcript replay code without a risky all-at-once rewrite.

## Non-Goals

- Do not change the LLM request format.
- Do not change how tool calls/results render during normal conversation replay.
- Do not introduce new AppWire methods for transcript prelude blocks.
- Do not rewrite hub source aggregation or live relay behavior.

## Design

### 1. Record Full Tool Definitions

Extend `llm.APILogRequest` with:

```go
Tools []llm.ToolDefinition `json:"tools,omitempty"`
```

Populate it from the actual `llm.Request.Tools` used for the provider call,
after profile/tool-name mapping and schema shaping. Keep `ToolNames` and
`ToolCount` for compatibility and cheap indexing.

Also remove the current logging duplication by using one helper for request
metadata construction in both the API logger and session transcript logging.

### 2. Share Prelude Projection

Create a small internal package, for example `internal/apptranscript`, with:

```go
func ScanPrelude(path string, maxLineBytes int) (agent.TranscriptHeader, *agent.TranscriptAPICall)
func PreludeTurn(header agent.TranscriptHeader, firstCall *agent.TranscriptAPICall) *appwire.Turn
func FormatTools(req llm.APILogRequest) string
```

Both `server/appwire_runtime.go` and `cmd/serf-hub/app_rpc.go` should call these
helpers. `FormatTools` should prefer `req.Tools` and fall back to
`req.ToolNames`.

Recommended full-tool markdown:

````markdown
```json
[
  {
    "name": "read_file",
    "description": "Read a file from disk.",
    "parameters": {
      "type": "object",
      "properties": {}
    }
  }
]
```
````

JSON is preferable here because it exactly preserves nested schemas and is easy
to copy/debug.

### 3. Share Replay Scanning

After the prelude extraction lands, move the common JSONL scan loop into the
same package. The shared loop should own:

- Header and first API call prelude emission.
- API call error projection into failed AppWire turns.
- Entry sequencing and synthetic `turn_N` IDs.

The package can accept a callback for entry conversion so the server path can
keep typed `agent.Turn` handling while hub keeps its loose replay structs during
the transition.

### 4. Consolidate Turn Projection

Once the scan loop is shared, consolidate the turn-to-item projection. The hub
path currently has useful extras, especially image SHA/size metadata for past
image serving. Keep that behavior by making image projection configurable rather
than forking the entire turn mapper.

Target shape:

```go
type ImageProjector func(llm.ImageData) appwire.InputItem
```

Then both paths can share user, steering, assistant, tool-call, tool-result, and
compaction mapping while differing only where they genuinely need different
metadata.

## Migration Plan

1. Add failing tests for a transcript with full `llm.ToolDefinition` entries.
   Assert both daemon transcript reads and hub RPC reads show the same full JSON
   tool payload.
2. Add `llm.APILogRequest.Tools` and populate it from actual requests.
3. Extract and use the shared prelude helpers.
4. Add a regression test for old transcripts with only `tool_names`.
5. Extract the shared scan loop.
6. Consolidate turn projection after the scan loop is stable.

## Test Plan

- Unit test `FormatTools` for full tools, strict tools, empty tools, and legacy
  `tool_names`.
- Server test: `appTurnsFromTranscriptFile` includes the full tools JSON.
- Hub RPC test: `ThreadRead` includes the same full tools JSON.
- Session test: a real `ProcessInput` transcript records full tool definitions
  in the first `api_call`.
- Existing web/TUI tests should continue to pass because the item remains a
  `systemMessage` with description `Tools (N)`.

## Risks

- Full schemas can make the transcript prelude large. This is acceptable because
  the user explicitly opens transcript details, and it is the truthful LLM
  request surface. Keep scanner limits unchanged unless tests expose an issue.
- Provider adapters may mutate tool schemas after `llm.Request` construction.
  If we need byte-for-byte provider payloads later, raw HTTP logging remains the
  authoritative source. This cleanup targets Serf's provider-normalized
  `llm.Request.Tools`.
- Hub replay handles legacy transcript shapes. Preserve loose decoding until the
  shared typed path proves it covers existing session files.
