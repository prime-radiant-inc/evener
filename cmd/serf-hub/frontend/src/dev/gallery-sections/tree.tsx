import { useState } from "react";
import { Tree, type TreeNode } from "../../widgets/tree";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./tree.module.css";

interface SessionNode extends TreeNode {
  label: string;
  children?: SessionNode[];
}

const INITIAL_NODES: SessionNode[] = [
  {
    id: "workspace",
    label: "Workspace",
    expanded: true,
    children: [
      { id: "session-1", label: "Fix flaky test" },
      {
        id: "session-2",
        label: "Refactor auth",
        expanded: false,
        children: [
          { id: "session-2a", label: "extract token store" },
          { id: "session-2b", label: "add retry policy" },
        ],
      },
      { id: "session-3", label: "Docs pass" },
    ],
  },
  {
    id: "archived",
    label: "Archived",
    expanded: false,
    children: [{ id: "old-1", label: "old thing" }],
  },
];

function withExpanded(nodes: SessionNode[], id: string, expanded: boolean): SessionNode[] {
  return nodes.map((node) => {
    if (node.id === id) return { ...node, expanded };
    if (node.children) return { ...node, children: withExpanded(node.children, id, expanded) };
    return node;
  });
}

// Interactive: the gallery is a design-review tool, but Tree is a
// controlled component (see its own doc comment), so demonstrating its
// keyboard path - the point of this widget - needs a real onToggle/
// onActivate loop, not just a static snapshot of one expand state.
function TreeDemo() {
  const [nodes, setNodes] = useState(INITIAL_NODES);
  const [lastActivated, setLastActivated] = useState<string | null>(null);

  return (
    <div>
      <Tree
        nodes={nodes}
        onToggle={(node) => setNodes((prev) => withExpanded(prev, node.id, node.expanded !== true))}
        onActivate={(node) => setLastActivated(node.label)}
        renderRow={(node, info) => (
          <span className={styles.row}>
            {info.hasChildren && (
              // Decorative mouse shortcut for the same action Right/Left
              // arrow already performs on the treeitem itself - hidden
              // from assistive tech (and out of tab order) so it isn't a
              // second, redundant "toggle" announcement.
              <button type="button" className={styles.chevron} aria-hidden="true" tabIndex={-1} onClick={info.toggle}>
                {info.expanded ? "▾" : "▸"}
              </button>
            )}
            <span className={styles.label} onClick={info.activate}>
              {node.label}
            </span>
          </span>
        )}
      />
      <p className={styles.status}>Last activated: {lastActivated ?? "(none yet)"}</p>
    </div>
  );
}

export default function TreeGallerySection() {
  return (
    <section>
      <h2>Tree</h2>
      <ThemeFlip>
        <div className={styles.frame}>
          <TreeDemo />
        </div>
      </ThemeFlip>
    </section>
  );
}
