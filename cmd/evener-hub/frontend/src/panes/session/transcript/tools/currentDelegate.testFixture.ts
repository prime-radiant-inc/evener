import { threadsStore } from "../../../../stores/threads";
import { makeTranscriptPreviewModel } from "../../../../transcriptDisplay/previewFixture";

/** Current owner evidence, deliberately separate from a tool's frozen receipt. */
export function seedCurrentDelegate(ref: string, delegateId: string, status: string, reason?: string) {
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
    },
  ];
  threadsStore.setState({ threads: new Map([[ref, thread]]) });
  return thread;
}
