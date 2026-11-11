// ModalFrame already wraps its content in a SafeAreaView, so a page inside
// one must not apply the same horizontal safe-area insets a second time.
// GroupedPage reads this flag; pages outside a modal frame get the insets.
import { createContext, useContext } from "react";

export const ModalFrameInsetContext = createContext(false);

export function useModalFrameAppliesInsets(): boolean {
	return useContext(ModalFrameInsetContext);
}
