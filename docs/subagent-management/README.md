# Delegate and subagent design set

Numbered design documents for delegated work. They were written as a sequence:
`06` through `11`, in that order. Later numbers supersede earlier ones where they
conflict.

Several have shipped in part or in full. The durable, current contracts are:

- [`docs/subagent-runtime-contracts.md`](../subagent-runtime-contracts.md) — what
  survives a delegate run and how reports reach the parent.
- [`docs/job-control.md`](../job-control.md) — launch, observation, waiting, and
  stopping, including delegation allowance.

Read those first. These documents remain for the reasoning, the rejected
alternatives, and the acceptance criteria behind the shipped behavior.

| Document | Status |
| --- | --- |
| [`06-plugin-agent-validation.md`](06-plugin-agent-validation.md) | Proposed. Some validation remains unbuilt; the runtime contract lists which. |
| [`07-lifecycle-hooks-claude-compat.md`](07-lifecycle-hooks-claude-compat.md) | The event and exit-code contract the hooks guide cites. |
| [`08-standalone-llm-calls.md`](08-standalone-llm-calls.md) | Proposed; the shared helper API it describes is not built. |
| [`09-session-tree-history-assessment.md`](09-session-tree-history-assessment.md) | An assessment, explicitly optional and not a committed feature. |
| [`10-runtime-contracts.md`](10-runtime-contracts.md) | Proposed evergreen spec. Carries its own historical note where the design moved on. |
| [`11-delegate-resource-model.md`](11-delegate-resource-model.md) | The stable delegate model the runtime contract treats as current. |

Do not cite a lower number as authority for current behavior. Links inside these
documents point at paths and line numbers from when they were written.
