// Real-geometry proof of the phone top bar's drawer placement: on the
// welcome pane the Sessions trigger sits at the visual leading edge beside a
// centered title (the bar row-reverses while the trigger keeps one mounted
// node), and a Back-owning pane keeps the trigger trailing. The source-text
// contract in StackHost.test.tsx cannot see the rendered box, so this guard
// measures it in the real shell.
import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { applyViewport, closePage, connectPage, evaluate, navigateTo, openPage } from "../browserGuardCdp.mjs";
import { startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";

const MOBILE_VIEWPORT = { width: 390, height: 844, mobile: true, touch: true };

async function wait(send, predicate) {
  await evaluate(
    send,
    `(async () => {
    const deadline = performance.now() + 15000;
    while (!(${predicate})) {
      if (performance.now() > deadline) throw new Error('top bar placement did not settle');
      await new Promise(resolve => requestAnimationFrame(resolve));
    }
  })()`,
  );
}

async function measureTopBar(send) {
  return evaluate(
    send,
    `(() => {
    const bar = document.querySelector('[data-testid="topbar"]');
    const sessions = bar?.querySelector('button[aria-label="Sessions"]');
    const title = bar?.querySelector('[data-testid="topbar-title"]');
    if (bar === null || sessions === null || title === null) return null;
    const sessionsRect = sessions.getBoundingClientRect();
    const titleRect = title.getBoundingClientRect();
    return {
      drawerLeads: bar.hasAttribute('data-drawer-leads'),
      sessions: { left: sessionsRect.left, right: sessionsRect.right },
      title: { left: titleRect.left, right: titleRect.right },
      barWidth: bar.getBoundingClientRect().width,
    };
  })()`,
  );
}

export async function checkTopBarDrawerPlacement(endpoint, vitePort) {
  const failures = [];
  const target = await openPage(endpoint, "about:blank");
  const page = await connectPage(endpoint, target.id);
  const { send } = page;
  try {
    await send("Page.addScriptToEvaluateOnNewDocument", { source: "try { localStorage.clear(); } catch {}" });
    await applyViewport(send, MOBILE_VIEWPORT);
    await navigateTo(page, `http://127.0.0.1:${vitePort}/shellguard.html`, {
      bootExpression: "typeof window.settledShell !== 'undefined'",
      bootLabel: "top bar placement fixture",
    });
    await evaluate(send, "window.settledShell");
    await wait(send, `document.querySelector('[data-testid="topbar-title"]')?.textContent === 'Welcome'`);
    // The welcome backstop auto-opens the Sessions drawer (the rail lives
    // inside it); a programmatic click on the trigger toggles it closed even
    // under the sheet's scrim, so the top bar's geometry is measurable.
    if (await evaluate(send, `!!document.querySelector('[role="dialog"][aria-label="Sessions"]')`)) {
      await evaluate(send, `document.querySelector('button[aria-label="Sessions"]').click()`);
      await wait(send, `!document.querySelector('[role="dialog"][aria-label="Sessions"]')`);
    }
    const welcome = await measureTopBar(send);
    console.log(`top bar welcome 390px: ${JSON.stringify(welcome)}`);
    try {
      assert.notEqual(welcome, null, "the welcome top bar renders its title and trigger");
      if (welcome === null) throw new Error("unreachable");
      assert.equal(welcome.drawerLeads, true, "the Back-less welcome bar marks the trigger to lead");
      assert.ok(
        welcome.sessions.right <= welcome.title.left + 0.5,
        `the Sessions trigger sits left of the title (${welcome.sessions.right} > ${welcome.title.left})`,
      );
      const titleCenter = (welcome.title.left + welcome.title.right) / 2;
      assert.ok(
        Math.abs(titleCenter - welcome.barWidth / 2) <= 0.5,
        `the Welcome title stays centered (${titleCenter} vs ${welcome.barWidth / 2})`,
      );
    } catch (error) {
      failures.push(`welcome: ${error.message}`);
    }
    await evaluate(
      send,
      `(async () => {
      const { navigate } = await import('/src/shell/routing.ts');
      navigate('/settings');
    })()`,
    );
    await wait(send, `!!document.querySelector('[data-testid="topbar"] button[aria-label="Back"]')`);
    const settings = await measureTopBar(send);
    console.log(`top bar settings 390px: ${JSON.stringify(settings)}`);
    try {
      assert.notEqual(settings, null, "the settings top bar renders its title and trigger");
      if (settings === null) throw new Error("unreachable");
      assert.equal(settings.drawerLeads, false, "a Back-owning pane keeps the bar in normal order");
      assert.ok(
        settings.sessions.left >= settings.title.right - 0.5,
        `the Sessions trigger stays right of the title behind Back (${settings.sessions.left} < ${settings.title.right})`,
      );
    } catch (error) {
      failures.push(`settings: ${error.message}`);
    }
  } catch (error) {
    failures.push(`top bar placement: ${error.stack}`);
  } finally {
    page.close();
    await closePage(endpoint, target.id);
  }
  return failures;
}

async function main() {
  const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
  const guard = await startBrowserGuard({
    frontend,
    profilePrefix: "top-bar-placement-chrome-",
    viteConfigFile: "scripts/shellguard/settingsGeometry.vite.config.mjs",
  });
  try {
    const endpoint = await waitForBrowserReady(guard);
    const failures = await checkTopBarDrawerPlacement(endpoint, guard.vitePort);
    for (const failure of failures) console.error(failure);
    if (failures.length) process.exitCode = 1;
  } finally {
    await guard.cleanup();
  }
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  });
}
