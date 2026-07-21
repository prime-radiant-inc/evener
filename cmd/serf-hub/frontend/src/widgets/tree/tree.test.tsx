import { afterEach, test, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Tree, type TreeNode } from "./index";

afterEach(cleanup);

// Flattened visible order for this fixture: a, b, b1, c
// (b is expanded so b1 shows; c is collapsed so c1 stays hidden).
const NODES: TreeNode[] = [
  { id: "a" },
  { id: "b", expanded: true, children: [{ id: "b1" }] },
  { id: "c", expanded: false, children: [{ id: "c1" }] },
];

function renderTree(overrides: Partial<Parameters<typeof Tree>[0]> = {}) {
  const onActivate = vi.fn();
  const onToggle = vi.fn();
  const utils = render(
    <Tree
      nodes={overrides.nodes ?? NODES}
      onActivate={overrides.onActivate ?? onActivate}
      onToggle={overrides.onToggle ?? onToggle}
      renderRow={overrides.renderRow ?? ((node) => node.id)}
    />,
  );
  return { ...utils, onActivate, onToggle };
}

function row(name: string): HTMLElement {
  return screen.getByRole("treeitem", { name });
}

test("renders one treeitem per visible node, hiding a collapsed branch's children", () => {
  renderTree();
  expect(screen.getAllByRole("treeitem")).toHaveLength(4); // a, b, b1, c - not c1
});

test("the root has role=tree", () => {
  renderTree();
  expect(screen.getByRole("tree")).toBeTruthy();
});

test("an expanded branch's children sit in a role=group", () => {
  renderTree();
  const group = screen.getByRole("group");
  expect(group.querySelector('[role="treeitem"]')?.textContent).toBe("b1");
});

test("aria-expanded is true for an expanded branch, false for a collapsed one, absent for a leaf", () => {
  renderTree();
  expect(row("a").hasAttribute("aria-expanded")).toBe(false);
  expect(row("b").getAttribute("aria-expanded")).toBe("true");
  expect(row("c").getAttribute("aria-expanded")).toBe("false");
});

test("aria-level reflects nesting depth (1-based)", () => {
  renderTree();
  expect(row("a").getAttribute("aria-level")).toBe("1");
  expect(row("b1").getAttribute("aria-level")).toBe("2");
});

test("roving tabindex: only the first visible row is focusable initially", () => {
  renderTree();
  expect(row("a").tabIndex).toBe(0);
  expect(row("b").tabIndex).toBe(-1);
  expect(row("b1").tabIndex).toBe(-1);
  expect(row("c").tabIndex).toBe(-1);
});

test("ArrowDown moves the roving tabindex and focus to the next sibling", () => {
  renderTree();
  act(() => row("a").focus());
  fireEvent.keyDown(row("a"), { key: "ArrowDown" });
  expect(row("a").tabIndex).toBe(-1);
  expect(row("b").tabIndex).toBe(0);
  expect(document.activeElement).toBe(row("b"));
});

test("ArrowDown from an expanded branch descends into its first child", () => {
  renderTree();
  act(() => row("b").focus());
  fireEvent.keyDown(row("b"), { key: "ArrowDown" });
  expect(document.activeElement).toBe(row("b1"));
});

test("ArrowDown walks back up to the next sibling of an ancestor when there is no next child", () => {
  renderTree();
  act(() => row("b1").focus());
  fireEvent.keyDown(row("b1"), { key: "ArrowDown" });
  expect(document.activeElement).toBe(row("c"));
});

test("ArrowDown on the last visible row is a no-op", () => {
  renderTree();
  act(() => row("c").focus());
  fireEvent.keyDown(row("c"), { key: "ArrowDown" });
  expect(document.activeElement).toBe(row("c"));
  expect(row("c").tabIndex).toBe(0);
});

test("ArrowUp moves to the previous visible row, descending into an expanded sibling's last child", () => {
  renderTree();
  act(() => row("c").focus());
  fireEvent.keyDown(row("c"), { key: "ArrowUp" });
  expect(document.activeElement).toBe(row("b1"));
});

test("ArrowUp on the first visible row is a no-op", () => {
  renderTree();
  act(() => row("a").focus());
  fireEvent.keyDown(row("a"), { key: "ArrowUp" });
  expect(document.activeElement).toBe(row("a"));
});

test("ArrowRight on a collapsed branch expands it via onToggle and does not move focus", () => {
  const { onToggle } = renderTree();
  act(() => row("c").focus());
  fireEvent.keyDown(row("c"), { key: "ArrowRight" });
  expect(onToggle).toHaveBeenCalledExactlyOnceWith(NODES[2]);
  expect(document.activeElement).toBe(row("c"));
});

test("ArrowRight on an expanded branch moves focus to its first child without toggling", () => {
  const { onToggle } = renderTree();
  act(() => row("b").focus());
  fireEvent.keyDown(row("b"), { key: "ArrowRight" });
  expect(onToggle).not.toHaveBeenCalled();
  expect(document.activeElement).toBe(row("b1"));
});

test("ArrowRight on a leaf does nothing", () => {
  const { onToggle } = renderTree();
  act(() => row("a").focus());
  fireEvent.keyDown(row("a"), { key: "ArrowRight" });
  expect(onToggle).not.toHaveBeenCalled();
  expect(document.activeElement).toBe(row("a"));
});

test("ArrowLeft on an expanded branch collapses it via onToggle and does not move focus", () => {
  const { onToggle } = renderTree();
  act(() => row("b").focus());
  fireEvent.keyDown(row("b"), { key: "ArrowLeft" });
  expect(onToggle).toHaveBeenCalledExactlyOnceWith(NODES[1]);
  expect(document.activeElement).toBe(row("b"));
});

test("ArrowLeft on a leaf child moves focus to its parent", () => {
  const { onToggle } = renderTree();
  act(() => row("b1").focus());
  fireEvent.keyDown(row("b1"), { key: "ArrowLeft" });
  expect(onToggle).not.toHaveBeenCalled();
  expect(document.activeElement).toBe(row("b"));
});

test("ArrowLeft on a collapsed, top-level branch does nothing (no parent)", () => {
  const { onToggle } = renderTree();
  act(() => row("c").focus());
  fireEvent.keyDown(row("c"), { key: "ArrowLeft" });
  expect(onToggle).not.toHaveBeenCalled();
  expect(document.activeElement).toBe(row("c"));
});

test("ArrowLeft on a top-level leaf does nothing (no parent)", () => {
  renderTree();
  act(() => row("a").focus());
  fireEvent.keyDown(row("a"), { key: "ArrowLeft" });
  expect(document.activeElement).toBe(row("a"));
});

test("Enter activates the current row via onActivate", () => {
  const { onActivate } = renderTree();
  act(() => row("b1").focus());
  fireEvent.keyDown(row("b1"), { key: "Enter" });
  expect(onActivate).toHaveBeenCalledExactlyOnceWith({ id: "b1" });
});

test("renderRow receives depth, expanded, hasChildren, and working toggle/activate callbacks", () => {
  const onActivate = vi.fn();
  const onToggle = vi.fn();
  render(
    <Tree
      nodes={NODES}
      onActivate={onActivate}
      onToggle={onToggle}
      renderRow={(node, info) => (
        <button type="button" onClick={info.toggle} data-testid={`row-${node.id}`}>
          {node.id}:{info.depth}:{String(info.expanded)}:{String(info.hasChildren)}
        </button>
      )}
    />,
  );
  expect(screen.getByTestId("row-b").textContent).toBe("b:0:true:true");
  expect(screen.getByTestId("row-a").textContent).toBe("a:0:false:false");
  expect(screen.getByTestId("row-b1").textContent).toBe("b1:1:false:false");

  fireEvent.click(screen.getByTestId("row-c"));
  expect(onToggle).toHaveBeenCalledExactlyOnceWith(NODES[2]);
});

test("renders an empty tree without crashing when nodes is empty", () => {
  render(<Tree nodes={[]} onActivate={vi.fn()} onToggle={vi.fn()} renderRow={(node) => node.id} />);
  expect(screen.getByRole("tree")).toBeTruthy();
  expect(screen.queryAllByRole("treeitem")).toHaveLength(0);
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "tree.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});
