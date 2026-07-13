# OpenAI Tool-Result Image Encoding Design

## Problem

Serf serializes an OpenAI Responses tool result with image data as two
top-level input items: a `function_call_output` containing text followed by an
`input_image`. GPT-5.6 Responses-Lite rejects the top-level `input_image`
because image objects are content items, not top-level conversation items.

The observed failure followed a successful `read_file` call for a PNG. The
next full-history request failed with HTTP 400 at the image item's input index.

## Decision

Encode a tool result's text and optional image together in the
`function_call_output.output` field. When an image is present, `output` is a
content array containing:

1. An `input_text` item carrying the same text Serf currently sends as the
   string output.
2. An `input_image` item carrying the image data URI and the model-appropriate
   detail field.

Apply this encoding to every OpenAI Responses model. It is the documented
Responses representation and matches the first-party Codex client. Keep string
output for tool results without images so this fix does not change their wire
shape.

## Data Flow

`llm.ToolResultData` remains unchanged. The OpenAI Responses serializer will:

1. Produce the existing output text, including the existing JSON wrapper for
   error results.
2. If no image bytes are present, emit the existing string-valued
   `function_call_output`.
3. If image bytes are present, emit one `function_call_output` whose `output`
   is the text-and-image content array.
4. Never append a separate top-level image for a tool result.

User-message image serialization remains unchanged because those images are
already nested in message content.

## Model Details

GPT-5.6 Responses-Lite continues to omit `detail` from the nested image. Other
Responses models continue to receive the existing default image detail. A
missing tool-result media type continues to default to `image/png`.

## Testing

Deterministic unit tests will inspect the structured request body and verify:

- a tool result with an image produces one `function_call_output` with text
  and image content;
- no top-level `input_image` is emitted for the tool result;
- missing media type defaults to PNG;
- error-result text retains its JSON-wrapped error semantics;
- GPT-5.6 omits image detail;
- GPT-5.5 retains its existing image detail.

The tests will not match rendered JSON or make live provider requests.

## Alternatives Rejected

- Encoding tool-result images differently only for GPT-5.6 would preserve an
  undocumented legacy shape and add unnecessary model-specific branching.
- Dropping tool-result images for GPT-5.6 would avoid the schema error by
  silently removing vision behavior.

## Scope

This change is limited to OpenAI Responses request serialization and its unit
tests. It does not alter Chat Completions, other providers, transcript storage,
tool execution, or the `llm.ToolResultData` domain model.
