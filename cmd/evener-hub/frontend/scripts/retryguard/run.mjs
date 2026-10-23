import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Driver } from "../skillguard/run.mjs";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";
import { installRetryWireFault } from "./wireFault.mjs";

const fixture = JSON.parse(readFileSync(0, "utf8"));
const driver = new Driver(fixture);
const q = JSON.stringify;

// A new readonly connection sees committed durable state, not the pending UI
// projection. Abort creation so an absent database cannot masquerade as empty.
async function readDurableRetry(clientMutationId) {
  const database = await new Promise((resolve, reject) => {
    const request = indexedDB.open("evener-mutation-outbox");
    request.onupgradeneeded = () => request.transaction.abort();
    request.onerror = () => reject(request.error);
    request.onsuccess = () => resolve(request.result);
  });
  try {
    const stores = ["outbox", "optimistic", "recovery"];
    const transaction = database.transaction(stores, "readonly");
    const rows = {};
    await new Promise((resolve, reject) => {
      transaction.oncomplete = resolve;
      transaction.onabort = () => reject(transaction.error);
      transaction.onerror = () => reject(transaction.error);
      for (const store of stores) {
        const request = transaction.objectStore(store).get(clientMutationId);
        request.onsuccess = () => {
          const record = request.result;
          rows[store] = record ? { clientMutationId: record.clientMutationId, state: record.state } : null;
        };
      }
    });
    return rows;
  } finally {
    database.close();
  }
}

try {
  await driver.start();
  await driver.send("Page.addScriptToEvaluateOnNewDocument", { source: `(${installRetryWireFault.toString()})()` });
  await navigateTo(driver.page, fixture.url);
  for (const scenario of fixture.scenarios) {
    await driver.openSession(scenario.ref);
    const editor = driver.editorExpr(scenario.ref);
    await driver.waitPage(`document.querySelector('[data-testid="turn-failure"]') !== null`, {
      label: "terminal provider failure",
    });
    await evaluate(driver.send, `${editor}.focus()`);
    await driver.typeText(scenario.ref, "RETRY_UNSENT_DRAFT");
    await evaluate(
      driver.send,
      `Object.assign(window.__retryWire, {mode:${q(scenario.mode)}, armed:true, requests:[]})`,
    );
    await driver.clickByText("Retry");
    await driver.waitPage("window.__retryWire.requests.length > 0", { label: "Retry reached transport" });
    assert.deepEqual(await evaluate(driver.send, "window.__retryWire.requests[0].params.input"), [
      { type: "text", text: "continue" },
    ]);
    assert.equal(await evaluate(driver.send, `${editor}.textContent`), "RETRY_UNSENT_DRAFT");
    if (scenario.mode === "refused") {
      await driver.waitPage(`document.querySelector('button[aria-label="Edit message"]') !== null`, {
        label: "refused continuation recovery",
      });
      assert.equal(await evaluate(driver.send, `${editor}.textContent`), "RETRY_UNSENT_DRAFT");
      await driver.selectAll(scenario.ref);
      await driver.press(scenario.ref, "Backspace");
      await driver.waitPage(`${editor}.textContent === ""`, { label: "explicit draft clear" });
      await driver.clickByText("Edit message");
      await driver.waitPage(`${editor}.textContent === "continue"`, { label: "explicit recovery loaded continuation" });
      await driver.clickByText("Send");
    }
    await driver.waitPage(`document.body.textContent.includes("RETRY_DONE_${scenario.mode}")`, {
      label: "continued turn completed",
    });
    assert.equal(
      await evaluate(driver.send, `${editor}.textContent`),
      scenario.mode === "refused" ? "" : "RETRY_UNSENT_DRAFT",
    );
    if (scenario.mode === "lost-reply") {
      await driver.waitPage("window.__retryWire.connections >= 2", { label: "transport reconnected" });
    }
    const wire = await evaluate(driver.send, "window.__retryWire");
    for (const request of wire.requests) {
      assert.deepEqual(request.params.input, [{ type: "text", text: "continue" }]);
      assert.equal(typeof request.params.clientMutationId, "string");
      assert.notEqual(request.params.clientMutationId, "");
    }
    if (scenario.mode === "lost-reply") {
      assert.equal(wire.dropped, 1);
      assert.equal(new Set(wire.requests.map((request) => request.params.clientMutationId)).size, 1);
      const clientMutationId = wire.requests[0].params.clientMutationId;
      console.log("retryguard: continued output observed before durable settlement check", { clientMutationId });
      // Recreate the production client without clearing its IndexedDB or draft.
      await navigateTo(driver.page, await evaluate(driver.send, "location.href"));
      await driver.openSession(scenario.ref);
      await driver.waitPage(`window.__retryWire.reads.some((read) => read.ref === ${q(scenario.ref)} && read.subscribed)`, {
        label: "reopened client read the session",
      });
      await driver.waitPage(`document.body.textContent.includes("RETRY_DONE_${scenario.mode}")`, {
        label: "continued output after reopen",
      });
      await driver.waitPage(`${editor}.textContent === "RETRY_UNSENT_DRAFT"`, { label: "draft restored after reopen" });
      const durableRead = `(${readDurableRetry.toString()})(${q(clientMutationId)})`;
      try {
        await driver.waitPage(`${durableRead}.then((rows) => Object.values(rows).every((row) => row === null))`, {
          label: `durable Retry settlement for ${clientMutationId}`,
        });
      } catch (error) {
        assert.deepEqual(await evaluate(driver.send, durableRead), { outbox: null, optimistic: null, recovery: null },
          `unsettled durable Retry ${clientMutationId}`);
        throw error;
      }
      assert.deepEqual(await evaluate(driver.send, durableRead), { outbox: null, optimistic: null, recovery: null });
      assert.deepEqual(await evaluate(driver.send, "window.__retryWire.requests"), [], "reopen must not resend Retry");
      assert.equal(await evaluate(driver.send, `${editor}.textContent`), "RETRY_UNSENT_DRAFT");
      console.log("retryguard: durable Retry settled after reopen", { clientMutationId });
    } else {
      assert.equal(wire.requests.length, scenario.mode === "refused" ? 2 : 1);
    }
    driver.milestone(scenario.mode, { requests: wire.requests.length });
  }
} catch (error) {
  console.error(await evaluate(driver.send, "document.body.innerText"));
  throw error;
} finally {
  await driver.stop();
}
