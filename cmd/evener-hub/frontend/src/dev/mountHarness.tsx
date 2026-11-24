// The standalone dev harnesses mount their own React tree instead of going
// through App.tsx; this is the one mount recipe they share.
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import "./harnessStyles";

/** The page element every harness HTML mounts into, or a named failure. */
export function harnessRoot(harness: string): HTMLElement {
  const root = document.getElementById("root");
  if (root === null) throw new Error(`${harness}.html is missing #root`);
  return root;
}

/** Mounts `node` into the page's #root and returns the root for a harness that
 *  unmounts later (transcriptscrollguard's pane). */
export function mountHarness(harness: string, node: ReactNode): Root {
  const root = createRoot(harnessRoot(harness));
  root.render(node);
  return root;
}
