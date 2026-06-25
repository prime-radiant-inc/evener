# Job Notification Renderer Design

Date: 2026-06-25

## Context

Serf job notifications currently arrive in the web UI as steering text. A typical terminal delegate notification looks like:

```text
<job-notification job_id="job_..." event="completed" job_type="delegate" status="completed" reason="" output_bytes="402" transcript_ref="local:...">
Job job_... completed. Output is available through read_transcript(transcript_ref="local:...") if needed.
excerpt:
{"message":"Status: DONE\nCommit hash(es): ...", "data": {...}, "artifacts": []}
</job-notification>
```

The current renderer treats this as unknown steering and renders a generic collapsible divider labelled `steering injected`, with raw XML and JSON in the body. That preserves the data, but it hides the useful outcome behind implementation-shaped text. The same problem can appear for related notification-shaped steering such as watch deliveries, no-job watch events, and observer callbacks.

## Goal

Render notification-shaped steering messages as coherent, readable notification cards while preserving exact raw evidence for inspection.

The renderer should prioritize the information a user or assistant needs to decide what happened and what to do next:

- what completed, failed, triggered, or delivered;
- which job/watch/delivery/transcript is involved;
- whether the outcome is successful or needs attention;
- the useful result summary, especially delegate `communicate` envelopes;
- where to inspect full output if needed.

## Non-goals

This change does not alter daemon notification formats, transcript storage, appwire delivery, job/watch semantics, or model-facing steering content.

This change does not require new transcript-fetching behavior, transcript action buttons, or transcript links. Render `transcript_ref` as readable text metadata; adding navigation actions is outside this design and requires a separate decision.

This change does not redesign unrelated steering messages. Task-list steering, current-task suppression, loop/read-only nudges, and unknown steering keep their existing behavior unless they match one of the notification shapes below.

## Notification shapes

The renderer should recognize these steering shapes after stripping an optional `<SYSTEM-REMINDER>` wrapper, using the same preprocessing path as existing steering classification.

### Job notification blocks

Recognize complete blocks of the form:

```text
<job-notification attr="value" ...>
body
</job-notification>
```

Relevant attributes include:

- `job_id`
- `event`
- `job_type`
- `status`
- `reason`
- `output_bytes`
- `exit_code`
- `transcript_ref`
- `delivery_id`
- `trigger`

Known variants from the daemon include:

1. **Terminal job notification**: `event`/`status` is a terminal job state such as `completed`, `failed`, `cancelled`, or `stopped`. The body contains prose like `Job <id> completed...` and may include an `excerpt:` section.
2. **No-job watch event**: `status="watch"`/`event="watch"` with no `job_id`, and body text like `Watch event triggered: <reason>.`.
3. **Watch-send delivery**: `event="watch_send"`, usually with `job_id`, `delivery_id`, and `trigger`; body is the delivered watch frame text.

### Observer callbacks

Recognize callback steering produced by observer sidecars:

```text
Observer callback:
message: ...
output: ...
```

The `output:` section may contain the same canonical `communicate` output envelope JSON used by delegate terminal excerpts.

## Rendering model

Recognized notification shapes render as a shared `.notification-card` family rather than the generic `.steering` details block.

### Card structure

Each card should contain:

1. **Header**
   - Icon or glyph indicating notification class.
   - Concise title derived from the notification type and status.
   - Examples: `Job completed`, `Job failed`, `Watch delivered`, `Watch triggered`, `Observer callback`.

2. **Metadata row**
   - Compact chips for parsed identifiers and state.
   - Show fields only when present and non-empty, except an explicit empty `reason` does not need a chip.
   - Candidate chips: `job_id`, `job_type`, `status`, `reason`, `exit_code`, `output_bytes`, `transcript_ref`, `delivery_id`, `trigger`.
   - Use existing formatting helpers where appropriate, for example byte formatting for `output_bytes`.

3. **Summary body**
   - Human-readable outcome text.
   - For terminal job notifications, show the daemon prose without forcing the user to read XML attributes.
   - For delegate excerpts that parse as a `communicate` JSON envelope, surface high-value fields:
     - top-level `message`;
     - `data.status`;
     - `data.commit_hashes`;
     - `data.test_summary`;
     - `data.concerns`;
     - `artifacts`.
   - For unstructured shell/job excerpts, show a short plain-text excerpt preview.
   - For watch-send and observer callback bodies, show the delivered message/callback summary first.

4. **Details / raw evidence**
   - Include collapsible raw notification text and/or raw excerpt.
   - Raw evidence should be available even when parsing succeeds.
   - This preserves exact auditability and avoids data loss when a summary is incomplete.

### Status tone

Use tone classes on the card so CSS can distinguish outcomes without changing semantics:

- success: `completed`, `DONE`, no concerns;
- warning/attention: watch deliveries, observer callbacks, cancellations, non-empty concerns;
- error: `failed`, non-zero exit code, parseable failure status;
- neutral: running/unknown or unclassified notification status.

The visual treatment should remain consistent with existing Serf hub design: compact, readable, and not louder than assistant/user messages unless the notification represents failure or attention needed.

## Parsing and data flow

Parsing is client-side and best-effort.

1. `STEERING_INJECTED` continues to call `appendSteeringMessage(text)`.
2. `classifySteering(text)` strips optional `<SYSTEM-REMINDER>` wrappers as it does now.
3. Before returning `unknown`, it detects notification shapes:
   - complete `<job-notification>` blocks;
   - `Observer callback:` blocks.
4. For job notification blocks:
   - parse quoted attributes from the opening tag;
   - extract body text between opening and closing tags;
   - split body into prose and optional `excerpt:` section;
   - detect watch-send and no-job watch variants from `event`, `status`, `job_id`, and `delivery_id`.
5. For observer callbacks:
   - parse `message:` and optional `output:` sections;
   - keep the full raw callback text.
6. For excerpts/output that appear to be JSON:
   - attempt `JSON.parse`;
   - if it looks like a `communicate` envelope, derive a structured summary;
   - if parsing fails, treat it as plain text.
7. Rendering creates DOM nodes with `textContent`; notification content is never injected as HTML.

## Error handling and fallback

Notification parsing must never make information disappear.

- If JSON parsing fails, render the card with plain-text excerpt and raw details.
- If an attribute is missing or malformed, omit that chip and keep the raw block.
- If the outer `<job-notification>` block is incomplete or cannot be confidently parsed, fall back to the existing generic steering renderer.
- If a recognized notification has an unknown event/status, render a neutral `Job notification` card with parsed metadata and raw details.

## Accessibility

The card must be readable without hover-only interactions. Collapsible details should use native `<details>/<summary>` or existing accessible disclosure patterns. Metadata chips are plain text, not icon-only. Any icon/glyph is decorative unless it conveys state not otherwise present in text.

## Test plan

Add deterministic JavaScript renderer tests using the existing JSDOM renderer harness.

Representative cases:

1. Terminal delegate completion:
   - feed a `STEERING_INJECTED` event containing `<job-notification ... job_type="delegate" status="completed" ...>` with a JSON `communicate` envelope excerpt;
   - assert the renderer creates a notification card, not a generic `steering injected` block;
   - assert status, commit hashes, test summary, and concerns are visible.

2. Terminal shell failure:
   - include `job_type="shell"`, `status="failed"`, `reason`, `exit_code`, `output_bytes`, and `transcript_ref`;
   - assert failure metadata and transcript/output information are visible.

3. No-job watch event:
   - include `event="watch"`, `status="watch"`, no `job_id`, and `Watch event triggered: ...` body;
   - assert it renders as a watch notification without a read-transcript prompt.

4. Watch-send delivery:
   - include `event="watch_send"`, `job_id`, `delivery_id`, `trigger`, and delivered frame body;
   - assert it renders as a watch-delivery card with delivery/trigger metadata and body preview.

5. Observer callback:
   - feed `Observer callback:\nmessage: ...\noutput: ...`;
   - assert it uses the same notification-card family and surfaces message/output summary.

6. Malformed or partial notification:
   - assert malformed blocks fall back safely or preserve raw evidence;
   - assert no parsed content is inserted as HTML.

Run the focused renderer test(s), then the relevant hub package tests during implementation verification.

## Acceptance criteria

- Job notification steering no longer appears as generic `steering injected` when the block is well-formed.
- Terminal job cards show lifecycle metadata and useful result summaries.
- Delegate `communicate` JSON excerpts are summarized into readable status/test/concern/artifact information when present.
- Watch events, watch-send deliveries, and observer callbacks use a coherent notification-card visual language.
- Raw notification content remains inspectable.
- Malformed or unknown notification content falls back without data loss.
- Tests cover representative terminal job, watch, observer callback, parsed JSON, and fallback cases.
