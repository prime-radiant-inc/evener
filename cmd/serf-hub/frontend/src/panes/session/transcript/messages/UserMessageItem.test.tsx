import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { UserMessageItem, UserMessageView } from "./UserMessageItem";
import { itemRendererFor } from "../types";
import type { ItemModel, TurnModel } from "../../../../protocol/model";

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "completed", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "userMessage", text: "", ...overrides };
}

test('self-registers under the wire\'s user-message item type ("userMessage")', () => {
  expect(itemRendererFor("userMessage")).toBe(UserMessageItem);
});

test("renders the prompt text", () => {
  render(<UserMessageItem item={item({ text: "hello there" })} turn={turn} live={false} />);
  expect(screen.getByText("hello there")).toBeTruthy();
});

test('carries a quiet "You" tag as a sibling of the text, not mixed into it', () => {
  const { container } = render(<UserMessageItem item={item({ text: "hi" })} turn={turn} live={false} />);
  expect(screen.getByText("You")).toBeTruthy();
  // The "You" tag and the prompt text are two separate nodes - proven by
  // being independently queryable by their own exact text (a mixed-in tag
  // would make "hi" only findable as part of a larger "You hi" string).
  expect(screen.getByText("hi")).toBeTruthy();
  expect(container.querySelector('[data-testid="user-message-item"]')).toBeTruthy();
});

test("no gallery thumbnails when the item carries no images", () => {
  render(<UserMessageItem item={item({ text: "no pictures here" })} turn={turn} live={false} />);
  expect(screen.queryAllByTestId("image-gallery-thumb")).toHaveLength(0);
});

test("a single image renders one gallery thumbnail", () => {
  render(<UserMessageItem item={item({ text: "look", images: ["data:image/png;base64,x"] })} turn={turn} live={false} />);
  expect(screen.getAllByTestId("image-gallery-thumb")).toHaveLength(1);
});

test("multiple images each render their own gallery thumbnail", () => {
  render(
    <UserMessageItem
      item={item({ text: "look", images: ["a", "b", "c"] })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getAllByTestId("image-gallery-thumb")).toHaveLength(3);
});

test("renders identically regardless of live/settled - the user's own words never stream", () => {
  const { container: liveContainer } = render(
    <UserMessageItem item={item({ text: "same either way" })} turn={turn} live={true} />,
  );
  const liveHtml = liveContainer.innerHTML;
  cleanup();
  const { container: settledContainer } = render(
    <UserMessageItem item={item({ text: "same either way" })} turn={turn} live={false} />,
  );
  expect(settledContainer.innerHTML).toBe(liveHtml);
});

test("UserMessageView is exported standalone for reuse by user-sourced steering", () => {
  render(<UserMessageView item={item({ text: "reused" })} />);
  expect(screen.getByText("reused")).toBeTruthy();
  expect(screen.getByText("You")).toBeTruthy();
});
