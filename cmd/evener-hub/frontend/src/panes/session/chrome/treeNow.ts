// treeNow.ts holds the dense activity tree's ticking clock context. It lives
// apart from the tree so the detail strips (ActivityRowDetail) can subscribe
// to the same clock without importing the tree that renders them - the tree
// imports the details, so importing the context back out of ActivityTree would
// close a cycle.
//
// The provider is the tree's only setInterval; only the context consumers
// (the live meta cluster and the live detail strips) re-render on a tick, so a
// tick touches live leaves only.
import { createContext, useContext } from "react";

export const TreeNowContext = createContext<number>(0);

// useTreeNow reads the tree's ticking clock. A component that calls it
// re-renders once a second while the tree is live, so callers must be the
// smallest leaf that actually needs the clock.
export function useTreeNow(): number {
  return useContext(TreeNowContext);
}
