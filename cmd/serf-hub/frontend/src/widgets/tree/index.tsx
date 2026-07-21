import { useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { requireClass } from "../internal/requireClass";
import styles from "./tree.module.css";

export interface TreeNode {
  id: string;
  children?: TreeNode[];
  /** Controlled: the caller owns expand/collapse state and passes it back
   * in via this field, reacting to onToggle. Omitted/false = collapsed. */
  expanded?: boolean;
}

export interface TreeRowInfo {
  depth: number;
  expanded: boolean;
  hasChildren: boolean;
  toggle: () => void;
  activate: () => void;
}

export interface TreeProps<T extends TreeNode = TreeNode> {
  nodes: T[];
  onActivate: (node: T) => void;
  onToggle: (node: T) => void;
  renderRow: (node: T, info: TreeRowInfo) => ReactNode;
}

const CLASS = {
  tree: requireClass(styles.tree, "tree.module.css", "tree"),
  row: requireClass(styles.row, "tree.module.css", "row"),
  group: requireClass(styles.group, "tree.module.css", "group"),
};

interface FlatEntry<T extends TreeNode> {
  node: T;
  depth: number;
  parent: T | null;
}

function hasChildrenOf(node: TreeNode): boolean {
  return node.children !== undefined && node.children.length > 0;
}

// Depth-first, skipping the children of any node that isn't expanded - a
// collapsed branch's descendants are never in this list (or the DOM),
// not just visually hidden. Used for keyboard math (next/previous/parent
// visible row); the render below walks `nodes`/`node.children` directly
// instead, since correct role=group nesting isn't recoverable from a flat
// list without reconstructing the tree shape it already has.
function flattenVisible<T extends TreeNode>(nodes: T[], depth = 0, parent: T | null = null): FlatEntry<T>[] {
  const out: FlatEntry<T>[] = [];
  for (const node of nodes) {
    out.push({ node, depth, parent });
    if (node.expanded === true && hasChildrenOf(node)) {
      out.push(...flattenVisible(node.children as T[], depth + 1, node));
    }
  }
  return out;
}

/**
 * A keyboard-navigable tree: roving tabindex across whichever rows are
 * currently visible, role=tree/treeitem/group, aria-expanded/aria-level.
 * `renderRow` owns each row's visible content (icon, label, whatever);
 * Tree owns structure, ARIA, and the keyboard path - Up/Down move,
 * Right expands a closed branch or steps into an open one's first child,
 * Left collapses an open branch or steps to the parent, Enter activates
 * (see https://www.w3.org/WAI/ARIA/apg/patterns/treeview/ - this is that
 * pattern's key bindings).
 *
 * Tree never wires up mouse/click behavior of its own: renderRow's `info`
 * exposes `toggle`/`activate` so consumer content (a chevron, the row
 * label, ...) can call them explicitly. A blanket "click anywhere on the
 * row activates it" would double-fire alongside a consumer's own chevron
 * click without every consumer remembering to stopPropagation, so this
 * leaves that choice to renderRow instead of guessing at it.
 *
 * Expand/collapse state is fully controlled by the caller (`node.expanded`
 * in, `onToggle` out) - Tree holds only the transient "which row is
 * currently the roving-tabindex target" state, the same way a native
 * `<select>`'s highlighted option isn't state the parent owns.
 */
export function Tree<T extends TreeNode = TreeNode>({ nodes, onActivate, onToggle, renderRow }: TreeProps<T>) {
  const flat = flattenVisible(nodes);
  const indexById = new Map(flat.map((entry, i) => [entry.node.id, i]));
  const rowRefs = useRef(new Map<string, HTMLDivElement>());
  const treeRef = useRef<HTMLDivElement>(null);
  const pendingRefocusRef = useRef<string | null>(null);

  const [currentId, setCurrentId] = useState<string | null>(flat[0]?.node.id ?? null);
  const effectiveCurrentId = currentId !== null && indexById.has(currentId) ? currentId : (flat[0]?.node.id ?? null);

  // Focus, not this function, is the source of truth for `currentId` (see
  // each row's onFocus below) - moving focus is enough; the row's own
  // focus event updates the roving-tabindex bookkeeping as a side effect,
  // the same way it would if focus reached that row by any other means
  // (a click, or a browser/AT-driven focus this component didn't initiate).
  function moveTo(id: string) {
    rowRefs.current.get(id)?.focus();
  }

  // Recovers real focus when the row `currentId` was tracking just fell
  // out of the visible set for a reason OTHER than this row's own
  // Left/Right handling above (which calls moveTo itself and so never
  // loses focus): an ancestor collapsed via a DIFFERENT row's chevron, or
  // the caller pushed new `nodes` that drop it entirely. Left alone, the
  // browser defocuses the removed row to <body> once the DOM update
  // commits, and nothing inside the tree is focused afterward - Tab, not
  // an arrow key, would be the only way back in.
  //
  // The check runs here, in the render body, because this is the one
  // moment "was focus actually inside the tree right before this update"
  // is answerable at all: React only touches the real DOM at commit,
  // after render returns, so `document.activeElement` here still reflects
  // the PREVIOUS commit (the about-to-be-removed row, if it was focused).
  // Only a ref write happens now (no real side effect during render);
  // actually moving focus happens in the layout effect below, once the
  // removal has actually committed and the fallback row's DOM node
  // exists. If focus was already somewhere else entirely (the user
  // clicked away before this update, unrelated to it), this correctly
  // does nothing - Tree must never steal focus back into itself.
  if (
    currentId !== null &&
    !indexById.has(currentId) &&
    effectiveCurrentId !== null &&
    treeRef.current?.contains(document.activeElement)
  ) {
    pendingRefocusRef.current = effectiveCurrentId;
  }

  useLayoutEffect(() => {
    if (pendingRefocusRef.current === null) return;
    const id = pendingRefocusRef.current;
    pendingRefocusRef.current = null;
    rowRefs.current.get(id)?.focus();
  });

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>, node: T, parent: T | null) {
    const index = indexById.get(node.id)!;
    const branchOpen = hasChildrenOf(node) && node.expanded === true;
    const branchClosed = hasChildrenOf(node) && node.expanded !== true;

    switch (event.key) {
      case "ArrowDown": {
        event.preventDefault();
        const next = flat[index + 1];
        if (next) moveTo(next.node.id);
        break;
      }
      case "ArrowUp": {
        event.preventDefault();
        const prev = flat[index - 1];
        if (prev) moveTo(prev.node.id);
        break;
      }
      case "ArrowRight": {
        event.preventDefault();
        if (branchClosed) {
          onToggle(node);
        } else if (branchOpen) {
          moveTo((node.children as T[])[0]!.id);
        }
        break;
      }
      case "ArrowLeft": {
        event.preventDefault();
        if (branchOpen) {
          onToggle(node);
        } else if (parent) {
          moveTo(parent.id);
        }
        break;
      }
      case "Enter": {
        event.preventDefault();
        onActivate(node);
        break;
      }
      default:
        break;
    }
  }

  function renderEntries(list: T[], depth: number, parent: T | null): ReactNode[] {
    const out: ReactNode[] = [];
    for (const node of list) {
      const branchHasChildren = hasChildrenOf(node);
      const expanded = node.expanded === true;
      out.push(
        <div
          key={node.id}
          ref={(el) => {
            if (el) rowRefs.current.set(node.id, el);
            else rowRefs.current.delete(node.id);
          }}
          role="treeitem"
          aria-expanded={branchHasChildren ? expanded : undefined}
          aria-level={depth + 1}
          tabIndex={node.id === effectiveCurrentId ? 0 : -1}
          className={CLASS.row}
          onKeyDown={(event) => handleKeyDown(event, node, parent)}
          onFocus={() => setCurrentId(node.id)}
        >
          {renderRow(node, {
            depth,
            expanded,
            hasChildren: branchHasChildren,
            toggle: () => onToggle(node),
            activate: () => onActivate(node),
          })}
        </div>,
      );
      if (branchHasChildren && expanded) {
        out.push(
          <div role="group" key={`${node.id}-group`} className={CLASS.group}>
            {renderEntries(node.children as T[], depth + 1, node)}
          </div>,
        );
      }
    }
    return out;
  }

  return (
    <div ref={treeRef} role="tree" className={CLASS.tree}>
      {renderEntries(nodes, 0, null)}
    </div>
  );
}
