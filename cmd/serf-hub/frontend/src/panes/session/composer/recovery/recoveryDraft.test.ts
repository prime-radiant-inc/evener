import { expect, test } from "vitest";
import type { InputItem } from "../../../../protocol/types.gen";
import type { MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import type { PendingAttachment } from "../attachments/useAttachments";
import { mergeRecoveryComposerDraft, recoveryComposerDraft } from "./recoveryDraft";

function recoveryRecord(input: InputItem[]): MutationRecoveryRecord {
  return {
    version: 1,
    clientMutationId: "mutation-1",
    intentSequence: 1,
    createdAt: 1,
    targetRef: "local:thread-1",
    threadId: "thread-1",
    method: "turn/start",
    payload: { ref: "local:thread-1", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
    state: "submitting",
    recoveryKind: "rejected",
  };
}

function settledAttachment(marker: number, name: string, data: string): PendingAttachment {
  return {
    marker,
    name,
    mediaType: "image/png",
    data,
    pending: false,
  };
}

test("projects durable image input into settled Composer attachment state", () => {
  expect(
    recoveryComposerDraft(
      recoveryRecord([
        { type: "text", text: "look [image 3]" },
        { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
      ]),
    ),
  ).toEqual({
    text: "look [image 3]",
    attachments: [
      {
        marker: 3,
        name: "proof.png",
        mediaType: "image/png",
        data: "AQID",
        pending: false,
      },
    ],
  });
});

test("merging queue-like recovery keeps current text first and renumbers recovered markers", () => {
  const merged = mergeRecoveryComposerDraft("current [image 1]", [settledAttachment(1, "current.png", "AAAA")], {
    text: "failed [image 1]",
    attachments: [settledAttachment(1, "failed.png", "BBBB")],
  });

  expect(merged.text).toBe("current [image 1]\n\nfailed [image 2]");
  expect(merged.attachments.map((attachment) => attachment.marker)).toEqual([1, 2]);
});
