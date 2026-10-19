// Explicit Chrome lane: node --test scripts/shellguard/overview-isolation.browser.mjs
import assert from "node:assert/strict";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { closePage, connectBrowser, connectPage, evaluate, navigateTo, openPage } from "../browserGuardCdp.mjs";
import { startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";
import { overviewOnPage } from "./run.mjs";

const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const viewport = { width: 390, height: 844, mobile: true, touch: true };
const key = "evener.activity-sidebar.v1";

test("Overview journeys isolate outgoing saved child views and dispose failed fixtures", async () => {
  const guard = await startBrowserGuard({ frontend, profilePrefix: "overview-isolation-" });
  let old;
  let browser;
  try {
    const endpoint = await waitForBrowserReady(guard);
    browser = await connectBrowser(endpoint);
    const target = await openPage(endpoint, "about:blank");
    old = await connectPage(endpoint, target.id);
    const origin = `http://127.0.0.1:${guard.vitePort}`;
    await navigateTo(old, `${origin}/shellguard.html`, {
      bootExpression: "typeof window.settledShell !== 'undefined'", bootLabel: "shell fixture",
    });
    await evaluate(old.send, "window.settledShell");
    const contexts = (await browser.send("Target.getBrowserContexts")).result.browserContextIds;
    await evaluate(old.send, `(async () => {
      const { activitySidebarStore: store } = await import('/src/shell/activitybar/activitySidebarStore.ts');
      store.getState().retarget('local:overview-child');
      store.getState().openWith('about');
      store.getState().close();
      // Queue actual trailing persistence. An origin-wide clear races with an
      // outgoing page's pagehide flush, just as closing fixture tabs can.
      store.getState().setCategoryView('local:overview-child', 'about', {
        anchor: { id: 'pending-departure', offset: 1 },
      });
      window.addEventListener('storage', event => {
        if (event.key === null) location.href = 'about:blank';
      }, { once: true });
    })()`);
    const { result, failures } = await overviewOnPage(endpoint, guard.vitePort, viewport, "dark", "close");
    // The outgoing fixture must really retain its closed About preference.
    // Isolation must not suppress its save to make the next journey pass.
    const items = await old.send("DOMStorage.getDOMStorageItems", {
      storageId: { securityOrigin: origin, isLocalStorage: true },
    });
    const saved = JSON.parse(items.result.entries.find(([name]) => name === key)?.[1] ?? "null");
    assert.equal(saved?.["local:overview-child"]?.open, false);
    assert.equal(saved?.["local:overview-child"]?.tab, "about");
    assert.equal(saved?.["local:overview-child"]?.categories.about.anchor.id, "pending-departure");
    assert.deepEqual(failures, []);
    assert.equal(result.keyboard.childReturn, "close");
    assert.deepEqual((await browser.send("Target.getBrowserContexts")).result.browserContextIds, contexts);

    const setupFailure = new Error("injected fixture setup failure");
    await assert.rejects(overviewOnPage(endpoint, guard.vitePort, viewport, "light", "escape", () => {
      throw setupFailure;
    }), error => error === setupFailure);
    assert.deepEqual((await browser.send("Target.getBrowserContexts")).result.browserContextIds, contexts,
      "failed fixture disposes its browser context");
    old.close();
    old = null;
    await closePage(endpoint, target.id);
  } finally {
    old?.close();
    browser?.close();
    await guard.cleanup();
  }
});
