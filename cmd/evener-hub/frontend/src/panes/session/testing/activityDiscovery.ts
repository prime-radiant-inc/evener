import { act } from "@testing-library/react";
import { connectionStore } from "../../../stores/connection";
import { acquireSessionActivity, sessionActivitySnapshot } from "../../../stores/sessionActivity";

// Keep the actual committed summary publication within the caller's act scope.
export async function settleActivityDiscovery(ref: string): Promise<void> {
  await act(async () => {
    const client = connectionStore.getState().client;
    if (!client || !sessionActivitySnapshot(client, ref, "session")?.summaryState.loading) return;
    const binding = acquireSessionActivity(client, ref);
    try {
      await new Promise<void>((resolve) => {
        const stop = binding.store.subscribe(() => {
          if (binding.store.getSnapshot().summaryState.loading) return;
          stop();
          resolve();
        });
      });
    } finally {
      binding.release();
    }
  });
}
