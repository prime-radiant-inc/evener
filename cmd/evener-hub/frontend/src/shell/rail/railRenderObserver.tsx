import { createContext, type ReactNode, useContext } from "react";

export type RailRenderCallback = (id: string) => void;
const Context = createContext<RailRenderCallback | null>(null);
export function RailRenderObserver({ value, children }: { value: RailRenderCallback | null; children: ReactNode }) {
  return <Context.Provider value={value}>{children}</Context.Provider>;
}
export const RailRenderObserverProvider = RailRenderObserver;
export function useRailRenderObserver(): RailRenderCallback | null {
  return useContext(Context);
}
