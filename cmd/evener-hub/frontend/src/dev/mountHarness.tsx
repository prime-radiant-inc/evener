// The standalone dev harnesses mount their own React tree instead of going
// through App.tsx; this is the one mount recipe they share.
import type { ReactNode } from "react";
import { createRoot } from "react-dom/client";
import "../styles/tokens.css";
import "../styles/global.css";

/** The page element every harness HTML mounts into, or a named failure. */
export function harnessRoot(harness: string): HTMLElement {
  const root = document.getElementById("root");
  if (root === null) throw new Error(`${harness}.html is missing #root`);
  return root;
}

/** Mounts `node` into the page's #root. */
export function mountHarness(harness: string, node: ReactNode): void {
  createRoot(harnessRoot(harness)).render(node);
}
