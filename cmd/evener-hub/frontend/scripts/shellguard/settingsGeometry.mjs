// Bounded XL settings geometry using real Settings, widgets and FakeClient.
// No server-backed preference writes or live hub connection are allowed.
import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { applyViewport, closePage, connectPage, evaluate, navigateTo, openPage, waitForFonts } from "../browserGuardCdp.mjs";
import { startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";

async function wait(send, predicate) {
  await evaluate(
    send,
    `(async () => {
    const deadline = performance.now() + 15000;
    while (!(${predicate})) {
      if (performance.now() > deadline) throw new Error('Settings geometry did not settle');
      await new Promise(resolve => requestAnimationFrame(resolve));
    }
  })()`,
  );
  await waitForFonts(send);
}

async function measureSearch(send) {
  return evaluate(
    send,
    `(() => {
    const input = document.querySelector('input[aria-label="Filter settings"]');
    const nav = input.closest('nav');
    input.focus();
    const field = input.getBoundingClientRect();
    const bounds = nav.getBoundingClientRect();
    const style = getComputedStyle(input);
    const ring = parseFloat(style.outlineWidth) + Math.max(0, parseFloat(style.outlineOffset));
    return {
      field: { left: field.left, right: field.right, width: field.width },
      nav: { left: bounds.left, right: bounds.right, width: bounds.width },
      scrollWidth: nav.scrollWidth, clientWidth: nav.clientWidth,
      focused: document.activeElement === input, ring, fontSize: style.fontSize,
    };
  })()`,
  );
}

async function measureDetails(send) {
  return evaluate(
    send,
    `Array.from(document.querySelectorAll('[data-testid^="transcript-display-controls-"]')).map(card => {
    const root = card.querySelector('section[aria-label="Transcript detail editor"]');
    const label = Array.from(root.querySelectorAll('label')).find(label => label.textContent.trim() === 'Transcript detail');
    const select = label ? document.getElementById(label.htmlFor) : null;
    if (select) {
      const rect = select.getBoundingClientRect();
      const style = getComputedStyle(select);
      const canvas = document.createElement('canvas');
      const context = canvas.getContext('2d');
      context.font = style.font;
      // Reserve the native arrow as well as the declared padding and borders.
      const textRoom = select.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight) - 24;
      return { card: card.dataset.testid, presentation: 'select', disabled: select.disabled,
        width: rect.width, height: rect.height, fontSize: style.fontSize, textRoom,
        labels: Array.from(select.options, option => ({ value: option.value,
          textWidth: context.measureText(option.textContent).width })),
        withinViewport: rect.left >= 0 && rect.right <= innerWidth };
    }
    const buttons = Array.from(root.querySelectorAll('[role="radio"]'));
    return { card: card.dataset.testid, presentation: 'segments', disabled: buttons.every(button => button.disabled),
      labels: buttons.map(button => {
        const label = button.querySelector('span');
        const range = document.createRange();
        range.selectNodeContents(label);
        const text = range.getBoundingClientRect();
        const rect = label.getBoundingClientRect();
        return { value: button.getAttribute('aria-label'), width: rect.width,
          textWidth: text.width, fontSize: getComputedStyle(label).fontSize,
          fits: text.left >= rect.left - 0.5 && text.right <= rect.right + 0.5 };
      }) };
  })`,
  );
}

export async function checkSettingsGeometry(endpoint, vitePort) {
  const failures = [];
  for (const viewport of [
    { width: 1400, height: 1000 },
    { width: 390, height: 844, mobile: true, touch: true },
  ]) {
    for (const theme of ["light", "dark"]) {
      const target = await openPage(endpoint, "about:blank");
      const page = await connectPage(endpoint, target.id);
      const { send } = page;
      const label = `${viewport.width}px XL ${theme}`;
      try {
        await send("Page.addScriptToEvaluateOnNewDocument", { source: "try { localStorage.clear(); } catch {}" });
        await applyViewport(send, viewport);
        await navigateTo(page, `http://127.0.0.1:${vitePort}/shellguard.html`, {
          bootExpression: "typeof window.settledShell !== 'undefined'",
          bootLabel: "settings fixture",
        });
        await evaluate(send, "window.settledShell");
        await evaluate(
          send,
          `(async () => {
          window.configureOverview(${JSON.stringify(theme)});
          const { navigate } = await import('/src/shell/routing.ts');
          navigate('/settings');
        })()`,
        );
        await wait(
          send,
          `document.querySelector('input[aria-label="Filter settings"]')?.getBoundingClientRect().width > 0`,
        );
        const search = await measureSearch(send);
        console.log(`settings search ${label}: ${JSON.stringify(search)}`);
        try {
          assert.equal(search.focused, true);
          assert.ok(search.ring > 0, "focused search must expose its focus ring");
          assert.ok(search.scrollWidth <= search.clientWidth + 1, "Settings nav must not scroll horizontally");
          assert.ok(search.field.left - search.ring >= search.nav.left - 0.5, "left focus ring must fit nav");
          assert.ok(
            search.field.right + search.ring <= search.nav.right + 0.5,
            "right border and focus ring must fit nav",
          );
        } catch (error) {
          failures.push(`search ${label}: ${error.message}`);
        }

        await evaluate(
          send,
          `(async () => {
          const { navigate } = await import('/src/shell/routing.ts');
          navigate('/settings/transcript');
        })()`,
        );
        for (const layout of ["desktop", "mobile"]) {
          await wait(send, `!!document.querySelector('[role="radio"][aria-label="Desktop"]')`);
          await evaluate(
            send,
            `document.querySelector('[role="radio"][aria-label="${layout === "desktop" ? "Desktop" : "Mobile"}"]').click()`,
          );
          await wait(send, `!!document.querySelector('[data-testid="transcript-display-controls-${layout}"]')`);
          const details = await measureDetails(send);
          console.log(`settings detail ${label} ${layout}: ${JSON.stringify(details)}`);
          try {
            assert.equal(details.length, 1, "the selected layout renders one card");
            for (const detail of details) {
              assert.equal(detail.disabled, true, "this fixture only claims unsupported/disabled synced defaults");
              assert.equal(detail.labels.length, 6);
              if (detail.presentation === "select") {
                assert.deepEqual(
                  detail.labels.map((option) => option.value),
                  ["chat", "intent", "tools", "activity", "full", "custom"],
                );
                assert.equal(detail.withinViewport, true);
                assert.ok(detail.height >= 44, "phone select retains tap floor");
                assert.ok(
                  detail.labels.every((option) => option.textWidth <= detail.textRoom),
                  "all complete option names must fit",
                );
              } else {
                assert.ok(
                  detail.labels.every((option) => option.fits),
                  "all preset labels must fit without ellipsis",
                );
              }
              if (viewport.width === 1400)
                assert.equal(detail.presentation, "segments", "desktop presentation stays unchanged");
            }
          } catch (error) {
            failures.push(`detail ${label} ${layout}: ${error.message}`);
          }
        }
        const safety = await evaluate(
          send,
          `({
          errors: window.__shellGuardErrors || [], calls: window.shellGuardCalls(),
          origins: [...new Set(performance.getEntriesByType('resource').map(entry => new URL(entry.name, location.href).origin))],
          origin: location.origin, pageWidth: document.documentElement.scrollWidth, viewport: innerWidth,
        })`,
        );
        assert.deepEqual(safety.errors, []);
        assert.ok(safety.calls.includes("evener/navigation/read"), "call witness includes actual fixture reads");
        assert.ok(
          !safety.calls.some((method) => /\/(set|update|save|patch)(?:$|\/)/.test(method)),
          "no remote preference mutations",
        );
        assert.deepEqual(safety.origins, [safety.origin], "fixture loads only its isolated Vite origin");
        assert.ok(safety.pageWidth <= safety.viewport, "no document-wide overflow");
      } catch (error) {
        failures.push(`${label}: ${error.stack}`);
      } finally {
        page.close();
        await closePage(endpoint, target.id);
      }
    }
  }
  return failures;
}

async function main() {
  const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
  const guard = await startBrowserGuard({
    frontend,
    profilePrefix: "settings-geometry-chrome-",
    viteConfigFile: "scripts/shellguard/settingsGeometry.vite.config.mjs",
  });
  try {
    const endpoint = await waitForBrowserReady(guard);
    const failures = await checkSettingsGeometry(endpoint, guard.vitePort);
    for (const failure of failures) console.error(failure);
    if (failures.length) process.exitCode = 1;
  } finally {
    await guard.cleanup();
  }
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error);
    process.exitCode = 1;
  });
}
