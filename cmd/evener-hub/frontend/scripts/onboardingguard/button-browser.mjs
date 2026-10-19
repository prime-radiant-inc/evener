// Real Chrome DOM test of the exact helper used by run.mjs, not an E2E claim.
import assert from "node:assert/strict";
import { evaluate } from "../browserGuardCdp.mjs";
import { Driver } from "../skillguard/run.mjs";
import { button } from "./button.mjs";

const driver = new Driver({ artifactDir: process.argv[2] });
try {
  await driver.start();
  const send = driver.send;
  let restoreAfterObservation = false;
  let observations = 0;
  // Forward every CDP command and result unchanged. After the first actual
  // activation/readiness observation of the blocked DOM, release that state.
  // No sleeps, provider timing assumptions or fake readiness return values.
  driver.page.send = async (method, params) => {
    const result = await send(method, params);
    if (restoreAfterObservation && method === "Runtime.evaluate") {
      restoreAfterObservation = false;
      observations++;
      assert.equal(await evaluate(send, "window.clicks"), 0, "blocked control was activated");
      await evaluate(send, `(() => {
        const old = document.querySelector('button');
        const next = old.cloneNode(true);
        next.disabled = false;
        next.removeAttribute('aria-disabled');
        next.style.visibility = 'visible';
        next.addEventListener('click', () => window.clicks++);
        old.replaceWith(next);
        document.querySelector('#cover')?.remove();
      })()`);
    }
    return result;
  };
  for (const state of ["disabled", "aria-disabled", "hidden", "covered"]) {
    await evaluate(send, `(() => {
      document.body.innerHTML = '<button>fixture-action</button>';
      window.clicks = 0;
      document.querySelector('button').addEventListener('click', () => window.clicks++);
    })()`);
    const pause = async () => {
      await evaluate(send, `(() => {
        const b = document.querySelector('button');
        const state = ${JSON.stringify(state)};
        if (state === 'disabled') b.disabled = true;
        if (state === 'aria-disabled') b.setAttribute('aria-disabled', 'true');
        if (state === 'hidden') b.style.visibility = 'hidden';
        if (state === 'covered') {
          const cover = document.createElement('div');
          cover.id = 'cover';
          cover.style.cssText = 'position:fixed;inset:0;z-index:100;background:white';
          document.body.append(cover);
        }
      })()`);
      restoreAfterObservation = true;
    };
    await button(driver, pause, "fixture-action");
    assert.equal(await evaluate(send, "window.clicks"), 1, `${state}: replacement activated exactly once`);
  }
  assert.equal(observations, 4);
  console.log("PASS actual onboarding helper waits through disabled, aria-disabled, hidden and covered DOM replacements");
} finally {
  await driver.stop();
}
