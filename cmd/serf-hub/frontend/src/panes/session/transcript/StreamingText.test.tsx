import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { StreamingText } from "./StreamingText";

afterEach(cleanup);

test("renders empty when chunks is empty", () => {
  const { container } = render(<StreamingText chunks={[]} />);
  expect(container.textContent).toBe("");
});

test("renders a single chunk's text", () => {
  const { container } = render(<StreamingText chunks={["hello"]} />);
  expect(container.textContent).toBe("hello");
});

test("an incremental re-render appends only the new chunk, joined with the old", () => {
  const { container, rerender } = render(<StreamingText chunks={["a"]} />);
  expect(container.textContent).toBe("a");
  rerender(<StreamingText chunks={["a", "b"]} />);
  expect(container.textContent).toBe("ab");
});

test("multiple sequential appends accumulate in order", () => {
  const { container, rerender } = render(<StreamingText chunks={["a"]} />);
  rerender(<StreamingText chunks={["a", "b"]} />);
  rerender(<StreamingText chunks={["a", "b", "c"]} />);
  rerender(<StreamingText chunks={["a", "b", "c", "d"]} />);
  expect(container.textContent).toBe("abcd");
});

test("re-rendering with the exact same array reference appends nothing (proven by the absence of duplication)", () => {
  const chunks = ["a", "b"];
  const { container, rerender } = render(<StreamingText chunks={chunks} />);
  expect(container.textContent).toBe("ab");
  rerender(<StreamingText chunks={chunks} />);
  expect(container.textContent).toBe("ab");
});

test("re-rendering with a NEW array instance holding the same content appends nothing - tracking is by rendered count, not array identity", () => {
  const { container, rerender } = render(<StreamingText chunks={["a", "b"]} />);
  expect(container.textContent).toBe("ab");
  // A fresh array literal, same length/content as before, different reference.
  rerender(<StreamingText chunks={[...["a", "b"]]} />);
  expect(container.textContent).toBe("ab"); // NOT "abab" - would be, if content got re-appended
});

test("a UTF-16 surrogate pair split across two chunks renders as the correct single glyph", () => {
  const emoji = "😀"; // 😀 GRINNING FACE
  const highSurrogate = emoji[0]!;
  const lowSurrogate = emoji[1]!;
  const { container, rerender } = render(<StreamingText chunks={[highSurrogate]} />);
  rerender(<StreamingText chunks={[highSurrogate, lowSurrogate]} />);
  expect(container.textContent).toBe(emoji);
});

test("a surrogate pair split across chunks with more text on either side still joins correctly", () => {
  const emoji = "😀";
  const chunks = [`hi ${emoji[0]}`, `${emoji[1]} there`];
  const { container, rerender } = render(<StreamingText chunks={[chunks[0]!]} />);
  rerender(<StreamingText chunks={chunks} />);
  expect(container.textContent).toBe(`hi ${emoji} there`);
});

test("onCommit is called with the joined text so far after new chunks are appended", () => {
  const onCommit = vi.fn();
  const { rerender } = render(<StreamingText chunks={["a"]} onCommit={onCommit} />);
  expect(onCommit).toHaveBeenCalledWith("a");
  rerender(<StreamingText chunks={["a", "b"]} onCommit={onCommit} />);
  expect(onCommit).toHaveBeenCalledWith("ab");
  expect(onCommit).toHaveBeenCalledTimes(2);
});

test("onCommit is not called again when a re-render carries no new chunks", () => {
  const onCommit = vi.fn();
  const { rerender } = render(<StreamingText chunks={["a"]} onCommit={onCommit} />);
  expect(onCommit).toHaveBeenCalledTimes(1);
  rerender(<StreamingText chunks={["a"]} onCommit={onCommit} />);
  expect(onCommit).toHaveBeenCalledTimes(1);
});

test("onCommit is optional - omitting it does not throw", () => {
  expect(() => render(<StreamingText chunks={["a"]} />)).not.toThrow();
});

test("declares no JSX children of its own - every character arrives imperatively, never via React's reconciliation", () => {
  const { container } = render(<StreamingText chunks={["x"]} />);
  const root = container.firstElementChild!;
  // Exactly one child: the imperatively-created text node. If StreamingText
  // ever rendered a chunk through JSX too, React would own a second,
  // redundant text node here and re-render risk would be back in play.
  expect(root.childNodes).toHaveLength(1);
  expect(root.childNodes[0]!.nodeType).toBe(Node.TEXT_NODE);
});
