import { act } from "@testing-library/react";
import { activitySummaryStore } from "../../../stores/activitySummary";

// A live composer's inline SessionChrome starts activity discovery on mount,
// and its settle re-renders the chrome. Call this synchronously after render()
// so the act() scope is open before the discovery request can settle: that
// keeps the re-render inside act() instead of landing after a test that
// asserts straight off the mount. A mount that started no discovery returns
// at once.
export async function settleActivityDiscovery(ref: string): Promise<void> {
  await act(async () => {
    if (!activitySummaryStore.getState().entries.get(ref)?.loading) return;
    await new Promise<void>((resolve) => {
      const unsubscribe = activitySummaryStore.subscribe((state) => {
        if (state.entries.get(ref)?.loading) return;
        unsubscribe();
        resolve();
      });
    });
  });
}
