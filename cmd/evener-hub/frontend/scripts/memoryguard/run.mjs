#!/usr/bin/env node
// memoryguard - drives the REAL memory-refresh renderer in headless Chrome.
//
// WHAT IT PROVES THAT jsdom CANNOT: jsdom runs no cascade (every box measures
// zero) and does not perform the browser's native <summary> Enter/Space
// activation. This guard mounts the production path - hydrateThread ->
// projectThread -> TurnBlock -> the memory renderer - at a phone (390px) and a
// desktop (1400px) width, then checks:
//
//   1. the refresh is collapsed at both widths, with the exact heading
//      "Refreshed my memory" and no model-facing envelope in the collapsed row;
//   2. a trusted CDP pointer click opens it to the scope/state line and the
//      decoded, formatted index (never the escaped envelope);
//   3. the folded Source keeps the complete original Text verbatim, including
//      the task-list markers the Markdown sanitizer strips;
//   4. a real trusted keyboard activation (Space on the native summary) opens
//      it, and a second closes it;
//   5. the pane and page never scroll sideways - the long-token fixture would
//      escape if the body did not wrap;
//   6. the Full preset does not auto-open it.
//
// Deterministic: no hub, no credentials, no shared dev server. Follows the
// sibling-guard lifecycle (mermaidguard): startBrowserGuard, wait for Vite,
// connect over CDP, navigate with a boot check, evaluate, clean up.
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  applyViewport,
  clearViewportOverride,
  connectOnlyPage,
  createStartupDeadline,
  evaluate,
  navigateTo,
  waitForFonts,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const WIDTHS = [390, 1400];
const BOOT = {
  bootExpression: "typeof window.memoryGuard !== 'undefined'",
  bootLabel: "the memoryguard entry global window.memoryGuard",
};

// Drive Space as a trusted input event on the focused native <summary>: it is
// the activation key this guard exercises (jsdom runs no native activation at
// all).
async function dispatchActivationKey(send) {
  for (const type of ["keyDown", "keyUp"]) {
    await send("Input.dispatchKeyEvent", {
      type,
      key: " ",
      code: "Space",
      windowsVirtualKeyCode: 32,
    });
  }
  await evaluate(send, "new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
}

// A trusted pointer activation: real Input.dispatchMouseEvent press/release at
// the summary's own painted center, not a scripted element.click().
async function trustedClick(send) {
  const point = JSON.parse(await evaluate(send, "JSON.stringify(window.memoryGuard.summaryPoint())"));
  await send("Input.dispatchMouseEvent", {
    type: "mousePressed",
    button: "left",
    clickCount: 1,
    x: point.x,
    y: point.y,
  });
  await send("Input.dispatchMouseEvent", {
    type: "mouseReleased",
    button: "left",
    clickCount: 1,
    x: point.x,
    y: point.y,
  });
  await evaluate(send, "new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
}

// The open disclosure's chevron transition paints a rotated box mid-flight;
// measure at rest or the row reports a transient escape that no reader sees.
async function settleAnimations(send) {
  await evaluate(
    send,
    "Promise.all(document.getAnimations().filter((a) => a.effect?.getTiming().iterations !== Number.POSITIVE_INFINITY).map((a) => a.finished.catch(() => undefined))).then(() => true)",
  );
}

async function probe(send) {
  return JSON.parse(await evaluate(send, "JSON.stringify(window.memoryGuard.probe())"));
}

function assertClosed(snapshot, label, failures) {
  if (!snapshot.found) failures.push(`${label}: the memory refresh never mounted`);
  if (snapshot.open) failures.push(`${label}: expected collapsed, got open`);
  if (snapshot.label !== "Refreshed my memory") {
    failures.push(`${label}: heading=${JSON.stringify(snapshot.label)}, expected "Refreshed my memory"`);
  }
  if ((snapshot.contentText ?? "").includes("Quoted index data:")) {
    failures.push(`${label}: the collapsed row leaked the model-facing envelope`);
  }
  if (snapshot.docScrollWidth > snapshot.docClientWidth + 1) {
    failures.push(
      `${label}: the page scrolls sideways (${snapshot.docScrollWidth}px in ${snapshot.docClientWidth}px)`,
    );
  }
  if (snapshot.detailsScrollWidth > snapshot.detailsClientWidth + 1) {
    failures.push(
      `${label}: the memory row scrolls sideways (${snapshot.detailsScrollWidth}px in ${snapshot.detailsClientWidth}px)`,
    );
  }
}

async function assertAt(cdpEndpoint, vitePort, width) {
  const failures = [];
  const page = await connectOnlyPage(cdpEndpoint);
  const { send } = page;
  const label = `${width}px`;
  try {
    await applyViewport(send, { width, height: 900, mobile: width < 900 });
    await navigateTo(page, `http://127.0.0.1:${vitePort}/memoryguard.html?w=${width}`, BOOT);
    await evaluate(send, "window.memoryGuard.settled");
    await waitForFonts(send);

    assertClosed(await probe(send), `${label} closed`, failures);

    // A trusted pointer click opens the decoded, formatted body.
    await trustedClick(send);
    await settleAnimations(send);
    const opened = await probe(send);
    if (!opened.open) failures.push(`${label}: a pointer click did not open the refresh`);
    if (!(opened.meta ?? "").includes("Personal memory · current")) {
      failures.push(`${label}: scope/state line=${JSON.stringify(opened.meta)}`);
    }
    if (!(opened.contentText ?? "").includes("guard checked")) {
      failures.push(`${label}: the formatted body is missing the decoded index`);
    }
    if ((opened.contentText ?? "").includes("Quoted index data:")) {
      failures.push(`${label}: the formatted body rendered the escaped envelope`);
    }
    if (!(opened.sourceText ?? "").includes("- [x] guard checked")) {
      failures.push(`${label}: the Source lost a checked task marker`);
    }
    if (!(opened.sourceText ?? "").includes("- [ ] guard unchecked")) {
      failures.push(`${label}: the Source lost an unchecked task marker`);
    }
    if (!(opened.sourceText ?? "").includes("Quoted index data:")) {
      failures.push(`${label}: the Source is not the complete original Text`);
    }
    if (opened.detailsScrollWidth > opened.detailsClientWidth + 1) {
      failures.push(
        `${label}: the OPEN memory row scrolls sideways (${opened.detailsScrollWidth}px in ${opened.detailsClientWidth}px)`,
      );
    }

    // A second trusted click closes it again.
    await trustedClick(send);
    if (await evaluate(send, "window.memoryGuard.isOpen()")) failures.push(`${label}: a second click did not close`);

    // Real trusted keyboard activation: focus the native summary, press Space.
    if (!(await evaluate(send, "window.memoryGuard.focusSummary()"))) {
      failures.push(`${label}: the summary did not take focus`);
    }
    await dispatchActivationKey(send);
    if (!(await evaluate(send, "window.memoryGuard.isOpen()"))) {
      failures.push(`${label}: Space did not open the refresh`);
    }
    await dispatchActivationKey(send);
    if (await evaluate(send, "window.memoryGuard.isOpen()")) {
      failures.push(`${label}: a second Space did not close`);
    }
  } finally {
    await clearViewportOverride(send);
    page.close();
  }
  return failures;
}

async function assertFullDoesNotAutoOpen(cdpEndpoint, vitePort) {
  const failures = [];
  const page = await connectOnlyPage(cdpEndpoint);
  const { send } = page;
  try {
    await applyViewport(send, { width: 1024, height: 900 });
    await navigateTo(page, `http://127.0.0.1:${vitePort}/memoryguard.html?w=1024&level=full`, BOOT);
    await evaluate(send, "window.memoryGuard.settled");
    await waitForFonts(send);
    if (await evaluate(send, "window.memoryGuard.isOpen()")) {
      failures.push("full preset: the refresh auto-opened under the Full expansion baseline");
    }
  } finally {
    await clearViewportOverride(send);
    page.close();
  }
  return failures;
}

async function main() {
  let guard;
  try {
    guard = await startBrowserGuard({ frontend: FRONTEND, profilePrefix: "memoryguard-chrome-" });
  } catch (error) {
    throw new Error(describeBrowserStartupFailure({ error, subsystem: "launch" }));
  }
  const { vitePort, cleanup } = guard;
  let cdpEndpoint;
  let failed = 0;

  try {
    const viteDeadline = createStartupDeadline();
    try {
      await waitForHttp(`http://127.0.0.1:${vitePort}/memoryguard.html`, "vite dev server", guard.getViteLaunchError, {
        signal: viteDeadline.signal,
      });
    } catch (error) {
      throw new Error(describeBrowserStartupFailure({ error, subsystem: "vite", viteStderr: guard.getViteError() }));
    } finally {
      viteDeadline.clear();
    }
    cdpEndpoint = await waitForBrowserReady(guard);

    for (const width of WIDTHS) {
      const failures = await assertAt(cdpEndpoint, vitePort, width);
      if (failures.length > 0) {
        failed++;
        for (const failure of failures) console.error(`memoryguard FAIL: ${failure}`);
      } else {
        console.log(
          `memoryguard ok: ${width}px closed/open geometry, pointer + keyboard, literal Source, no overflow`,
        );
      }
    }

    const fullFailures = await assertFullDoesNotAutoOpen(cdpEndpoint, vitePort);
    if (fullFailures.length > 0) {
      failed++;
      for (const failure of fullFailures) console.error(`memoryguard FAIL: ${failure}`);
    } else {
      console.log("memoryguard ok: Full does not auto-open the refresh");
    }
  } finally {
    await cleanup();
  }
  return failed > 0 ? 1 : 0;
}

main().then(
  (status) => {
    if (process.exitCode === undefined) process.exitCode = status;
  },
  (error) => {
    console.error(error instanceof Error ? error.message : String(error));
    if (process.exitCode === undefined) process.exitCode = 1;
  },
);
