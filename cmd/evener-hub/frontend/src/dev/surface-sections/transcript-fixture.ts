import type { ItemModel } from "../../protocol/model";
import type { EvenerDelegateInfo } from "../../protocol/types.gen";
import { makeTranscriptPreviewModel } from "../../transcriptDisplay/previewFixture";

/** Fixed wire-shaped collaborator examples; never seeded into a live store. */
export function makeEditorialTranscriptModel() {
  const model = makeTranscriptPreviewModel();
  const path = `/workspace/preview/src/${"deeply-nested-evidence/".repeat(8)}source.ts`;
  model.turns = [
    ...model.turns,
    {
      id: "editorial_evidence",
      status: "completed",
      items: [
        {
          id: "editorial_source",
          turnId: "editorial_evidence",
          type: "commandExecution",
          toolName: "read_file",
          text: "",
          status: "completed",
          description: "Reading the exact source before changing behavior",
          argumentsJSON: JSON.stringify({ file_path: path, offset: 1, limit: 2 }),
          output: `   1\tconst source = "${"retained-evidence/".repeat(18)}";\n   2\texport { source };\n`,
        },
      ],
    },
  ];
  const cases = [
    {
      id: "running",
      status: "running",
      outcome: "completed",
      terminal: false,
      intent: "Checking the source against the latest report",
    },
    {
      id: "attention",
      status: "running",
      terminal: false,
      needsAttention: true,
      intent: "Reviewing the independent verification findings",
    },
    {
      id: "reported",
      status: "idle",
      outcome: "completed",
      terminal: true,
      intent: "Reporting the accessibility audit",
    },
    {
      id: "failed",
      status: "idle",
      outcome: "failed",
      terminal: true,
      reason: "The verification command exited with a failure; retained output is in the child transcript.",
      intent: "Verifying the changed boundary",
    },
    { id: "stopped", status: "stopped", terminal: true, intent: "Inspecting a cancelled investigation" },
    {
      id: "exhausted",
      status: "idle",
      outcome: "exhausted",
      terminal: true,
      reason: "The run reached its configured budget before completing the audit.",
      exhaustionBudget: "turns",
      exhaustionLimit: 12,
      resumable: true,
      intent: "Auditing a long-running investigation",
    },
    {
      id: "unknown",
      status: "unknown",
      terminal: false,
      intent: "Inspecting a collaborator whose lifecycle is unavailable",
    },
  ];
  model.delegates = cases.map(
    ({ id, intent: _intent, ...state }): EvenerDelegateInfo => ({
      ...state,
      delegateId: `dlg_editorial_${id}`,
      ownerSessionId: "editorial_parent",
      rootSessionId: "editorial_parent",
      childSessionId: `editorial_${id}`,
      transcriptRef: `local:editorial_${id}`,
      type: "delegate",
      lifecycle: state.status,
      phase: state.status,
      resumable: state.resumable ?? false,
      needsAttention: state.needsAttention ?? false,
      projectionRevision: 1,
    }),
  );
  const items: ItemModel[] = cases.map(({ id, intent }) => ({
    id: `editorial_${id}`,
    turnId: "editorial_collaborators",
    type: "commandExecution",
    toolName: "delegate",
    callId: `call_editorial_${id}`,
    description: intent,
    text: "",
    status: "completed",
    // The receipt is deliberately stale: the owner projection must win.
    output: JSON.stringify({
      delegate_id: `dlg_editorial_${id}`,
      status: "completed",
      transcript_ref: `local:editorial_${id}`,
    }),
  }));
  items.push({
    id: "editorial_missing",
    turnId: "editorial_collaborators",
    type: "commandExecution",
    toolName: "delegate",
    text: "",
    status: "completed",
    description: "Reading a launch receipt with missing lifecycle data",
    output: JSON.stringify({ delegate_id: "dlg_editorial_missing" }),
  });
  model.turns = [...model.turns, { id: "editorial_collaborators", status: "completed", items }];
  return model;
}
