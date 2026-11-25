import { createContext, type ReactNode, useContext } from "react";

// Hands StackHost's tablet Board toggle into the rail slot without creating an
// eager import edge from StackHost or the lazy rail barrel into Rail.tsx.
const RailHeaderActionContext = createContext<ReactNode>(null);

export const RailHeaderActionProvider = RailHeaderActionContext.Provider;

export function useRailHeaderAction(): ReactNode {
  return useContext(RailHeaderActionContext);
}
