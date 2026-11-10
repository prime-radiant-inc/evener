import { expect, test } from "vitest";
import { buildComposerInput, type InputAttachment } from "./composerInput";
import {
  type ComposerComposition,
  mergeComposerCompositions,
  nextComposerImageMarker,
  recoverMutationComposition,
} from "./composerRecovery";
import type { MutationAttachmentRef, MutationRecord } from "./state/mutation/records";
import { markerText, stripMarker } from "./textareaMarkers";
import type { InputItem } from "./types.gen";

function attachment(marker: number, name: string): MutationAttachmentRef {
  return {
    presentationId: `presentation-${name}`,
    marker,
    name,
    mediaType: "image/png",
  };
}

function mutationRecord(input: InputItem[], overrides: Partial<MutationRecord> = {}): MutationRecord {
  return {
    version: 1,
    clientMutationId: "mutation-1",
    intentSequence: 1,
    createdAt: 1,
    targetRef: "local:thread-1",
    threadId: "thread-1",
    method: "turn/queue",
    payload: { ref: "local:thread-1", input },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input },
    ...overrides,
  };
}

function imageData(input: InputItem[]): string[] {
  return input.flatMap((item) => (item.type === "image" && typeof item.data === "string" ? [item.data] : []));
}

test("compositionRoundTripPreservesAssociations", () => {
  const imageOnly = recoverMutationComposition(
    mutationRecord([{ type: "image", mediaType: "image/png", data: "IMAGE_ONLY", name: "only.png" }], {
      composerText: "",
      attachments: [attachment(4, "only.png")],
    }),
  );
  expect(imageOnly).toEqual({
    text: "",
    attachments: [{ marker: 4, mediaType: "image/png", data: "IMAGE_ONLY", name: "only.png" }],
    skillNames: [],
    commandNames: [],
    imageReferences: "recorded",
  });

  const selectionOnly = recoverMutationComposition(
    mutationRecord([
      { type: "skill", name: " pkg:inspect " },
      { type: "command", name: "review" },
    ]),
  );
  expect(selectionOnly).toMatchObject({
    text: "",
    attachments: [],
    skillNames: ["pkg:inspect"],
    commandNames: ["review"],
  });

  const currentAttachment = {
    marker: 9,
    mediaType: "image/png",
    data: "CURRENT_IMAGE",
    name: "current.png",
  };
  const currentText = "😀 [image 1][image 2][image 3][image 4][image 5][image 6][image 7][image 8][image 9]";
  const recovered = recoverMutationComposition(
    mutationRecord(
      [
        { type: "text", text: "wire text", mentions: [{ kind: "command", name: "inspect", offset: 28 }] },
        { type: "image", mediaType: "image/png", data: "SECOND_IMAGE", name: "second.png" },
        { type: "image", mediaType: "image/png", data: "SEVENTH_IMAGE", name: "seventh.png" },
        { type: "skill", name: "pkg:inspect" },
        { type: "command", name: "inspect" },
      ],
      {
        composerText: "😀 [image 2] then [image 7] /inspect",
        composerMentions: [{ kind: "command", name: "inspect", offset: 28 }],
        attachments: [attachment(2, "second.png"), attachment(7, "seventh.png")],
      },
    ),
  );
  const current: ComposerComposition<typeof currentAttachment> = {
    text: currentText,
    attachments: [currentAttachment],
    skillNames: ["pkg:current"],
    commandNames: [],
  };

  const merged = mergeComposerCompositions(current, recovered);
  expect(merged.text).toBe(`${currentText}\n\n😀 [image 10] then [image 11] /inspect`);
  expect(merged.attachments[0]).toBe(currentAttachment);
  expect(merged.attachments.map((image) => image.marker)).toEqual([9, 10, 11]);
  expect(merged.skillNames).toEqual(["pkg:current", "pkg:inspect"]);
  expect(merged.commandNames).toEqual(["inspect"]);
  expect(merged.mentions).toEqual([{ kind: "command", name: "inspect", offset: 116 }]);

  const encoded = buildComposerInput(
    merged.text,
    merged.attachments,
    merged.skillNames,
    merged.commandNames,
    merged.mentions,
  );
  expect(imageData(encoded)).toEqual(["CURRENT_IMAGE", "SECOND_IMAGE", "SEVENTH_IMAGE"]);
  expect(encoded.find((item) => item.type === "text")?.mentions).toEqual([
    { kind: "command", name: "inspect", offset: 181 },
  ]);
});

test("recordedLiteralNeverCapturesCurrentTile", () => {
  const currentText = "current [image 1] /inspect";
  const recoveredText =
    "literal [image 1][image 2][image 3][image 4][image 5][image 6][image 7][image 8][image 9] /inspect";
  const current: ComposerComposition<InputAttachment> = {
    text: currentText,
    attachments: [{ marker: 1, name: "current.png", mediaType: "image/png", data: "CURRENT" }],
    skillNames: ["inspect"],
    commandNames: [],
    mentions: [{ kind: "skill", name: "inspect", offset: currentText.indexOf("/inspect") }],
  };
  const recovered = recoverMutationComposition(
    mutationRecord(
      [
        { type: "text", text: recoveredText },
        { type: "command", name: "inspect" },
      ],
      {
        composerText: recoveredText,
        composerMentions: [{ kind: "command", name: "inspect", offset: recoveredText.indexOf("/inspect") }],
      },
    ),
  );
  const merged = mergeComposerCompositions(current, recovered);
  const wire = buildComposerInput(
    merged.text,
    merged.attachments,
    merged.skillNames,
    merged.commandNames,
    merged.mentions,
  );
  expect(wire[0]?.text).toBe(`current (attached image 10: current.png) /inspect\n\n${recoveredText}`);
  expect(merged.text).toBe(`current [image 10] /inspect\n\n${recoveredText}`);
  expect(merged.attachments).toEqual([{ ...current.attachments[0], marker: 10 }]);
  expect(merged.mentions).toEqual([
    { kind: "skill", name: "inspect", offset: merged.text.indexOf("/inspect") },
    { kind: "command", name: "inspect", offset: merged.text.lastIndexOf("/inspect") },
  ]);
  expect(wire[0]?.mentions).toEqual([
    { kind: "skill", name: "inspect", offset: wire[0]?.text?.indexOf("/inspect") },
    { kind: "command", name: "inspect", offset: wire[0]?.text?.lastIndexOf("/inspect") },
  ]);
  expect(imageData(wire)).toEqual(["CURRENT"]);
});

test("legacyLiteralNeverCapturesATile", () => {
  const currentImageData = "CURRENT_IMAGE_DATA";
  const legacyImageData = "LEGACY_IMAGE_DATA";
  const legacyText = "legacy 😀 [image 1] stays literal";
  expect(nextComposerImageMarker(legacyText, [])).toBe(2);
  const currentText = "current [image 1][image 2][image 3][image 4][image 5][image 6][image 7][image 8][image 9]";
  const currentAttachment = {
    marker: 1,
    mediaType: "image/png",
    data: currentImageData,
    name: "current.png",
  };
  const current: ComposerComposition<typeof currentAttachment> = {
    text: currentText,
    attachments: [currentAttachment],
    skillNames: [],
    commandNames: [],
    mentions: [{ kind: "command", name: "after", offset: 89 }],
  };
  const legacy = recoverMutationComposition(
    mutationRecord(
      [
        { type: "text", text: legacyText },
        { type: "image", mediaType: "image/png", data: legacyImageData, name: "legacy.png" },
      ],
      { attachments: [attachment(7, "legacy.png")] },
    ),
  );

  const merged = mergeComposerCompositions(current, legacy);
  expect(merged.text).toContain(legacyText);
  expect(merged.text).toBe(
    `current [image 10][image 2][image 3][image 4][image 5][image 6][image 7][image 8][image 9]\n\n${legacyText}`,
  );
  expect(merged.attachments.every((image) => image.marker !== 1)).toBe(true);
  expect(merged.attachments[0]).not.toBe(currentAttachment);
  expect(merged.mentions).toEqual([{ kind: "command", name: "after", offset: 90 }]);
  const encodedImageData = imageData(
    buildComposerInput(merged.text, merged.attachments, merged.skillNames, merged.commandNames, merged.mentions),
  );
  expect(encodedImageData).toEqual([currentImageData, legacyImageData]);
  expect(buildComposerInput(merged.text, merged.attachments)[0]).toMatchObject({
    type: "text",
    text: expect.stringContaining(legacyText),
  });

  for (const removed of merged.attachments) {
    const withoutMarker = stripMarker(merged.text, merged.text.length, removed.marker).value;
    const remaining = merged.attachments.filter((image) => image !== removed);
    const marker = nextComposerImageMarker(withoutMarker, remaining, 11);
    expect(marker).toBe(12);
    const added = {
      marker,
      mediaType: "image/png",
      data: `ADDED_AFTER_${removed.marker}`,
      name: "added.png",
    };
    const withAdded: ComposerComposition<InputAttachment> = {
      text: `${withoutMarker}\n${markerText(marker)}`,
      attachments: [...remaining, added],
      skillNames: [],
      commandNames: [],
      mentions: merged.mentions,
    };
    const later = recoverMutationComposition(
      mutationRecord(
        [
          { type: "text", text: "later wire" },
          { type: "image", mediaType: "image/png", data: "LATER_IMAGE", name: "later.png" },
        ],
        {
          clientMutationId: `later-${removed.marker}`,
          composerText: "later [image 1]",
          attachments: [attachment(1, "later.png")],
        },
      ),
    );
    const afterLaterMerge = mergeComposerCompositions(withAdded, later);
    const encoded = buildComposerInput(
      afterLaterMerge.text,
      afterLaterMerge.attachments,
      afterLaterMerge.skillNames,
      afterLaterMerge.commandNames,
      afterLaterMerge.mentions,
    );
    const encodedText = encoded.find((item) => item.type === "text")?.text;

    expect(encodedText).toContain(legacyText);
    expect(afterLaterMerge.attachments.every((image) => image.marker !== 1)).toBe(true);
    expect(imageData(encoded)).toEqual([...remaining.map((image) => image.data), added.data, "LATER_IMAGE"]);
    if (remaining.some((image) => image.marker === 10)) {
      expect(encodedText).toContain("(attached image 10: current.png)");
    }
  }
});
