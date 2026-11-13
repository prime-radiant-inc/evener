# Runbook: assistant-fabrication

**Question:** did any assistant turn in this session emit a fabricated harness
or user input envelope (`<delegate-notification>`, `<job-notification>`,
`<system-notification>`, `<SYSTEM-REMINDER>`, `<environment_context>`, or
`[answers]`) or unparsed raw tool-call markup (`<｜DSML｜>`, `<invoke>`,
`<parameter>`, `<tool_call>`) outside Markdown code spans?

At deep context, end-of-turn rollover (such as missing `<｜User｜>` stop tokens
on an upstream gateway) or an omitted tool-call opener can emit synthetic
runtime/user frames or raw XML/DSML tool markup into `ContentText`, potentially
poisoning downstream turns (memory writes, delegates, or repository actions) in
that session. `TranscriptHealth` detects these recorded turns read-only without
double-counting compaction replay copies.

## HEALTHY

- Zero assistant turns carry fabricated harness/user envelopes or unparsed
  tool-call markup outside fenced code blocks, inline code spans, 4-column
  indented code, blockquotes, and list items. `doctor_evener` `transcript`
  with `health: true` reports `assistant_fabrications` equal to `0`.

## INSPECT

Take the target session id from the runbook invocation rather than hardcoding one.

```
doctor_evener transcript, selector: <selector>, health: true
# (human reproduction: evener doctor transcript <selector> --health --json)
```

## CLASSIFY

```yaml
audit:
  - title: "Assistant fabricated harness or user input envelope"
    severity: high
    category: assistant_fabrication
    suggested_fix: diagnosis
    metric: assistant_fabrications.harness_envelope
    op: ">="
    value: 1
  - title: "Assistant leaked unparsed tool-call markup"
    severity: medium
    category: assistant_fabrication
    suggested_fix: diagnosis
    metric: assistant_fabrications.tool_markup
    op: ">="
    value: 1
```

- Inspect the flagged session's `ASSISTANT` turns with `doctor_evener`
  `transcript` (`full_text: true` on the narrowed `range`) and cross-check
  `doctor_evener` `mutations` to verify whether any subsequent memory write,
  delegate spawn, or repository action relied on fabricated `[answers]` or
  `<delegate-notification>` blocks rather than a real `USER_INPUT` or runtime
  `STEERING` turn.

`category: assistant_fabrication` identifies assistant-generated input-frame
spoofing and unparsed tool-call markup leaks in recorded transcripts.

A healthy run emits zero findings.
