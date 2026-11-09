import { createContext, type ReactNode, useContext } from "react";

const MobileSheetDismissContext = createContext<(() => void) | null>(null);

// The sheet that hosts the rail owns dismissal; the rail calls this at the
// session press so a slow route or an unchanged focus cannot leave it open.
export function MobileSheetDismissProvider({ onDismiss, children }: { onDismiss: () => void; children: ReactNode }) {
  return <MobileSheetDismissContext.Provider value={onDismiss}>{children}</MobileSheetDismissContext.Provider>;
}

export function useMobileSheetDismiss(): (() => void) | null {
  return useContext(MobileSheetDismissContext);
}
