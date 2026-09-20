import type { EvenerDelegateInfo } from "@evener/appwire-client";
import { threadsStore } from "../../../../stores/threads";
import { makeTranscriptPreviewModel } from "../../../../transcriptDisplay/previewFixture";

/** Current owner evidence, deliberately separate from a tool's frozen receipt.
 * `overrides` spreads last and cannot shadow the status/reason parameters. */
export function seedCurrentDelegate(
  ref: string,
  delegateId: string,
  status: string,
  reason?: string,
  overrides?: Partial<Omit<EvenerDelegateInfo, "status" | "reason">>,
) {
  const thread = makeTranscriptPreviewModel();
  thread.ref = ref;
  thread.delegates = [
    {
      delegateId,
      ownerSessionId: "owner",
      rootSessionId: "owner",
      childSessionId: "child",
      transcriptRef: "local:child",
      type: "delegate",
      lifecycle: status === "running" ? "running" : "idle",
      phase: status === "running" ? "working" : "idle",
      status,
      reason,
      outcome: status === "running" ? undefined : status,
      terminal: status !== "running",
      resumable: true,
      needsAttention: false,
      projectionRevision: 1,
      ...overrides,
    },
  ];
  threadsStore.setState({ threads: new Map([[ref, thread]]) });
  return thread;
}
