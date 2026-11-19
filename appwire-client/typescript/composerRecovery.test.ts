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

// The worst shape: a reader holds its OWN old tile 1, adopts another reader's
// "[image 1]" (whose bytes never travelled), and then edits a queued message
// whose prose also says "[image 1]". A merge that renumbered by number alone
// would move the tile to 2 and rewrite BOTH occurrences to "[image 2]" - the
// adopted anchor then sends the retained tile's bytes, and the repair that
// removes the "missing" reference deletes the queued prose. Adoption is what
// tells the two occurrences apart.
test("adoptedOccurrenceSurvivesAQueuedLiteralBesideARetainedTile", () => {
  const currentText = "adopted [image 1] prose";
  const current: ComposerComposition<InputAttachment> = {
    text: currentText,
    attachments: [{ marker: 1, name: "retained.png", mediaType: "image/png", data: "RETAINED_BYTES" }],
    skillNames: [],
    commandNames: [],
    // Another writer staged this marker and the bytes stayed with them.
    adoptedImageMarkers: [1],
  };
  const queuedText = "queued prose says [image 1]";
  const recovered = recoverMutationComposition(mutationRecord([{ type: "text", text: queuedText }]));

  const merged = mergeComposerCompositions(current, recovered);
  const wire = buildComposerInput(merged.text, merged.attachments, merged.skillNames, merged.commandNames);

  // The queued prose keeps the number it names, untouched.
  expect(merged.text).toContain(queuedText);
  // The adopted occurrence moved off that number; the retained tile moved too,
  // so the anchor is not backed by the old image's bytes.
  expect(merged.attachments.map((image) => image.marker)).toEqual([2]);
  const adoptedMarker = merged.adoptedImageMarkers?.[0];
  if (adoptedMarker === undefined) throw new Error("expected the adopted occurrence to keep its provenance");
  expect(adoptedMarker).not.toBe(1);
  // Exactly one occurrence still says "[image 1]" - the queued prose - so the
  // repair can only ever remove the text the person just edited back in.
  expect(merged.text.match(/\[image 1\]/g)?.length).toBe(1);
  // And the adopted anchor is the one occurrence nothing on the wire backs:
  // the retained tile moved off 1 too, so no text names its number and its
  // bytes are never translated into a reference the adopted anchor sits on.
  // (It still travels as its own image part, which is the pre-existing
  // behavior for a staged tile whose text reference was edited away.)
  expect(merged.text).not.toContain("[image 2]");
  expect(wire.find((item) => item.type === "text")?.text).not.toContain("(attached image");
  expect(wire.filter((item) => item.type === "image").map((item) => item.data)).toEqual(["RETAINED_BYTES"]);
});

// Restore: a durable record that names an adopted anchor must give it back.
// Without this the row's text returns as if its writer had staged every
// marker it spells, so a reload sends the orphan as prose or pairs it with a
// retained tile at that number.
test("recoverMutationComposition restores the adoption a record carries", () => {
  const text = "kept [image 1] and [image 2]";
  const record = mutationRecord([{ type: "text", text }], {
    composerText: text,
    adoptedImageMarkers: [1],
  });

  const recovered = recoverMutationComposition(record);
  expect(recovered.adoptedImageMarkers).toEqual([1]);
});

// The range survives the durable hop, so a record holding a literal beside an
// adopted spelling of one number comes back naming WHICH occurrence it means.
// The number alone cannot distinguish them, and a reader that guesses removes
// the person's prose.
test("recoverMutationComposition restores the exact occurrence a record names", () => {
  const text = "[image 1] tail [image 1]";
  const record = mutationRecord([{ type: "text", text }], {
    composerText: text,
    adoptedImageMarkers: [1],
    adoptedImageRanges: [{ marker: 1, start: 15, end: 24 }],
  });

  const recovered = recoverMutationComposition(record);
  expect(recovered.adoptedImageMarkers).toEqual([1]);
  expect(recovered.adoptedImageRanges).toEqual([{ marker: 1, start: 15, end: 24 }]);
});

// A merge that appends the recovered text moves its adopted occurrence by the
// append offset. The range must travel with it, or the repaired record would
// name an offset that now holds someone else's prose.
test("a merged recovered adoption keeps its range at the offset it lands on", () => {
  const current: ComposerComposition<InputAttachment> = {
    text: "mine ",
    attachments: [],
    skillNames: [],
    commandNames: [],
  };
  const queuedText = "[image 1] tail";
  const recovered = recoverMutationComposition(
    mutationRecord([{ type: "text", text: queuedText }], {
      composerText: queuedText,
      adoptedImageMarkers: [1],
      adoptedImageRanges: [{ marker: 1, start: 0, end: 9 }],
    }),
  );

  const merged = mergeComposerCompositions(current, recovered);
  // The merge joins the two sides with a blank line: "mine " is 5 units, the
  // separator 2, so the adopted occurrence lands at 7.
  const expected = merged.text.indexOf("[image 1]");
  expect(expected).toBe(7);
  expect(merged.adoptedImageRanges).toEqual([{ marker: 1, start: expected, end: expected + 9 }]);
  expect(merged.text.slice(expected, expected + 9)).toBe("[image 1]");
});

// The record's provenance is only ever about anchors its own text spells. A
// repair that removed one retires nothing durable, so a stale number must be
// filtered rather than restored as a refusal the text no longer earns.
test("recoverMutationComposition drops adoption its text no longer spells", () => {
  const record = mutationRecord([{ type: "text", text: "prose only" }], {
    composerText: "prose only",
    adoptedImageMarkers: [1],
  });

  expect(recoverMutationComposition(record).adoptedImageMarkers).toBeUndefined();
});

// Bytes that DID travel back the marker, so a record naming it is describing a
// staged tile rather than a missing image. Restoring adoption here would refuse
// a send whose image is right there in the composer.
test("recoverMutationComposition does not adopt a marker its own attachments back", () => {
  const text = "backed [image 1]";
  const record = mutationRecord(
    [
      { type: "text", text },
      { type: "image", data: "BACKED_BYTES", mediaType: "image/png" },
    ],
    {
      composerText: text,
      attachments: [attachment(1, "backed.png")],
      adoptedImageMarkers: [1],
    },
  );

  expect(recoverMutationComposition(record).adoptedImageMarkers).toBeUndefined();
});

// The merge rule stated as what it protects: provenance belongs to the side
// that supplied the occurrence. A current-side adoption whose anchor the
// person already removed is dormant - it must not claim a literal marker in
// the queued message, because repairing that "missing" reference would delete
// prose the person wrote.
test("a dormant current-side adoption does not claim a queued literal", () => {
  const current: ComposerComposition<InputAttachment> = {
    text: "prose only",
    attachments: [],
    skillNames: [],
    commandNames: [],
    // Removed from the text, still carried: this is what an undo would restore.
    adoptedImageMarkers: [1],
  };
  const queuedText = "queued prose says [image 1]";
  const recovered = recoverMutationComposition(mutationRecord([{ type: "text", text: queuedText }]));

  const merged = mergeComposerCompositions(current, recovered);
  expect(merged.adoptedImageMarkers).toBeUndefined();
  expect(merged.text).toContain(queuedText);
});

// The same rule from the other side: a recovered adoption the merge had to
// renumber keeps its provenance at the number it actually sits on, so the
// refusal and the repair both name the occurrence whose bytes are gone.
test("a recovered adoption keeps its provenance at the number it lands on", () => {
  const current: ComposerComposition<InputAttachment> = {
    text: "mine [image 1]",
    attachments: [{ marker: 1, name: "retained.png", mediaType: "image/png", data: "RETAINED_BYTES" }],
    skillNames: [],
    commandNames: [],
  };
  const queuedText = "theirs [image 1]";
  const recovered = recoverMutationComposition(
    mutationRecord([{ type: "text", text: queuedText }], {
      composerText: queuedText,
      adoptedImageMarkers: [1],
    }),
  );

  const merged = mergeComposerCompositions(current, recovered);
  // The retained tile moves off 1, because the queued literal spells that
  // number and the tile's bytes must never back it.
  expect(merged.attachments.map((item) => item.marker)).toEqual([2]);
  // Nothing on this side claims 1 any more, so the recovered adoption keeps the
  // number its own text names.
  expect(merged.adoptedImageMarkers).toEqual([1]);
  expect(merged.text).toContain("theirs [image 1]");
});

// The allocation rule: a number a dormant adoption still names is not free.
// Reusing it would pair a fresh tile's bytes with an anchor the person did
// not stage, and the stale refusal would then block the new image.
test("nextComposerImageMarker skips a number a dormant adoption reserves", () => {
  // The person removed the anchor, so the text no longer spells 1 - without the
  // reservation the next gesture reuses it, which is the bug.
  expect(nextComposerImageMarker("kept ", [], 0)).toBe(1);
  expect(nextComposerImageMarker("kept ", [], 0, [1])).toBe(2);
});

// The current side of a merge, with a number spelled twice: one occurrence is
// the missing anchor its record located by range, the other is prose the
// person typed beside it. A queued message that also says "[image 1]" forces
// this side's adopted anchor off that number, and the rename may move ONLY the
// located occurrence. Rewriting by number silently changes the person's prose.
test("a current-side adopted rename moves only the occurrence the record located", () => {
  const currentText =
    "missing [image 1] typed [image 1] [image 2] [image 3] [image 4] [image 5] [image 6] [image 7] [image 8] [image 9] /inspect";
  const adoptedStart = currentText.indexOf("[image 1]");
  const adopted = { marker: 1, start: adoptedStart, end: adoptedStart + "[image 1]".length };
  expect(adopted).toEqual({ marker: 1, start: 8, end: 17 });
  const current: ComposerComposition<InputAttachment> = {
    text: currentText,
    attachments: [],
    skillNames: [],
    commandNames: [],
    mentions: [{ kind: "command", name: "inspect", offset: currentText.indexOf("/inspect") }],
    adoptedImageMarkers: [1],
    adoptedImageRanges: [adopted],
  };
  const recovered = recoverMutationComposition(mutationRecord([{ type: "text", text: "queued prose says [image 1]" }]));

  const merged = mergeComposerCompositions(current, recovered);
  const expected =
    "missing [image 10] typed [image 1] [image 2] [image 3] [image 4] [image 5] [image 6] [image 7] [image 8] [image 9] /inspect\n\nqueued prose says [image 1]";
  expect(merged.text).toBe(expected);
  // The adopted anchor follows the number it was renamed to, at the position
  // the one width-growing replacement left it.
  expect(merged.adoptedImageMarkers).toEqual([10]);
  expect(merged.adoptedImageRanges).toEqual([{ marker: 10, start: 8, end: 18 }]);
  expect(merged.text.slice(8, 18)).toBe("[image 10]");
  // ...and the literal the person typed still spells its own number, exactly
  // where it was.
  const literal = merged.text.indexOf("[image 1]", adopted.end);
  expect(literal).toBe(25);
  expect(merged.text.slice(literal, literal + "[image 1]".length)).toBe("[image 1]");
  // Exactly one +1 shift was made, so the mention past both occurrences moves
  // by one and lands on the literal "/inspect" in the merged text.
  const expectedInspect = expected.indexOf("/inspect");
  expect(expectedInspect).toBe(115);
  expect(merged.mentions).toEqual([{ kind: "command", name: "inspect", offset: expectedInspect }]);
});

// The mirror case, on the recovered side: the record it came from located its
// missing anchor by range, and the same number is spelled again as the queued
// message's own prose. The merge has to move the adopted occurrence to a free
// number, and only that occurrence - the queued literal keeps the number it
// names, and the mention past it keeps its exact position.
test("a recovered-side adopted rename moves only the occurrence the record located", () => {
  const currentText = "mine [image 1] [image 2] [image 3] [image 4] [image 5] [image 6] [image 7] [image 8] [image 9]";
  const current: ComposerComposition<InputAttachment> = {
    text: currentText,
    attachments: [],
    skillNames: [],
    commandNames: [],
  };
  const queuedText = "theirs [image 1] literal [image 1] /inspect";
  const adoptedStart = queuedText.indexOf("[image 1]");
  expect(adoptedStart).toBe(7);
  const recovered = recoverMutationComposition(
    mutationRecord([{ type: "text", text: queuedText }], {
      composerText: queuedText,
      composerMentions: [{ kind: "command", name: "inspect", offset: queuedText.indexOf("/inspect") }],
      adoptedImageMarkers: [1],
      adoptedImageRanges: [{ marker: 1, start: adoptedStart, end: adoptedStart + "[image 1]".length }],
    }),
  );

  const merged = mergeComposerCompositions(current, recovered);
  const expected = `${currentText}\n\ntheirs [image 10] literal [image 1] /inspect`;
  expect(merged.text).toBe(expected);
  // The queued message's own literal keeps its spelling and its place.
  const literal = merged.text.indexOf("[image 1]", currentText.length);
  // Its own spelling is untouched; the only change to its offset is the one
  // the renamed occurrence before it made by growing a character.
  expect(literal).toBe(currentText.length + 2 + 26);
  expect(merged.text.slice(literal, literal + "[image 1]".length)).toBe("[image 1]");
  // One width-growing replacement, so the queued mention moved exactly one.
  const expectedInspect = expected.indexOf("/inspect");
  expect(expectedInspect).toBe(132);
  expect(merged.mentions).toEqual([{ kind: "command", name: "inspect", offset: expectedInspect }]);
  // The adopted occurrence's range moved with it and past the join.
  expect(merged.adoptedImageMarkers).toEqual([10]);
  const range = merged.adoptedImageRanges?.[0];
  if (range === undefined) throw new Error("expected the adopted occurrence to keep its provenance");
  expect(range).toEqual({ marker: 10, start: 103, end: 113 });
  expect(merged.text.slice(range.start, range.end)).toBe("[image 10]");
});
