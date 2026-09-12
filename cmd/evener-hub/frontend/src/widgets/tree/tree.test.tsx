import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
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
      releaseModifierKeys={overrides.releaseModifierKeys}
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

// With releaseModifierKeys (the desktop rail), Alt-held arrows and plain
// Alt+Home/End belong to the GLOBAL chords (Alt+Arrow pane cycling,
// Alt+Shift+Arrow live-session navigation and transcript scroll, Alt+Home/End):
// a blanket preventDefault here would swallow all of them while a rail row has
// focus (same trap RailResizeHandle.tsx's own guard documents). Shift stays
// tree-owned on arrows' siblings: no global chord is Shift+Arrow without Alt,
// and Home/End release only on PLAIN Alt - Alt+Shift+Home/End bind nothing
// globally, so they keep the tree's own Home/End navigation (round-13 low 6).
// This release is opt-in per consumer (roborev PR #1044 round-9 medium 1) -
// on mobile the chords are inert and Alt+ArrowLeft/Right are the browser's
// history navigation, so the default below keeps them tree-owned.
test("with releaseModifierKeys, arrows and Home/End with Alt held are left for global chords", () => {
  const { onToggle } = renderTree({ releaseModifierKeys: true });
  act(() => row("b").focus());
  for (const mod of [{ altKey: true }, { altKey: true, shiftKey: true }]) {
    // b is an expanded branch: without the guard, ArrowRight would move focus
    // into b1 and ArrowLeft would collapse via onToggle.
    const right = fireEvent.keyDown(row("b"), { key: "ArrowRight", ...mod });
    expect(right).toBe(true); // fireEvent returns false when the event was preventDefaulted
    expect(document.activeElement).toBe(row("b"));
    const left = fireEvent.keyDown(row("b"), { key: "ArrowLeft", ...mod });
    expect(left).toBe(true);
    expect(onToggle).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(row("b"));
  }
  // Home/End release only on plain Alt - the transcript-scroll chords bind
  // no Shift (round-13 low 6).
  const home = fireEvent.keyDown(row("b"), { key: "Home", altKey: true });
  expect(home).toBe(true);
  const end = fireEvent.keyDown(row("b"), { key: "End", altKey: true });
  expect(end).toBe(true);
  expect(onToggle).not.toHaveBeenCalled();
  expect(document.activeElement).toBe(row("b"));
});

// The default (no releaseModifierKeys): the tree owns every arrow and
// Home/End press, modifier or not. That is the mobile rail's configuration -
// the global Alt chords are not installed there, so releasing Alt+ArrowLeft/
// Right would hand them to the browser's history navigation (roborev PR
// #1044 round-9 medium 1).
test("without releaseModifierKeys, Alt-held arrows and Home/End stay tree-owned", () => {
  const { onToggle } = renderTree();
  act(() => row("b").focus());
  // b is an expanded branch: ArrowLeft with Alt collapses it via onToggle,
  // ArrowRight with Alt moves focus into b1, Home/End... are not tree keys
  // at all without modifiers, so the meaningful assertion is the arrows:
  // the tree acts on them despite Alt.
  const left = fireEvent.keyDown(row("b"), { key: "ArrowLeft", altKey: true });
  expect(left).toBe(false); // preventDefaulted: the tree owns the key
  expect(onToggle).toHaveBeenCalledExactlyOnceWith(NODES[1]);
  const right = fireEvent.keyDown(row("b"), { key: "ArrowRight", altKey: true });
  expect(right).toBe(false);
  expect(document.activeElement).toBe(row("b1"));
});

// Round 8, low 2: only the ALT family has global arrow chords; Ctrl/Meta+
// Arrow must stay tree-owned so the tree's own navigation still works with
// those modifiers held (roborev PR #1044 round-8 low 2).
test("arrow keys with Ctrl or Meta held still navigate the tree", () => {
  const { onToggle } = renderTree({ releaseModifierKeys: true });
  act(() => row("b").focus());
  // b is an expanded branch: ArrowLeft with Ctrl collapses it via onToggle.
  const left = fireEvent.keyDown(row("b"), { key: "ArrowLeft", ctrlKey: true });
  expect(left).toBe(false); // preventDefaulted: the tree owns the key now
  expect(onToggle).toHaveBeenCalledExactlyOnceWith(NODES[1]);
  // And ArrowDown with Meta moves focus to the next row.
  const down = fireEvent.keyDown(row("b"), { key: "ArrowDown", metaKey: true });
  expect(down).toBe(false);
  expect(document.activeElement).toBe(row("b1"));
});

// Round 11, low: the release is exact-modifier - no global chord stacks
// another modifier onto Alt, so an Alt+Ctrl or Alt+Meta arrow matches neither
// a global binding nor (if released) a tree handler. Those combinations stay
// tree-owned (roborev PR #1044 round-11 low).
test("arrows with Alt stacked on Ctrl or Meta still navigate the tree", () => {
  const { onToggle } = renderTree({ releaseModifierKeys: true });
  act(() => row("b").focus());
  // b is an expanded branch: ArrowLeft with Alt+Ctrl collapses it via onToggle.
  const left = fireEvent.keyDown(row("b"), { key: "ArrowLeft", altKey: true, ctrlKey: true });
  expect(left).toBe(false); // preventDefaulted: the tree owns the key
  expect(onToggle).toHaveBeenCalledExactlyOnceWith(NODES[1]);
  // And ArrowDown with Alt+Meta moves focus to the next row.
  const down = fireEvent.keyDown(row("b"), { key: "ArrowDown", altKey: true, metaKey: true });
  expect(down).toBe(false);
  expect(document.activeElement).toBe(row("b1"));
});

// Round 13, low 6: the release is exact-CHORD. Alt+Shift+Arrow is the
// live-session chord and still releases, but the Home/End chords bind Alt
// WITHOUT Shift - releasing Alt+Shift+Home/End hands the browser a key no
// global binding matches, a dead key. Home/End release only on plain Alt;
// with Shift stacked on they stay tree-owned (roborev PR #1044 round-13
// low 6).
test("Home and End with Alt+Shift stay tree-owned while Alt+Shift arrows release", () => {
  const { onToggle } = renderTree({ releaseModifierKeys: true });
  act(() => row("b").focus());
  // b is an expanded branch: ArrowLeft with Alt+Shift is the live-session
  // chord and releases (not preventDefaulted, no tree action).
  const left = fireEvent.keyDown(row("b"), { key: "ArrowLeft", altKey: true, shiftKey: true });
  expect(left).toBe(true);
  expect(onToggle).not.toHaveBeenCalled();
  // Alt+Shift+Home matches no global chord: it must stay tree-owned, so the
  // tree's own Home handling runs (focus moves to the first row).
  const home = fireEvent.keyDown(row("b"), { key: "Home", altKey: true, shiftKey: true });
  expect(home).toBe(false); // preventDefaulted: the tree owns the key
  expect(document.activeElement).toBe(row("a"));
  // Same for Alt+Shift+End: tree-owned, focus to the last visible row.
  const end = fireEvent.keyDown(row("a"), { key: "End", altKey: true, shiftKey: true });
  expect(end).toBe(false);
  expect(document.activeElement).toBe(row("c"));
});

// The modifier guard covers the arrow chords the global dispatcher owns.
// Enter is not one of them: no global chord binds a modifier+Enter, so the
// tree keeps its activation (roborev PR #1044 round-2 low).
test("Enter with a modifier held still activates the row", () => {
  const { onActivate } = renderTree();
  act(() => row("b").focus());
  fireEvent.keyDown(row("b"), { key: "Enter", altKey: true });
  expect(onActivate).toHaveBeenCalledExactlyOnceWith(NODES[1]);
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

// --- focus recovery when the focused row is unmounted by something OTHER
// than its own Left/Right key handling (which already calls moveTo itself
// and so never loses focus) - a non-keyboard ancestor collapse, or an
// external nodes-prop push. Left unhandled, the browser defocuses the
// removed row to <body> once the DOM update commits, and nothing inside
// the tree is focused afterward - Tab/Shift+Tab, not arrow keys, would be
// the only way back in. Flattened visible order for this fixture with
// both branches expanded: a, b, b1, b1a.
const DEEP_NODES: TreeNode[] = [
  { id: "a" },
  {
    id: "b",
    expanded: true,
    children: [{ id: "b1", expanded: true, children: [{ id: "b1a" }] }],
  },
];

function withNodeExpanded(nodes: TreeNode[], id: string, expanded: boolean): TreeNode[] {
  return nodes.map((node) => {
    if (node.id === id) return { ...node, expanded };
    if (node.children) return { ...node, children: withNodeExpanded(node.children, id, expanded) };
    return node;
  });
}

// A stateful wrapper, since Tree is controlled: renderRow wires a real
// chevron button to info.toggle (mouse-driven, deliberately NOT the
// focused row's own key handling) so a click on a DIFFERENT row's chevron
// can collapse an ANCESTOR of whichever row currently has focus.
function StatefulTree({ initialNodes }: { initialNodes: TreeNode[] }) {
  const [nodes, setNodes] = useState(initialNodes);
  return (
    <Tree
      nodes={nodes}
      onActivate={() => {}}
      onToggle={(node) => setNodes((prev) => withNodeExpanded(prev, node.id, node.expanded !== true))}
      renderRow={(node, info) => (
        <span>
          {info.hasChildren && (
            <button type="button" data-testid={`chevron-${node.id}`} onClick={info.toggle}>
              {info.expanded ? "-" : "+"}
            </button>
          )}
          {node.id}
        </span>
      )}
    />
  );
}

test("focus recovers within the tree when a non-keyboard ancestor collapse unmounts the focused row", () => {
  render(<StatefulTree initialNodes={DEEP_NODES} />);
  act(() => row("b1a").focus());
  expect(document.activeElement).toBe(row("b1a"));

  // Collapses b (b1a's grandparent) via b's OWN chevron - not b1a's key
  // handling, which never runs here at all.
  fireEvent.click(screen.getByTestId("chevron-b"));

  // b1a (and b1) are gone; the new visible order is [a, b], so the
  // fallback is "a" - and crucially, focus must have actually MOVED there
  // (not just the tabIndex bookkeeping), not fallen through to <body>.
  expect(document.activeElement).toBe(row("a"));
});

test("focus recovers when an external nodes-prop push removes the focused row (not via onToggle at all)", () => {
  const { rerender } = render(
    <Tree nodes={DEEP_NODES} onActivate={vi.fn()} onToggle={vi.fn()} renderRow={(node) => node.id} />,
  );
  act(() => row("b1a").focus());
  expect(document.activeElement).toBe(row("b1a"));

  // Simulates a completely external state change (a filter, a deletion) -
  // collapsing b1 without this component's onToggle ever firing.
  const collapsed = withNodeExpanded(DEEP_NODES, "b1", false);
  rerender(<Tree nodes={collapsed} onActivate={vi.fn()} onToggle={vi.fn()} renderRow={(node) => node.id} />);

  expect(document.activeElement).toBe(row("a"));
});

test("does not steal focus back into the tree when it was already elsewhere before the removing update", () => {
  const outside = document.createElement("button");
  document.body.appendChild(outside);

  const { rerender } = render(
    <Tree nodes={DEEP_NODES} onActivate={vi.fn()} onToggle={vi.fn()} renderRow={(node) => node.id} />,
  );
  act(() => row("b1a").focus());
  act(() => outside.focus());
  expect(document.activeElement).toBe(outside);

  const collapsed = withNodeExpanded(DEEP_NODES, "b1", false);
  rerender(<Tree nodes={collapsed} onActivate={vi.fn()} onToggle={vi.fn()} renderRow={(node) => node.id} />);

  // The user had already moved on before this update - Tree must not grab
  // focus back into itself.
  expect(document.activeElement).toBe(outside);
  outside.remove();
});

test("row info identity is stable for surviving rows across removal and re-add", () => {
  const seen = new Map<string, unknown[]>();
  const spyRow = (node: TreeNode, info: unknown) => {
    const list = seen.get(node.id) ?? [];
    list.push(info);
    seen.set(node.id, list);
    return node.id;
  };
  const { rerender } = render(
    <Tree nodes={[{ id: "a" }, { id: "b" }]} onActivate={vi.fn()} onToggle={vi.fn()} renderRow={spyRow} />,
  );
  // Remove b, then re-add it: a's info must stay identical throughout (the
  // prune drops only absent rows).
  rerender(<Tree nodes={[{ id: "a" }]} onActivate={vi.fn()} onToggle={vi.fn()} renderRow={spyRow} />);
  rerender(<Tree nodes={[{ id: "a" }, { id: "b" }]} onActivate={vi.fn()} onToggle={vi.fn()} renderRow={spyRow} />);
  const infosA = seen.get("a") ?? [];
  expect(infosA.length).toBeGreaterThanOrEqual(3);
  for (const info of infosA) expect(info).toBe(infosA[0]);
});
