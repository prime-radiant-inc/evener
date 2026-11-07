import { createContext, useContext } from "react";

/** The right pane's width on the Duo split, or null in a full-screen screen.
 * Header pieces that size themselves from the window read this instead. */
export const DetailPaneWidthContext = createContext<number | null>(null);

export function useDetailPaneWidth(): number | null {
	return useContext(DetailPaneWidthContext);
}
