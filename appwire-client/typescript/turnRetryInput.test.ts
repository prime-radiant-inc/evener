// The error row's Retry re-issues the input that opened the failed turn:
// the person's own words and images (see turnRetryInput.ts's header). These
// tests pin the module-level contract both apps' Retry actions depend on; the
// web component suite drives the same function through the end-cap and adds
// the marker-pairing corners live in the same shared shape.

import { describe, expect, it } from "vitest";
import type { ItemModel, TurnModel } from "./model";
import { originatingInput } from "./turnRetryInput";

const dataUri = (name?: string) => ({
  src: `data:image/png;base64,${name ?? "img"}-bytes`,
  ...(name ? { name } : {}),
});

function userItem(over: Partial<ItemModel> = {}): ItemModel {
  return {
    id: "u-1",
    turnId: "turn_1",
    type: "userMessage",
    text: "run the tests",
    ...over,
  } as ItemModel;
}

function turn(id: string, items: ItemModel[], status = "failed"): TurnModel {
  return { id, status, items } as TurnModel;
}

describe("originatingInput", () => {
  it("re-issues the failed turn's own user input", () => {
    const origin = originatingInput([turn("turn_1", [userItem()])], "turn_1");
    expect(origin).toEqual({ kind: "retry", input: { text: "run the tests", sourceImageCount: 0 } });
  });

  it("finds the input in an earlier turn for a reloaded failure, and stops at the failed turn", () => {
    const origin = originatingInput(
      [
        turn("turn_1", [userItem({ text: "the original question", id: "u-0", turnId: "turn_1" })], "completed"),
        // A reloaded failure holds only the failure entry: no user item.
        turn("turn_2", [], "failed"),
        // A later prompt is a different exchange: it must never be re-issued.
        turn("turn_3", [userItem({ text: "a different question", id: "u-2", turnId: "turn_3" })], "completed"),
      ],
      "turn_2",
    );
    expect(origin).toMatchObject({ kind: "retry", input: { text: "the original question" } });
  });

  it("pairs each image with its translated marker number and rebuilds composer anchors", () => {
    const origin = originatingInput(
      [
        turn("turn_1", [
          userItem({
            text: "(attached image 2) describe the plot",
            images: [dataUri()],
          }),
        ]),
      ],
      "turn_1",
    );
    expect(origin).toEqual({
      kind: "retry",
      input: {
        text: "[image 2] describe the plot",
        attachments: [{ marker: 2, mediaType: "image/png", data: "img-bytes" }],
        sourceImageCount: 1,
      },
    });
  });

  it("refuses the images when the marker-to-bytes identity is ambiguous, but still retries the text", () => {
    const origin = originatingInput(
      [
        turn("turn_1", [
          userItem({
            text: "compare (attached image 1) and (attached image 1)",
            // Two images, one marker claim: refuse rather than guess bytes.
            images: [dataUri("a.png"), dataUri("b.png")],
          }),
        ]),
      ],
      "turn_1",
    );
    expect(origin).toEqual({
      kind: "retry",
      input: { text: "compare (attached image 1) and (attached image 1)", sourceImageCount: 2 },
    });
  });

  it("drops images whose bytes did not survive, and reports the count", () => {
    const origin = originatingInput(
      [
        turn("turn_1", [
          userItem({
            text: "describe (attached image 1) and this too",
            images: [{ src: "/s/session/images/sha-1", name: "gone.png" }],
          }),
        ]),
      ],
      "turn_1",
    );
    expect(origin).toEqual({
      kind: "retry",
      input: { text: "describe (attached image 1) and this too", sourceImageCount: 1 },
    });
  });

  it("treats marker-only text as image-only: retryable with bytes, a dead end without", () => {
    const withBytes = originatingInput(
      [turn("turn_1", [userItem({ text: "(attached image 1)", images: [dataUri()] })])],
      "turn_1",
    );
    expect(withBytes).toMatchObject({
      kind: "retry",
      input: { text: "[image 1]", attachments: [{ marker: 1 }], sourceImageCount: 1 },
    });
    const withoutBytes = originatingInput(
      [turn("turn_1", [userItem({ text: "(attached image 1)", images: [{ src: "/s/s/images/sha" }] })])],
      "turn_1",
    );
    expect(withoutBytes).toEqual({ kind: "images-unavailable", sourceImageCount: 1 });
  });

  it("returns nothing when no turn at or before the failure holds usable input", () => {
    expect(originatingInput([turn("turn_1", [])], "turn_1")).toBeUndefined();
    expect(originatingInput([turn("turn_1", [userItem({ text: "   " })])], "turn_1")).toBeUndefined();
  });

  it("skips a whitespace-only input and takes the last usable one at or before the failure", () => {
    const origin = originatingInput(
      [
        turn("turn_1", [userItem({ text: "older", id: "u-0", turnId: "turn_1" })], "completed"),
        turn("turn_2", [userItem({ text: "  ", id: "u-1", turnId: "turn_2" })], "completed"),
      ],
      "turn_2",
    );
    expect(origin).toMatchObject({ kind: "retry", input: { text: "older" } });
  });
});
