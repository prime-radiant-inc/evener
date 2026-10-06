import { createContext, useContext } from "react";

export const PaneVisibilityContext = createContext(true);

export function usePaneVisible(): boolean {
  return useContext(PaneVisibilityContext);
}
