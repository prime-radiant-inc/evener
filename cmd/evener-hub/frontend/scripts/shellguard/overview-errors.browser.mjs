// Explicit Chrome lane, never collected by the default Vitest/Node test globs:
// node --test scripts/shellguard/overview-errors.browser.mjs
// Catches a guard that snapshots page errors before trusted interactions, or
// loses them when an interaction assertion throws. The flow and CDP stay real.
import assert from "node:assert/strict";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { evaluate } from "../browserGuardCdp.mjs";
import { startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";
import { overviewOnPage } from "./run.mjs";

const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const viewport = { width: 1400, height: 900 };

test("Overview retains real late browser errors after successful or throwing trusted interactions", async t => {
  const guard = await startBrowserGuard({ frontend, profilePrefix: "overview-errors-" });
  try {
    const endpoint = await waitForBrowserReady(guard);
    await t.test("no-error trusted flow succeeds", async () => {
      const { result, failures } = await overviewOnPage(endpoint, guard.vitePort, viewport, "light", null);
      assert.deepEqual(failures, []);
      assert.deepEqual(result.errors, []);
      assert.equal(result.keyboard.status, true);
    });

    for (const kind of ["error", "unhandledrejection"]) {
      for (const throwInteraction of [false, true]) {
        await t.test(`${kind}, trusted flow ${throwInteraction ? "throws" : "succeeds"}`, async () => {
          const marker = `overview-late-${kind}-${throwInteraction}`;
          let observed;
          const { result, failures } = await overviewOnPage(
            endpoint, guard.vitePort, viewport, "light", null,
            async send => {
              await evaluate(send, `(() => {
                window.__overviewErrorSeen = new Promise(resolve => {
                  window.addEventListener(${JSON.stringify(kind)}, event => {
                    resolve({ type: event.type, inputTrusted: window.__overviewErrorInputTrusted,
                      errors: [...window.__shellGuardErrors] });
                  }, { once: true });
                });
                window.addEventListener('keyup', event => {
                  if (event.key !== 'Home') return;
                  window.__overviewErrorInputTrusted = event.isTrusted;
                  // Fault the real focus check synchronously with trusted
                  // input, independently of async rejection delivery.
                  if (${throwInteraction}) {
                    const style = document.createElement('style');
                    style.textContent = '[data-testid="activity-sidebar"] [role="radio"] { outline: none !important; }';
                    document.head.append(style);
                  }
                  ${kind === "error" ? `throw new Error(${JSON.stringify(marker)});` : `void Promise.reject(new Error(${JSON.stringify(marker)}));`}
                }, { once: true });
              })()`);
              // Await the actual browser event, not a sleep or fabricated list.
              observed = evaluate(send, "window.__overviewErrorSeen");
              return { afterInteraction: observed };
            },
          );
          const witness = await observed;
          console.log(JSON.stringify({ marker, witness, errors: result.errors, failures }));
          assert.equal(witness.type, kind);
          assert.equal(witness.inputTrusted, true);
          assert.equal(witness.errors.length, 1);
          assert.ok(witness.errors[0].includes(marker));
          assert.equal(result.errors.length, 1, "late browser error must be exposed in the result");
          assert.ok(result.errors[0].includes(marker));
          assert.equal(failures.filter(failure => failure.startsWith("page error:")).length, 1);
          assert.ok(failures.some(failure => failure.startsWith("page error:") && failure.includes(marker)));
          assert.equal(failures.length, throwInteraction ? 2 : 1);
          if (throwInteraction) {
            assert.equal(result.keyboard, undefined);
            assert.ok(failures.some(failure => failure.startsWith("trusted interaction:") && failure.includes(" has no visible selected-radio focus:")));
          } else {
            assert.equal(result.keyboard.status, true);
          }
        });
      }
    }

    await t.test("an error already measured is reported only once", async () => {
      const { result, failures } = await overviewOnPage(
        endpoint, guard.vitePort, viewport, "light", null,
        send => evaluate(send, `new Promise(resolve => {
          window.addEventListener('error', () => resolve(true), { once: true });
          setTimeout(() => { throw new Error('overview-before-measure'); }, 0);
        })`),
      );
      assert.equal(result.keyboard.status, true);
      assert.equal(result.errors.length, 1);
      assert.ok(result.errors[0].includes("overview-before-measure"));
      assert.equal(failures.length, 1);
      assert.ok(failures[0].startsWith("page error:") && failures[0].includes("overview-before-measure"));
    });
  } finally {
    await guard.cleanup();
  }
});
