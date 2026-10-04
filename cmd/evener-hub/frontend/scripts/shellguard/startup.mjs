// Hold the real lazy DockHost request across the floating fixture's first
// workspace read. Capturing a Settings ID before restore retires that ID.
import assert from "node:assert/strict";
import { applyViewport, clearViewportOverride, closePage, connectPage, createStartupDeadline, evaluate, navigateTo, openPage, waitForFonts } from "../browserGuardCdp.mjs";
import { Driver } from "../skillguard/run.mjs";

export function startupCancellation(signal) {
  const aborted = new Promise((_, reject) => {
    signal.addEventListener("abort", () => reject(signal.reason), { once: true });
  });
  // Setup may outlast the deadline before reaching its first cancellation wait.
  // Observe that rejection now, while keeping the original promise rejected.
  aborted.catch(() => {});
  return aborted;
}

export async function measureDelayedFloatingDock(endpoint, url, boot, viewport, measureFloatingDock) {
  const target = await openPage(endpoint, "about:blank");
  const page = await connectPage(endpoint, target.id);
  const send = page.send;
  const driver = new Driver({});
  driver.page = page;
  const origin = new URL(url).origin;
  const deadline = createStartupDeadline();
  const aborted = startupCancellation(deadline.signal);
  let heldRequest;
  let measurement;
  const held = Promise.withResolvers();
  const handler = (event) => {
    const message = JSON.parse(event.data);
    if (message.method === "Fetch.requestPaused") {
      heldRequest = message.params.requestId;
      held.resolve();
    }
  };
  try {
    await applyViewport(send, viewport);
    await send("Storage.clearDataForOrigin", { origin, storageTypes: "local_storage" });
    await navigateTo(page, url, boot);
    await evaluate(send, "window.settledShell");
    await driver.waitPage(`(async () => {
      const { getDockviewApi } = await import('/src/shell/workspace.ts');
      return getDockviewApi() != null && localStorage.getItem('evener.workspace.layout.v2') != null;
    })()`, { label: "real workspace and saved startup layout" });

    page.ws.addEventListener("message", handler);
    await send("Network.setCacheDisabled", { cacheDisabled: true });
    await send("Fetch.enable", { patterns: [{ urlPattern: "*/src/shell/DockHost.tsx*", requestStage: "Request" }] });
    await navigateTo(page, url, boot);
    await Promise.race([held.promise, aborted]);
    await evaluate(send, "window.settledShell");
    await waitForFonts(send);

    // Observe completed reads on the real CDP channel without replacing any
    // workspace behavior. The first read must not open Settings while held.
    const firstRead = Promise.withResolvers();
    page.send = async (method, params) => {
      const result = await send(method, params);
      if (method === "Runtime.evaluate") firstRead.resolve();
      return result;
    };
    measurement = measureFloatingDock(page).then(
      (result) => ({ result }),
      (error) => ({ error }),
    );
    await Promise.race([firstRead.promise, aborted]);
    const before = await evaluate(send, `(async () => {
      const { workspaceStore, getDockviewApi } = await import('/src/shell/workspace.ts');
      return { ready: getDockviewApi() != null, types: workspaceStore.getState().panes.map(p => p.type),
        errors: window.__shellGuardErrors ?? [] };
    })()`);
    assert.equal(before.ready, false, "the real DockHost request must still be held");
    assert.deepEqual(before.errors, []);
    assert.deepEqual(before.types, ["welcome"], "floating fixture opens Settings before workspace restore completes");

    await send("Fetch.continueRequest", { requestId: heldRequest });
    heldRequest = null;
    await send("Fetch.disable");
    const outcome = await measurement;
    if (outcome.error) throw outcome.error;
    const after = await evaluate(send, "window.__shellGuardErrors ?? []");
    assert.deepEqual(after, []);
    return outcome.result;
  } finally {
    deadline.clear();
    page.send = send;
    if (heldRequest) await send("Fetch.continueRequest", { requestId: heldRequest });
    await send("Fetch.disable");
    page.ws.removeEventListener("message", handler);
    // Settle the actual fixture before closing its page, even when the
    // pre-restore assertion fails.
    if (measurement) await measurement;
    await clearViewportOverride(send);
    page.close();
    await closePage(endpoint, target.id);
  }
}
