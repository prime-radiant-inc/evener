import { describe, expect, it } from "vitest";
import { translateAttachmentMarkers } from "../../cmd/evener-hub/frontend/src/protocol/attachmentMarkers";
import {
  buildComposerInput,
  buildInput,
} from "../../cmd/evener-hub/frontend/src/stores/composerInput";

describe("shared composer input", () => {
  it("preserves ordinary text and allows image-only input", () => {
    expect(buildComposerInput("  hello\n")).toEqual([
      { type: "text", text: "  hello\n" },
    ]);
    expect(
      buildComposerInput(" \n", [
        { marker: 3, mediaType: "image/png", data: "AQID" },
      ]),
    ).toEqual([{ type: "image", mediaType: "image/png", data: "AQID" }]);
  });

  it("translates marker identities at submit while retaining recovery anchors", () => {
    const attachments = [
      { marker: 7, name: "seven.png", mediaType: "image/png", data: "AQID" },
      { marker: 2, name: "two.png", mediaType: "image/png", data: "BAUG" },
    ];
    const text = "[image 2] / [image 7] / [image 9]";
    const sent = buildComposerInput(text, attachments);
    expect(sent[0]).toEqual({
      type: "text",
      text: translateAttachmentMarkers(text, attachments),
    });
    expect(sent.slice(1)).toEqual([
      {
        type: "image",
        name: "seven.png",
        mediaType: "image/png",
        data: "AQID",
      },
      { type: "image", name: "two.png", mediaType: "image/png", data: "BAUG" },
    ]);
    expect(buildInput(text, attachments)[0]).toEqual({ type: "text", text });
    expect(attachments.map((image) => image.marker)).toEqual([7, 2]);
  });
});
