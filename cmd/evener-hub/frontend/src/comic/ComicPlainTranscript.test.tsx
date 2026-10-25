import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { ComicPlainTranscript } from "./ComicPlainTranscript";
import type { ComicEntry } from "./projection";

afterEach(cleanup);

test("plain transcript keeps literal text, thought/source semantics and complete identity", () => {
  const literal = "  **hello** <script>window.attack()</script> 👩🏽‍🚀\n\tlast  ";
  const entries: ComicEntry[] = [
    {
      id: "human",
      sourceItemId: "human",
      sourceIndex: 0,
      kind: "dialogue",
      speaker: "human",
      sourceLabel: "Human",
      text: literal,
      streaming: false,
      persona: { key: "human", character: "anna", name: "Anna", badge: "human" },
    },
    {
      id: "thought",
      sourceItemId: "thought",
      sourceIndex: 1,
      kind: "thought",
      speaker: "assistant",
      sourceLabel: "Thought",
      text: "  Consider 🧭\n\ncontinue  ",
      streaming: true,
      persona: { key: "delegate", character: "tiki", name: "Same", badge: "dlg_7", ownerRef: "remote:root" },
    },
    {
      id: "redacted",
      sourceItemId: "redacted",
      sourceIndex: 2,
      kind: "thought",
      speaker: "assistant",
      sourceLabel: "Thought",
      text: "",
      streaming: false,
    },
    {
      id: "parent",
      sourceItemId: "parent",
      sourceIndex: 3,
      kind: "caption",
      speaker: "source",
      sourceLabel: "From parent",
      text: "  Do this  ",
      streaming: false,
    },
    {
      id: "image",
      sourceItemId: "image",
      sourceIndex: 4,
      kind: "images",
      speaker: "source",
      sourceLabel: "read_file",
      text: "",
      streaming: false,
      images: [{ src: "/s/root/images/a", name: "frame.png", source: "read-file" }],
    },
  ];
  const { container } = render(<ComicPlainTranscript entries={entries} />);
  expect(container.querySelector("script")).toBeNull();
  expect(container.querySelector("strong")?.textContent).not.toContain("hello");
  const rows = screen.getAllByRole("listitem");
  expect(rows).toHaveLength(entries.length);
  expect(rows[0]?.textContent).toContain(literal);
  expect(rows[1]?.textContent).toContain("  Consider 🧭\n\ncontinue  ");
  expect(rows[1]?.textContent).toContain("remote:root");
  expect(rows[1]?.textContent).toContain("dlg_7");
  expect(rows[3]?.textContent).toContain("  Do this  ");
  expect(rows[2]?.getAttribute("title")).toBeNull();
  expect(rows[2]?.textContent).not.toContain("Consider");
  expect(within(rows[4]!).getByTestId("image-gallery-thumb").querySelector("img")?.getAttribute("src")).toBe(
    "/s/root/images/a",
  );
  expect(rows[4]?.textContent).toContain("frame.png");
});
