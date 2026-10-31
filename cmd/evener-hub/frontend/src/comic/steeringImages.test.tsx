import { hydrateThread, type TranscriptAuthorship } from "@evener/appwire-client";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { ComicPanel } from "./ComicPanel";
import { ComicPlainTranscript } from "./ComicPlainTranscript";
import { projectComicConversation } from "./projection";

const authorships: (TranscriptAuthorship | undefined)[] = [{ author: { kind: "human" }, inherited: false }, undefined];

test.each(
  ["comic", "plain"].flatMap((mode) =>
    authorships.flatMap((authorship) => ["  STEER_IMAGE\n", ""].map((text) => ({ mode, authorship, text }))),
  ),
)("$mode preserves image-bearing steering %#", ({ mode, authorship, text }) => {
  const images = [
    { type: "image", url: "/s/root/images/steer-one", name: "one.png" },
    { type: "image", url: "/s/root/images/steer-two", name: "two.png" },
  ];
  const model = hydrateThread(
    {
      thread: wireThread("local:root", {
        turns: [
          {
            id: "turn",
            status: "completed",
            itemsView: "full",
            items: [{ type: "steering", id: "steer", turnId: "turn", source: "user", authorship, text, images }],
          },
        ],
      }),
    },
    "local:root",
    0,
  );
  const entries = projectComicConversation(model);
  expect(entries).toHaveLength(1);
  const entry = entries[0];
  if (!entry) throw new Error("missing steering entry");
  const { container } = render(
    mode === "plain" ? (
      <ComicPlainTranscript entries={entries} />
    ) : (
      <ComicPanel
        entry={entry}
        slice={{ entryId: entry.id, start: 0, end: text.length }}
        composition="wide"
        scene="room"
        expression="neutral"
      />
    ),
  );
  expect(container.querySelector("[data-comic-text]")?.textContent ?? "").toBe(text);
  expect(
    screen.getAllByTestId("image-gallery-thumb").map((thumb) => thumb.querySelector("img")?.getAttribute("src")),
  ).toEqual(images.map((image) => image.url));
});
