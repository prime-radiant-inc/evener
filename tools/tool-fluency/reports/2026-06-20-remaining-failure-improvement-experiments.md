# Remaining Failure Improvement Experiments - 2026-06-20

This report records 25 improvement-screening experiments for the remaining
tool-fluency failures after the first prompt/schema fixes. The variants are
prompt-append files under
`tools/tool-fluency/variants/remaining-failures-2026-06-20/`; the scenarios,
models, harnesses, and acceptance criteria stay fixed.

## Session Interrogation Summary

Before designing new variants, I resumed representative failed sessions with
`serf --resume-with` and asked the agents why they struggled.

Findings:

- `job_watch.observer_callback`, GPT:
  - Agents conflated watch creation with callback delivery payloads and placed
    delivery fields at the top level.
  - Callback messages were not always treated as terminal completion evidence,
    so the caller inspected job state after the callback.
  - Some runs triggered the watched read before the watch was installed.
- `communicate`, GPT:
  - One repair path confused top-level `purpose` with fields inside strict
    `output`.
- final marker handling, Kimi:
  - Several runs completed the tool work but put required result markers in
    purpose/metadata or omitted them from the visible final message.
- `apply_patch`, GPT:
  - The first failed patch was built from an invented old line instead of the
    file's actual content.

One Kimi CLI callback session did not answer the interrogation and instead
inspected state. I stopped it after it continued over-inspecting; that session
is evidence for the same tool fluency problem, not a usable self-diagnosis.

## Protocol

- Use prior failed reruns as baselines; each experiment below changes one
  prompt-append variant.
- Keep scenarios unchanged.
- Keep model, harness, timeout, and acceptance criteria unchanged for each
  compared failure path.
- Use the live harness for `job_watch.observer_callback`.
- Use the CLI harness for ordinary one-shot tools.
- Avoid negative "do not" prompting in variants; phrase the desired behavior
  positively.
- Inspect results with `serf-fluency` output, `result.json`, and `serf-doctor`
  where needed. No custom Python or jq parsing.

## Experiment Matrix

Kimi hit provider quota during the campaign, so the Kimi-specific marker runs
were blocked by 429s before any tool call. To keep 25 useful non-infra
experiments, I ran OpenAI analogs for the marker/fetch variants and recorded the
Kimi block separately below.

| ID | Variant | Model | Probe | Harness | Result | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| E01 | `01-job-watch-create-shape.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | failed | Repaired `job_watch`, then called `job_read_output`. |
| E02 | `02-job-watch-field-groups.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | failed | Correct shape, but used `job_list`. |
| E03 | `03-job-watch-callback-evidence.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | failed | No polling; failed on empty placeholder `communicate`. |
| E04 | `04-job-watch-completion-sequence.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | passed | Clean sequence; no forbidden inspection. |
| E05 | `05-job-watch-install-before-trigger.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | failed | Repaired `job_watch`; duplicate read. |
| E06 | `06-job-watch-target-caller.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | failed | Called `job_read_output`. |
| E07 | `07-job-watch-validation-repair.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | passed | Clean watch and callback path. |
| E08 | `08-job-watch-observer-boundary.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | failed | Repaired `job_watch`. |
| E09 | `09-job-watch-callback-final-answer.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | failed | Called `job_list`. |
| E10 | `10-job-watch-combined.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | failed | Repaired `job_watch`; broad combined guidance was weaker. |
| E11 | `11-communicate-envelope.md` | GPT 5.4 mini | `list_dir.inventory` | CLI | passed | Clean `communicate` envelope. |
| E12 | `12-communicate-message-carries-literals.md` | GPT 5.4 mini | `read_file.happy_path` | CLI | passed | OpenAI analog after Kimi quota block. |
| E13 | `13-communicate-final-check.md` | GPT 5.4 mini | `read_file.happy_path` | CLI | passed | OpenAI analog after Kimi quota block. |
| E14 | `14-communicate-purpose-boundary.md` | GPT 5.4 mini | `list_dir.inventory` | CLI | passed | Clean `purpose` placement. |
| E15 | `15-communicate-data-summary.md` | GPT 5.4 mini | `list_dir.inventory` | CLI | passed | Clean data-to-message carryover. |
| E16 | `16-communicate-combined.md` | GPT 5.4 mini | `web_fetch.example` | CLI | failed | Skipped `web_fetch`; answered from known page facts. |
| E17 | `17-apply-patch-read-first.md` | GPT 5.4 mini | `apply_patch.happy_path` | CLI | passed | Read file, then patched exact line. |
| E18 | `18-apply-patch-exact-hunk.md` | GPT 5.4 mini | `apply_patch.happy_path` | CLI | passed | Read file, then patched exact line. |
| E19 | `19-apply-patch-single-file.md` | GPT 5.4 mini | `apply_patch.happy_path` | CLI | passed | Read file, then patched exact line. |
| E20 | `20-apply-patch-repair.md` | GPT 5.4 mini | `apply_patch.happy_path` | CLI | passed | Read file, then patched exact line. |
| E21 | `21-general-required-literals.md` | GPT 5.4 mini | `read_file.happy_path` | CLI | passed | OpenAI analog after Kimi quota block. |
| E22 | `22-general-completion-evidence.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | passed | Clean watch delivery; doctor confirmed one delivered frame. |
| E23 | `23-general-tool-schema-first.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | passed | Clean watch delivery; doctor confirmed one delivered frame. |
| E24 | `24-general-observe-then-answer.md` | GPT 5.4 mini | `web_fetch.example` | CLI | failed | Skipped `web_fetch`; answered from known page facts. |
| E25 | `25-general-combined.md` | GPT 5.4 mini | `job_watch.observer_callback` | live | failed | Called `job_read_output`. |

## Commands

The runner and CLI binary were built once for the campaign:

```sh
go build -o /tmp/serf-fluency-exp-serf ./cmd/serf
go build -o /tmp/serf-fluency-runner ./tools/tool-fluency/cmd/serf-fluency
```

Each result directory is `/tmp/serf-fluency-improve-eNN`.

## Blocked Kimi Runs

Kimi provider quota blocked the intended Kimi-specific marker runs:

- `/tmp/serf-fluency-improve-e12`: `read_file.happy_path`, 429 before tool call.
- `/tmp/serf-fluency-improve-e13`: `shell.command`, 429 before tool call.
- `/tmp/serf-fluency-improve-e16`: `web_fetch.example`, 429 before tool call.

I did not launch further Kimi runs after the repeated 429s.

## Results

Summary of the 25 useful runs:

- Passed: 14.
- Failed: 11.
- Blocked infra, not counted in the 25 useful runs: 3 Kimi quota failures.

Strong signals:

- `job_watch`: The most useful atomic guidance was a short ordered sequence and
  schema-first repair. Broad combined guidance was weaker than targeted
  sequence/schema guidance.
- Polling/waiting: Passing watch variants used no `job_list` or
  `job_read_output`. Doctor confirmed the clean watch passes had one delivered
  watch frame and no self-loop.
- `apply_patch`: Every read-first or exact-hunk variant passed. The original
  self-diagnosis was correct: the failure path was invented old text.
- `communicate`: GPT list/read-file envelope variants passed, but the watch
  path still exposed occasional `purpose`-inside-`output` errors.
- `web_fetch`: Marker/final-answer guidance did not fix selection. The model can
  answer `example.com` from prior knowledge unless the explicit-tool-call
  contract is made more salient.

## Fixes Applied

Based on the experiments, I made small product changes:

- Promoted the successful watch sequence into `background-jobs.md`.
- Clarified normal watch completion as callback to final result.
- Clarified `target:"caller"` watch delivery as `send.to` without
  `send.include_excerpt`.
- Clarified explicit tool requests as part of the task.
- Moved `apply_patch` exact-line guidance to the OpenAI provider prompt append,
  leaving the generic prompt provider-neutral.
- Changed terminal job notification wording from an imperative
  `Use job_read_output to inspect output.` to an affordance:
  `Output is available through job_read_output if needed.`
- Updated the `job_watch` tool description for callback completion and
  `include_excerpt` boundaries.
- Added top-level `purpose` placement to the `communicate` tool schema
  description without loosening the strict schema.

## Post-Fix Reruns

Post-fix GPT 5.4 mini reruns without prompt-append variants:

| Probe | Result | Artifact |
| --- | --- | --- |
| `apply_patch.happy_path` | 3/3 passed | `/tmp/serf-fluency-postfix-apply-patch` |
| `list_dir.inventory` | 3/3 passed | `/tmp/serf-fluency-postfix-list-dir` |
| `web_fetch.example` before explicit-tool wording | 2/3 passed | `/tmp/serf-fluency-postfix-web-fetch` |
| `web_fetch.example` after explicit-tool wording | 3/3 passed | `/tmp/serf-fluency-postfix2-web-fetch` |
| `job_watch.observer_callback` after prompt-only fixes | 2/3 passed | `/tmp/serf-fluency-postfix2-jobwatch` |
| `job_watch.observer_callback` after notification wording | 0/3 passed | `/tmp/serf-fluency-postfix4-jobwatch` |
| `job_watch.observer_callback` after `job_watch` schema description | 2/3 passed | `/tmp/serf-fluency-postfix5-jobwatch` |
| `job_watch.observer_callback` after `communicate` schema description | 1/3 passed | `/tmp/serf-fluency-postfix6-jobwatch` |

The watch path remains unstable. The remaining failures are not one thing:

- premature progress `communicate`;
- `job_watch` repair after unrelated extra fields;
- terminal notification treated as new work;
- `communicate.output.purpose` schema error.

## Conclusions

Prompting helped but did not solve `job_watch.observer_callback`. The right next
fix is not more "please don't poll" language. The better engineering direction
is to improve the runtime/tool affordance for callback-driven observer work:

- consider suppressing or changing duplicate terminal notifications for
  watch-driven delegate completions after the callback result has already been
  delivered;
- consider a narrower helper/tool mode for caller-session observer watches, so
  the model does not need to assemble a conditional `job_watch` shape from
  several optional fields;
- keep strict `communicate` output schema, but continue improving the schema
  description and validation repair path rather than auto-upgrading misplaced
  fields.
