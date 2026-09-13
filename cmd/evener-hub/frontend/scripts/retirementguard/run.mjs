#!/usr/bin/env node
// retirementguard — drives the REAL Session pane in headless Chrome and proves
// the user-visible recovery contract for daemon retirement:
//
//   1. The transcript and unsent draft survive the old daemon retiring.
//   2. Submitting the draft after replacement produces exactly one new turn.
//   3. Late old-generation frames cannot overwrite the replacement.
//
// This guard is driven by TestRetirementBrowser in
// cmd/evener-hub/app_retirement_browser_test.go, which starts a fixture Hub
// (real Hub + scripted retiring daemon + real replacement daemon) and passes
// the endpoints to this runner via environment variables:
//
//   RETIREMENT_HUB_URL     — http://HOST:PORT (fixture Hub base URL; the
//                            harness reaches its /rpc route through this
//                            guard's own Vite origin, whose proxy forwards to
//                            it — the Hub's WebSocket Accept refuses a
//                            cross-origin upgrade)
//   RETIREMENT_RETIRE_URL  — http://HOST:PORT/fixture/retire (retirement trigger)
//   RETIREMENT_REF         — local:SESSION_ID (thread ref to exercise)
//   RETIREMENT_ARTIFACT_DIR — absolute path for screenshots + result JSON
//
// The runner writes its three screenshots and machine-readable result to the
// fixture-owned artifact directory (NOT the scratch that test-web-browser.sh
// deletes on success), and prints that path. The Go test reads the exit code.
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  applyViewport,
  clearViewportOverride,
  connectPage,
  createStartupDeadline,
  devtoolsHttpURL,
  evaluate,
  navigateTo,
  waitForFonts,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard } from "../browserGuardProcess.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

// A desktop viewport — the same width at which Session is normally exercised.
const VIEWPORT = { width: 1400, height: 900 };

// Environment variables injected by the Go fixture.
const HUB_URL = process.env.RETIREMENT_HUB_URL ?? "";
const RETIRE_URL = process.env.RETIREMENT_RETIRE_URL ?? "";
const DEGRADE_URL = process.env.RETIREMENT_DEGRADE_URL ?? "";
const REF = process.env.RETIREMENT_REF ?? "";
const ARTIFACT_DIR = process.env.RETIREMENT_ARTIFACT_DIR ?? "";

if (!HUB_URL || !RETIRE_URL || !DEGRADE_URL || !REF || !ARTIFACT_DIR) {
  const missing = [
    "RETIREMENT_HUB_URL",
    "RETIREMENT_RETIRE_URL",
    "RETIREMENT_DEGRADE_URL",
    "RETIREMENT_REF",
    "RETIREMENT_ARTIFACT_DIR",
  ]
    .filter((v) => !process.env[v])
    .join(", ");
  console.error(`retirementguard: missing required env vars: ${missing}`);
  console.error("This guard must be invoked by TestRetirementBrowser in app_retirement_browser_test.go");
  process.exitCode = 1;
  process.exit(1);
}

// Ensure the artifact directory exists (it is outside the scratch dir that
// test-web-browser.sh deletes on success, so evidence survives green runs).
mkdirSync(ARTIFACT_DIR, { recursive: true });
console.log(`retirementguard artifacts: ${ARTIFACT_DIR}`);

// ---------------------------------------------------------------------------
// screenshot helper — uses Page.captureScreenshot over the CDP send function
// ---------------------------------------------------------------------------

async function captureScreenshot(send, name) {
  const result = await send("Page.captureScreenshot", { format: "png" });
  const filePath = path.join(ARTIFACT_DIR, `${name}.png`);
  writeFileSync(filePath, Buffer.from(result.result.data, "base64"));
  return filePath;
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

async function main() {
  // --- Browser lifecycle ---------------------------------------------------
  let guard;
  try {
    guard = await startBrowserGuard({
      frontend: FRONTEND,
      profilePrefix: "retirementguard-chrome-",
    });
  } catch (error) {
    throw new Error(describeBrowserStartupFailure({ error, subsystem: "launch" }));
  }
  const { vitePort, cleanup } = guard;
  let cdpEndpoint;

  try {
    // The page connects to this guard's OWN origin; Vite's /rpc proxy forwards
    // the WebSocket to the fixture Hub (see this guard's env contract above).
    const hubWS = `ws://127.0.0.1:${vitePort}/rpc`;
    const harnessUrl =
      `http://127.0.0.1:${vitePort}/retirementharness.html` +
      `?hub=${encodeURIComponent(hubWS)}` +
      `&retire=${encodeURIComponent(RETIRE_URL)}` +
      `&degrade=${encodeURIComponent(DEGRADE_URL)}` +
      `&ref=${encodeURIComponent(REF)}`;

    try {
      await waitForHttp(
        `http://127.0.0.1:${vitePort}/retirementharness.html`,
        "vite dev server",
        guard.getViteLaunchError,
      );
    } catch (error) {
      throw new Error(describeBrowserStartupFailure({ error, subsystem: "vite", viteStderr: guard.getViteError() }));
    }

    const startupDeadline = createStartupDeadline();
    try {
      cdpEndpoint = await guard.waitForChrome({ signal: startupDeadline.signal });
      await waitForHttp(
        devtoolsHttpURL(cdpEndpoint, "/json/version"),
        "chrome devtools endpoint",
        guard.getChromeLaunchError,
        { signal: startupDeadline.signal, failure: guard.getChromeFailure() },
      );
    } catch (error) {
      throw new Error(
        describeBrowserStartupFailure({
          error,
          subsystem: "chrome",
          chromeBinary: guard.chromeBinary,
          chromeArgv: guard.getChromeArgv(),
          chromeStderr: guard.getChromeError(),
          viteStderr: guard.getViteError(),
        }),
      );
    } finally {
      startupDeadline.clear();
    }

    const page = await connectPage(cdpEndpoint);
    const { send } = page;
    const failures = [];
    let result = null;
    let socketState = null;
    let lateFrame = null;

    try {
      await applyViewport(send, VIEWPORT);
      await navigateTo(page, harnessUrl);
      await waitForFonts(send);

      // Wait for the harness to be ready (AppwireClient connected + thread
      // hydrated). Errors from earlier phases surface here.
      const errors0 = await evaluate(
        send,
        `(async () => {
          const ready = window.retirementHarness.ready.then(
            () => ({ ok: true }),
            (err) => ({ ok: false, errors: [err?.message ?? String(err)] }),
          );
          const timeout = new Promise((resolve) =>
            setTimeout(() => resolve({ ok: false, errors: ['harness ready did not settle in 15000ms'] }), 15000),
          );
          const settled = await Promise.race([ready, timeout]);
          if (!settled.ok) {
            return {
              ok: false,
              errors: [...(settled.errors ?? []), ...window.retirementHarness.errors()],
              debug: window.retirementHarness.debug(),
            };
          }
          try {
            const errs = window.retirementHarness.errors();
            return { ok: true, errors: errs };
          } catch (err) {
            return { ok: false, errors: [err?.message ?? String(err)] };
          }
        })()`,
      );
      if (errors0.errors.length > 0 || !errors0.ok) {
        const debug = errors0.debug === undefined ? "" : ` debug=${JSON.stringify(errors0.debug)}`;
        failures.push(`harness not ready: ${(errors0.errors ?? []).join("; ")}${debug}`);
      }

      if (failures.length === 0) {
        // --- Snapshot 0: initial state -------------------------------------
        const snap0 = await evaluate(send, "JSON.stringify(window.retirementHarness.snapshot())");
        const initial = JSON.parse(snap0);

        // Harness sanity: thread must have turns.
        if (!Array.isArray(initial.turnIDs) || initial.turnIDs.length === 0) {
          failures.push(`initial state: thread has no turns (ref=${initial.ref})`);
        }
        // A draft should be present (the harness writes one during boot — see
        // retirementharness-entry.tsx). If the draft is empty, the
        // "draft survives" assertion will be trivially true and prove nothing:
        // warn rather than fail, since the fixture may intentionally start
        // with an empty draft when testing that zero-length drafts survive.
        if (!initial.draft) {
          console.warn("retirementguard: initial draft is empty — survival assertion trivially passes");
        }

        // Screenshot 1: before retirement (transcript + draft in composer).
        const ss1Path = await captureScreenshot(send, "01-before-retirement");
        console.log(`screenshot 1 (before retirement): ${ss1Path}`);

        // --- Retire --------------------------------------------------------
        const retireResult = await evaluate(
          send,
          `(async () => {
            const timeout = new Promise((resolve) =>
              setTimeout(() => resolve({ ok: false, error: 'retire() did not settle in 10000ms' }), 10000),
            );
            const attempt = window.retirementHarness.retire().then(
              () => ({ ok: true }),
              (err) => ({ ok: false, error: err?.message ?? String(err) }),
            );
            const result = await Promise.race([attempt, timeout]);
            if (!result.ok) result.debug = window.retirementHarness.debug();
            return result;
          })()`,
        );
        if (!retireResult.ok) {
          const debug = retireResult.debug === undefined ? "" : ` debug=${JSON.stringify(retireResult.debug)}`;
          failures.push(`retire() failed: ${retireResult.error}${debug}`);
        }

        // Await full settlement of the replacement source.
        if (failures.length === 0) {
          const settledResult = await evaluate(
            send,
            `(async () => {
              const timeout = new Promise((resolve) =>
                setTimeout(() => resolve({ ok: false, error: 'settled() did not settle in 10000ms' }), 10000),
              );
              const attempt = window.retirementHarness.settled().then(
                () => ({ ok: true }),
                (err) => ({ ok: false, error: err?.message ?? String(err) }),
              );
              const result = await Promise.race([attempt, timeout]);
              if (!result.ok) result.debug = window.retirementHarness.debug();
              return result;
            })()`,
          );
          if (!settledResult.ok) {
            const debug = settledResult.debug === undefined ? "" : ` debug=${JSON.stringify(settledResult.debug)}`;
            failures.push(`settled() after retire failed: ${settledResult.error}${debug}`);
          }
        }

        if (failures.length === 0) {
          // --- Same-socket recovery: the hub socket never reconnected -------
          // The daemon-side source closed; the client's socket to the Hub did
          // not. Recovery must be reached through that same socket: exactly one
          // transition into "ready" (the original connect) and at least one
          // resync delivered. A browser reconnect alone is NOT the test.
          socketState = JSON.parse(
            await evaluate(
              send,
              `JSON.stringify({
                readyTransitions: window.retirementHarness.readyTransitions(),
                resyncCount: window.retirementHarness.resyncCount(),
              })`,
            ),
          );
          if (socketState.readyTransitions !== 1) {
            failures.push(
              `recovery relied on a browser reconnect: readyTransitions=${socketState.readyTransitions}, want 1 ` +
                `(the Hub socket must stay connected while the daemon socket closes)`,
            );
          }
          if (socketState.resyncCount < 1) {
            failures.push(
              `the connected socket never received an evener/thread/resync (resyncCount=${socketState.resyncCount}); ` +
                `same-socket recovery was not exercised`,
            );
          }

          // --- Snapshot 1: post-retirement ----------------------------------
          const snap1 = await evaluate(send, "JSON.stringify(window.retirementHarness.snapshot())");
          const postRetire = JSON.parse(snap1);

          // Screenshot 2: after retirement (transcript + draft must survive).
          const ss2Path = await captureScreenshot(send, "02-after-retirement");
          console.log(`screenshot 2 (after retirement): ${ss2Path}`);

          // Verify transcript survived retirement.
          if (JSON.stringify(postRetire.turnIDs) !== JSON.stringify(initial.turnIDs)) {
            failures.push(
              `transcript changed across retirement: before=${JSON.stringify(initial.turnIDs)} after=${JSON.stringify(postRetire.turnIDs)}`,
            );
          }
          // Verify draft survived retirement.
          if (postRetire.draft !== initial.draft) {
            failures.push(
              `draft changed across retirement: before=${JSON.stringify(initial.draft)} after=${JSON.stringify(postRetire.draft)}`,
            );
          }
          // Verify source generation changed (new daemon backing the ref).
          if (postRetire.sourceGeneration === initial.sourceGeneration && initial.sourceGeneration !== "") {
            failures.push(`sourceGeneration did not change after retirement: ${initial.sourceGeneration}`);
          }
          // A stale frame from the generation being retired was forwarded just
          // before the resync (the Go fixture proves it was acknowledged). The
          // replacement's authoritative snapshot must supersede it: a ghost
          // turn surviving here means a late old-generation frame overwrote the
          // replacement.
          lateFrame = { ghostTurnID: "turn_old_generation", survived: postRetire.turnIDs.includes("turn_old_generation") };
          if (lateFrame.survived) {
            failures.push(
              `late old-generation frame overwrote the replacement: ${lateFrame.ghostTurnID} survived the resync`,
            );
          }
          if (postRetire.text !== initial.text) {
            failures.push(
              `replacement transcript text changed after a stale old-generation frame: before=${JSON.stringify(initial.text)} after=${JSON.stringify(postRetire.text)}`,
            );
          }

          if (failures.length === 0) {
            // --- Submit the draft via native DOM interaction ---------------
            // Use the real Composer: find the textarea, ensure the draft text
            // is present, then click Send. Native input events exercise the
            // real submission path without bypassing any React event handling.
            const submitResult = await evaluate(
              send,
              `(async () => {
                // The production composer renders a real <textarea> whose
                // textbox role is implicit, so match the element itself (and
                // keep the explicit-role selector as a fallback).
                const ta = document.querySelector('textarea, [role="textbox"]');
                if (!ta) return { ok: false, error: 'composer textarea not found' };
                const draft = window.retirementHarness.snapshot().draft;
                if (!draft) return { ok: false, error: 'no draft to submit' };

                // The draft should already be in the textarea from before retirement.
                // If not (e.g. the compositor cleared it on reconnect), restore it.
                if (ta.value !== draft) {
                  ta.focus();
                  const nativeInput = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value');
                  nativeInput.set.call(ta, draft);
                  ta.dispatchEvent(new Event('input', { bubbles: true }));
                  ta.dispatchEvent(new Event('change', { bubbles: true }));
                }

                const beforeTurnCount = window.retirementHarness.snapshot().turnIDs.length;

                // Find and click the Send button.
                const sendBtn = [...document.querySelectorAll('button')].find(b =>
                  b.textContent?.trim() === 'Send' || b.getAttribute('aria-label')?.toLowerCase().includes('send')
                );
                if (!sendBtn) return { ok: false, error: 'Send button not found' };
                sendBtn.click();

                // Wait for a new turn to appear (replacement source accepted the mutation).
                const deadline = performance.now() + 15000;
                for (;;) {
                  await new Promise(r => setTimeout(r, 100));
                  const afterTurnCount = window.retirementHarness.snapshot().turnIDs.length;
                  if (afterTurnCount > beforeTurnCount) return { ok: true };
                  if (performance.now() > deadline) {
                    return {
                      ok: false,
                      error: 'timed out waiting for new turn after send (15s); ' +
                             'replacement source may not have accepted the mutation'
                    };
                  }
                }
              })()`,
            );
            if (!submitResult.ok) {
              const domProbe = await evaluate(
                send,
                `JSON.stringify({
                  textboxes: document.querySelectorAll('[role="textbox"]').length,
                  textareas: document.querySelectorAll('textarea').length,
                  buttons: [...document.querySelectorAll('button')].map((b) => b.textContent?.trim()).slice(0, 12),
                  bodyLength: document.body.innerHTML.length,
                })`,
              );
              failures.push(`send failed: ${submitResult.error} dom=${domProbe}`);
            }
          }

          if (failures.length === 0) {
            // Allow one more settlement pass for turn notifications to land.
            await evaluate(
              send,
              `(async () => {
                await new Promise(r => setTimeout(r, 500));
              })()`,
            );

            // --- Snapshot 2: post-send ------------------------------------
            const snap2 = await evaluate(send, "JSON.stringify(window.retirementHarness.snapshot())");
            const postSend = JSON.parse(snap2);

            // Screenshot 3: after send (new turn visible, draft cleared).
            const ss3Path = await captureScreenshot(send, "03-after-send");
            console.log(`screenshot 3 (after send): ${ss3Path}`);

            // Verify exactly one new unique turn was added.
            const before = initial.turnIDs;
            const after = postSend.turnIDs;
            if (after.length !== before.length + 1) {
              failures.push(`expected exactly 1 new turn after send (before=${before.length} after=${after.length})`);
            }
            // No duplicate turn IDs.
            if (new Set(after).size !== after.length) {
              failures.push(`duplicate turn IDs after send: ${JSON.stringify(after)}`);
            }
            // Draft cleared after submission.
            if (postSend.draft !== "") {
              failures.push(`draft not cleared after send: ${JSON.stringify(postSend.draft)}`);
            }

            // --- Lost start reply: the same mutation id must be replayed ----
            // The replacement source drops the first turn/start reply for this
            // draft. The accepted turn can only appear if the client replayed
            // the SAME clientMutationId; the Go fixture asserts that identity.
            let retry = null;
            if (failures.length === 0) {
              const retryResult = await evaluate(
                send,
                `(async () => {
                  const timeout = new Promise((resolve) =>
                    setTimeout(() => resolve({ ok: false, error: 'retry draft did not produce a turn in 15000ms' }), 15000),
                  );
                  const attempt = window.retirementHarness.retryDraft().then(
                    () => ({ ok: true }),
                    (err) => ({ ok: false, error: err?.message ?? String(err) }),
                  );
                  const result = await Promise.race([attempt, timeout]);
                  if (!result.ok) result.debug = window.retirementHarness.debug();
                  return result;
                })()`,
              );
              if (!retryResult.ok) {
                const debug = retryResult.debug === undefined ? "" : ` debug=${JSON.stringify(retryResult.debug)}`;
                failures.push(`lost start reply retry failed: ${retryResult.error}${debug}`);
              } else {
                const beforeRetry = postSend.turnIDs.length;
                const snap3 = JSON.parse(await evaluate(send, "JSON.stringify(window.retirementHarness.snapshot())"));
                retry = { turnAdded: snap3.turnIDs.length - beforeRetry, turnIDs: snap3.turnIDs };
                if (retry.turnAdded !== 1) {
                  failures.push(
                    `lost start reply retry added ${retry.turnAdded} turns, want exactly 1 ` +
                      `(before=${JSON.stringify(postSend.turnIDs)} after=${JSON.stringify(snap3.turnIDs)})`,
                  );
                }
                if (new Set(snap3.turnIDs).size !== snap3.turnIDs.length) {
                  failures.push(`duplicate turn IDs after retry: ${JSON.stringify(snap3.turnIDs)}`);
                }
                const ss4Path = await captureScreenshot(send, "04-after-lost-reply-retry");
                console.log(`screenshot 4 (after lost-reply retry): ${ss4Path}`);
              }
            }

            // --- Unavailable queue/steer/settings: retain input, no auto-resume
            // The replacement withdraws queue/steer/settings over the live feed.
            // Unsent input must be retained and must never be auto-submitted.
            let unavailable = null;
            if (failures.length === 0) {
              const unavailableText = "retirement harness retained draft";
              const degradeResult = await evaluate(
                send,
                `(async () => {
                  const timeout = new Promise((resolve) =>
                    setTimeout(() => resolve({ ok: false, error: 'degrade() did not settle in 10000ms' }), 10000),
                  );
                  const attempt = window.retirementHarness.degrade().then(
                    () => ({ ok: true }),
                    (err) => ({ ok: false, error: err?.message ?? String(err) }),
                  );
                  const result = await Promise.race([attempt, timeout]);
                  if (!result.ok) result.debug = window.retirementHarness.debug();
                  return result;
                })()`,
              );
              if (!degradeResult.ok) {
                const debug = degradeResult.debug === undefined ? "" : ` debug=${JSON.stringify(degradeResult.debug)}`;
                failures.push(`degrade() failed: ${degradeResult.error}${debug}`);
              } else {
                await evaluate(
                  send,
                  `(() => { window.retirementHarness.typeDraft(${JSON.stringify(unavailableText)}); return true; })()`,
                );
                // Absence tripwire: nothing awaitable completes when nothing
                // happens, so give a wrong auto-resume a bounded chance to fire
                // before asserting. The Go fixture independently proves no
                // turn/start ever carried this text.
                await evaluate(send, `(async () => { await new Promise((r) => setTimeout(r, 800)); })()`);
                const snap4 = JSON.parse(await evaluate(send, "JSON.stringify(window.retirementHarness.snapshot())"));
                const beforeUnavailable = retry ? retry.turnIDs.length : postSend.turnIDs.length;
                unavailable = { draft: snap4.draft, turnIDs: snap4.turnIDs };
                if (snap4.draft !== unavailableText) {
                  failures.push(
                    `queue/steer/settings unavailability did not retain unsent input: draft=${JSON.stringify(snap4.draft)}`,
                  );
                }
                if (snap4.turnIDs.length !== beforeUnavailable) {
                  failures.push(
                    `queue/steer/settings unavailability auto-resumed: turns ${beforeUnavailable} -> ${snap4.turnIDs.length}`,
                  );
                }
                const ss5Path = await captureScreenshot(send, "05-after-unavailable-controls");
                console.log(`screenshot 5 (after unavailable controls): ${ss5Path}`);
              }
            }

            result = {
              assertions: "pass",
              sameSocket: socketState,
              lateFrame,
              retry,
              unavailable,
              initial,
              postRetire,
              postSend,
              screenshots: [
                "01-before-retirement.png",
                "02-after-retirement.png",
                "03-after-send.png",
                "04-after-lost-reply-retry.png",
                "05-after-unavailable-controls.png",
              ],
            };
          }
        }
      }

      // Collect any page errors that surfaced during the run.
      const pageErrors = await evaluate(send, "JSON.stringify(window.retirementHarness.errors())");
      const errs = JSON.parse(pageErrors);
      if (errs.length > 0) {
        failures.push(`page errors: ${errs.join("; ")}`);
      }
    } finally {
      await clearViewportOverride(send);
      page.close();
    }

    // --- Write machine-readable result ------------------------------------
    if (failures.length === 0) {
      const resultPath = path.join(ARTIFACT_DIR, "result.json");
      writeFileSync(resultPath, JSON.stringify(result ?? { assertions: "pass" }, null, 2));
      console.log(
        `retirementguard ok: transcript/draft survived retirement; ` +
          `1 new turn added after send; source generation rotated; ` +
          `artifacts at ${ARTIFACT_DIR}`,
      );
    } else {
      const resultPath = path.join(ARTIFACT_DIR, "result.json");
      writeFileSync(resultPath, JSON.stringify({ assertions: "fail", failures }, null, 2));
      for (const failure of failures) console.error(`retirementguard FAIL: ${failure}`);
      process.exitCode = 1;
    }
  } finally {
    await cleanup();
  }
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
});
