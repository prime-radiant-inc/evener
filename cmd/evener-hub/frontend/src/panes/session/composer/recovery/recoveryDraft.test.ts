import { expect, test } from "vitest";
import type { InputItem } from "../../../../protocol/types.gen";
import type { MutationAttachment, MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import type { PendingAttachment } from "../attachments/useAttachments";
import { mergeRecoveryComposerDraft, recoveryComposerDraft } from "./recoveryDraft";

function recoveryRecord(input: InputItem[], extra: Partial<MutationRecoveryRecord> = {}): MutationRecoveryRecord {
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
    ...extra,
  };
}

function durableAttachment(marker: number, name: string): MutationAttachment {
  return {
    presentationId: `presentation-${marker}`,
    marker,
    name,
    mediaType: "image/png",
    blob: new Blob([name], { type: "image/png" }),
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
      recoveryRecord(
        [
          { type: "text", text: "look [image 3]" },
          { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
        ],
        { composerText: "look [image 3]", attachments: [durableAttachment(3, "proof.png")] },
      ),
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
    skillNames: [],
  });
});

// The submit boundary translates every marker to prose before the payload is
// stored, so the record's own wire text has no anchors left to restore from.
test("restores the composer's own marker anchors, not the prose the wire was given", () => {
  const draft = recoveryComposerDraft(
    recoveryRecord(
      [
        { type: "text", text: "(attached image 1: proof.png)Describe the attached image" },
        { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
      ],
      {
        composerText: "[image 1]Describe the attached image",
        attachments: [durableAttachment(1, "proof.png")],
      },
    ),
  );

  expect(draft.text).toBe("[image 1]Describe the attached image");
  expect(draft.attachments.map((attachment) => attachment.marker)).toEqual([1]);
});

test("recovers each attachment under the marker its durable record carries, not its order of appearance", () => {
  const draft = recoveryComposerDraft(
    recoveryRecord(
      [
        { type: "text", text: "look [image 3] then [image 1]" },
        { type: "image", mediaType: "image/png", data: "AAAA", name: "first.png" },
        { type: "image", mediaType: "image/png", data: "BBBB", name: "third.png" },
      ],
      {
        composerText: "look [image 3] then [image 1]",
        attachments: [durableAttachment(1, "first.png"), durableAttachment(3, "third.png")],
      },
    ),
  );

  expect(draft.attachments.map((attachment) => [attachment.marker, attachment.name])).toEqual([
    [1, "first.png"],
    [3, "third.png"],
  ]);
});

test("falls back to the payload's own text when the record carries no composer text", () => {
  const draft = recoveryComposerDraft(
    recoveryRecord([
      { type: "text", text: "plain" },
      { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
    ]),
  );

  expect(draft.text).toBe("plain");
  expect(draft.attachments.map((attachment) => attachment.marker)).toEqual([1]);
});

test("recovers canonical selections without converting them to prose", () => {
  const draft = recoveryComposerDraft(
    recoveryRecord(
      [
        { type: "text", text: "WIRE_818" },
        { type: "skill", name: "pkg:probe" },
        { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
      ],
      {
        composerText: "REQUEST_818 [image 3]",
        attachments: [durableAttachment(3, "proof.png")],
      },
    ),
  );
  expect(draft.text).toBe("REQUEST_818 [image 3]");
  expect(draft.skillNames).toEqual(["pkg:probe"]);
  expect(draft.attachments).toEqual([
    { marker: 3, name: "proof.png", mediaType: "image/png", data: "AQID", pending: false },
  ]);
});

test("a skill-only recovery record restores its selections with no text", () => {
  expect(recoveryComposerDraft(recoveryRecord([{ type: "skill", name: "pkg:probe" }]))).toEqual({
    text: "",
    attachments: [],
    skillNames: ["pkg:probe"],
  });
});

test("recovered selections are deduplicated in catalog order", () => {
  const draft = recoveryComposerDraft(
    recoveryRecord([
      { type: "skill", name: "pkg:probe" },
      { type: "skill", name: "pkg:probe" },
      { type: "skill", name: "pkg:other" },
    ]),
  );
  expect(draft.skillNames).toEqual(["pkg:probe", "pkg:other"]);
});

test("merging queue-like recovery keeps current text first and renumbers recovered markers", () => {
  const merged = mergeRecoveryComposerDraft("current [image 1]", [settledAttachment(1, "current.png", "AAAA")], {
    text: "failed [image 1]",
    attachments: [settledAttachment(1, "failed.png", "BBBB")],
    skillNames: [],
  });

  expect(merged.text).toBe("current [image 1]\n\nfailed [image 2]");
  expect(merged.attachments.map((attachment) => attachment.marker)).toEqual([1, 2]);
});

test("merging unions recovered selections with the composer's current ones, deduplicated", () => {
  const merged = mergeRecoveryComposerDraft(
    "",
    [],
    { text: "recovered", attachments: [], skillNames: ["pkg:probe", "pkg:duplicate"] },
    ["pkg:duplicate", "pkg:current"],
  );
  expect(merged.text).toBe("recovered");
  expect(merged.skillNames).toEqual(["pkg:duplicate", "pkg:current", "pkg:probe"]);
});

test("merging without current selections keeps the recovered selections", () => {
  const merged = mergeRecoveryComposerDraft("current", [], {
    text: "recovered",
    attachments: [],
    skillNames: ["pkg:probe"],
  });
  expect(merged.skillNames).toEqual(["pkg:probe"]);
});
