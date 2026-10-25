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

test("plain transcript puts positionless turn errors in a separate error group", () => {
  const error = "Failed before any item was recorded.";
  const entries: ComicEntry[] = [
    {
      id: "first",
      sourceItemId: "first",
      sourceIndex: 0,
      kind: "dialogue",
      speaker: "human",
      sourceLabel: "Human",
      text: "First",
      streaming: false,
    },
    {
      id: "turn:missing:error",
      sourceTurnId: "missing",
      kind: "caption",
      speaker: "source",
      sourceLabel: "Turn error",
      text: error,
      streaming: false,
    },
    {
      id: "last",
      sourceItemId: "last",
      sourceIndex: 1,
      kind: "dialogue",
      speaker: "assistant",
      sourceLabel: "Assistant",
      text: "Last",
      streaming: false,
    },
  ];
  render(<ComicPlainTranscript entries={entries} />);
  const conversation = screen.getByRole("list", { name: "Conversation" });
  expect(
    within(conversation)
      .getAllByRole("listitem")
      .map((row) => row.textContent),
  ).toEqual(expect.arrayContaining([expect.stringContaining("First"), expect.stringContaining("Last")]));
  expect(within(conversation).queryByText(error)).toBeNull();
  const errors = screen.getByRole("region", { name: /turn errors/i });
  expect(within(errors).getByText(error)).toBeTruthy();
  expect(within(errors).getByText(/missing/)).toBeTruthy();
});
